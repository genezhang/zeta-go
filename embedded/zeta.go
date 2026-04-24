// Package embedded provides an in-process binding to the Zeta database engine
// via cgo and the libzeta.a static library.
//
// This package is for the embedded form factor. To connect to a Zeta server
// (single-node or distributed), use a standard PostgreSQL or MySQL Go driver —
// Zeta speaks both wire protocols on the same database. See the repository
// README for server connection examples.
package embedded

/*
#include "zeta.h"
*/
import "C"

// Version returns the Zeta engine version string compiled into libzeta.a.
// Useful as a smoke test that the binding links correctly.
func Version() string {
	return C.GoString(C.zeta_version())
}
