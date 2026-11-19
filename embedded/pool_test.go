package embedded

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
)

// A database/sql pool is ONE engine with one session per pooled connection (genezhang/zeta-go#8). Before that,
// every pooled connection was its own zeta_open handle: over a persistent path that is several engines on one
// data directory (the engine says that corrupts it; an archive that takes the directory lock refuses the second,
// one that does not lets each be blind to the other's writes), and over ":memory:" it was a separate empty
// database per connection.

// openPool opens a pool of up to maxOpen connections on dsn.
func openPool(t *testing.T, dsn string, maxOpen int) *sql.DB {
	t.Helper()
	db, err := sql.Open(DriverName, dsn)
	if err != nil {
		t.Fatalf("sql.Open(%q): %v", dsn, err)
	}
	db.SetMaxOpenConns(maxOpen)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// heldConns takes n connections at the same time. database/sql can only give out a second connection while the
// first is still held, so these are n distinct driver connections.
func heldConns(t *testing.T, db *sql.DB, n int) []*sql.Conn {
	t.Helper()
	ctx := context.Background()
	cs := make([]*sql.Conn, n)
	for i := range cs {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("connection %d of %d: %v", i+1, n, err)
		}
		cs[i] = c
		t.Cleanup(func() { _ = c.Close() })
	}
	return cs
}

func connExec(t *testing.T, c *sql.Conn, query string, args ...any) {
	t.Helper()
	if _, err := c.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("Exec %q: %v", query, err)
	}
}

func connCount(t *testing.T, c *sql.Conn, table string) int {
	t.Helper()
	var n int
	if err := c.QueryRowContext(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count(%s): %v", table, err)
	}
	return n
}

// The reproduction from the issue: with two connections held, a write on one must be visible on the other, in
// both directions. The older archive failed the second half silently (c1 stayed at 1); the newer one failed the
// second connection's open.
func TestPooledConnectionsOverAPersistentPathShareOneEngine(t *testing.T) {
	db := openPool(t, t.TempDir(), 2)
	cs := heldConns(t, db, 2)

	connExec(t, cs[0], "CREATE TABLE t (id INTEGER PRIMARY KEY)")
	connExec(t, cs[0], "INSERT INTO t VALUES (1)")
	if n := connCount(t, cs[1], "t"); n != 1 {
		t.Fatalf("the second connection sees %d rows of the first's committed write, want 1", n)
	}
	connExec(t, cs[1], "INSERT INTO t VALUES (2)")
	if n := connCount(t, cs[0], "t"); n != 2 {
		t.Fatalf("the first connection sees %d rows after the second's write, want 2 (two engines on one directory)", n)
	}
}

// ":memory:" was a separate empty database per connection, which is why the driver doc said to pin the pool to
// one connection. One engine per sql.DB makes it one database.
func TestPooledMemoryConnectionsShareOneDatabase(t *testing.T) {
	db := openPool(t, ":memory:", 3)
	cs := heldConns(t, db, 3)

	connExec(t, cs[0], "CREATE TABLE t (id INTEGER PRIMARY KEY)")
	connExec(t, cs[1], "INSERT INTO t VALUES (1)")
	connExec(t, cs[2], "INSERT INTO t VALUES (2)")
	for i, c := range cs {
		if n := connCount(t, c, "t"); n != 2 {
			t.Errorf("connection %d sees %d rows, want 2", i, n)
		}
	}
}

// The engine belongs to the sql.DB, not to its connections: a pool that closes every connection when it goes
// idle (SetMaxIdleConns(0)) must not lose an in-memory database between statements.
func TestAnInMemoryDatabaseOutlivesTheConnectionsOfAnIdlePool(t *testing.T) {
	db := openPool(t, ":memory:", 4)
	db.SetMaxIdleConns(0)
	sqlExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY)")
	sqlExec(t, db, "INSERT INTO t VALUES (7)")
	var id int
	if err := db.QueryRow("SELECT id FROM t").Scan(&id); err != nil || id != 7 {
		t.Fatalf("after every connection was closed: id = %d, %v; want 7", id, err)
	}
	if got := db.Stats().OpenConnections; got > 1 {
		t.Fatalf("test premise: %d connections still open, the pool should have closed them", got)
	}
}

// Two databases are two engines: sharing is per sql.DB, not per DSN string.
func TestTwoPoolsOnMemoryAreTwoDatabases(t *testing.T) {
	a := openPool(t, ":memory:", 2)
	b := openPool(t, ":memory:", 2)
	sqlExec(t, a, "CREATE TABLE t (id INTEGER PRIMARY KEY)")
	sqlExec(t, a, "INSERT INTO t VALUES (1)")
	if _, err := b.Exec("SELECT count(*) FROM t"); err == nil {
		t.Fatal("a second sql.Open(\":memory:\") sees the first one's table: they share an engine")
	}
}

