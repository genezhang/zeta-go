package embedded

import (
	"testing"
	"time"
)

// Row-ABI codes 10-13 (zeta-embedded#86): TIMESTAMP, TIMESTAMPTZ, DATE and TIME
// scan as time.Time through database/sql. Before this reader knew the codes,
// columnToAny's default arm turned them into nil.
func TestDriverTemporalColumnsScanAsTime(t *testing.T) {
	db := openSQLDB(t)
	sqlExec(t, db, "CREATE TABLE t (ts TIMESTAMP, tz TIMESTAMPTZ, d DATE, tm TIME, n NUMERIC(10,2))")
	sqlExec(t, db, "INSERT INTO t VALUES ('2026-01-15 12:34:56.5', '2026-01-15 12:34:56+02', "+
		"'2026-01-15', '12:34:56.25', 3.5)")

	var ts, tz, d, tm time.Time
	var n any
	if err := db.QueryRow("SELECT ts, tz, d, tm, n FROM t").Scan(&ts, &tz, &d, &tm, &n); err != nil {
		t.Fatalf("scan: %v", err)
	}
	checks := []struct {
		name      string
		got, want time.Time
	}{
		{"ts", ts, time.Date(2026, 1, 15, 12, 34, 56, 500_000_000, time.UTC)},
		{"tz", tz, time.Date(2026, 1, 15, 10, 34, 56, 0, time.UTC)},
		{"d", d, time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)},
		{"tm", tm, time.Date(0, 1, 1, 12, 34, 56, 250_000_000, time.UTC)},
	}
	for _, c := range checks {
		if !c.got.Equal(c.want) {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	// A typed-text code (NUMERIC) reads as its text, not nil.
	if n != "3.50" {
		t.Errorf("n = %#v, want \"3.50\"", n)
	}
}
