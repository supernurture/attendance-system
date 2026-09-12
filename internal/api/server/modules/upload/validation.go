package upload

import (
	"fmt"
	"regexp"
	"strings"
)

// ValidationError is a rejected request; its message is safe to show the client.
type ValidationError struct{ msg string }

func (e *ValidationError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return &ValidationError{msg: fmt.Sprintf(format, args...)}
}

var fileName = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.[a-z]+$`)

func ruleFor(purpose Purpose) (rule, error) {
	r, ok := rules[purpose]
	if !ok {
		return rule{}, invalid("unknown purpose %q", purpose)
	}
	return r, nil
}

// checkType returns the file extension for contentType, whether declared by the client or sniffed.
func checkType(r rule, purpose Purpose, contentType string) (string, error) {
	ext, ok := r.types[contentType]
	if !ok {
		return "", invalid("content type %s is not allowed for %s", contentType, purpose)
	}
	return ext, nil
}

func checkSize(r rule, purpose Purpose, size int64) error {
	if size < 1 || size > r.maxBytes {
		return invalid("%d bytes is outside the 1 to %d allowed for %s", size, r.maxBytes, purpose)
	}
	return nil
}

// checkKey accepts only {prefix}/{userID}/<uuid>.<ext>, the shape Intent issues, which also bars "..".
func checkKey(r rule, userID int64, key string) error {
	name, ok := strings.CutPrefix(key, ownPrefix(r, userID))
	if !ok {
		return ErrForbidden
	}
	if !fileName.MatchString(name) {
		return invalid("malformed upload key %q", key)
	}
	return nil
}

func ownPrefix(r rule, userID int64) string {
	return fmt.Sprintf("%s/%d/", r.prefix, userID)
}
