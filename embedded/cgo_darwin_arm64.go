//go:build darwin && arm64

package embedded

// #cgo CFLAGS: -I${SRCDIR}/include
// #cgo LDFLAGS: /usr/local/lib/zeta/libzeta.a -lc++ -framework CoreFoundation -framework Security -framework SystemConfiguration
import "C"