// Committed writes from many connections at once all land, and a reader running alongside never sees a torn count.
func TestConcurrentWritersOnPooledConnectionsAllLand(t *testing.T) {
	const writers, each = 8, 40
	db := openPool(t, t.TempDir(), writers)
	sqlExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY, w INTEGER)")

	var wg sync.WaitGroup
	errs := make(chan error, writers+1)
	stop := make(chan struct{})
	go func() { // a reader on yet another pooled connection, while the writers run
		for {
			select {
			case <-stop:
				return
			default:
				var n int
				if err := db.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil {
					errs <- fmt.Errorf("reader: %w", err)
					return
				}
				if n < 0 || n > writers*each {
					errs <- fmt.Errorf("reader saw an impossible count %d", n)
					return
				}
			}
		}
	}()
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if _, err := db.Exec("INSERT INTO t VALUES ($1, $2)", w*each+i, w); err != nil {
					errs <- fmt.Errorf("writer %d, row %d: %w", w, i, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(stop)
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil || n != writers*each {
		t.Fatalf("count = %d, %v; want %d", n, err, writers*each)
	}
}

// Each connection has its own transaction: uncommitted work on one is invisible to the other, and both commit.
func TestTransactionsOnDifferentPooledConnectionsAreIndependent(t *testing.T) {
	db := openPool(t, ":memory:", 2)
	sqlExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY)")

	tx1, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	tx2, err := db.Begin() // tx1 holds its connection, so this is a second one
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx1.Exec("INSERT INTO t VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx2.Exec("INSERT INTO t VALUES (2)"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx2.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil || n != 1 {
		t.Fatalf("tx2 sees %d rows, %v; want 1 (its own; tx1 is uncommitted)", n, err)
	}
	if err := tx1.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil || n != 2 {
		t.Fatalf("after both commits: %d rows, %v; want 2", n, err)
	}
}

// BranchSelector means what it says per connection now: sessions over one engine, so a branch created through
// the pool exists for every connection, and a selection stays on the connection that made it.
func TestBranchesAreSharedAcrossPooledConnectionsAndSelectionIsPerConnection(t *testing.T) {
	db := openPool(t, ":memory:", 2)
	sqlExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY)")
	sqlExec(t, db, "INSERT INTO t VALUES (1)")
	sqlExec(t, db, "CREATE BRANCH feature")

	cs := heldConns(t, db, 2)
	a, b := cs[0], cs[1]
	if err := a.Raw(func(dc any) error { return dc.(BranchSelector).SetBranch("feature") }); err != nil {
		t.Fatalf("SetBranch on the first connection: %v (the branch was created through the pool)", err)
	}
	connExec(t, a, "INSERT INTO t VALUES (2)")
	if n := connCount(t, a, "t"); n != 2 {
		t.Errorf("the branch's own connection sees %d rows, want 2", n)
	}
	if n := connCount(t, b, "t"); n != 1 {
		t.Errorf("the other connection (still on main) sees %d rows, want 1: the selection leaked across connections", n)
	}
}

// ---- engine lifecycle -------------------------------------------------------------------------------------
//
// The pool's connector owns the engine; sessions borrow it. liveEngines counts engine handles, so these pin when
// one is opened and, above all, that it is closed once and only after the last session over it.

