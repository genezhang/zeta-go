//go:build darwin && amd64

package embedded

// #cgo CFLAGS: -I${SRCDIR}/include
// #cgo LDFLAGS: /usr/local/lib/zeta/libzeta.a -lc++ -lz -framework CoreFoundation -framework Security -framework SystemConfiguration
import "C"
