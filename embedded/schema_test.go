package embedded

import (
	"testing"
)

func TestListTablesEmpty(t *testing.T) {
	db := openTestDB(t)
	tables, err := db.ListTables()
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	if len(tables) != 0 {
		t.Errorf("got %v, want empty", tables)
	}
}

func TestListTables(t *testing.T) {
	db := openTestDB(t)
	mustExec(t, db, "CREATE TABLE users (id INTEGER)")
	mustExec(t, db, "CREATE TABLE orders (id INTEGER)")
	mustExec(t, db, "CREATE TABLE items (id INTEGER)")

	tables, err := db.ListTables()
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	if len(tables) != 3 {
		t.Fatalf("got %v, want 3 tables", tables)
	}
	// zeta_list_tables returns alphabetically sorted.
	want := []string{"items", "orders", "users"}
	for i, w := range want {
		if tables[i] != w {
			t.Errorf("tables[%d]: got %q, want %q", i, tables[i], w)
		}
	}
}

func TestTableInfo(t *testing.T) {
	db := openTestDB(t)
	mustExec(t, db, `
		CREATE TABLE users (
			id BIGINT PRIMARY KEY,
			name TEXT NOT NULL,
			email TEXT
		)
	`)

	ti, err := db.TableInfo("users")
	if err != nil {
		t.Fatalf("TableInfo: %v", err)
	}
	if ti == nil {
		t.Fatal("got nil for existing table")
	}
	if ti.Name != "users" {
		t.Errorf("Name: got %q, want users", ti.Name)
	}
	if len(ti.Columns) != 3 {
		t.Fatalf("got %d columns, want 3", len(ti.Columns))
	}

	// id: PK, not nullable
	id := ti.Columns[0]
	if id.Name != "id" || !id.IsPrimaryKey || id.Nullable {
		t.Errorf("id column: %+v", id)
	}
	// name: not nullable
	name := ti.Columns[1]
	if name.Name != "name" || name.Nullable || name.IsPrimaryKey {
		t.Errorf("name column: %+v", name)
	}
	// email: nullable
	email := ti.Columns[2]
	if email.Name != "email" || !email.Nullable || email.IsPrimaryKey {
		t.Errorf("email column: %+v", email)
	}
}

func TestTableInfoMissing(t *testing.T) {
	db := openTestDB(t)
	ti, err := db.TableInfo("nonexistent")
	if err != nil {
		t.Fatalf("TableInfo: %v", err)
	}
	if ti != nil {
		t.Errorf("got %+v, want nil for missing table", ti)
	}
}