func TestAnEngineIsOpenedByTheFirstConnectionAndClosedWithTheDB(t *testing.T) {
	base := liveEngines.Load()
	db, err := sql.Open(DriverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if n := liveEngines.Load() - base; n != 0 {
		t.Fatalf("sql.Open opened %d engines; it must do no I/O", n)
	}
	db.SetMaxOpenConns(4)
	cs := heldConns(t, db, 3)
	if n := liveEngines.Load() - base; n != 1 {
		t.Fatalf("three pooled connections hold %d engines, want 1", n)
	}
	for _, c := range cs {
		_ = c.Close()
	}
	if n := liveEngines.Load() - base; n != 1 {
		t.Fatalf("after every connection was returned the pool has %d engines, want 1: it belongs to the sql.DB", n)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if n := liveEngines.Load() - base; n != 0 {
		t.Fatalf("after db.Close %d engines remain open", n)
	}
}

// DB.Close does not wait for a checked-out connection. The engine must stay open under it, and close with it.
func TestACheckedOutConnectionKeepsTheEngineOpenPastDBClose(t *testing.T) {
	base := liveEngines.Load()
	db, err := sql.Open(DriverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	c, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	connExec(t, c, "CREATE TABLE t (id INTEGER PRIMARY KEY)")
	connExec(t, c, "INSERT INTO t VALUES (1)")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if n := liveEngines.Load() - base; n != 1 {
		t.Fatalf("db.Close with a connection checked out left %d engines, want 1 (the session still needs it)", n)
	}
	if n := connCount(t, c, "t"); n != 1 {
		t.Fatalf("the checked-out connection reads %d rows after db.Close, want 1", n)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if n := liveEngines.Load() - base; n != 0 {
		t.Fatalf("after the last connection closed %d engines remain open", n)
	}
}

// Driver.Open (not through sql.Open) gives a connection a private engine that closes with it.
func TestDriverOpenGivesAPrivateEngineThatClosesWithTheConnection(t *testing.T) {
	base := liveEngines.Load()
	a, err := Driver{}.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Driver{}.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if n := liveEngines.Load() - base; n != 2 {
		t.Fatalf("two Driver.Open calls hold %d engines, want 2", n)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if n := liveEngines.Load() - base; n != 1 {
		t.Fatalf("closing one connection left %d engines, want 1", n)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if n := liveEngines.Load() - base; n != 0 {
		t.Fatalf("%d engines remain after both connections closed", n)
	}
}

// The connector is a driver.Connector and an io.Closer; closing it twice is harmless, and nothing connects after.
func TestAConnectorClosedTwiceIsHarmlessAndRefusesNewConnections(t *testing.T) {
	base := liveEngines.Load()
	ci, err := Driver{}.OpenConnector(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	cc, ok := ci.(interface{ Close() error })
	if !ok {
		t.Fatal("the connector is not an io.Closer, so database/sql would never close its engine")
	}
	c, err := ci.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := cc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cc.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := ci.Connect(context.Background()); err == nil {
		t.Fatal("Connect succeeded on a closed connector")
	}
	if n := liveEngines.Load() - base; n != 1 {
		t.Fatalf("%d engines while a session is still open, want 1", n)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if n := liveEngines.Load() - base; n != 0 {
		t.Fatalf("%d engines remain after the last session closed", n)
	}
}

// A failed open holds nothing, and the pool tries again on the next connection rather than caching the failure.
func TestAFailedOpenHoldsNoEngineAndIsRetried(t *testing.T) {
	base := liveEngines.Load()
	path := t.TempDir() + "/data"
	if err := os.WriteFile(path, []byte("a regular file where the data directory should be"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := openPool(t, path, 1)
	if err := db.Ping(); err == nil {
		t.Skip("this archive opened a database at a regular file; there is no failure to retry")
	}
	if n := liveEngines.Load() - base; n != 0 {
		t.Fatalf("a failed open left %d engines", n)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("the retry after the obstacle was removed: %v", err)
	}
	if n := liveEngines.Load() - base; n != 1 {
		t.Fatalf("%d engines after the retry, want 1", n)
	}
}

// A persistent pool is reopened after db.Close: the first release really closed the engine (an archive that takes
// the directory lock refuses this otherwise), and what two connections wrote is all there.
func TestAPersistentPoolReopensWithWhatItsConnectionsWrote(t *testing.T) {
	dir := t.TempDir()
	first, err := sql.Open(DriverName, dir)
	if err != nil {
		t.Fatal(err)
	}
	first.SetMaxOpenConns(2)
	cs := heldConns(t, first, 2)
	connExec(t, cs[0], "CREATE TABLE t (id INTEGER PRIMARY KEY)")
	connExec(t, cs[0], "INSERT INTO t VALUES (1)")
	connExec(t, cs[1], "INSERT INTO t VALUES (2)")
	for _, c := range cs {
		_ = c.Close()
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second := openPool(t, dir, 2)
	var n int
	if err := second.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil || n != 2 {
		t.Fatalf("reopened: count = %d, %v; want 2", n, err)
	}
}

// A connection request whose context is already done opens nothing.
func TestConnectOnACancelledContextOpensNoEngine(t *testing.T) {
	base := liveEngines.Load()
	ci, err := Driver{}.OpenConnector(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer ci.(interface{ Close() error }).Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ci.Connect(ctx); err == nil {
		t.Fatal("Connect succeeded on a cancelled context")
	}
	if n := liveEngines.Load() - base; n != 0 {
		t.Fatalf("a cancelled Connect opened %d engines", n)
	}
}

// The first connections of a pool race to open the engine; exactly one may be opened.
func TestConcurrentFirstConnectionsOpenOneEngine(t *testing.T) {
	base := liveEngines.Load()
	ci, err := Driver{}.OpenConnector(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	conns := make([]interface{ Close() error }, n)
	var wg sync.WaitGroup
	errs := make(chan error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			c, err := ci.Connect(context.Background())
			if err != nil {
				errs <- err
				return
			}
			conns[i] = c
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := liveEngines.Load() - base; got != 1 {
		t.Fatalf("%d engines after %d concurrent first Connects, want 1", got, n)
	}
	for _, c := range conns {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := ci.(interface{ Close() error }).Close(); err != nil {
		t.Fatal(err)
	}
	if got := liveEngines.Load() - base; got != 0 {
		t.Fatalf("%d engines remain", got)
	}
}

// database/sql can close a driver connection twice (through sql.Conn.Raw and then normally); the engine must not be
// told twice that a session went away, or it closes under the sessions that remain.
func TestClosingADriverConnectionTwiceDoesNotCountItOutTwice(t *testing.T) {
	base := liveEngines.Load()
	db := openPool(t, ":memory:", 2)
	cs := heldConns(t, db, 2)
	connExec(t, cs[1], "CREATE TABLE t (id INTEGER PRIMARY KEY)")
	if err := cs[0].Raw(func(dc any) error { return dc.(driver.Conn).Close() }); err != nil { // the first close
		t.Fatal(err)
	}
	_ = cs[0].Close() // database/sql closes it again
	if n := liveEngines.Load() - base; n != 1 {
		t.Fatalf("%d engines, want 1", n)
	}
	// The other session is still open on a live engine.
	if n := connCount(t, cs[1], "t"); n != 0 {
		t.Fatalf("count = %d, want 0", n)
	}
	_ = cs[1].Close()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if n := liveEngines.Load() - base; n != 0 {
		t.Fatalf("%d engines remain after everything closed", n)
	}
}

// Two connections writing the same row at once is a write-write conflict, as it is between two transactions, and
// it shows even in autocommit: the loser gets an *Error of Kind ErrConflict, nothing hangs, and nothing that was
// reported committed is lost. (Several connections over one engine made this reachable; the pool used to hide it
// by being several separate engines.)
func TestConcurrentWritesToOneRowConflictCleanlyAndLoseNothing(t *testing.T) {
	const writers, each = 4, 60
	db := openPool(t, ":memory:", writers)
	sqlExec(t, db, "CREATE TABLE t (id INTEGER PRIMARY KEY, v INTEGER)")
	sqlExec(t, db, "INSERT INTO t VALUES (1, 0)")

	var wg sync.WaitGroup
	var mu sync.Mutex
	committed, conflicts := 0, 0
	var other error
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				_, err := db.Exec("UPDATE t SET v = v + 1 WHERE id = 1")
				mu.Lock()
				var ze *Error
				switch {
				case err == nil:
					committed++
				case errors.As(err, &ze) && ze.Kind == ErrConflict:
					conflicts++
				default:
					other = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if other != nil {
		t.Fatalf("a failure that is not a conflict: %v", other)
	}
	var v int
	if err := db.QueryRow("SELECT v FROM t WHERE id = 1").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != committed {
		t.Fatalf("v = %d but %d updates reported success (%d conflicts): a committed update was lost or a failed one applied", v, committed, conflicts)
	}
	t.Logf("%d committed, %d conflicts", committed, conflicts)
	if committed+conflicts != writers*each {
		t.Fatalf("%d committed + %d conflicts != %d attempts", committed, conflicts, writers*each)
	}
}

// Temporary tables belong to the engine, not to a session: one connection's is visible to the others, and a
// second CREATE of the same name fails. The driver doc says so; this keeps it true.
func TestTemporaryTablesAreEngineWideNotPerConnection(t *testing.T) {
	db := openPool(t, ":memory:", 2)
	cs := heldConns(t, db, 2)
	connExec(t, cs[0], "CREATE TEMP TABLE tt (id INTEGER PRIMARY KEY)")
	connExec(t, cs[0], "INSERT INTO tt VALUES (1)")
	if n := connCount(t, cs[1], "tt"); n != 1 {
		t.Fatalf("the other connection sees %d rows of the temporary table, want 1", n)
	}
	if _, err := cs[1].ExecContext(context.Background(), "CREATE TEMP TABLE tt (id INTEGER PRIMARY KEY)"); err == nil {
		t.Fatal("a second connection created a temporary table of the same name: they are session-scoped now, so update the driver doc")
	}
}
