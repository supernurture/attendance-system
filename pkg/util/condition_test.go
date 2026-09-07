package util

import "testing"

func TestTernary(t *testing.T) {
	if got := Ternary(true, "yes", "no"); got != "yes" {
		t.Errorf("Ternary(true) = %q, want %q", got, "yes")
	}
	if got := Ternary(false, "yes", "no"); got != "no" {
		t.Errorf("Ternary(false) = %q, want %q", got, "no")
	}
	if got := Ternary(2 > 4, 10, 20); got != 20 {
		t.Errorf("Ternary(2 > 4) = %d, want 20", got)
	}
}
