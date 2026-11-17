package embedded

import (
	"context"
	"strings"
	"testing"
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
	if _, err := s.Exec("CREATE BRANCH two"); err == nil || !strings.Contains(err.Error(), "main") {
		t.Fatalf("CREATE BRANCH on a branch-selected session: err = %v; want one that mentions main", err)
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
	db := openSQLDB(t) // one connection, so the same one is reused
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
	if err == nil || !strings.Contains(err.Error(), "clean") {
		t.Fatalf("reading a merged branch: err = %v; want one naming the consumed branch", err)
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
