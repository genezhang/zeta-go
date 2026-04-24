package embedded

import (
	"errors"
	"testing"
)

func TestTxCommit(t *testing.T) {
	db := openTestDB(t)
	mustExec(t, db, "CREATE TABLE t (id INTEGER, name TEXT)")

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec("INSERT INTO t VALUES ($1, $2)", 1, "alice"); err != nil {
		t.Fatalf("tx.Exec: %v", err)
	}
	if _, err := tx.Exec("INSERT INTO t VALUES ($1, $2)", 2, "bob"); err != nil {
		t.Fatalf("tx.Exec: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Verify the committed data is visible outside the transaction.
	rows, err := db.Query("SELECT COUNT(*) FROM t")
	if err != nil {
		t.Fatalf("count query: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("no row: %v", rows.Err())
	}
	var n int64
	if err := rows.Scan(&n); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if n != 2 {
		t.Errorf("row count: got %d, want 2", n)
	}
}

func TestTxRollback(t *testing.T) {
	db := openTestDB(t)
	mustExec(t, db, "CREATE TABLE t (id INTEGER)")

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := tx.Exec("INSERT INTO t VALUES ($1)", 1); err != nil {
		t.Fatalf("tx.Exec: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	// Verify no row was persisted.
	rows, err := db.Query("SELECT COUNT(*) FROM t")
	if err != nil {
		t.Fatalf("count query: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("no row: %v", rows.Err())
	}
	var n int64
	if err := rows.Scan(&n); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if n != 0 {
		t.Errorf("row count: got %d, want 0", n)
	}
}

func TestTxReadYourOwnWrites(t *testing.T) {
	db := openTestDB(t)
	mustExec(t, db, "CREATE TABLE t (id INTEGER, name TEXT)")

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec("INSERT INTO t VALUES ($1, $2)", 1, "alice"); err != nil {
		t.Fatalf("tx.Exec: %v", err)
	}

	// Read inside the same transaction should see the uncommitted write.
	rows, err := tx.Query("SELECT name FROM t WHERE id = $1", 1)
	if err != nil {
		t.Fatalf("tx.Query: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("expected row; err=%v", rows.Err())
	}
	var name string
	if err := rows.Scan(&name); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if name != "alice" {
		t.Errorf("got %q, want alice", name)
	}
}

func TestTxRollbackOnDeferAfterCommit(t *testing.T) {
	// Confirms the idiomatic pattern: defer tx.Rollback() after Commit
	// must not return an error or undo the commit.
	db := openTestDB(t)
	mustExec(t, db, "CREATE TABLE t (x INTEGER)")

	err := func() error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback() // should be a no-op after Commit

		if _, err := tx.Exec("INSERT INTO t VALUES ($1)", 42); err != nil {
			return err
		}
		return tx.Commit()
	}()
	if err != nil {
		t.Fatalf("tx func: %v", err)
	}

	// Data should be persisted.
	var n int64
	rows, err := db.Query("SELECT COUNT(*) FROM t")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	defer rows.Close()
	rows.Next()
	rows.Scan(&n)
	if n != 1 {
		t.Errorf("got %d rows, want 1", n)
	}
}

func TestTxCommitAfterCommit(t *testing.T) {
	db := openTestDB(t)
	mustExec(t, db, "CREATE TABLE t (x INTEGER)")

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := tx.Commit(); !errors.Is(err, ErrTxDone) {
		t.Errorf("second Commit: got %v, want ErrTxDone", err)
	}
	if _, err := tx.Exec("INSERT INTO t VALUES (1)"); !errors.Is(err, ErrTxDone) {
		t.Errorf("Exec after commit: got %v, want ErrTxDone", err)
	}
}

func TestTxConstraintError(t *testing.T) {
	db := openTestDB(t)
	mustExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY)")
	mustExec(t, db, "INSERT INTO t VALUES (1)")

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	// Duplicate PK — should raise a constraint error.
	_, err = tx.Exec("INSERT INTO t VALUES (1)")
	if err == nil {
		t.Fatal("expected constraint error")
	}
	var ze *Error
	if !errors.As(err, &ze) {
		t.Fatalf("got %T, want *Error", err)
	}
	if ze.Kind != ErrConstraint {
		t.Errorf("Kind=%v, want ErrConstraint", ze.Kind)
	}
}
