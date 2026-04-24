//go:build darwin && arm64

package embedded

// #cgo CFLAGS: -I${SRCDIR}/include
// #cgo LDFLAGS: ${SRCDIR}/internal/lib/darwin_arm64/libzeta.a -lc++ -framework CoreFoundation -framework Security -framework SystemConfiguration
import "C"
