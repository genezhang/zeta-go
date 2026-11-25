//go:build linux && arm64

package embedded

// #cgo CFLAGS: -I${SRCDIR}/include
// #cgo LDFLAGS: /usr/local/lib/zeta/libzeta.a -lpthread -ldl -lm -lgcc_s -lrt
import "C"
