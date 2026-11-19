package embedded

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Sessions and branch selection (Database.Connect / SetBranch). The same
// assertions as crates/zeta-embedded/tests/session_sync.rs in zeta-embedded, so
// the binding is held to the contract the Rust crate pins.

// seededBranchDB is an in-memory database with t holding two rows on main.
func seededBranchDB(t *testing.T) *Database {
	t.Helper()
	db := openTestDB(t)
	for _, q := range []string{
		"CREATE TABLE t (id INTEGER PRIMARY KEY)",
		"INSERT INTO t VALUES (1)",
		"INSERT INTO t VALUES (2)",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return db
}

// session opens a session and closes it when the test ends, before the
// Database it came from (t.Cleanup runs last-in first-out).
func session(t *testing.T, db *Database) *Database {
	t.Helper()
	s, err := db.Connect()
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func countRows(t *testing.T, db *Database) int {
	t.Helper()
	rows, err := db.Query("SELECT id FROM t")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return n
}

func TestSessionsHoldIndependentBranches(t *testing.T) {
	db := seededBranchDB(t)
	mustExec(t, db, "CREATE BRANCH agent_a")
	mustExec(t, db, "CREATE BRANCH agent_b")
	a, b := session(t, db), session(t, db)
	if err := a.SetBranch("agent_a"); err != nil {
		t.Fatalf("SetBranch a: %v", err)
	}
	if err := b.SetBranch("agent_b"); err != nil {
		t.Fatalf("SetBranch b: %v", err)
	}

	mustExec(t, a, "INSERT INTO t VALUES (100)")
	mustExec(t, a, "INSERT INTO t VALUES (101)")
	mustExec(t, b, "INSERT INTO t VALUES (200)")

	if got := countRows(t, a); got != 4 {
		t.Fatalf("agent_a sees %d rows; want 4 (main's 2 + its two)", got)
	}
	if got := countRows(t, b); got != 3 {
		t.Fatalf("agent_b sees %d rows; want 3 (main's 2 + its one, not agent_a's)", got)
	}
	if got := countRows(t, db); got != 2 {
		t.Fatalf("the original handle sees %d rows; want 2 (main)", got)
	}

	if err := a.SetBranch(""); err != nil {
		t.Fatalf("clear a: %v", err)
	}
	if got := countRows(t, a); got != 2 {
		t.Fatalf("a after clearing sees %d rows; want 2", got)
	}
	if got := countRows(t, b); got != 3 {
		t.Fatalf("b is disturbed by a's change: %d rows; want 3", got)
	}
}

// Callers that share one Database share its selection; a session does not.
func TestSharedDatabaseSharesTheSelectionAndASessionDoesNot(t *testing.T) {
	db := seededBranchDB(t)
	mustExec(t, db, "CREATE BRANCH shared")
	s := session(t, db)
	if err := db.SetBranch("shared"); err != nil {
		t.Fatalf("SetBranch: %v", err)
	}
	mustExec(t, db, "INSERT INTO t VALUES (50)")
	if got := countRows(t, db); got != 3 {
		t.Fatalf("db sees %d rows; want 3", got)
	}
	if got := countRows(t, s); got != 2 {
		t.Fatalf("a session is affected by its parent's selection: %d rows; want 2", got)
	}
}

func TestUnknownBranchFailsAtTheNextStatementAndClearingRecovers(t *testing.T) {
	db := seededBranchDB(t)
	s := session(t, db)
	if err := s.SetBranch("no-such-branch"); err != nil {
		t.Fatalf("selection must not be validated eagerly: %v", err)
	}
	// Preparing does not resolve the branch; the first step does, so the error may come from Query or
	// from Rows. Close the rows either way: an open Rows holds the session's lock.
	rows, err := s.Query("SELECT id FROM t")
	if err == nil {
		for rows.Next() {
		}
		err = rows.Err()
		rows.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "no-such-branch") {
		t.Fatalf("a statement on an unknown branch: err = %v; want one naming the branch", err)
	}
	if err := s.SetBranch(""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := countRows(t, s); got != 2 {
		t.Fatalf("after clearing: %d rows; want 2", got)
	}
}

func TestBranchDDLNeedsASessionWithNoBranchSelected(t *testing.T) {
	db := seededBranchDB(t)
	mustExec(t, db, "CREATE BRANCH one")
	s := session(t, db)
	if err := s.SetBranch("one"); err != nil {
		t.Fatalf("SetBranch: %v", err)
	}
	if _, err := s.Exec("CREATE BRANCH two"); err == nil || !strings.Contains(err.Error(), "branch DDL must run on main") {
		t.Fatalf("CREATE BRANCH on a branch-selected session: err = %v; want \"branch DDL must run on main\"", err)
	}
	if err := s.SetBranch(""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	mustExec(t, s, "CREATE BRANCH two")
}

func TestTransactionOnASessionRunsOnItsBranchAndMerges(t *testing.T) {
	db := seededBranchDB(t)
	mustExec(t, db, "CREATE BRANCH txn_b")
	s := session(t, db)
	if err := s.SetBranch("txn_b"); err != nil {
		t.Fatalf("SetBranch: %v", err)
	}

	tx, err := s.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := tx.Exec("INSERT INTO t VALUES (400)"); err != nil {
		t.Fatalf("tx.Exec: %v", err)
	}
	rows, err := tx.Query("SELECT id FROM t")
	if err != nil {
		t.Fatalf("tx.Query: %v", err)
	}
	n := 0
	for rows.Next() {
		n++
	}
	rows.Close()
	if n != 3 {
		t.Fatalf("the transaction sees %d rows; want 3 (the branch)", n)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if got := countRows(t, db); got != 2 {
		t.Fatalf("the commit leaked to main: %d rows; want 2", got)
	}
	if got := countRows(t, s); got != 3 {
		t.Fatalf("the session sees %d rows; want 3", got)
	}

	if err := s.SetBranch(""); err != nil { // MERGE BRANCH, like CREATE BRANCH, runs with none selected
		t.Fatalf("clear: %v", err)
	}
	if conflicts := mergeBranch(t, s, "txn_b"); conflicts != 0 {
		t.Fatalf("MERGE BRANCH reported %d conflicts; want none", conflicts)
	}
	if got := countRows(t, db); got != 3 {
		t.Fatalf("after MERGE BRANCH main has %d rows; want 3", got)
	}
}

func TestClosedHandlesRefuseBranchCalls(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	s, err := db.Connect()
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBranch("x"); err != ErrClosed {
		t.Fatalf("SetBranch on a closed session = %v; want ErrClosed", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Connect(); err != ErrClosed {
		t.Fatalf("Connect on a closed database = %v; want ErrClosed", err)
	}
}

// database/sql: a branch selected on a pooled connection must not be inherited
// by the next user of that connection.
func TestDriverBranchSelectionDoesNotLeakThroughThePool(t *testing.T) {
	db := openSQLDB(t)
	db.SetMaxOpenConns(1) // one connection, so the same one is reused: that is what a reset is tested against
	sqlExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY)")
	sqlExec(t, db, "INSERT INTO t VALUES (1)")
	sqlExec(t, db, "CREATE BRANCH pooled")

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	if err := conn.Raw(func(dc any) error {
		bs, ok := dc.(BranchSelector)
		if !ok {
			t.Fatalf("driver connection %T does not implement BranchSelector", dc)
		}
		return bs.SetBranch("pooled")
	}); err != nil {
		t.Fatalf("Raw/SetBranch: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "INSERT INTO t VALUES (2)"); err != nil {
		t.Fatalf("insert on the branch: %v", err)
	}
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM t").Scan(&n); err != nil || n != 2 {
		t.Fatalf("on the branch: count = %d, %v; want 2", n, err)
	}
	if err := conn.Close(); err != nil { // back to the pool
		t.Fatalf("conn.Close: %v", err)
	}

	// The next user of the (only) pooled connection reads main.
	if err := db.QueryRow("SELECT COUNT(*) FROM t").Scan(&n); err != nil || n != 1 {
		t.Fatalf("after the pool reset: count = %d, %v; want 1 (main)", n, err)
	}
}

// mergeBranch runs MERGE BRANCH through Query (Exec drops rows) and returns the number of conflict rows.
func mergeBranch(t *testing.T, db *Database, name string) int {
	t.Helper()
	rows, err := db.Query("MERGE BRANCH " + name)
	if err != nil {
		t.Fatalf("MERGE BRANCH %s: %v", name, err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("MERGE BRANCH %s rows: %v", name, err)
	}
	return n
}

func value(t *testing.T, db *Database, id int) string {
	t.Helper()
	rows, err := db.Query("SELECT v FROM kv WHERE id = $1", id)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("row %d: not found (err %v)", id, rows.Err())
	}
	var v string
	if err := rows.Scan(&v); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return v
}

func seededKV(t *testing.T) *Database {
	t.Helper()
	db := openTestDB(t)
	mustExec(t, db, "CREATE TABLE kv (id INTEGER PRIMARY KEY, v TEXT)")
	mustExec(t, db, "INSERT INTO kv VALUES ($1, $2)", 1, "base1")
	mustExec(t, db, "INSERT INTO kv VALUES ($1, $2)", 2, "base2")
	return db
}

func TestBranchReadsMainAsOfTheFork(t *testing.T) {
	db := seededKV(t)
	mustExec(t, db, "CREATE BRANCH fork")
	mustExec(t, db, "UPDATE kv SET v = $1 WHERE id = $2", "main-after", 1)
	mustExec(t, db, "INSERT INTO kv VALUES ($1, $2)", 3, "main-new")
	mustExec(t, db, "DELETE FROM kv WHERE id = $1", 2)

	s := session(t, db)
	if err := s.SetBranch("fork"); err != nil {
		t.Fatal(err)
	}
	if got := value(t, s, 1); got != "base1" {
		t.Fatalf("row 1 on the branch = %q; want base1 (main's later update is invisible)", got)
	}
	if got := value(t, s, 2); got != "base2" {
		t.Fatalf("row 2 on the branch = %q; want base2 (main's later delete is invisible)", got)
	}
	rows, err := s.Query("SELECT v FROM kv WHERE id = $1", 3)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("row 3 (inserted on main after the fork) is visible on the branch; want it invisible")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
}

// MERGE BRANCH returns conflicts as rows; a conflicted merge merges nothing and leaves the branch alone, and a
// clean merge consumes the branch. Exec drops rows, which is why mergeBranch uses Query.
func TestMergeReportsConflictsAsRowsAndACleanMergeConsumesTheBranch(t *testing.T) {
	db := seededKV(t)
	mustExec(t, db, "CREATE BRANCH clash")
	mustExec(t, db, "UPDATE kv SET v = $1 WHERE id = $2", "main", 1)
	s := session(t, db)
	if err := s.SetBranch("clash"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, "UPDATE kv SET v = $1 WHERE id = $2", "branch", 1)
	mustExec(t, s, "UPDATE kv SET v = $1 WHERE id = $2", "branch2", 2)
	mustExec(t, s, "INSERT INTO kv VALUES ($1, $2)", 3, "new")
	if err := s.SetBranch(""); err != nil {
		t.Fatal(err)
	}

	if n := mergeBranch(t, s, "clash"); n != 1 {
		t.Fatalf("MERGE BRANCH reported %d conflict rows; want 1", n)
	}
	if got := value(t, db, 1); got != "main" {
		t.Fatalf("main row 1 = %q; want main", got)
	}
	if got := value(t, db, 2); got != "base2" {
		t.Fatalf("main row 2 = %q; want base2 (nothing was merged)", got)
	}
	if err := s.SetBranch("clash"); err != nil {
		t.Fatal(err)
	}
	if got := value(t, s, 2); got != "branch2" {
		t.Fatalf("the branch lost its write: row 2 = %q; want branch2", got)
	}
	if err := s.SetBranch(""); err != nil {
		t.Fatal(err)
	}

	mustExec(t, db, "CREATE BRANCH clean")
	if err := s.SetBranch("clean"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, "INSERT INTO kv VALUES ($1, $2)", 4, "clean")
	if err := s.SetBranch(""); err != nil {
		t.Fatal(err)
	}
	if n := mergeBranch(t, s, "clean"); n != 0 {
		t.Fatalf("a clean MERGE BRANCH reported %d conflict rows; want 0", n)
	}
	if got := value(t, db, 4); got != "clean" {
		t.Fatalf("main row 4 = %q; want clean", got)
	}
	if err := s.SetBranch("clean"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Query("SELECT id FROM kv")
	if err == nil {
		for rows.Next() {
		}
		err = rows.Err()
		rows.Close()
	}
	var zerr *Error
	if !errors.As(err, &zerr) || zerr.Kind != ErrNotFound || !strings.Contains(err.Error(), `branch "clean" does not exist`) {
		t.Fatalf("reading a merged branch: err = %v; want ErrNotFound naming the consumed branch", err)
	}
	if err := s.SetBranch(""); err != nil {
		t.Fatal(err)
	}
}

func TestBranchDDLNeedsMainAndADroppedBranchNeverReadsMain(t *testing.T) {
	db := seededKV(t)
	mustExec(t, db, "CREATE BRANCH held")
	s := session(t, db)
	if err := s.SetBranch("held"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"CREATE BRANCH other", "MERGE BRANCH held", "DROP BRANCH held"} {
		rows, err := s.Query(q)
		if err == nil {
			for rows.Next() {
			}
			err = rows.Err()
			rows.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "must run on main") {
			t.Fatalf("%s on a branch-selected session: err = %v; want one that says it must run on main", q, err)
		}
	}
	mustExec(t, db, "DROP BRANCH held")
	rows, err := s.Query("SELECT id FROM kv")
	if err == nil {
		for rows.Next() {
		}
		err = rows.Err()
		rows.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "held") {
		t.Fatalf("reading a dropped branch: err = %v; want one naming it, not a fall back to main", err)
	}
}

func TestNewSessionStartsOnMainWhateverItsParentSelected(t *testing.T) {
	db := seededBranchDB(t)
	mustExec(t, db, "CREATE BRANCH parent_sel")
	if err := db.SetBranch("parent_sel"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "INSERT INTO t VALUES (77)")
	child := session(t, db)
	if got := countRows(t, db); got != 3 {
		t.Fatalf("the parent reads %d rows; want 3 (its branch)", got)
	}
	if got := countRows(t, child); got != 2 {
		t.Fatalf("the child reads %d rows; want 2 (main)", got)
	}
}

// The README says an open *Tx does not hold the database lock (it locks per call), a transaction keeps the branch
// it began on, and only an open *Rows holds the lock. Each of those is pinned here.
func TestATransactionKeepsItsBranchWhenTheHandleReselectsMidway(t *testing.T) {
	db := seededBranchDB(t)
	mustExec(t, db, "CREATE BRANCH keep")
	s := session(t, db)
	if err := s.SetBranch("keep"); err != nil {
		t.Fatal(err)
	}
	tx, err := s.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- s.SetBranch("") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SetBranch with a Tx open: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SetBranch blocked while a Tx was open; an open Tx must not hold the database lock between its calls")
	}

	if _, err := tx.Exec("INSERT INTO t VALUES (500)"); err != nil {
		t.Fatalf("tx.Exec: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := countRows(t, s); got != 2 { // s now reads main
		t.Fatalf("after the commit the handle (on main) sees %d rows; want 2: the Tx wrote to main", got)
	}
	if err := s.SetBranch("keep"); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, s); got != 3 {
		t.Fatalf("the branch the Tx began on has %d rows; want 3", got)
	}
}

func TestAnOpenRowsHoldsTheLockThatSetBranchAndConnectNeed(t *testing.T) {
	db := seededBranchDB(t)
	rows, err := db.Query("SELECT id FROM t")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close() // idempotent; on a failure below it frees the lock so the cleanup's db.Close cannot deadlock

	setDone := make(chan error, 1)
	type connected struct {
		s   *Database
		err error
	}
	connDone := make(chan connected, 1)
	go func() { setDone <- db.SetBranch("") }()
	go func() {
		s, err := db.Connect()
		connDone <- connected{s, err}
	}()
	select {
	case <-setDone:
		t.Fatal("SetBranch returned while a Rows was open on the same Database")
	case <-connDone:
		t.Fatal("Connect returned while a Rows was open on the same Database")
	case <-time.After(300 * time.Millisecond):
	}

	rows.Close()
	for i := 0; i < 2; i++ {
		select {
		case err := <-setDone:
			if err != nil {
				t.Fatalf("SetBranch after the Rows closed: %v", err)
			}
		case c := <-connDone:
			if c.err != nil {
				t.Fatalf("Connect after the Rows closed: %v", c.err)
			}
			defer c.s.Close() // before db: t.Cleanup runs after this test function returns
		case <-time.After(5 * time.Second):
			t.Fatal("SetBranch / Connect still blocked after the Rows was closed")
		}
	}
}

// Query runs MERGE BRANCH when the rows are first stepped, so closing them without a Next leaves the merge
// undone and the branch intact. The README tells callers to iterate.
func TestMergeRunsWhenTheRowsAreFirstSteppedNotWhenQueryReturns(t *testing.T) {
	db := seededBranchDB(t)
	mustExec(t, db, "CREATE BRANCH lazy")
	s := session(t, db)
	if err := s.SetBranch("lazy"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, "INSERT INTO t VALUES (9)")
	if err := s.SetBranch(""); err != nil {
		t.Fatal(err)
	}

	rows, err := s.Query("MERGE BRANCH lazy")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := countRows(t, db); got != 2 {
		t.Fatalf("main has %d rows after Query+Close without Next; want 2 (the merge has not run)", got)
	}

	if n := mergeBranch(t, s, "lazy"); n != 0 { // iterating runs it
		t.Fatalf("MERGE BRANCH reported %d conflicts; want 0", n)
	}
	if got := countRows(t, db); got != 3 {
		t.Fatalf("main has %d rows after iterating the merge; want 3", got)
	}
}

// The columns the README documents for a conflict row, and that Exec reports a conflicted merge as success.
func TestMergeConflictRowsCarryTheDocumentedColumnsAndExecHidesThem(t *testing.T) {
	db := seededKV(t)
	mustExec(t, db, "CREATE BRANCH clash")
	mustExec(t, db, "UPDATE kv SET v = $1 WHERE id = $2", "main", 1)
	s := session(t, db)
	if err := s.SetBranch("clash"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, "UPDATE kv SET v = $1 WHERE id = $2", "branch", 1)
	if err := s.SetBranch(""); err != nil {
		t.Fatal(err)
	}

	rows, err := s.Query("MERGE BRANCH clash")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !rows.Next() {
		rows.Close()
		t.Fatalf("no conflict row; err = %v", rows.Err())
	}
	var table, pk, op, base string
	if err := rows.Scan(&table, &pk, &op, &base); err != nil {
		rows.Close()
		t.Fatalf("Scan(4 columns): %v", err)
	}
	cols := rows.Columns()
	rows.Close()
	if strings.Join(cols, ",") != "table_name,pk,branch_op,base_change" {
		t.Fatalf("conflict columns = %v; want table_name, pk, branch_op, base_change", cols)
	}
	if table != "public.kv" || pk != "1" || op == "" || base == "" {
		t.Fatalf("conflict row = %q %q %q %q; want public.kv, 1, then a branch_op and a base_change", table, pk, op, base)
	}

	// Exec drops the rows: the same conflicted merge "succeeds" and merges nothing.
	if _, err := s.Exec("MERGE BRANCH clash"); err != nil {
		t.Fatalf("Exec of a conflicted MERGE BRANCH: %v; the README says it succeeds", err)
	}
	if got := value(t, db, 1); got != "main" {
		t.Fatalf("main row 1 = %q; want main (nothing merged)", got)
	}
	if n := mergeBranch(t, s, "clash"); n != 1 {
		t.Fatalf("the branch should still conflict after the Exec: %d conflict rows; want 1", n)
	}
}

// A name with a NUL would reach the engine as a C string cut at the NUL, selecting a different branch.
func TestABranchNameWithANulByteIsRefusedAndKeepsTheSelection(t *testing.T) {
	db := seededBranchDB(t)
	mustExec(t, db, "CREATE BRANCH good")
	s := session(t, db)
	if err := s.SetBranch("good"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, "INSERT INTO t VALUES (77)")

	for _, name := range []string{"good\x00junk", "\x00", "\x00good"} {
		if err := s.SetBranch(name); err == nil || !strings.Contains(err.Error(), "NUL") {
			t.Fatalf("SetBranch(%q) = %v; want an error about the NUL byte", name, err)
		}
	}
	if got := countRows(t, s); got != 3 {
		t.Fatalf("after the refused names the session sees %d rows; want 3 (still on good)", got)
	}

	// The database/sql connection takes the same path.
	sdb := openSQLDB(t)
	conn, err := sdb.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer conn.Close()
	err = conn.Raw(func(dc any) error { return dc.(BranchSelector).SetBranch("a\x00b") })
	if err == nil || !strings.Contains(err.Error(), "NUL") {
		t.Fatalf("driver SetBranch with a NUL = %v; want an error about the NUL byte", err)
	}
}
