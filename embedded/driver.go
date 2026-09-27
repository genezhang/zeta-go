package embedded

/*
#include <stdlib.h>
#include "zeta.h"
*/
import "C"

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"time"
	"unsafe"
)

// DriverName is the name this driver registers with database/sql.
//
//	import _ "github.com/genezhang/zeta-go/embedded"
//	db, err := sql.Open("zeta", ":memory:")
const DriverName = "zeta"

func init() {
	sql.Register(DriverName, Driver{})
}

// Driver implements [database/sql/driver.Driver] (and DriverContext) for the
// embedded Zeta engine. Registration happens in init, so importing this
// package enables sql.Open("zeta", path).
//
// # Connection model
//
// Each database/sql connection opens its own in-process Zeta handle (the same
// model as mattn/go-sqlite3). Two consequences worth knowing:
//
//   - ":memory:" databases are per-connection, so a pool of size > 1 would see
//     independent, empty databases. Call db.SetMaxOpenConns(1) for a single
//     shared in-memory database.
//   - For a persistent path, set db.SetMaxOpenConns(1) if the workload needs
//     single-writer semantics.
//
// For an explicitly shared handle, the lower-level [Database] API (Open /
// Query / Exec / Begin) remains available alongside this driver.
//
// # Limitations
//
//   - Context deadlines/cancellation are honored only before a call begins; a
//     statement already executing in the engine cannot be interrupted (there is
//     no equivalent of sqlite3_interrupt).
//   - Named parameters are not supported — use positional $1, $2, ....
//   - LastInsertId is unsupported; use INSERT ... RETURNING.
//   - time.Time parameters are bound as UTC, microsecond-precision timestamp
//     text. That binds into TIMESTAMP and TIMESTAMPTZ columns, but the engine
//     does not yet accept timestamp text for DATE or TIME (genezhang/zeta#3935),
//     so a DATE or TIME value read back cannot be passed as a parameter; bind
//     it as a string ("2006-01-02", "15:04:05.999999") instead.
//   - With an archive carrying the typed temporal codes (zeta-embedded#86),
//     TIMESTAMP / TIMESTAMPTZ / DATE / TIME columns read as time.Time (UTC).
//     Scanning one into *int64 then fails, and into *string yields
//     database/sql's RFC 3339 rendering; the native [Rows.Scan] into *string
//     still returns the engine's own text.
type Driver struct{}

var (
	_ driver.Driver        = Driver{}
	_ driver.DriverContext = Driver{}
)

// Open opens a new connection to the database identified by dsn — a filesystem
// path, or ":memory:".
func (Driver) Open(dsn string) (driver.Conn, error) {
	return openConn(dsn)
}

// OpenConnector returns a connector for dsn. Each Connect opens a fresh handle.
func (Driver) OpenConnector(dsn string) (driver.Connector, error) {
	return connector{dsn: dsn}, nil
}

type connector struct {
	dsn string
}

var _ driver.Connector = connector{}

func (c connector) Connect(_ context.Context) (driver.Conn, error) {
	return openConn(c.dsn)
}

func (connector) Driver() driver.Driver { return Driver{} }

func openConn(dsn string) (*conn, error) {
	cdsn := C.CString(dsn)
	defer C.free(unsafe.Pointer(cdsn))

	var errMsg *C.char
	h := C.zeta_open(cdsn, &errMsg)
	if h == nil {
		return nil, consumeErrMsg(errMsg, ErrUnknown)
	}
	return &conn{handle: h}, nil
}

// conn is one Zeta connection (one open zeta_db handle). database/sql
// guarantees a driver.Conn is used by a single goroutine at a time, so no
// internal locking is required here.
type conn struct {
	handle *C.zeta_db_t
	txn    *C.zeta_txn_t // non-nil while a transaction is open
}

var (
	_ driver.Conn               = (*conn)(nil)
	_ driver.ConnPrepareContext = (*conn)(nil)
	_ driver.ConnBeginTx        = (*conn)(nil)
)

func (c *conn) Prepare(query string) (driver.Stmt, error) {
	return c.prepare(query)
}

func (c *conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.prepare(query)
}

func (c *conn) prepare(query string) (*stmt, error) {
	if c.handle == nil {
		return nil, ErrClosed
	}
	s := &stmt{c: c, query: query}
	if err := s.ensurePrepared(); err != nil {
		return nil, err
	}
	return s, nil
}

func (c *conn) Close() error {
	if c.txn != nil {
		C.zeta_rollback(c.txn)
		c.txn = nil
	}
	if c.handle != nil {
		C.zeta_close(c.handle)
		c.handle = nil
	}
	return nil
}

