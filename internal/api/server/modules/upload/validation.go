package upload

import (
	"attendance-system/internal/pkg/apperr"
	"fmt"
	"regexp"
	"strings"
)

var fileName = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.[a-z]+$`)

func ruleFor(purpose Purpose) (rule, error) {
	r, ok := rules[purpose]
	if !ok {
		return rule{}, apperr.Invalid("unknown purpose %q", purpose)
	}
	return r, nil
}

// checkType returns the file extension for contentType, whether declared by the client or sniffed.
func checkType(r rule, purpose Purpose, contentType string) (string, error) {
	ext, ok := r.types[contentType]
	if !ok {
		return "", apperr.Invalid("content type %s is not allowed for %s", contentType, purpose)
	}
	return ext, nil
}

func checkSize(r rule, purpose Purpose, size int64) error {
	if size < 1 || size > r.maxBytes {
		return apperr.Invalid("%d bytes is outside the 1 to %d allowed for %s", size, r.maxBytes, purpose)
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
		return apperr.Invalid("malformed upload key %q", key)
	}
	return nil
}

func ownPrefix(r rule, userID int64) string {
	return fmt.Sprintf("%s/%d/", r.prefix, userID)
}
