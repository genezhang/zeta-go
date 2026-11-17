package embedded

/*
#include <stdlib.h>
#include "zeta.h"
*/
import "C"

import (
	"strings"
	"unsafe"
)

// Connect opens a new session (connection) over the same engine as d.
//
// The storage engine is shared. The returned Database owns its session state:
// the branch selection (see SetBranch), the error slot and the changes
// counter, starting on the main branch. Branch selection belongs to a session,
// not to the engine, so two callers that share one Database share one branch
// pointer, and one caller's SetBranch silently retargets the other's
// statements. Give each concurrent caller (a worker, an agent, a request) its
// own session:
//
//	db.Exec("CREATE BRANCH agent_a")
//	a, _ := db.Connect()
//	a.SetBranch("agent_a")
//	a.Exec("INSERT INTO notes VALUES (1)") // lands on agent_a; db still reads main
//
// Close each session with Close. Close sessions before the Database they were
// opened from: the C API requires the original handle to be closed last.
//
// Connect takes d's lock, so it waits for an open *Rows on d to be closed
// (called from the goroutine that holds that Rows, it never returns).
func (d *Database) Connect() (*Database, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.handle == nil {
		return nil, ErrClosed
	}
	var errMsg *C.char
	h := C.zeta_connect(d.handle, &errMsg)
	if h == nil {
		return nil, consumeErrMsg(errMsg, ErrUnknown)
	}
	return &Database{handle: h}, nil
}

// SetBranch selects the database branch that following statements on d run
// against; "" returns to main.
//
// Create branches with db.Exec("CREATE BRANCH name") on a Database that has
// none selected ("MERGE BRANCH name" publishes a branch to main and consumes
// it; "DROP BRANCH name" discards it). The selection is sticky on d and applies
// to its statements, prepared statements and transactions. A transaction runs
// on the branch that was selected when it began, and keeps it: a SetBranch
// made while a Tx is open takes effect for d's later statements, not for that
// Tx. Select the branch before Begin all the same. SetBranch takes d's lock, so
// it waits for an open *Rows on d to be closed. The name is not checked here: a
// branch that does not exist, or that was dropped later, is reported by the
// next statement, and SetBranch("") recovers. A name that contains a NUL byte
// is refused, because the engine would read it as ending at the NUL and select
// a different branch.
func (d *Database) SetBranch(name string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.handle == nil {
		return ErrClosed
	}
	return setBranch(d.handle, name)
}

// setBranch wraps zeta_set_branch. The empty name goes to the engine as the
// empty string, which it treats as main.
func setBranch(h *C.zeta_db_t, name string) error {
	if strings.IndexByte(name, 0) >= 0 {
		return &Error{Kind: ErrUnknown, Message: "branch name contains a NUL byte"}
	}
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))

	var errMsg *C.char
	if rc := C.zeta_set_branch(h, cname, &errMsg); rc != C.ZETA_OK {
		return consumeErrMsg(errMsg, errorKindFromCode(int(rc)))
	}
	return nil
}

// BranchSelector is implemented by the connections this package's database/sql
// driver hands out. Reach it through sql.Conn.Raw:
//
//	conn, _ := db.Conn(ctx)
//	defer conn.Close()
//	err := conn.Raw(func(dc any) error {
//		return dc.(embedded.BranchSelector).SetBranch("agent_a")
//	})
//
// Statements run on that sql.Conn then use the branch. A connection returned
// to the pool goes back to main (the driver implements driver.SessionResetter),
// so a selection never leaks to the next user of a pooled connection.
type BranchSelector interface {
	SetBranch(name string) error
}