func (c *conn) Begin() (driver.Tx, error) {
	return c.beginTx()
}

func (c *conn) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Isolation / read-only options are not plumbed through the C API; Zeta
	// uses snapshot isolation by default.
	return c.beginTx()
}

func (c *conn) beginTx() (driver.Tx, error) {
	if c.handle == nil {
		return nil, ErrClosed
	}
	if c.txn != nil {
		return nil, errors.New("zeta: nested transactions are not supported")
	}
	var errMsg *C.char
	h := C.zeta_begin(c.handle, &errMsg)
	if h == nil {
		return nil, consumeErrMsg(errMsg, errorKindFromCode(int(C.zeta_errcode(c.handle))))
	}
	c.txn = h
	return &tx{c: c}, nil
}

// stmt is a prepared statement. The underlying zeta_stmt_t is held across
// executions; each Exec/Query resets it and rebinds, reusing the compiled
// plan — the embedded equivalent of SQLite's prepare/step.
type stmt struct {
	c      *conn
	h      *C.zeta_stmt_t
	query  string
	inTxn  bool // whether h was prepared against c.txn (vs the autocommit handle)
	closed bool
}

var (
	_ driver.Stmt              = (*stmt)(nil)
	_ driver.StmtExecContext   = (*stmt)(nil)
	_ driver.StmtQueryContext  = (*stmt)(nil)
	_ driver.NamedValueChecker = (*stmt)(nil)
)

// ensurePrepared (re)prepares the C handle so it matches the connection's
// current transaction context. Zeta binds a statement to its txn/autocommit
// context at prepare time (distinct zeta_prepare vs zeta_txn_prepare), but
// database/sql may reuse a statement prepared in autocommit inside a
// transaction (Tx.Stmt), or reuse a txn statement after the txn ends. When the
// context changes we finalize and re-prepare; within one context the handle —
// and its compiled plan — is reused.
func (s *stmt) ensurePrepared() error {
	if s.closed {
		return errors.New("zeta: statement is closed")
	}
	if s.c.handle == nil {
		return ErrClosed
	}
	want := s.c.txn != nil
	if s.h != nil && s.inTxn == want {
		return nil
	}
	if s.h != nil {
		C.zeta_finalize(s.h)
		s.h = nil
	}

	csql := C.CString(s.query)
	defer C.free(unsafe.Pointer(csql))

	var errMsg *C.char
	var h *C.zeta_stmt_t
	if want {
		h = C.zeta_txn_prepare(s.c.txn, csql, &errMsg)
	} else {
		h = C.zeta_prepare(s.c.handle, csql, &errMsg)
	}
	if h == nil {
		return consumeErrMsg(errMsg, errorKindFromCode(int(C.zeta_errcode(s.c.handle))))
	}
	s.h = h
	s.inTxn = want
	return nil
}

func (s *stmt) Close() error {
	s.closed = true
	if s.h != nil {
		C.zeta_finalize(s.h)
		s.h = nil
	}
	return nil
}

// CheckNamedValue lets []float32 vector parameters reach the driver — the
// default database/sql converter rejects non-[]byte slices. All other types
// fall through to the standard converter.
func (s *stmt) CheckNamedValue(nv *driver.NamedValue) error {
	if _, ok := nv.Value.([]float32); ok {
		return nil
	}
	return driver.ErrSkip
}

// NumInput returns -1: the argument count is not checked by database/sql,
// leaving validation to the engine at bind time.
func (s *stmt) NumInput() int { return -1 }

func (s *stmt) Exec(args []driver.Value) (driver.Result, error) {
	if err := s.resetAndBind(args); err != nil {
		return nil, err
	}
	for {
		switch rc := C.zeta_step(s.h); rc {
		case C.ZETA_DONE:
			return result{affected: int64(C.zeta_changes(s.c.handle))}, nil
		case C.ZETA_ROW:
			continue // drain any produced rows; Exec discards them
		default:
			return nil, stmtError(s.h, rc)
		}
	}
}

func (s *stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	vals, err := namedToValues(args)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.Exec(vals)
}

func (s *stmt) Query(args []driver.Value) (driver.Rows, error) {
	if err := s.resetAndBind(args); err != nil {
		return nil, err
	}
	// Step once up front: column metadata is only valid after the first
	// zeta_step, and database/sql calls Columns() before Next(). The fetched
	// row (if any) is returned by the first Next.
	r := &rows{s: s}
	switch rc := C.zeta_step(s.h); rc {
	case C.ZETA_ROW:
		r.pending = true
	case C.ZETA_DONE:
		r.done = true
	default:
		return nil, stmtError(s.h, rc)
	}
	return r, nil
}

func (s *stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	vals, err := namedToValues(args)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.Query(vals)
}

