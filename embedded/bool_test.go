package embedded

import "testing"

// TestBoolParamRoundTrip verifies that a bound Go bool, inserted into a BOOLEAN
// column, reads back as a SQL boolean (true/false) rather than an integer
// (1/0). This exercises the zeta_bind_bool path: scanning into *any surfaces a
// real Go bool only when the column round-trips as ZETA_TYPE_BOOL.
func TestBoolParamRoundTrip(t *testing.T) {
	db := openTestDB(t)
	mustExec(t, db, "CREATE TABLE t (id INTEGER, flag BOOLEAN)")
	mustExec(t, db, "INSERT INTO t VALUES ($1, $2)", 1, true)
	mustExec(t, db, "INSERT INTO t VALUES ($1, $2)", 2, false)

	rows, err := db.Query("SELECT flag FROM t ORDER BY id")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	var got []any
	for rows.Next() {
		var v any
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d rows; want 2", len(got))
	}
	if got[0] != true {
		t.Fatalf("row 1 = %#v (%T); want true (bool)", got[0], got[0])
	}
	if got[1] != false {
		t.Fatalf("row 2 = %#v (%T); want false (bool)", got[1], got[1])
	}
}
