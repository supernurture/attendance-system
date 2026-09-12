// Package apperr holds the errors every module reports and every handler maps to a status code.
package apperr

import (
	"errors"
	"fmt"
)

var (
	// ErrForbidden means the caller's role or hierarchy does not reach the target.
	ErrForbidden = errors.New("forbidden")
	// ErrNotFound means no live record matches.
	ErrNotFound = errors.New("not found")
	// ErrConflict means a unique value is taken.
	ErrConflict = errors.New("already exists")
)

// Validation is a rejected request; its message is safe to show the client.
type Validation struct{ msg string }

func (e *Validation) Error() string { return e.msg }

// Invalid builds a Validation error. A handler turns it into 400 with the message.
func Invalid(format string, args ...any) error {
	return &Validation{msg: fmt.Sprintf(format, args...)}
}

// IsValidation reports whether err is one a client can fix from the message.
func IsValidation(err error) bool {
	_, ok := errors.AsType[*Validation](err)
	return ok
}