func (s *stmt) resetAndBind(args []driver.Value) error {
	if err := s.ensurePrepared(); err != nil {
		return err
	}
	if rc := C.zeta_reset(s.h); rc != C.ZETA_OK {
		return stmtError(s.h, rc)
	}
	return bindValues(s.h, args)
}

// rows iterates the result set produced by stmt.Query.
type rows struct {
	s       *stmt
	pending bool // a row was fetched by the up-front step and not yet returned
	done    bool
	closed  bool
}

var _ driver.Rows = (*rows)(nil)

// Columns returns the result column names. For a zero-row result the engine
// may report no columns; that is fine for the QueryRow/ErrNoRows path.
func (r *rows) Columns() []string {
	if r.closed || r.s.h == nil {
		return nil
	}
	n := int(C.zeta_column_count(r.s.h))
	cols := make([]string, n)
	for i := 0; i < n; i++ {
		cols[i] = C.GoString(C.zeta_column_name(r.s.h, C.int(i)))
	}
	return cols
}

func (r *rows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	// The statement is reusable; reset it to release the engine-side cursor
	// while leaving the compiled plan intact (the point of prepared stmts).
	if r.s.h != nil {
		C.zeta_reset(r.s.h)
	}
	r.done = true
	r.pending = false
	return nil
}

func (r *rows) Next(dest []driver.Value) error {
	if r.closed || r.s.h == nil {
		return io.EOF
	}
	if !r.pending {
		if r.done {
			return io.EOF
		}
		switch rc := C.zeta_step(r.s.h); rc {
		case C.ZETA_ROW:
			// a row is ready; read it below
		case C.ZETA_DONE:
			r.done = true
			return io.EOF
		default:
			r.done = true
			return stmtError(r.s.h, rc)
		}
	}
	r.pending = false

	n := int(C.zeta_column_count(r.s.h))
	for i := 0; i < n && i < len(dest); i++ {
		dest[i] = columnToValue(r.s.h, C.int(i))
	}
	return nil
}

// result reports the affected-row count. Zeta exposes no implicit rowid, so
// LastInsertId is unsupported (use INSERT ... RETURNING).
type result struct {
	affected int64
}

var _ driver.Result = result{}

func (r result) LastInsertId() (int64, error) {
	return 0, errors.New("zeta: LastInsertId is not supported (use INSERT ... RETURNING)")
}

func (r result) RowsAffected() (int64, error) { return r.affected, nil }

// tx wraps the conn's active zeta_txn_t.
type tx struct {
	c *conn
}

var _ driver.Tx = (*tx)(nil)

func (t *tx) Commit() error {
	if t.c.txn == nil {
		return ErrTxDone
	}
	var errMsg *C.char
	rc := C.zeta_commit(t.c.txn, &errMsg)
	t.c.txn = nil
	if rc != C.ZETA_OK {
		return consumeErrMsg(errMsg, errorKindFromCode(int(rc)))
	}
	return nil
}

func (t *tx) Rollback() error {
	if t.c.txn == nil {
		return ErrTxDone
	}
	rc := C.zeta_rollback(t.c.txn)
	t.c.txn = nil
	if rc != C.ZETA_OK {
		return &Error{Kind: errorKindFromCode(int(rc)), Message: "rollback failed"}
	}
	return nil
}

// namedToValues converts database/sql named arguments to positional values.
// Named parameters are not supported; only ordinal $1, $2, ... binding.
func namedToValues(named []driver.NamedValue) ([]driver.Value, error) {
	vals := make([]driver.Value, len(named))
	for i, nv := range named {
		if nv.Name != "" {
			return nil, errors.New("zeta: named parameters are not supported; use positional $1, $2, ...")
		}
		vals[i] = nv.Value
	}
	return vals, nil
}

// bindValues binds database/sql driver values to the statement. driver.Value is
// one of: int64, float64, bool, []byte, string, time.Time, nil — all handled by
// bindParams except time.Time, which is rendered to a timestamp string here.
func bindValues(h *C.zeta_stmt_t, args []driver.Value) error {
	conv := make([]any, len(args))
	for i, a := range args {
		if t, ok := a.(time.Time); ok {
			conv[i] = t.UTC().Format("2006-01-02 15:04:05.999999")
		} else {
			conv[i] = a
		}
	}
	return bindParams(h, conv)
}

// columnToValue reads column i as a driver.Value. columnToAny already returns
// only int64/float64/string/[]byte/bool/time.Time/nil, all valid driver.Value
// types.
func columnToValue(h *C.zeta_stmt_t, i C.int) driver.Value {
	return columnToAny(h, i, C.zeta_column_type(h, i))
}
