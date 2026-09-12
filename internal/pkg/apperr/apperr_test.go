package apperr

import (
	"errors"
	"fmt"
	"testing"
)

func TestInvalid(t *testing.T) {
	err := Invalid("size %d is over the limit", 42)

	if err.Error() != "size 42 is over the limit" {
		t.Errorf("message = %q, want the formatted text", err.Error())
	}
	if !IsValidation(err) {
		t.Error("IsValidation = false, want true")
	}
	if !IsValidation(fmt.Errorf("while saving: %w", err)) {
		t.Error("IsValidation = false through a wrap, want true")
	}
}

func TestIsValidationOnOtherErrors(t *testing.T) {
	for name, err := range map[string]error{
		"nil":        nil,
		"plain":      errors.New("boom"),
		"a sentinel": ErrNotFound,
	} {
		t.Run(name, func(t *testing.T) {
			if IsValidation(err) {
				t.Errorf("IsValidation(%v) = true, want false", err)
			}
		})
	}
}
