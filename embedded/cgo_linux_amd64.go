//go:build linux && amd64

package embedded

// #cgo CFLAGS: -I${SRCDIR}/include
// #cgo LDFLAGS: ${SRCDIR}/internal/lib/linux_amd64/libzeta.a -lpthread -ldl -lm -lstdc++ -lgcc_s -lrt
import "C"
