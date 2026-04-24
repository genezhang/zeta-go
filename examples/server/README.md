# Connecting to a Zeta server from Go

Zeta's server (single-node or distributed) speaks PostgreSQL and MySQL
wire protocols simultaneously on the same database. You can connect with
any standard Go Postgres or MySQL driver — no Zeta-specific client library
is required.

## Postgres (pgx)

```bash
go get github.com/jackc/pgx/v5
```

```go
package main

import (
    "context"
    "fmt"
    "github.com/jackc/pgx/v5"
)

func main() {
    ctx := context.Background()
    conn, err := pgx.Connect(ctx, "postgres://user@localhost:5432/mydb")
    if err != nil {
        panic(err)
    }
    defer conn.Close(ctx)

    var version string
    err = conn.QueryRow(ctx, "SELECT version()").Scan(&version)
    if err != nil {
        panic(err)
    }
    fmt.Println(version)
}
```

## MySQL (go-sql-driver/mysql)

```bash
go get github.com/go-sql-driver/mysql
```

```go
package main

import (
    "database/sql"
    "fmt"
    _ "github.com/go-sql-driver/mysql"
)

func main() {
    db, err := sql.Open("mysql", "user@tcp(localhost:3306)/mydb")
    if err != nil {
        panic(err)
    }
    defer db.Close()

    var version string
    err = db.QueryRow("SELECT version()").Scan(&version)
    if err != nil {
        panic(err)
    }
    fmt.Println(version)
}
```

## Vector columns

Zeta's pgwire currently serializes `VECTOR(n)` columns as text in the
PostgreSQL array literal format (`[1.0,2.0,3.0]`). Read them as `string`
and parse with `strings.Trim` + `strings.Split`, or use `pgx`'s custom
type registration to marshal `[]float32` directly. A helper package
for this is planned.
