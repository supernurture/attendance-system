package util

import (
	"encoding/base64"
	"fmt"

	"github.com/google/uuid"
)

const maxIDLength = 22

// GenerateBasicAuth returns a "Basic <base64>" header value, or "" if either credential is empty.
func GenerateBasicAuth(user, password string) string {
	if user == "" || password == "" {
		return ""
	}
	credentials := user + ":" + password
	encodedCredentials := base64.StdEncoding.EncodeToString([]byte(credentials))
	return "Basic " + encodedCredentials
}

// GenerateUniqueID returns a URL-safe random ID of the given length, at most maxIDLength.
func GenerateUniqueID(length int) (string, error) {
	if length < 1 || length > maxIDLength {
		return "", fmt.Errorf("length must be between 1 and %d, got %d", maxIDLength, length)
	}

	id, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("generate uuid: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(id[:])[:length], nil
}
