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

(*Database).Close() error
(*Database).Exec(sql string, params ...any) (Result, error)
(*Database).Query(sql string, params ...any) (*Rows, error)
(*Database).Begin() (*Tx, error)
(*Database).ListTables() ([]string, error)
(*Database).TableInfo(name string) (*TableInfo, error)

embedded.Version() string
```

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
operations serialise through an internal mutex; for parallelism, open
separate `*Database` handles.

An open `*Rows` or `*Tx` holds the database mutex until closed /
committed / rolled back. Iterate `Rows` to completion (Next returning
false auto-closes) and always defer Close/Rollback.

### Inspecting an embedded database with psql / mysql (dev only)

An in-process Zeta database can optionally expose itself on a loopback
port so a developer can attach `psql`, `mysql`, DBeaver, MySQL
Workbench, or any other standard client for inspection / debugging.
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
if err := db.StartMysqlwireDev("127.0.0.1:3307"); err != nil {
    log.Fatal(err)
}

// ... run application logic in-process ...
// Connect from another terminal:
//   psql  -h 127.0.0.1 -p 5433 -U zeta
//   mysql -h 127.0.0.1 -P 3307 -u zeta
```

`db.Close()` drains both listeners; explicit `db.StopDevListeners()`
takes them down without closing the database.

**Caveats**:
- Defaults to loopback only. Set `ZETA_ALLOW_NONLOCAL_EMBED_LISTEN=1`
  in the environment to bypass the check (developer use only).
- Trust authentication. SCRAM/TLS support is deferred.
- Replication slots are kept in memory only.
- Requires `libzeta.a` built with the `wire-pg` / `wire-mysql` /
  `dev-listeners` Cargo features. The default `zeta-setup install`
  artifact today is *not* built with these features; rebuild from
  source or wait for a release that bundles them. Building with
  `-tags zeta_dev` against a stock `libzeta.a` produces a linker
  error pointing at `zeta_start_pgwire` / `zeta_start_mysqlwire`.

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
