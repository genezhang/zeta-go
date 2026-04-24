package embedded

import "fmt"

// ErrorKind categorises the broad class of a Zeta error, mirroring
// ZETA_ERR_* codes from the C API.
type ErrorKind int

const (
	// ErrUnknown is the catch-all for unclassified errors, including
	// the explicit ZETA_ERR_UNKNOWN code.
	ErrUnknown ErrorKind = iota
	// ErrParse indicates a SQL parse or syntax error.
	ErrParse
	// ErrType indicates a type mismatch (e.g. binding a string to a BIGINT column).
	ErrType
	// ErrConstraint indicates a primary key, unique, foreign key, or check violation.
	ErrConstraint
	// ErrConflict indicates a transaction serialization failure; retry is appropriate.
	ErrConflict
	// ErrNotFound indicates a missing row or object.
	ErrNotFound
	// ErrStorage indicates an underlying storage or I/O error.
	ErrStorage
)

func (k ErrorKind) String() string {
	switch k {
	case ErrParse:
		return "parse error"
	case ErrType:
		return "type mismatch"
	case ErrConstraint:
		return "constraint violation"
	case ErrConflict:
		return "serialization conflict"
	case ErrNotFound:
		return "not found"
	case ErrStorage:
		return "storage error"
	default:
		return "unknown error"
	}
}

// Error is the typed error returned by all Zeta operations. Check Kind
// to branch on the error class (e.g. retry on ErrConflict).
type Error struct {
	Kind    ErrorKind
	Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return "zeta: " + e.Kind.String()
	}
	return fmt.Sprintf("zeta: %s: %s", e.Kind, e.Message)
}

// errorKindFromCode maps a ZETA_ERR_* integer code (from zeta.h) to an ErrorKind.
//
//	ZETA_ERR_PARSE       = -1
//	ZETA_ERR_TYPE        = -2
//	ZETA_ERR_CONSTRAINT  = -3
//	ZETA_ERR_CONFLICT    = -4
//	ZETA_ERR_NOT_FOUND   = -5
//	ZETA_ERR_STORAGE     = -6
//	ZETA_ERR_UNKNOWN     = -99
func errorKindFromCode(code int) ErrorKind {
	switch code {
	case -1:
		return ErrParse
	case -2:
		return ErrType
	case -3:
		return ErrConstraint
	case -4:
		return ErrConflict
	case -5:
		return ErrNotFound
	case -6:
		return ErrStorage
	default:
		return ErrUnknown
	}
}
