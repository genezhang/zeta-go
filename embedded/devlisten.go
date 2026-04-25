//go:build zeta_dev

// Embedded wire-protocol listeners — developer inspection facility.
//
// When compiled with `-tags zeta_dev` this file binds three Go entry
// points for starting / stopping pgwire and mysqlwire listeners that
// expose an in-process Zeta database to standard `psql` / `mysql`
// clients on a loopback port.
//
// Requires `libzeta.a` to have been built with the `wire-pg` and/or
// `wire-mysql` Cargo features (or the convenience `dev-listeners`
// feature that enables both). A stock `libzeta.a` built without
// those features will fail to link with "undefined reference to
// zeta_start_pgwire" — see the README.
//
// **Not for production.** The listeners default to:
//   - loopback-only bind addresses (overridden by setting
//     `ZETA_ALLOW_NONLOCAL_EMBED_LISTEN=1` in the environment),
//   - trust authentication (no password),
//   - in-memory replication slots (pgwire).

package embedded

/*
#define ZETA_DEV_LISTENERS
#include <stdlib.h>
#include "zeta.h"
*/
import "C"

import (
	"errors"
	"unsafe"
)

// ErrDevListenersNotBuilt documents the failure mode when libzeta.a
// was built without the wire features.
//
// In this build configuration the dev-listener entry points are bound
// directly from C, so a library missing those symbols fails at link
// time before any Go code can run — the methods in this file do not
// return this sentinel. It is kept only as a stable error value for
// callers / documentation that want to describe that case (e.g. a
// build script that catches the linker error and surfaces a friendlier
// Go-level message).
var ErrDevListenersNotBuilt = errors.New(
	"zeta: dev-listeners not built into libzeta.a (rebuild with --features dev-listeners)",
)

// StartPgwireDev starts a PostgreSQL wire-protocol listener bound to
// `addr` (e.g. "127.0.0.1:5433") that exposes this Database to
// standard Postgres clients. Loopback only by default; set
// `ZETA_ALLOW_NONLOCAL_EMBED_LISTEN=1` in the environment to bypass.
//
// Pass "127.0.0.1:0" to let the OS pick an ephemeral port — the
// chosen port is logged at INFO level via `tracing` from the engine.
//
// Auto-stopped when the Database is Closed.
func (d *Database) StartPgwireDev(addr string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.handle == nil {
		return ErrClosed
	}
	caddr := C.CString(addr)
	defer C.free(unsafe.Pointer(caddr))
	var errMsg *C.char
	rc := C.zeta_start_pgwire(d.handle, caddr, &errMsg)
	if rc != C.ZETA_OK {
		return consumeErrMsg(errMsg, errorKindFromCode(int(rc)))
	}
	return nil
}

// StartMysqlwireDev starts a MySQL wire-protocol listener bound to
// `addr` (e.g. "127.0.0.1:3307"). Same loopback restriction and
// developer-only caveats as StartPgwireDev.
func (d *Database) StartMysqlwireDev(addr string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.handle == nil {
		return ErrClosed
	}
	caddr := C.CString(addr)
	defer C.free(unsafe.Pointer(caddr))
	var errMsg *C.char
	rc := C.zeta_start_mysqlwire(d.handle, caddr, &errMsg)
	if rc != C.ZETA_OK {
		return consumeErrMsg(errMsg, errorKindFromCode(int(rc)))
	}
	return nil
}

// StopDevListeners stops and drains all wire-protocol listeners
// associated with this Database. Database.Close calls this internally;
// explicit use is for callers that want to take down listeners
// without closing the database (e.g. to restart on a different port).
func (d *Database) StopDevListeners() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.handle == nil {
		return ErrClosed
	}
	rc := C.zeta_stop_listeners(d.handle)
	if rc != C.ZETA_OK {
		return &Error{
			Kind:    errorKindFromCode(int(rc)),
			Message: "zeta_stop_listeners failed",
		}
	}
	return nil
}
