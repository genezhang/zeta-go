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

// ErrClosed is returned when an operation is attempted on a closed Database.
var ErrClosed = errors.New("zeta: database is closed")

// Result carries the outcome of a non-query statement (INSERT, UPDATE,
// DELETE, DDL).
type Result struct {
	rowsAffected int64
}

// RowsAffected reports the number of rows changed by INSERT, UPDATE, or
// DELETE. Returns 0 for DDL and SELECT.
func (r Result) RowsAffected() int64 {
	return r.rowsAffected
}

// Exec runs a SQL statement that returns no rows (DDL, INSERT, UPDATE,
// DELETE). Use Query for SELECT.
//
// Parameters are bound by 1-based position: "$1", "$2", ... in the SQL
// text. See the package docs for supported parameter types.
func (d *Database) Exec(sql string, params ...any) (Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.handle == nil {
		return Result{}, ErrClosed
	}

	stmt, err := d.prepareLocked(sql)
	if err != nil {
		return Result{}, err
	}
	defer C.zeta_finalize(stmt)

	if err := bindParams(stmt, params); err != nil {
		return Result{}, err
	}

	// Step to completion; silently drop any rows produced (consistent
	// with database/sql's Exec semantics).
	for {
		rc := C.zeta_step(stmt)
		switch rc {
		case C.ZETA_DONE:
			return Result{rowsAffected: int64(C.zeta_changes(d.handle))}, nil
		case C.ZETA_ROW:
			continue
		default:
			return Result{}, stmtError(stmt, rc)
		}
	}
}

// prepareLocked wraps zeta_prepare; must be called with d.mu held.
func (d *Database) prepareLocked(sql string) (*C.zeta_stmt_t, error) {
	csql := C.CString(sql)
	defer C.free(unsafe.Pointer(csql))

	var errMsg *C.char
	stmt := C.zeta_prepare(d.handle, csql, &errMsg)
	if stmt == nil {
		return nil, consumeErrMsg(errMsg, errorKindFromCode(int(C.zeta_errcode(d.handle))))
	}
	return stmt, nil
}
