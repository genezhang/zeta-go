//go:build darwin && amd64

package embedded

// #cgo CFLAGS: -I${SRCDIR}/include
// #cgo LDFLAGS: ${SRCDIR}/internal/lib/darwin_amd64/libzeta.a -lc++ -framework CoreFoundation -framework Security -framework SystemConfiguration
import "C"
