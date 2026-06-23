package embedded

import (
	"database/sql"
	"errors"
	"testing"
)

// openSQLDB opens a *sql.DB through the registered "zeta" driver against a
// shared in-memory database (MaxOpenConns(1) so every pooled connection sees
// the same in-memory data).
func openSQLDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("zeta", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func sqlExec(t *testing.T, db *sql.DB, query string, args ...any) sql.Result {
	t.Helper()
	res, err := db.Exec(query, args...)
	if err != nil {
		t.Fatalf("Exec %q: %v", query, err)
	}
	return res
}

func TestDriverExecAndQueryRow(t *testing.T) {
	db := openSQLDB(t)
	sqlExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT, score REAL)")

	res := sqlExec(t, db, "INSERT INTO t VALUES ($1, $2, $3)", 1, "alice", 9.5)
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("RowsAffected = %d, %v; want 1, nil", n, err)
	}

	var name string
	var score float64
	if err := db.QueryRow("SELECT name, score FROM t WHERE id = $1", 1).Scan(&name, &score); err != nil {
		t.Fatalf("QueryRow.Scan: %v", err)
	}
	if name != "alice" || score != 9.5 {
		t.Fatalf("got (%q, %v); want (alice, 9.5)", name, score)
	}
}

func TestDriverQueryRowNoRows(t *testing.T) {
	db := openSQLDB(t)
	sqlExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY)")
	var id int
	err := db.QueryRow("SELECT id FROM t WHERE id = 99").Scan(&id)
	if err != sql.ErrNoRows {
		t.Fatalf("got %v; want sql.ErrNoRows", err)
	}
}

func TestDriverRowsIterationAndColumns(t *testing.T) {
	db := openSQLDB(t)
	sqlExec(t, db, "CREATE TABLE t (id INTEGER, name TEXT)")
	sqlExec(t, db, "INSERT INTO t VALUES (1,'a'),(2,'b'),(3,'c')")

	rows, err := db.Query("SELECT id, name FROM t ORDER BY id")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("Columns: %v", err)
	}
	if len(cols) != 2 || cols[0] != "id" || cols[1] != "name" {
		t.Fatalf("Columns = %v; want [id name]", cols)
	}

	n := 0
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		n++
		if id != n {
			t.Fatalf("row %d: id = %d; want %d", n, id, n)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if n != 3 {
		t.Fatalf("iterated %d rows; want 3", n)
	}
}

func TestDriverPreparedReuse(t *testing.T) {
	db := openSQLDB(t)
	sqlExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)")

	ins, err := db.Prepare("INSERT INTO t VALUES ($1, $2)")
	if err != nil {
		t.Fatalf("Prepare insert: %v", err)
	}
	defer ins.Close()
	for i, nm := range []string{"a", "b", "c"} {
		if _, err := ins.Exec(i+1, nm); err != nil {
			t.Fatalf("ins.Exec(%d): %v", i+1, err)
		}
	}

	sel, err := db.Prepare("SELECT name FROM t WHERE id = $1")
	if err != nil {
		t.Fatalf("Prepare select: %v", err)
	}
	defer sel.Close()
	for i, want := range []string{"a", "b", "c"} {
		var got string
		if err := sel.QueryRow(i + 1).Scan(&got); err != nil {
			t.Fatalf("sel.QueryRow(%d): %v", i+1, err)
		}
		if got != want {
			t.Fatalf("id %d: got %q; want %q", i+1, got, want)
		}
	}
}

func TestDriverTransactionCommit(t *testing.T) {
	db := openSQLDB(t)
	sqlExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY)")

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := tx.Exec("INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("tx.Exec: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := driverCount(t, db); got != 1 {
		t.Fatalf("after commit count = %d; want 1", got)
	}
}

func TestDriverTransactionRollback(t *testing.T) {
	db := openSQLDB(t)
	sqlExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY)")

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := tx.Exec("INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("tx.Exec: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := driverCount(t, db); got != 0 {
		t.Fatalf("after rollback count = %d; want 0", got)
	}
}

func TestDriverParamTypes(t *testing.T) {
	db := openSQLDB(t)
	sqlExec(t, db, "CREATE TABLE t (i BIGINT, f REAL, s TEXT, b BYTEA)")
	sqlExec(t, db, "INSERT INTO t VALUES ($1, $2, $3, $4)", int64(42), 3.5, "hi", []byte{1, 2, 3})

	var i int64
	var f float64
	var s string
	var b []byte
	if err := db.QueryRow("SELECT i, f, s, b FROM t").Scan(&i, &f, &s, &b); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if i != 42 || f != 3.5 || s != "hi" || string(b) != "\x01\x02\x03" {
		t.Fatalf("got (%d, %v, %q, %v)", i, f, s, b)
	}
}

