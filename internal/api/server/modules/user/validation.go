package user

import (
	"fmt"
	"strings"

	"attendance-system/internal/middleware"
)

// ValidationError is a rejected request; its message is safe to show the client.
type ValidationError struct{ msg string }

func (e *ValidationError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return &ValidationError{msg: fmt.Sprintf(format, args...)}
}

// normalizeEmail makes lookups ignore case and surrounding space; users.email is stored lowercase.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// checkName trims a full name or department name and rejects what the column cannot hold.
func checkName(field, name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", invalid("%s is required", field)
	case len(name) > 100:
		return "", invalid("%s must be at most 100 characters", field)
	}
	return name, nil
}

// checkNewUser validates what only creation carries; bcrypt enforces the 72-byte password maximum.
func checkNewUser(email, password string) (string, error) {
	email = normalizeEmail(email)
	switch {
	case !strings.Contains(email, "@"):
		return "", invalid("email %q is not an email address", email)
	case len(email) > 254:
		return "", invalid("email must be at most 254 characters")
	case len(password) < minPasswordLen:
		return "", invalid("password must be at least %d bytes", minPasswordLen)
	}
	return email, nil
}

// checkRole keeps an unknown role out of the database, which would refuse it anyway.
func checkRole(role middleware.Role) error {
	if !role.Valid() {
		return invalid("role %q is not one of employee, supervisor, hr_admin, super_admin", role)
	}
	return nil
}

// checkManager rejects a user managing themselves. Longer cycles are left to the subtree query,
// whose CYCLE clause keeps them from looping forever.
func checkManager(userID int64, managerID *int64) error {
	if managerID != nil && *managerID == userID {
		return invalid("a user cannot be their own manager")
	}
	return nil
}
