package embedded

/*
#include <stdlib.h>
#include "zeta.h"
*/
import "C"

import (
	"errors"
	"unsafe"
)

// ErrTxDone is returned by Tx methods after the transaction has been
// committed or rolled back.
var ErrTxDone = errors.New("zeta: transaction already committed or rolled back")

// Tx is an in-progress database transaction, obtained from
// (*Database).Begin. All queries run inside a Tx see a consistent snapshot
// and any writes are only visible after Commit.
//
// A Tx must be finalised with Commit or Rollback; deferring Rollback
// immediately after Begin is the idiomatic pattern:
//
//	tx, err := db.Begin()
//	if err != nil { return err }
//	defer tx.Rollback()           // no-op if Commit succeeded
//	if _, err := tx.Exec(...); err != nil { return err }
//	return tx.Commit()
//
// Close the transaction before closing the parent Database; closing the
// database with an open transaction is undefined behaviour.
type Tx struct {
	db     *Database
	handle *C.zeta_txn_t
	done   bool
}

// Begin starts a new transaction. The returned Tx must be finalised with
// Commit or Rollback.
func (d *Database) Begin() (*Tx, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.handle == nil {
		return nil, ErrClosed
	}

	var errMsg *C.char
	h := C.zeta_begin(d.handle, &errMsg)
	if h == nil {
		return nil, consumeErrMsg(errMsg, errorKindFromCode(int(C.zeta_errcode(d.handle))))
	}
	return &Tx{db: d, handle: h}, nil
}

// Exec runs a non-query statement (DDL, INSERT, UPDATE, DELETE) within
// the transaction. Parameters bind as with (*Database).Exec.
func (t *Tx) Exec(sql string, params ...any) (Result, error) {
	t.db.mu.Lock()
	defer t.db.mu.Unlock()
	if t.done {
		return Result{}, ErrTxDone
	}
	if t.db.handle == nil {
		return Result{}, ErrClosed
	}

	stmt, err := t.prepareLocked(sql)
	if err != nil {
		return Result{}, err
	}
	defer C.zeta_finalize(stmt)

	if err := bindParams(stmt, params); err != nil {
		return Result{}, err
	}

	for {
		rc := C.zeta_step(stmt)
		switch rc {
		case C.ZETA_DONE:
			return Result{rowsAffected: int64(C.zeta_changes(t.db.handle))}, nil
		case C.ZETA_ROW:
			continue
		default:
			return Result{}, stmtError(stmt, rc)
		}
	}
}

// Query runs a SELECT within the transaction. The returned Rows sees
// the transaction's snapshot (including any writes made earlier in the
// same transaction).
//
// As with (*Database).Query, the returned Rows holds the Database's
// internal mutex until Close. Defer rows.Close().
func (t *Tx) Query(sql string, params ...any) (*Rows, error) {
	t.db.mu.Lock()
	if t.done {
		t.db.mu.Unlock()
		return nil, ErrTxDone
	}
	if t.db.handle == nil {
		t.db.mu.Unlock()
		return nil, ErrClosed
	}

	stmt, err := t.prepareLocked(sql)
	if err != nil {
		t.db.mu.Unlock()
		return nil, err
	}

	if err := bindParams(stmt, params); err != nil {
		C.zeta_finalize(stmt)
		t.db.mu.Unlock()
		return nil, err
	}

	return &Rows{db: t.db, stmt: stmt}, nil
}

// Commit finalises the transaction, persisting any writes. The Tx
// is consumed; further calls on it return ErrTxDone.
func (t *Tx) Commit() error {
	t.db.mu.Lock()
	defer t.db.mu.Unlock()
	if t.done {
		return ErrTxDone
	}
	t.done = true

	var errMsg *C.char
	rc := C.zeta_commit(t.handle, &errMsg)
	t.handle = nil // zeta_commit consumes the handle regardless of outcome
	if rc != C.ZETA_OK {
		return consumeErrMsg(errMsg, errorKindFromCode(int(rc)))
	}
	return nil
}

// Rollback discards any writes made in the transaction. Idempotent —
// Rollback on an already-finalised Tx is a no-op, which makes
// `defer tx.Rollback()` safe to use alongside an explicit Commit.
func (t *Tx) Rollback() error {
	t.db.mu.Lock()
	defer t.db.mu.Unlock()
	if t.done {
		return nil
	}
	t.done = true

	rc := C.zeta_rollback(t.handle)
	t.handle = nil // zeta_rollback consumes the handle
	if rc != C.ZETA_OK {
		return &Error{Kind: errorKindFromCode(int(rc)), Message: "rollback failed"}
	}
	return nil
}

// prepareLocked wraps zeta_txn_prepare; must be called with t.db.mu held.
func (t *Tx) prepareLocked(sql string) (*C.zeta_stmt_t, error) {
	csql := C.CString(sql)
	defer C.free(unsafe.Pointer(csql))

	var errMsg *C.char
	stmt := C.zeta_txn_prepare(t.handle, csql, &errMsg)
	if stmt == nil {
		return nil, consumeErrMsg(errMsg, errorKindFromCode(int(C.zeta_errcode(t.db.handle))))
	}
	return stmt, nil
}
