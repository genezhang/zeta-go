// Example: in-process Zeta usage via cgo.
//
// Build and run:
//
//	go build -o zeta-demo ./examples/embedded
//	./zeta-demo
//
// Requires libzeta.a installed at /usr/local/lib/zeta/libzeta.a
// (see repository README for `zeta-setup install`).
package main

import (
	"fmt"
	"log"

	"github.com/genezhang/zeta-go/embedded"
)

func main() {
	fmt.Printf("Zeta engine %s\n\n", embedded.Version())

	db, err := embedded.OpenMemory()
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Schema.
	if _, err := db.Exec(`
		CREATE TABLE users (
			id BIGINT PRIMARY KEY,
			name TEXT NOT NULL,
			embedding VECTOR(3)
		)
	`); err != nil {
		log.Fatal(err)
	}

	// Transaction with DML and a vector.
	tx, err := db.Begin()
	if err != nil {
		log.Fatal(err)
	}
	defer tx.Rollback()

	for _, u := range []struct {
		id        int64
		name      string
		embedding []float32
	}{
		{1, "alice", []float32{1.0, 0.0, 0.0}},
		{2, "bob", []float32{0.0, 1.0, 0.0}},
		{3, "carol", []float32{0.0, 0.0, 1.0}},
	} {
		if _, err := tx.Exec(
			"INSERT INTO users VALUES ($1, $2, $3)",
			u.id, u.name, u.embedding,
		); err != nil {
			log.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		log.Fatal(err)
	}

	// Query.
	rows, err := db.Query("SELECT id, name, embedding FROM users ORDER BY id")
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()

	fmt.Println("rows:")
	for rows.Next() {
		var id int64
		var name, embedding string
		if err := rows.Scan(&id, &name, &embedding); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("  %d  %-8s  %s\n", id, name, embedding)
	}
	if err := rows.Err(); err != nil {
		log.Fatal(err)
	}

	// Schema introspection.
	tables, err := db.ListTables()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\ntables: %v\n", tables)

	if info, err := db.TableInfo("users"); err == nil && info != nil {
		fmt.Printf("users columns:\n")
		for _, c := range info.Columns {
			pk := ""
			if c.IsPrimaryKey {
				pk = " PK"
			}
			nullable := "NOT NULL"
			if c.Nullable {
				nullable = "NULL"
			}
			fmt.Printf("  %-12s %-12s %s%s\n", c.Name, c.SQLType, nullable, pk)
		}
	}
}
