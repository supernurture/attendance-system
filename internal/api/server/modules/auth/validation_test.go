package auth

import (
	"strings"
	"testing"
)

func TestNormalizeEmail(t *testing.T) {
	if got := normalizeEmail("  Admin@Example.COM \t"); got != "admin@example.com" {
		t.Errorf("normalizeEmail = %q, want admin@example.com", got)
	}
}

func TestValidateSeedAdmin(t *testing.T) {
	tests := map[string]struct {
		email, password string
		wantErr         string
	}{
		"valid":                   {"admin@example.com", "eight888", ""},
		"not an email":            {"admin", "eight888", "not an email address"},
		"password too short":      {"admin@example.com", "seven77", "at least 8 bytes"},
		"long is bcrypt's to say": {"admin@example.com", strings.Repeat("x", 100), ""},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateSeedAdmin(test.email, test.password)
			switch {
			case test.wantErr == "" && err != nil:
				t.Errorf("err = %v, want none", err)
			case test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)):
				t.Errorf("err = %v, want it to mention %q", err, test.wantErr)
			}
		})
	}
}
