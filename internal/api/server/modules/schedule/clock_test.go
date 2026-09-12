package schedule

import (
	"testing"

	"attendance-system/internal/pkg/apperr"
)

func TestParseClock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  Clock
		bad   bool
	}{
		{name: "hours and minutes", value: "08:30", want: 8*60 + 30},
		{name: "seconds are ignored", value: "22:00:00", want: 22 * 60},
		{name: "surrounding space", value: " 06:00 ", want: 6 * 60},
		{name: "midnight", value: "00:00"},
		{name: "the last minute of the day", value: "23:59", want: 23*60 + 59},
		{name: "no colon", value: "0830", bad: true},
		{name: "too many parts", value: "08:30:00:00", bad: true},
		{name: "hours out of range", value: "24:00", bad: true},
		{name: "minutes out of range", value: "08:60", bad: true},
		{name: "negative", value: "-1:00", bad: true},
		{name: "not a number", value: "ab:cd", bad: true},
		{name: "minutes not a number", value: "08:cd", bad: true},
		{name: "empty", value: "", bad: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseClock(test.value)
			if test.bad {
				if !apperr.IsValidation(err) {
					t.Fatalf("ParseClock(%q) error = %v, want a validation error", test.value, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseClock(%q): %v", test.value, err)
			}
			if got != test.want {
				t.Errorf("ParseClock(%q) = %d, want %d", test.value, got, test.want)
			}
		})
	}
}

func TestClockString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		clock Clock
		want  string
	}{
		{clock: 0, want: "00:00"},
		{clock: 8 * 60, want: "08:00"},
		{clock: 22*60 + 5, want: "22:05"},
		{clock: minutesPerDay - 1, want: "23:59"},
	}

	for _, test := range tests {
		if got := test.clock.String(); got != test.want {
			t.Errorf("Clock(%d).String() = %q, want %q", test.clock, got, test.want)
		}
	}
}

func TestClockScan(t *testing.T) {
	t.Parallel()

	var clock Clock
	if err := clock.Scan("08:00:00"); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if clock != 8*60 {
		t.Errorf("clock = %d, want %d", clock, 8*60)
	}

	// Postgres sends a time column as text; anything else means the column is not what we think.
	if err := clock.Scan([]byte("08:00:00")); err == nil {
		t.Error("Scan([]byte) should report the type it got")
	}
	if err := clock.Scan("not a time"); err == nil {
		t.Error("Scan of a malformed time should fail")
	}
}

func TestClockValue(t *testing.T) {
	t.Parallel()

	value, err := Clock(22 * 60).Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if value != "22:00:00" {
		t.Errorf("Value() = %v, want 22:00:00", value)
	}
}
