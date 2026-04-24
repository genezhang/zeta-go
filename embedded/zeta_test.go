package embedded

import (
	"errors"
	"testing"
)

func TestVersion(t *testing.T) {
	v := Version()
	if v == "" {
		t.Fatal("Version() returned empty string")
	}
	t.Logf("zeta version: %s", v)
}

// openTestDB opens an in-memory database for a single test, registering
// Close with t.Cleanup so it runs even if the test fails mid-way.
func openTestDB(t *testing.T) *Database {
	t.Helper()
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestOpenMemoryClose(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Idempotent.
	if err := db.Close(); err != nil {
		t.Fatalf("Close (second call): %v", err)
	}
}

func TestOpenColonMemory(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	if _, err := db.Exec("CREATE TABLE t (x INTEGER)"); err != nil {
		t.Fatalf("Exec CREATE: %v", err)
	}
}

func TestExecDDL(t *testing.T) {
	db := openTestDB(t)
	res, err := db.Exec("CREATE TABLE kv (k TEXT PRIMARY KEY, v TEXT)")
	if err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if got := res.RowsAffected(); got != 0 {
		t.Errorf("DDL RowsAffected: got %d, want 0", got)
	}
}

func TestExecInsertAffected(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec("CREATE TABLE t (id INTEGER, name TEXT)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	res, err := db.Exec("INSERT INTO t VALUES ($1, $2)", 1, "alice")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if got := res.RowsAffected(); got != 1 {
		t.Errorf("RowsAffected: got %d, want 1", got)
	}
}

func TestExecUpdateDelete(t *testing.T) {
	db := openTestDB(t)
	mustExec(t, db, "CREATE TABLE t (id INTEGER, name TEXT)")
	mustExec(t, db, "INSERT INTO t VALUES ($1, $2)", 1, "alice")
	mustExec(t, db, "INSERT INTO t VALUES ($1, $2)", 2, "bob")
	mustExec(t, db, "INSERT INTO t VALUES ($1, $2)", 3, "carol")

	res, err := db.Exec("UPDATE t SET name = $1 WHERE id > $2", "updated", 1)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := res.RowsAffected(); got != 2 {
		t.Errorf("UPDATE RowsAffected: got %d, want 2", got)
	}

	res, err = db.Exec("DELETE FROM t WHERE id = $1", 2)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := res.RowsAffected(); got != 1 {
		t.Errorf("DELETE RowsAffected: got %d, want 1", got)
	}
}

func TestQueryBasic(t *testing.T) {
	db := openTestDB(t)
	mustExec(t, db, "CREATE TABLE t (id INTEGER, name TEXT)")
	mustExec(t, db, "INSERT INTO t VALUES ($1, $2)", 1, "alice")

	rows, err := db.Query("SELECT id, name FROM t")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	if !rows.Next() {
		t.Fatalf("expected a row; Err=%v", rows.Err())
	}
	var id int64
	var name string
	if err := rows.Scan(&id, &name); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if id != 1 || name != "alice" {
		t.Errorf("got id=%d name=%q; want 1 alice", id, name)
	}
	if rows.Next() {
		t.Fatal("expected no more rows")
	}
	if err := rows.Err(); err != nil {
		t.Errorf("Err after exhaustion: %v", err)
	}
}

func TestQueryMultiRow(t *testing.T) {
	db := openTestDB(t)
	mustExec(t, db, "CREATE TABLE t (n INTEGER)")
	for i := 1; i <= 5; i++ {
		mustExec(t, db, "INSERT INTO t VALUES ($1)", i)
	}

	rows, err := db.Query("SELECT n FROM t ORDER BY n")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	var got []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 5 {
		t.Errorf("got %d rows; want 5", len(got))
	}
	for i, v := range got {
		if v != int64(i+1) {
			t.Errorf("row %d: got %d, want %d", i, v, i+1)
		}
	}
}

func TestQueryParams(t *testing.T) {
	db := openTestDB(t)
	mustExec(t, db, "CREATE TABLE t (id INTEGER, name TEXT)")
	mustExec(t, db, "INSERT INTO t VALUES ($1, $2)", 1, "alice")
	mustExec(t, db, "INSERT INTO t VALUES ($1, $2)", 2, "bob")
	mustExec(t, db, "INSERT INTO t VALUES ($1, $2)", 3, "carol")

	rows, err := db.Query("SELECT name FROM t WHERE id = $1", 2)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	if !rows.Next() {
		t.Fatalf("expected a row; err=%v", rows.Err())
	}
	var name string
	if err := rows.Scan(&name); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if name != "bob" {
		t.Errorf("got %q, want bob", name)
	}
}

func TestScanIntoAny(t *testing.T) {
	db := openTestDB(t)
	mustExec(t, db, "CREATE TABLE t (i BIGINT, f DOUBLE PRECISION, s TEXT)")
	mustExec(t, db, "INSERT INTO t VALUES ($1, $2, $3)",
		int64(42), 3.14, "hello")

	rows, err := db.Query("SELECT i, f, s FROM t")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	if !rows.Next() {
		t.Fatalf("expected row; err=%v", rows.Err())
	}
	var i, f, s any
	if err := rows.Scan(&i, &f, &s); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if iv, ok := i.(int64); !ok || iv != 42 {
		t.Errorf("i: got %v (%T), want int64(42)", i, i)
	}
	if fv, ok := f.(float64); !ok || fv != 3.14 {
		t.Errorf("f: got %v (%T), want float64(3.14)", f, f)
	}
	if sv, ok := s.(string); !ok || sv != "hello" {
		t.Errorf("s: got %v (%T), want \"hello\"", s, s)
	}
}

// TestBytea documents Zeta's BYTEA column behaviour: values are returned
// to the cursor API as hex-encoded TEXT (\x010203), matching Postgres
// text protocol semantics. Scan into *string to read them verbatim, or
// decode the hex manually. Binding []byte on INSERT works normally.
func TestBytea(t *testing.T) {
	db := openTestDB(t)
	mustExec(t, db, "CREATE TABLE t (b BYTEA)")
	mustExec(t, db, "INSERT INTO t VALUES ($1)", []byte{0x01, 0x02, 0x03})

	rows, err := db.Query("SELECT b FROM t")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("no row: %v", rows.Err())
	}
	var hex string
	if err := rows.Scan(&hex); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if hex != `\x010203` {
		t.Errorf("got %q, want %q", hex, `\x010203`)
	}
}

