package auth

import (
	"fmt"
	"strings"
)

// normalizeEmail makes lookups ignore case and surrounding space; users.email is stored lowercase.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// validateSeedAdmin checks the configured super_admin; bcrypt enforces the 72-byte password maximum itself.
func validateSeedAdmin(email, password string) error {
	if !strings.Contains(email, "@") {
		return fmt.Errorf("seed admin email %q is not an email address", email)
	}
	if len(password) < minPasswordLen {
		return fmt.Errorf("seed admin password must be at least %d bytes", minPasswordLen)
	}
	return nil
}
