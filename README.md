# zeta-go

Go bindings for [Zeta](https://github.com/genezhang/zeta) — a PostgreSQL-dialect
database engine with JSONB, vector similarity, property graphs, and SSI
transactions.

Zeta ships in three form factors. This repository covers the embedded one
directly; the other two are served by the existing Go Postgres / MySQL
ecosystem because Zeta's servers speak both wire protocols on the same
database.

| Form factor | Go package |
|---|---|
| **Embedded** (in-process, `libzeta.a`) | `github.com/genezhang/zeta-go/embedded` |
| **Single-node server** | Any Postgres or MySQL Go driver |
| **Distributed servers** | Any Postgres or MySQL Go driver |

## Embedded usage

```go
import "github.com/genezhang/zeta-go/embedded"

db, err := embedded.OpenMemory()
if err != nil { log.Fatal(err) }
defer db.Close()

// DDL
if _, err := db.Exec(`CREATE TABLE users (
    id BIGINT PRIMARY KEY,
    name TEXT NOT NULL,
    embedding VECTOR(3)
)`); err != nil { log.Fatal(err) }

// Transaction with DML
tx, err := db.Begin()
if err != nil { log.Fatal(err) }
defer tx.Rollback()               // no-op if Commit succeeds

if _, err := tx.Exec(
    "INSERT INTO users VALUES ($1, $2, $3)",
    1, "alice", []float32{1.0, 0.0, 0.0},
); err != nil { log.Fatal(err) }

if err := tx.Commit(); err != nil { log.Fatal(err) }

// Query
rows, err := db.Query("SELECT id, name FROM users WHERE id = $1", 1)
if err != nil { log.Fatal(err) }
defer rows.Close()

for rows.Next() {
    var id int64
    var name string
    if err := rows.Scan(&id, &name); err != nil { log.Fatal(err) }
    fmt.Println(id, name)
}
if err := rows.Err(); err != nil { log.Fatal(err) }
```

See [`examples/embedded`](examples/embedded/main.go) for a runnable program.

### Install the native library

`libzeta.a` is a 40–115 MB precompiled archive per platform — too large
to vendor in the Go module. Install it once via the bundled `zeta-setup`
tool:

```bash
go install github.com/genezhang/zeta-go/cmd/zeta-setup@latest
sudo zeta-setup install
```

This downloads the matching artifact for your `GOOS/GOARCH` from the
zeta-go GitHub Releases page, verifies its sha256 checksum, and places
it at `/usr/local/lib/zeta/libzeta.a`. The `embedded` package's cgo
preamble references that path.

Without sudo, use `-prefix`:

```bash
zeta-setup install -prefix $HOME/.local
# Linux: set before building
export CGO_LDFLAGS="$HOME/.local/lib/zeta/libzeta.a -lpthread -ldl -lm -lstdc++ -lgcc_s -lrt"
```

Other commands:

```bash
zeta-setup version              # print installed version
zeta-setup install -force       # reinstall
zeta-setup install -version v0.2.0
zeta-setup uninstall
```

Platforms: `linux-{amd64,arm64}` and `darwin-{amd64,arm64}`. Windows is
not supported.

### API reference

#### Type: `*Database`
```go
embedded.Open(path string) (*Database, error)   // file or ":memory:"
embedded.OpenMemory() (*Database, error)
embedded.OpenMode(path string, mode Mode) (*Database, error)

(*Database).Close() error
(*Database).Connect() (*Database, error)                          // a new session: its own branch selection
(*Database).SetBranch(name string) error                          // select a branch; "" returns to main
(*Database).Exec(sql string, params ...any) (Result, error)
(*Database).Query(sql string, params ...any) (*Rows, error)
(*Database).Begin() (*Tx, error)                                  // snapshot isolation
(*Database).BeginWithIsolation(level Isolation) (*Tx, error)
(*Database).ListTables() ([]string, error)
(*Database).TableInfo(name string) (*TableInfo, error)

embedded.Version() string
```

`Open` uses the default backend (Quartz, OLTP profile), which does **not** start
the columnar analytics tier. To opt in, `OpenMode(path, embedded.ModeQuartz|embedded.ModeFlagHTAP)`
(also `ModeLSM`, `ModeMemory`); analytical SQL (aggregates, `GROUP BY`) then routes to it. The retired
BTree backend (mode 0) is rejected, and a data directory opens only with the backend that created it.

#### Isolation

`Begin` is snapshot isolation: one consistent snapshot, and concurrent writes to the same row conflict,
but two transactions that each read what the other writes can **both commit** (write skew). For
check-then-write logic use `BeginWithIsolation(embedded.IsolationSerializable)`; a dangerous pattern then
fails one transaction at `Commit` with an `*Error` of `Kind == ErrConflict`, and you retry the whole
transaction. Conflict detection is coarse, so budget for retries. Through `database/sql`,
`BeginTx` with `sql.LevelSerializable` does the same; `ReadCommitted`/`RepeatableRead`/`Snapshot` run at
snapshot isolation, `sql.LevelLinearizable` is rejected, and `ReadOnly` is not enforced.

#### Type: `*Tx`
```go
(*Tx).Exec(sql string, params ...any) (Result, error)
(*Tx).Query(sql string, params ...any) (*Rows, error)
(*Tx).Commit() error
(*Tx).Rollback() error    // idempotent; safe to defer after Commit
```

#### Type: `*Rows`
```go
(*Rows).Next() bool           // auto-closes on false
(*Rows).Scan(dest ...any) error
(*Rows).Columns() []string    // populated after first Next
(*Rows).Err() error
(*Rows).Close() error
```

#### Supported parameter / scan types

| SQL type | Bind Go type | Scan Go type |
|---|---|---|
| NULL | `nil` | `*any` (→ nil) |
| BOOLEAN | `bool` | `*bool` |
| SMALLINT / INTEGER / BIGINT | `int`, `int32`, `int64`, `uint`, `uint32`, `uint64` | `*int`, `*int32`, `*int64` |
| REAL / DOUBLE PRECISION | `float32`, `float64` | `*float32`, `*float64` |
| TEXT / VARCHAR | `string` | `*string` |
| BYTEA | `[]byte` | `*string` (returned as Postgres hex `\x010203`) |
| VECTOR(n) | `[]float32` | `*string` (returned as `[1,2,3]`) |

`*any` is always supported — the concrete type depends on the column's
Zeta type at runtime (see `embedded.ColumnInfo`).

#### Errors

```go
type Error struct {
    Kind    ErrorKind
    Message string
}

// Kinds: ErrParse, ErrType, ErrConstraint, ErrConflict,
//        ErrNotFound, ErrStorage, ErrUnknown
```

Use `errors.As` to inspect Kind for retry logic (e.g. retry on
`ErrConflict`):

```go
var ze *embedded.Error
if errors.As(err, &ze) && ze.Kind == embedded.ErrConflict {
    // retry the transaction
}
```

### Concurrency

`*Database` is safe for concurrent use by multiple goroutines. All
operations serialise through an internal mutex; for parallelism, give each
goroutine its own session with `Connect` (reads on sessions over one engine run
in parallel with each other). Do not `Open` a second `*Database` on the same
persistent path for that: it is a second engine on one directory, which the
engine says corrupts it.

An open `*Rows` holds the database mutex until it is closed: every other
call on the same `*Database` (including `Connect` and `SetBranch`) waits for
it, and from the goroutine that holds the `Rows` it never returns. Iterate
`Rows` to completion (Next returning false auto-closes) and always defer
Close. A `*Tx` locks the database only for the duration of each of its calls,
so other calls on the same `*Database` are not held up between them; always
defer Rollback.

### `database/sql` pools

`sql.Open("zeta", path)` gives a pool over **one engine**: the first connection
opens it, and every pooled connection is a session over it (`Database.Connect`
is the same thing in the native API). So:

- A pool of any size is safe over a persistent path. Connections see each
  other's committed writes, and reads on different connections run in
  parallel; writes do not get faster with more connections, and one connection
  runs one statement at a time. There is no need for `SetMaxOpenConns(1)`.
- Writes to the **same row** from different connections at once conflict, as
  they do between two transactions, even in autocommit: the loser gets an
  `*Error` with `Kind == ErrConflict` and nothing is lost. Retry it, or
  serialise the writers with `SetMaxOpenConns(1)`.
- Temporary tables belong to the engine, not to a session: one connection's
  `CREATE TEMP TABLE` is visible to the others until `db.Close()`.
- `":memory:"` is one database per `sql.DB`, shared by all of its connections,
  and it lives until `db.Close()`: it survives the pool closing idle
  connections. Two `sql.Open` calls are two databases.
- `db.Close()` closes the engine once the last connection over it is closed.
- Do not open two `sql.DB` values, or a `sql.DB` and a `*Database`, on the same
  persistent path in one process: that is two engines on one directory. An
  archive that takes the data-directory lock refuses the second open with an
  error; an older one does not, and the two are blind to each other's writes.

### Branching

`CREATE BRANCH name` forks the database copy-on-write; a session that selects the branch reads
main as it was when the branch was created plus its own changes, and writes only to the branch.
`MERGE BRANCH name` publishes the branch's writes to main and consumes it; `DROP BRANCH name`
discards it. Branch DDL is plain SQL and must run on a handle with no branch selected (it fails on
one that has selected a branch); *selecting* a branch is `SetBranch`, because selection is session
state.

Selection belongs to a session, not to the engine, so two goroutines that share one `*Database`
share one branch pointer. Give each concurrent caller its own session with `Connect`, and close
sessions before the `*Database` they came from:

```go
db, _ := embedded.Open("/var/lib/app/db")
defer db.Close()
db.Exec("CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)")
db.Exec("CREATE BRANCH agent_a")

a, _ := db.Connect() // one session per concurrent caller
defer a.Close()
a.SetBranch("agent_a")
a.Exec("INSERT INTO notes VALUES (1, 'draft')") // lands on agent_a; db still reads main

a.SetBranch("") // back to main, then publish
rows, _ := a.Query("MERGE BRANCH agent_a")
conflicts := 0
for rows.Next() {
	conflicts++
}
rows.Close() // conflicts == 0: merged
```

**Read the result of `MERGE BRANCH`.** It reports conflicts as rows (`table_name`, `pk`, `branch_op`,
`base_change`). No rows means the branch was merged and is gone. Any row means **nothing was merged**:
the merge is all-or-nothing, the branch is left as it was, and the call still succeeds. `Exec` drops
rows, so a conflicted merge made through `Exec` looks like success: run it through `Query`. `Query`
runs the statement when you first step the rows, so always iterate before you close them: closing the
`*Rows` without calling `Next` leaves the merge undone, with no error.

The branch name is stored, not checked: a branch that does not exist (or was dropped) is reported
by the next statement, and `SetBranch("")` recovers. A name containing a NUL byte is refused. A
transaction runs on the branch that was selected when it began and keeps it, even if `SetBranch`
is called on the same handle while the `*Tx` is open (the new selection applies to the handle's
later statements); select before `Begin`.

With `database/sql`, each connection holds its own branch selection. Select the branch through
`sql.Conn.Raw`:

```go
conn, _ := sqlDB.Conn(ctx)
defer conn.Close()
conn.Raw(func(dc any) error { return dc.(embedded.BranchSelector).SetBranch("agent_a") })
```

A connection that goes back to the pool is reset to main, so a selection never reaches the next
user of a pooled connection.

A pooled connection is a session over the pool's one engine (see [`database/sql` pools](#databasesql-pools)),
so a branch created through the pool exists for every connection, and a selection stays on the connection
that made it. For the native API, `Database.Connect` gives each caller its own branch the same way.

### Inspecting an embedded database with psql (dev only)

An in-process Zeta database can optionally expose itself on a loopback
port so a developer can attach `psql`, DBeaver, or any other PostgreSQL
client for inspection / debugging. (The engine's MySQL listener was removed
for the first public release, so there is no `StartMysqlwireDev`.)
The facility is gated on a build tag because it is **not for
production**:

```bash
go build -tags zeta_dev ./...
```

```go
db, _ := embedded.OpenMemory()
defer db.Close()

// Loopback only by default. Pass "127.0.0.1:0" to let the OS pick a
// port — the chosen port is logged to stderr at INFO level via the
// engine's tracing output. (Bare ":0" binds all interfaces and is
// rejected by the loopback-only check.)
if err := db.StartPgwireDev("127.0.0.1:5433"); err != nil {
    log.Fatal(err)
}

// ... run application logic in-process ...
// Connect from another terminal:
//   psql  -h 127.0.0.1 -p 5433 -U zeta
```

`db.Close()` drains the listener; explicit `db.StopDevListeners()`
takes them down without closing the database.

**Caveats**:
- Defaults to loopback only. Set `ZETA_ALLOW_NONLOCAL_EMBED_LISTEN=1`
  in the environment to bypass the check (developer use only).
- Trust authentication. SCRAM/TLS support is deferred.
- Replication slots are kept in memory only.
- Requires `libzeta.a` built with the `wire-pg` Cargo feature. The default `zeta-setup install`
  artifact today is *not* built with these features; rebuild from
  source or wait for a release that bundles them. Building with
  `-tags zeta_dev` against a stock `libzeta.a` produces a linker
  error pointing at `zeta_start_pgwire`.

## Single-node or distributed server: use pgx or go-sql-driver/mysql

Zeta's server speaks **both PostgreSQL and MySQL wire protocols
simultaneously** on the same database. Pick whichever Go driver matches
your existing codebase — no Zeta-specific client library needed.

### Postgres (recommended)

```go
import (
    "context"
    "github.com/jackc/pgx/v5"
)

conn, err := pgx.Connect(ctx, "postgres://user@localhost:5432/mydb")
// use conn like any Postgres database
```

### MySQL

```go
import (
    "database/sql"
    _ "github.com/go-sql-driver/mysql"
)

db, err := sql.Open("mysql", "user@tcp(localhost:3306)/mydb")
// use db like any standard database/sql handle
```

### Parameter placeholder styles

| Connection path | Placeholders |
|---|---|
| Embedded (cgo) | `$1`, `$2`, ... |
| pgx / Postgres wire | `$1`, `$2`, ... |
| go-sql-driver/mysql / MySQL wire | `?` |

The same SQL engine underlies all three; only the parameter binding
convention differs based on which protocol you're speaking.

## License

Apache 2.0 — see [LICENSE](LICENSE).

This repository contains Go glue code and a vendored C header. The
`libzeta.a` static library is governed by the
[Zeta Embedded License](https://github.com/genezhang/zeta-embedded/blob/main/LICENSE).
