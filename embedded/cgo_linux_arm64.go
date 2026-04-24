//go:build linux && arm64

package embedded

// #cgo CFLAGS: -I${SRCDIR}/include
// #cgo LDFLAGS: ${SRCDIR}/internal/lib/linux_arm64/libzeta.a -lpthread -ldl -lm -lstdc++ -lgcc_s -lrt
import "C"
