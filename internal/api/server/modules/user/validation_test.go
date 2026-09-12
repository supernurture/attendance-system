package user

import (
	"errors"
	"strings"
	"testing"

	"attendance-system/internal/middleware"
)

func TestNormalizeEmail(t *testing.T) {
	if got := normalizeEmail("  Budi@Example.COM \t"); got != "budi@example.com" {
		t.Errorf("normalizeEmail = %q, want budi@example.com", got)
	}
}

func TestCheckName(t *testing.T) {
	if got, err := checkName("full_name", "  Budi Santoso  "); err != nil || got != "Budi Santoso" {
		t.Errorf("checkName = %q, %v; want it trimmed", got, err)
	}
	for name, value := range map[string]string{"empty": "   ", "too long": strings.Repeat("x", 101)} {
		if _, err := checkName("full_name", value); !isValidationError(err) {
			t.Errorf("%s: err = %v, want a ValidationError", name, err)
		}
	}
}

func TestCheckNewUser(t *testing.T) {
	if got, err := checkNewUser(" Budi@Example.com ", "eight888"); err != nil || got != "budi@example.com" {
		t.Errorf("checkNewUser = %q, %v; want the normalized email", got, err)
	}

	tests := map[string][2]string{
		"not an email":       {"budi", "eight888"},
		"email too long":     {strings.Repeat("x", 250) + "@test.local", "eight888"},
		"password too short": {"budi@example.com", "seven77"},
	}
	for name, args := range tests {
		if _, err := checkNewUser(args[0], args[1]); !isValidationError(err) {
			t.Errorf("%s: err = %v, want a ValidationError", name, err)
		}
	}
}

func TestCheckRole(t *testing.T) {
	for _, role := range []middleware.Role{
		middleware.RoleEmployee, middleware.RoleSupervisor, middleware.RoleHRAdmin, middleware.RoleSuperAdmin,
	} {
		if err := checkRole(role); err != nil {
			t.Errorf("checkRole(%q) = %v, want none", role, err)
		}
	}
	for _, role := range []middleware.Role{"", "admin", "Employee", "owner"} {
		if err := checkRole(role); !isValidationError(err) {
			t.Errorf("checkRole(%q) = %v, want a ValidationError", role, err)
		}
	}
}

func TestCheckManager(t *testing.T) {
	self, other := int64(7), int64(8)
	if err := checkManager(7, &self); !isValidationError(err) {
		t.Errorf("managing themselves: err = %v, want a ValidationError", err)
	}
	if err := checkManager(7, &other); err != nil {
		t.Errorf("another manager: err = %v, want none", err)
	}
	if err := checkManager(7, nil); err != nil {
		t.Errorf("no manager: err = %v, want none", err)
	}
}

func isValidationError(err error) bool {
	_, ok := errors.AsType[*ValidationError](err)
	return ok
}
