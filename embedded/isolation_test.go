package embedded

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// writeSkew runs the classic anomaly: each transaction reads the total, then
// zeroes a different row. "At least one row stays 1" holds for each alone and
// is broken if both commit.
func writeSkew(t *testing.T, level Isolation) (errA, errB error, zeroed int64) {
	t.Helper()
	db := openTestDB(t)
	mustExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY, n INTEGER)")
	mustExec(t, db, "INSERT INTO t VALUES (1, 1), (2, 1)")

	a, err := db.BeginWithIsolation(level)
	if err != nil {
		t.Fatalf("BeginWithIsolation: %v", err)
	}
	defer a.Rollback()
	b, err := db.BeginWithIsolation(level)
	if err != nil {
		t.Fatalf("BeginWithIsolation: %v", err)
	}
	defer b.Rollback()

	for _, tx := range []*Tx{a, b} {
		rows, err := tx.Query("SELECT SUM(n) FROM t")
		if err != nil {
			t.Fatalf("tx.Query: %v", err)
		}
		for rows.Next() {
		}
		rows.Close()
	}
	if _, err := a.Exec("UPDATE t SET n = 0 WHERE id = 1"); err != nil {
		t.Fatalf("a.Exec: %v", err)
	}
	if _, err := b.Exec("UPDATE t SET n = 0 WHERE id = 2"); err != nil {
		t.Fatalf("b.Exec: %v", err)
	}
	errA, errB = a.Commit(), b.Commit()

	rows, err := db.Query("SELECT COUNT(*) FROM t WHERE n = 0")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("no row: %v", rows.Err())
	}
	if err := rows.Scan(&zeroed); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return errA, errB, zeroed
}

func TestSnapshotAllowsWriteSkew(t *testing.T) {
	// The baseline, pinned so the contrast below means something.
	a, b, zeroed := writeSkew(t, IsolationSnapshot)
	if a != nil || b != nil || zeroed != 2 {
		t.Fatalf("snapshot should let both commit: errA=%v errB=%v zeroed=%d", a, b, zeroed)
	}
}

func TestSerializablePreventsWriteSkew(t *testing.T) {
	a, b, zeroed := writeSkew(t, IsolationSerializable)
	var failed []error
	for _, e := range []error{a, b} {
		if e != nil {
			failed = append(failed, e)
		}
	}
	if len(failed) != 1 {
		t.Fatalf("exactly one commit must fail: errA=%v errB=%v", a, b)
	}
	var ze *Error
	if !errors.As(failed[0], &ze) || ze.Kind != ErrConflict {
		t.Fatalf("want an *Error of Kind ErrConflict, got %#v", failed[0])
	}
	if zeroed != 1 {
		t.Fatalf("only the survivor's update may apply, zeroed=%d", zeroed)
	}
}

func TestBeginWithIsolationRejectsUnknownLevel(t *testing.T) {
	db := openTestDB(t)
	if tx, err := db.BeginWithIsolation(Isolation(42)); err == nil {
		tx.Rollback()
		t.Fatal("an unknown isolation level must be an error")
	}
}

// The level chosen at BeginTx is the one the engine runs the transaction at:
// raising it inside a snapshot transaction is an error, and a transaction that
// is already serializable accepts the same statement.
func TestDriverBeginTxHonoursSerializable(t *testing.T) {
	db, err := sql.Open(DriverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	const raise = "SET TRANSACTION ISOLATION LEVEL SERIALIZABLE"

	snap, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := snap.ExecContext(ctx, raise); err == nil {
		t.Error("raising to SERIALIZABLE inside a snapshot transaction must be an error")
	}
	snap.Rollback()

	ser, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ser.ExecContext(ctx, raise); err != nil {
		t.Errorf("a serializable transaction accepts the statement: %v", err)
	}
	ser.Rollback()

	for _, l := range []sql.IsolationLevel{sql.LevelReadCommitted, sql.LevelRepeatableRead} {
		tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: l})
		if err != nil {
			t.Errorf("%v should run at snapshot isolation: %v", l, err)
			continue
		}
		tx.Rollback()
	}
	if tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelLinearizable}); err == nil {
		tx.Rollback()
		t.Error("LevelLinearizable must be rejected, not silently weakened")
	}
}

func TestOpenModeQuartzWithHTAP(t *testing.T) {
	db, err := OpenMode(t.TempDir(), ModeQuartz|ModeFlagHTAP)
	if err != nil {
		t.Fatalf("OpenMode: %v", err)
	}
	defer db.Close()
	mustExec(t, db, "CREATE TABLE t (id INTEGER, v INTEGER)")
	mustExec(t, db, "INSERT INTO t VALUES (1, 10), (2, 20)")
	rows, err := db.Query("SELECT COUNT(*) FROM t")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var n int64
	if !rows.Next() || rows.Scan(&n) != nil || n != 2 {
		t.Fatalf("count: got %d, err %v", n, rows.Err())
	}
}

func TestOpenModeRejectsRetiredBTree(t *testing.T) {
	if db, err := OpenMode(t.TempDir(), 0); err == nil {
		db.Close()
		t.Fatal("the retired BTree backend (mode 0) must be rejected")
	}
}
