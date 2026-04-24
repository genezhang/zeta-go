# zeta-go

Go bindings for [Zeta](https://github.com/genezhang/zeta) — a PostgreSQL-dialect
database engine with JSONB, vector similarity, property graphs, and SSI
transactions.

Zeta ships in three form factors. This repository covers only one of them
directly; the other two are served by the existing Go Postgres / MySQL
ecosystem because Zeta's servers speak both wire protocols on the same
database.

| Form factor | Go package |
|---|---|
| **Embedded** (in-process, `libzeta.a`) | `github.com/genezhang/zeta-go/embedded` |
| **Single-node server** | Any Postgres or MySQL Go driver |
| **Distributed servers** | Any Postgres or MySQL Go driver |

## Embedded: in-process database

In-process usage via cgo. Zero-network overhead; the database runs inside
your Go binary.

> **Status:** scaffolding phase. The package currently exposes only
> `Version()` as a link-verification smoke test. The full API (`Open`,
> `Execute`, `Query`, transactions, vector binding, schema introspection)
> is planned — see the issue tracker.

### Install

`libzeta.a` is a 40–115 MB precompiled archive per platform — too large
to vendor in the Go module (linux-amd64 alone exceeds GitHub's 100 MB
per-file limit). Install it once via the bundled `zeta-setup` tool:

```bash
go install github.com/genezhang/zeta-go/cmd/zeta-setup@latest
sudo zeta-setup install
```

`zeta-setup` downloads the matching artifact for your `GOOS/GOARCH` from
the zeta-go GitHub Releases page, verifies its sha256 checksum, and
installs it to `/usr/local/lib/zeta/libzeta.a`. The `embedded` package's
cgo preamble references that path.

To install without sudo, use `-prefix`:

```bash
zeta-setup install -prefix $HOME/.local
# then, before building, set:
export CGO_LDFLAGS="$HOME/.local/lib/zeta/libzeta.a -lpthread -ldl -lm -lstdc++ -lgcc_s -lrt"  # Linux
# or on macOS:
export CGO_LDFLAGS="$HOME/.local/lib/zeta/libzeta.a -lc++ -framework CoreFoundation -framework Security -framework SystemConfiguration"
```

Other commands:

```bash
zeta-setup version              # show installed version
zeta-setup install -force       # reinstall
zeta-setup install -version v0.2.0
zeta-setup uninstall
```

Platforms: `linux-{amd64,arm64}` and `darwin-{amd64,arm64}`. Windows is
not supported.

### Use

```go
import "github.com/genezhang/zeta-go/embedded"

v := embedded.Version()  // "0.1.0"
```

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