func TestQueryNoRows(t *testing.T) {
	db := openTestDB(t)
	mustExec(t, db, "CREATE TABLE t (id INTEGER)")
	rows, err := db.Query("SELECT id FROM t WHERE id = $1", 999)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("unexpected row")
	}
	if err := rows.Err(); err != nil {
		t.Errorf("Err: %v", err)
	}
}

func TestColumns(t *testing.T) {
	db := openTestDB(t)
	mustExec(t, db, "CREATE TABLE t (id INTEGER, name TEXT)")
	mustExec(t, db, "INSERT INTO t VALUES ($1, $2)", 1, "x")

	rows, err := db.Query("SELECT id, name FROM t")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	// Column metadata is populated once the first row is stepped.
	if !rows.Next() {
		t.Fatalf("expected a row; err=%v", rows.Err())
	}
	cols := rows.Columns()
	if len(cols) != 2 || cols[0] != "id" || cols[1] != "name" {
		t.Errorf("Columns: got %v, want [id name]", cols)
	}
}

func TestScanWrongColumnCount(t *testing.T) {
	db := openTestDB(t)
	mustExec(t, db, "CREATE TABLE t (a INTEGER, b INTEGER)")
	mustExec(t, db, "INSERT INTO t VALUES ($1, $2)", 1, 2)

	rows, err := db.Query("SELECT a, b FROM t")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	if !rows.Next() {
		t.Fatalf("expected row; err=%v", rows.Err())
	}
	var x int64
	if err := rows.Scan(&x); err == nil {
		t.Fatal("expected error for wrong dest count")
	}
}

func TestParseError(t *testing.T) {
	db := openTestDB(t)
	_, err := db.Exec("NOT VALID SQL AT ALL")
	if err == nil {
		t.Fatal("expected error")
	}
	var ze *Error
	if !errors.As(err, &ze) {
		t.Fatalf("expected *Error, got %T: %v", err, err)
	}
	if ze.Kind != ErrParse {
		t.Errorf("got Kind=%v, want ErrParse", ze.Kind)
	}
}

func TestClosedDatabase(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE t (x INTEGER)"); !errors.Is(err, ErrClosed) {
		t.Errorf("Exec on closed: got %v, want ErrClosed", err)
	}
	if _, err := db.Query("SELECT 1"); !errors.Is(err, ErrClosed) {
		t.Errorf("Query on closed: got %v, want ErrClosed", err)
	}
}

func TestParamBool(t *testing.T) {
	db := openTestDB(t)
	mustExec(t, db, "CREATE TABLE t (b BOOLEAN)")
	mustExec(t, db, "INSERT INTO t VALUES ($1)", true)
	mustExec(t, db, "INSERT INTO t VALUES ($1)", false)

	rows, err := db.Query("SELECT b FROM t ORDER BY b")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	var values []bool
	for rows.Next() {
		var b bool
		if err := rows.Scan(&b); err != nil {
			t.Fatalf("scan: %v", err)
		}
		values = append(values, b)
	}
	if len(values) != 2 {
		t.Fatalf("got %d rows, want 2", len(values))
	}
}

func TestNullValues(t *testing.T) {
	db := openTestDB(t)
	mustExec(t, db, "CREATE TABLE t (x INTEGER, y TEXT)")
	mustExec(t, db, "INSERT INTO t VALUES ($1, $2)", nil, nil)

	rows, err := db.Query("SELECT x, y FROM t")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	if !rows.Next() {
		t.Fatalf("expected row; err=%v", rows.Err())
	}
	var x, y any
	if err := rows.Scan(&x, &y); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if x != nil || y != nil {
		t.Errorf("got x=%v y=%v, want both nil", x, y)
	}
}

// mustExec is a test helper that fails the test if Exec returns an error.
func mustExec(t *testing.T, db *Database, sql string, args ...any) Result {
	t.Helper()
	r, err := db.Exec(sql, args...)
	if err != nil {
		t.Fatalf("Exec(%q): %v", sql, err)
	}
	return r
}
