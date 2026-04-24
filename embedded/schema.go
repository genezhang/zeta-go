package embedded

/*
#include <stdlib.h>
#include "zeta.h"
*/
import "C"

import (
	"strings"
	"unsafe"
)

// ColumnInfo describes a single column of a table.
type ColumnInfo struct {
	Name         string
	SQLType      string
	Nullable     bool
	IsPrimaryKey bool
}

// TableInfo describes the schema of a table.
type TableInfo struct {
	Name    string
	Columns []ColumnInfo
}

// ListTables returns all user table names in the database, sorted
// alphabetically. Returns an empty slice if the database has no tables.
func (d *Database) ListTables() ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.handle == nil {
		return nil, ErrClosed
	}

	var out, errMsg *C.char
	rc := C.zeta_list_tables(d.handle, &out, &errMsg)
	if rc != C.ZETA_OK {
		return nil, consumeErrMsg(errMsg, errorKindFromCode(int(rc)))
	}

	s := C.GoString(out)
	C.zeta_free(unsafe.Pointer(out))

	s = strings.TrimRight(s, "\n")
	if s == "" {
		return []string{}, nil
	}
	return strings.Split(s, "\n"), nil
}

// TableInfo returns schema information for the named table, or nil if
// the table does not exist.
func (d *Database) TableInfo(name string) (*TableInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.handle == nil {
		return nil, ErrClosed
	}

	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))

	var out, errMsg *C.char
	rc := C.zeta_table_info(d.handle, cname, &out, &errMsg)
	if rc != C.ZETA_OK {
		return nil, consumeErrMsg(errMsg, errorKindFromCode(int(rc)))
	}

	s := C.GoString(out)
	C.zeta_free(unsafe.Pointer(out))

	if s == "" {
		return nil, nil
	}
	return parseTableInfo(s), nil
}

// parseTableInfo parses the newline/tab-separated schema format emitted
// by zeta_table_info. The first line is the table name; subsequent lines
// have the form  <col>\t<sql_type>\t<nullable>\t<is_pk>
func parseTableInfo(s string) *TableInfo {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) == 0 {
		return nil
	}
	ti := &TableInfo{Name: lines[0]}
	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 4 {
			continue
		}
		ti.Columns = append(ti.Columns, ColumnInfo{
			Name:         parts[0],
			SQLType:      parts[1],
			Nullable:     parts[2] == "1",
			IsPrimaryKey: parts[3] == "1",
		})
	}
	return ti
}
