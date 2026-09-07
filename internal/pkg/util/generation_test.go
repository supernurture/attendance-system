package util

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestGenerateBasicAuth(t *testing.T) {
	cases := []struct {
		user, password, want string
	}{
		{"admin", "s4cr4t", "Basic YWRtaW46czRjcjR0"},
		{"", "s4cr4t", ""},
		{"admin", "", ""},
		{"", "", ""},
	}
	for _, test := range cases {
		if got := GenerateBasicAuth(test.user, test.password); got != test.want {
			t.Errorf("GenerateBasicAuth(%q, %q) = %q, want %q", test.user, test.password, got, test.want)
		}
	}
}

func TestGenerateUniqueID(t *testing.T) {
	for _, length := range []int{2, 20, maxIDLength} {
		id, err := GenerateUniqueID(length)
		if err != nil {
			t.Fatalf("GenerateUniqueID(%d): %v", length, err)
		}
		if len(id) != length {
			t.Errorf("GenerateUniqueID(%d) = %q, length %d", length, id, len(id))
		}
	}
}

func TestGenerateUniqueIDInvalidLength(t *testing.T) {
	for _, length := range []int{0, -2, maxIDLength + 2} {
		if _, err := GenerateUniqueID(length); err == nil {
			t.Errorf("GenerateUniqueID(%d) = nil error, want a length error", length)
		}
	}
}

func TestGenerateUniqueIDIsDistinct(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for range 1000 {
		id, err := GenerateUniqueID(20)
		if err != nil {
			t.Fatalf("GenerateUniqueID: %v", err)
		}
		if seen[id] {
			t.Fatalf("GenerateUniqueID returned a duplicate: %q", id)
		}
		seen[id] = true
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestGenerateUniqueIDRandomFailure(t *testing.T) {
	uuid.SetRand(failingReader{})
	t.Cleanup(func() { uuid.SetRand(nil) })

	if _, err := GenerateUniqueID(20); err == nil {
		t.Error("GenerateUniqueID = nil error, want the entropy failure propagated")
	}
}
