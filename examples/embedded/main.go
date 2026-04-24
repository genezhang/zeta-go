// Example: in-process Zeta usage via cgo.
//
// Build:  go build ./examples/embedded
// Run:    ./embedded
//
// Requires libzeta.a to be installed; see repository README.
package main

import (
	"fmt"

	"github.com/genezhang/zeta-go/embedded"
)

func main() {
	fmt.Printf("Zeta engine version: %s\n", embedded.Version())
}
