//go:build zeta_dev

// End-to-end smoke test for the embedded dev-listeners feature.
// Starts an in-process Zeta database, opens a pgwire listener on a
// loopback port, then shells out to `psql` to run an actual query
// over the wire. Locks in the path that motivated RFC #576.

package embedded

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// pickEphemeralPort returns a TCP port the OS just assigned to a
// loopback listener and immediately closes — relies on the OS not
// reusing the port within the next few milliseconds. Sufficient for a
// single test process; not a general race-free port allocator.
func pickEphemeralPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick ephemeral port: %v", err)
	}
	addr := l.Addr().(*net.TCPAddr)
	_ = l.Close()
	return addr.Port
}

func waitTCPReady(t *testing.T, addr string, deadline time.Duration) {
	t.Helper()
	until := time.Now().Add(deadline)
	for time.Now().Before(until) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("listener at %s never became reachable within %s", addr, deadline)
}

// startPgwireOnEphemeralPort picks a port and starts a pgwire listener,
// retrying a handful of times to absorb the inherent race between the
// OS releasing our probe socket and StartPgwireDev binding (another
// process can grab the port in between). Returns the bound address.
func startPgwireOnEphemeralPort(t *testing.T, db *Database) string {
	t.Helper()
	const attempts = 5
	var lastErr error
	for i := 0; i < attempts; i++ {
		port := pickEphemeralPort(t)
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		if err := db.StartPgwireDev(addr); err == nil {
			return addr
		} else {
			lastErr = err
		}
	}
	t.Fatalf("StartPgwireDev failed after %d attempts: %v", attempts, lastErr)
	return ""
}

// runPsql invokes psql against the dev listener with stdout/stderr
// captured separately. -X skips ~/.psqlrc so per-user prompt or pager
// settings can't pollute parsed output.
func runPsql(t *testing.T, port int, sql string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx,
		"psql",
		"-X",
		"-h", "127.0.0.1",
		"-p", strconv.Itoa(port),
		"-U", "zeta",
		"-d", "zeta",
		"-t", "-A",
		"-c", sql,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%v\n--- stderr ---\n%s--- stdout ---\n%s",
			err, stderr.String(), stdout.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

func TestDevListener_PsqlEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql not on PATH; skipping end-to-end dev-listener test")
	}

	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec("CREATE TABLE smoke (id INTEGER, label TEXT)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.Exec("INSERT INTO smoke VALUES ($1, $2)", 1, "via-embedded-api"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	addr := startPgwireOnEphemeralPort(t, db)
	waitTCPReady(t, addr, 5*time.Second)

	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%s): %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port %q: %v", portStr, err)
	}

	got, err := runPsql(t, port, "SELECT label FROM smoke WHERE id = 1")
	if err != nil {
		t.Fatalf("psql SELECT failed: %v", err)
	}
	if got != "via-embedded-api" {
		t.Fatalf("psql returned %q, want %q", got, "via-embedded-api")
	}

	// Round-trip the other direction: write via psql, read via embedded API.
	if _, err := runPsql(t, port, "INSERT INTO smoke VALUES (2, 'via-psql')"); err != nil {
		t.Fatalf("psql INSERT failed: %v", err)
	}

	rows, err := db.Query("SELECT label FROM smoke WHERE id = 2")
	if err != nil {
		t.Fatalf("Query after psql write: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("row inserted via psql not visible to embedded API; err=%v", rows.Err())
	}
	var label string
	if err := rows.Scan(&label); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if label != "via-psql" {
		t.Fatalf("got %q, want via-psql", label)
	}
}
