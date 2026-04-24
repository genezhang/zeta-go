// Package embedded provides an in-process binding to the Zeta database engine
// via cgo and the libzeta.a static library.
//
// This package is for the embedded form factor. To connect to a Zeta server
// (single-node or distributed), use a standard PostgreSQL or MySQL Go driver —
// Zeta speaks both wire protocols on the same database. See the repository
// README for server connection examples.
package embedded

/*
#include <stdlib.h>
#include "zeta.h"
*/
import "C"

import (
	"sync"
	"unsafe"
)

// Database is a handle to an open Zeta database. Obtain one via Open or
// OpenMemory and release it with Close.
//
// All operations on a Database are serialised through an internal mutex.
// Multiple goroutines may share a Database safely; calls are executed
// one-at-a-time. For parallelism, open separate Database handles.
type Database struct {
	mu     sync.Mutex
	handle *C.zeta_db_t
}

// Open opens or creates a persistent database at path. Pass ":memory:" for
// an in-memory database (equivalent to OpenMemory).
func Open(path string) (*Database, error) {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))

	var errMsg *C.char
	handle := C.zeta_open(cpath, &errMsg)
	if handle == nil {
		return nil, consumeErrMsg(errMsg, ErrUnknown)
	}
	return &Database{handle: handle}, nil
}

// OpenMemory opens a new in-memory database. Data is lost when the handle
// is closed.
func OpenMemory() (*Database, error) {
	var errMsg *C.char
	handle := C.zeta_open_memory(&errMsg)
	if handle == nil {
		return nil, consumeErrMsg(errMsg, ErrUnknown)
	}
	return &Database{handle: handle}, nil
}

// Close releases the database handle. Subsequent calls are no-ops.
// Close is safe to call concurrently but only one call will actually
// close the handle.
func (d *Database) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.handle != nil {
		C.zeta_close(d.handle)
		d.handle = nil
	}
	return nil
}

// Version returns the Zeta engine version string compiled into libzeta.a.
func Version() string {
	return C.GoString(C.zeta_version())
}

// consumeErrMsg converts a heap-allocated C error string (as returned via
// an err_msg out-parameter) into a Go *Error and frees the C memory.
// Pass the ErrorKind inferred from context, or ErrUnknown.
func consumeErrMsg(msgC *C.char, kind ErrorKind) error {
	e := &Error{Kind: kind}
	if msgC != nil {
		e.Message = C.GoString(msgC)
		C.zeta_free(unsafe.Pointer(msgC))
	}
	return e
}

// dbError builds a *Error from a database handle's last error state.
// Used when a C call returns a negative code without populating the
// err_msg out-parameter (e.g. mid-statement step failures).
func dbError(handle *C.zeta_db_t) error {
	code := int(C.zeta_errcode(handle))
	msg := C.GoString(C.zeta_errmsg(handle))
	return &Error{Kind: errorKindFromCode(code), Message: msg}
}

// stmtError builds a *Error from a statement handle's last error state,
// used when zeta_step or a bind call returns a negative code.
func stmtError(stmt *C.zeta_stmt_t, code C.int) error {
	msgC := C.zeta_stmt_errmsg(stmt)
	msg := ""
	if msgC != nil {
		msg = C.GoString(msgC)
	}
	return &Error{Kind: errorKindFromCode(int(code)), Message: msg}
}
