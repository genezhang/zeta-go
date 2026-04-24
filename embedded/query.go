package embedded

/*
#include <stdlib.h>
#include "zeta.h"
*/
import "C"

import (
	"errors"
	"fmt"
)

// Query runs a SELECT statement and returns a Rows iterator. Call
// Next/Scan in a loop and Close when done.
//
// The returned Rows holds the Database's internal mutex until it is closed;
// call Rows.Close() (typically via defer) to release it, or iterate to
// completion (Next returns false at end).
//
// Parameters are bound by 1-based position: "$1", "$2", ... in the SQL text.
func (d *Database) Query(sql string, params ...any) (*Rows, error) {
	d.mu.Lock()
	if d.handle == nil {
		d.mu.Unlock()
		return nil, ErrClosed
	}

	stmt, err := d.prepareLocked(sql)
	if err != nil {
		d.mu.Unlock()
		return nil, err
	}

	if err := bindParams(stmt, params); err != nil {
		C.zeta_finalize(stmt)
		d.mu.Unlock()
		return nil, err
	}

	return &Rows{db: d, stmt: stmt}, nil
}

// Rows is an iterator over the result set of a query. It must be closed
// to release the underlying statement handle and the Database's mutex.
type Rows struct {
	db      *Database
	stmt    *C.zeta_stmt_t
	closed  bool
	lastErr error
	atRow   bool // true when the current position has a valid row
}

// Next advances to the next row. Returns true if a row is available for
// Scan, false at end-of-rows or on error (inspect Err for the distinction).
//
// When Next returns false the Rows is automatically closed, so the common
// for-loop pattern does not leak the database mutex. Deferring Close is
// still recommended to cover early returns.
func (r *Rows) Next() bool {
	if r.closed || r.lastErr != nil {
		return false
	}
	rc := C.zeta_step(r.stmt)
	switch rc {
	case C.ZETA_ROW:
		r.atRow = true
		return true
	case C.ZETA_DONE:
		r.atRow = false
		_ = r.Close()
		return false
	default:
		r.lastErr = stmtError(r.stmt, rc)
		r.atRow = false
		_ = r.Close()
		return false
	}
}

// Err returns the error, if any, that terminated iteration early.
// Returns nil for normal end-of-rows.
func (r *Rows) Err() error {
	return r.lastErr
}

// Close releases the statement handle and the Database mutex. Safe to
// call multiple times; subsequent calls are no-ops.
func (r *Rows) Close() error {
	if r.closed {
		return nil
	}
	C.zeta_finalize(r.stmt)
	r.stmt = nil
	r.closed = true
	r.atRow = false
	r.db.mu.Unlock()
	return nil
}

// Columns returns the column names of the result set.
//
// Column metadata is only populated once zeta_step has been called, so
// Columns must be called after at least one successful Next. Calling
// Columns before Next or after Close returns nil.
func (r *Rows) Columns() []string {
	if r.closed || !r.atRow {
		return nil
	}
	n := int(C.zeta_column_count(r.stmt))
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = C.GoString(C.zeta_column_name(r.stmt, C.int(i)))
	}
	return out
}

// ErrNoRow is returned by Scan when called without a current row (either
// before the first Next, after Next returned false, or after Close).
var ErrNoRow = errors.New("zeta: Scan called without a current row (call Next first)")

// Scan copies the current row's column values into the destinations.
// Each destination must be a pointer to a supported type:
//
//	*bool, *int, *int32, *int64, *float32, *float64, *string,
//	*[]byte, *[]float32, *any, *sql.NullXxx-style Scanner (not yet supported)
//
// The number of destinations must equal the number of columns.
func (r *Rows) Scan(dest ...any) error {
	if r.closed {
		return ErrNoRow
	}
	if !r.atRow {
		return ErrNoRow
	}
	n := int(C.zeta_column_count(r.stmt))
	if len(dest) != n {
		return fmt.Errorf("zeta: Scan expected %d destinations, got %d", n, len(dest))
	}
	for i, d := range dest {
		if err := scanColumn(r.stmt, C.int(i), d); err != nil {
			return fmt.Errorf("zeta: column %d: %w", i, err)
		}
	}
	return nil
}

// scanColumn copies column i into the destination pointer dest.
func scanColumn(stmt *C.zeta_stmt_t, i C.int, dest any) error {
	ctype := C.zeta_column_type(stmt, i)
	isNull := ctype == C.ZETA_TYPE_NULL

	switch d := dest.(type) {
	case *bool:
		*d = !isNull && C.zeta_column_int64(stmt, i) != 0
	case *int:
		if isNull {
			*d = 0
		} else {
			*d = int(C.zeta_column_int64(stmt, i))
		}
	case *int32:
		if isNull {
			*d = 0
		} else {
			*d = int32(C.zeta_column_int64(stmt, i))
		}
	case *int64:
		if isNull {
			*d = 0
		} else {
			*d = int64(C.zeta_column_int64(stmt, i))
		}
	case *float32:
		if isNull {
			*d = 0
		} else {
			*d = float32(C.zeta_column_double(stmt, i))
		}
	case *float64:
		if isNull {
			*d = 0
		} else {
			*d = float64(C.zeta_column_double(stmt, i))
		}
	case *string:
		if isNull {
			*d = ""
		} else {
			*d = C.GoString(C.zeta_column_text(stmt, i))
		}
	case *[]byte:
		if isNull {
			*d = nil
			return nil
		}
		var blen C.int
		ptr := C.zeta_column_blob(stmt, i, &blen)
		if ptr == nil || blen == 0 {
			*d = nil
			return nil
		}
		*d = C.GoBytes(ptr, blen)
	case *any:
		*d = columnToAny(stmt, i, ctype)
	default:
		return fmt.Errorf("unsupported scan destination type %T", dest)
	}
	return nil
}

// columnToAny returns a column's value boxed into the natural Go type for
// its Zeta column type.
func columnToAny(stmt *C.zeta_stmt_t, i C.int, ctype C.int) any {
	switch ctype {
	case C.ZETA_TYPE_NULL:
		return nil
	case C.ZETA_TYPE_INT:
		return int64(C.zeta_column_int64(stmt, i))
	case C.ZETA_TYPE_FLOAT:
		return float64(C.zeta_column_double(stmt, i))
	case C.ZETA_TYPE_TEXT:
		return C.GoString(C.zeta_column_text(stmt, i))
	case C.ZETA_TYPE_BLOB:
		var blen C.int
		ptr := C.zeta_column_blob(stmt, i, &blen)
		if ptr == nil {
			return nil
		}
		return C.GoBytes(ptr, blen)
	case C.ZETA_TYPE_BOOL:
		return C.zeta_column_int64(stmt, i) != 0
	default:
		return nil
	}
}
