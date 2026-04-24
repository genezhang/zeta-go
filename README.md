# zeta-go

Go bindings for [Zeta](https://github.com/genezhang/zeta) — a PostgreSQL-dialect
database engine with JSONB, vector similarity, property graphs, and SSI
transactions.

Zeta ships in three form factors. This repository covers only one of them
directly; the other two are served by the existing Go Postgres / MySQL
ecosystem because Zeta's servers speak both wire protocols on the same
database.

| Form factor | How to use it from Go |
|---|---|
| **Embedded** (in-process, `libzeta.a`) | `github.com/genezhang/zeta-go/embedded` — this repo |
| **Single-node server** | Any Postgres or MySQL Go driver |
| **Distributed servers** | Any Postgres or MySQL Go driver |

## Embedded: in-process database

In-process usage via cgo. Zero-network overhead; the database is compiled
into your binary.

> **Status:** scaffolding phase. The package currently exposes only
> `Version()` as a link-verification smoke test. The full API (Open,
> Execute, Query, Transactions, vector binding, schema introspection) is
> planned; see [issue tracker](https://github.com/genezhang/zeta-go/issues).

```go
import "github.com/genezhang/zeta-go/embedded"

v := embedded.Version()  // "0.1.0"
```

### Build prerequisites

- cgo toolchain (gcc / clang) — included in standard Go distributions
- Platform: linux-amd64, linux-arm64, darwin-amd64, darwin-arm64 (Windows not supported)
- `libzeta.a` artifact for your platform (distribution approach TBD — see below)

### Native library distribution

`libzeta.a` is a ~40-115 MB precompiled static archive per platform, which
exceeds GitHub's 100 MB per-file limit on linux-amd64. Distribution strategy
is being finalized. Planned: fetch-on-first-build from a GitHub Release asset
into the user's module cache. Until then, set `ZETA_LIB_DIR` to a directory
containing the right `.a` for your platform.

## Single-node or distributed server: use pgx or go-sql-driver/mysql

Zeta's server speaks **both PostgreSQL and MySQL wire protocols
simultaneously** on the same database. Pick whichever Go driver matches
your existing codebase — no Zeta-specific client library needed.

### Postgres wire (recommended)

```go
import (
    "context"
    "github.com/jackc/pgx/v5"
)

conn, err := pgx.Connect(ctx, "postgres://user@localhost:5432/mydb")
// use conn just like you would with any Postgres database
```

### MySQL wire

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
`libzeta.a` static library distributed alongside it is governed by the
[Zeta Embedded License](https://github.com/genezhang/zeta-embedded/blob/main/LICENSE).
