package embedded

/*
#include <stdlib.h>
#include "zeta.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// bindParams binds the given Go values as statement parameters, 1-indexed
// in the order supplied. Supported types:
//
//	nil, bool, int, int32, int64, float32, float64, string, []byte, []float32
//
// []float32 is bound as a Zeta VECTOR. Other slice types are rejected.
func bindParams(stmt *C.zeta_stmt_t, params []any) error {
	for i, p := range params {
		idx := C.int(i + 1)
		rc, err := bindOne(stmt, idx, p)
		if err != nil {
			return fmt.Errorf("bind parameter $%d: %w", i+1, err)
		}
		if rc != C.ZETA_OK {
			return stmtError(stmt, rc)
		}
	}
	return nil
}

func bindOne(stmt *C.zeta_stmt_t, idx C.int, p any) (C.int, error) {
	switch v := p.(type) {
	case nil:
		return C.zeta_bind_null(stmt, idx), nil
	case bool:
		var n C.int32_t
		if v {
			n = 1
		}
		return C.zeta_bind_int32(stmt, idx, n), nil
	case int:
		return C.zeta_bind_int64(stmt, idx, C.int64_t(v)), nil
	case int32:
		return C.zeta_bind_int32(stmt, idx, C.int32_t(v)), nil
	case int64:
		return C.zeta_bind_int64(stmt, idx, C.int64_t(v)), nil
	case uint:
		// Fits into int64 for values up to math.MaxInt64; reject higher.
		if uint64(v) > 1<<63-1 {
			return 0, fmt.Errorf("uint value %d exceeds int64 range", v)
		}
		return C.zeta_bind_int64(stmt, idx, C.int64_t(v)), nil
	case uint32:
		return C.zeta_bind_int64(stmt, idx, C.int64_t(v)), nil
	case uint64:
		if v > 1<<63-1 {
			return 0, fmt.Errorf("uint64 value %d exceeds int64 range", v)
		}
		return C.zeta_bind_int64(stmt, idx, C.int64_t(v)), nil
	case float32:
		return C.zeta_bind_double(stmt, idx, C.double(v)), nil
	case float64:
		return C.zeta_bind_double(stmt, idx, C.double(v)), nil
	case string:
		cs := C.CString(v)
		defer C.free(unsafe.Pointer(cs))
		return C.zeta_bind_text(stmt, idx, cs, C.int(len(v))), nil
	case []byte:
		var ptr unsafe.Pointer
		if len(v) > 0 {
			ptr = unsafe.Pointer(&v[0])
		}
		return C.zeta_bind_blob(stmt, idx, ptr, C.int(len(v))), nil
	case []float32:
		var ptr *C.float
		if len(v) > 0 {
			ptr = (*C.float)(unsafe.Pointer(&v[0]))
		}
		return C.zeta_bind_vector(stmt, idx, ptr, C.int(len(v))), nil
	default:
		return 0, fmt.Errorf("unsupported parameter type %T", p)
	}
}