// TestDriverTxStmtReuse covers reusing an autocommit-prepared statement inside
// a transaction via Tx.Stmt: the statement must re-bind to the transaction
// (and roll back with it), then revert to autocommit afterward.
func TestDriverTxStmtReuse(t *testing.T) {
	db := openSQLDB(t)
	sqlExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY)")

	s, err := db.Prepare("INSERT INTO t VALUES ($1)") // prepared in autocommit
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer s.Close()
	if _, err := s.Exec(1); err != nil {
		t.Fatalf("autocommit Exec: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := tx.Stmt(s).Exec(2); err != nil {
		t.Fatalf("tx.Stmt Exec: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := driverCount(t, db); got != 1 {
		t.Fatalf("count = %d; want 1 (row 2 must roll back with the txn, not autocommit)", got)
	}

	if _, err := s.Exec(3); err != nil { // same stmt, back in autocommit
		t.Fatalf("post-txn Exec: %v", err)
	}
	if got := driverCount(t, db); got != 2 {
		t.Fatalf("count = %d; want 2", got)
	}
}

func TestDriverNullAndBoolExpr(t *testing.T) {
	db := openSQLDB(t)
	sqlExec(t, db, "CREATE TABLE t (id INTEGER, note TEXT)")
	sqlExec(t, db, "INSERT INTO t VALUES ($1, $2)", 1, nil) // bind SQL NULL

	var note sql.NullString
	if err := db.QueryRow("SELECT note FROM t WHERE id = 1").Scan(&note); err != nil {
		t.Fatalf("Scan NULL: %v", err)
	}
	if note.Valid {
		t.Fatalf("note = %q; want NULL", note.String)
	}

	// A boolean expression yields a BOOLEAN result, scanned into *bool.
	var b bool
	if err := db.QueryRow("SELECT 1 < 2").Scan(&b); err != nil {
		t.Fatalf("Scan bool: %v", err)
	}
	if !b {
		t.Fatal("1 < 2 scanned as false")
	}
}

func TestDriverRowsAffectedAndLastInsertId(t *testing.T) {
	db := openSQLDB(t)
	sqlExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY, v INTEGER)")
	sqlExec(t, db, "INSERT INTO t VALUES (1,10),(2,20),(3,30)")

	res, err := db.Exec("UPDATE t SET v = v + 1 WHERE id <= 2")
	if err != nil {
		t.Fatalf("UPDATE: %v", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 2 {
		t.Fatalf("RowsAffected = %d, %v; want 2, nil", n, err)
	}
	if _, err := res.LastInsertId(); err == nil {
		t.Fatal("LastInsertId: expected an unsupported error")
	}
}

func TestDriverErrorPropagation(t *testing.T) {
	db := openSQLDB(t)
	sqlExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY)")
	sqlExec(t, db, "INSERT INTO t VALUES (1)")

	_, err := db.Exec("INSERT INTO t VALUES (1)") // primary-key conflict
	if err == nil {
		t.Fatal("expected a constraint error")
	}
	var ze *Error
	if !errors.As(err, &ze) {
		t.Fatalf("error is not *embedded.Error: %v (%T)", err, err)
	}
	t.Logf("propagated error: kind=%v message=%q", ze.Kind, ze.Message)
}

func TestDriverVectorParam(t *testing.T) {
	db := openSQLDB(t)
	sqlExec(t, db, "CREATE TABLE t (id INTEGER, embedding VECTOR(3))")
	// []float32 reaches the driver via CheckNamedValue (the default converter
	// would otherwise reject it) and binds as a Zeta VECTOR.
	sqlExec(t, db, "INSERT INTO t VALUES ($1, $2)", 1, []float32{1, 2, 3})

	var v string // VECTOR is surfaced as text ("[1,2,3]")
	if err := db.QueryRow("SELECT embedding FROM t WHERE id = $1", 1).Scan(&v); err != nil {
		t.Fatalf("Scan vector: %v", err)
	}
	if v == "" {
		t.Fatal("vector round-tripped empty")
	}
}

func driverCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM t").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}
