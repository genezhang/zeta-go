package embedded

/*
#include <stdlib.h>
#include "zeta.h"
*/
import "C"

import (
	"context"
	"database/sql/driver"
	"io"
	"sync"
	"sync/atomic"
	"unsafe"
)

// liveEngines counts the engine handles (zeta_open) this process holds for database/sql pools. It exists so
// tests can assert that an engine is closed once, and only after the last connection over it.
var liveEngines atomic.Int64

// engine is the one zeta_open handle behind a pool: every database/sql connection is a zeta_connect session over
// it, the embedded counterpart of DuckDB's connections over one database. Before this, each pooled connection
// opened its own engine, which over a persistent path meant several engines on one data directory (the engine
// says that corrupts it) and over ":memory:" meant a separate empty database per connection
// (genezhang/zeta-go#8).
//
// Lifetime has two owners. The pool (through its connector) holds the engine open for as long as the sql.DB
// lives, so an in-memory database survives a pool that has closed every idle connection. The engine is closed
// when the pool has let go of it AND the last session over it has closed, in that order of sessions first: the C
// API requires the handle sessions were opened from to be closed last, and this never relies on the engine
// tolerating the other order.
type engine struct {
	dsn string

	mu       sync.Mutex
	handle   *C.zeta_db_t // opened by the first connect, so sql.Open still does no I/O; nil before that
	sessions int          // sessions open over handle
	released bool         // the owner (the connector, or a one-shot Driver.Open) has let go
}

// connect opens a session, opening the engine first if this is the first.
func (e *engine) connect() (*conn, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.released {
		return nil, ErrClosed
	}
	if e.handle == nil {
		cdsn := C.CString(e.dsn)
		defer C.free(unsafe.Pointer(cdsn))
		var errMsg *C.char
		h := C.zeta_open(cdsn, &errMsg)
		if h == nil {
			return nil, consumeErrMsg(errMsg, ErrUnknown)
		}
		e.handle = h
		liveEngines.Add(1)
	}
	var errMsg *C.char
	s := C.zeta_connect(e.handle, &errMsg)
	if s == nil {
		return nil, consumeErrMsg(errMsg, ErrUnknown)
	}
	e.sessions++
	return &conn{handle: s, eng: e}, nil
}

// sessionClosed is called by conn.Close after its session handle is closed.
func (e *engine) sessionClosed() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sessions--
	e.closeIfDone()
}

// release is the owner letting go. Sessions still open keep the engine alive until the last one closes.
func (e *engine) release() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.released = true
	e.closeIfDone()
}

func (e *engine) closeIfDone() {
	if e.released && e.sessions == 0 && e.handle != nil {
		C.zeta_close(e.handle)
		e.handle = nil
		liveEngines.Add(-1)
	}
}

// connector is what sql.Open gets from Driver.OpenConnector: one engine for the sql.DB, shared by all of its
// connections. database/sql closes it (io.Closer) from DB.Close, after the idle connections and possibly while
// others are still checked out.
type connector struct {
	eng *engine
}

var (
	_ driver.Connector = (*connector)(nil)
	_ io.Closer        = (*connector)(nil)
)

func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cn, err := c.eng.connect()
	if err != nil {
		return nil, err
	}
	return cn, nil
}

func (*connector) Driver() driver.Driver { return Driver{} }

// Close lets go of the engine: it closes once the last connection over it has.
func (c *connector) Close() error {
	c.eng.release()
	return nil
}
