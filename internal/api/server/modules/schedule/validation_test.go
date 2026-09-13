package schedule

import (
	"slices"
	"strings"
	"testing"
	"time"

	"attendance-system/internal/pkg/apperr"
)

func TestCheckRange(t *testing.T) {
	t.Parallel()

	from := day(2026, time.September, 1)
	tests := []struct {
		name string
		span Range
		bad  bool
	}{
		{name: "one day", span: Range{From: from, To: from}},
		{name: "a month", span: Range{From: from, To: from.AddDate(0, 1, 0)}},
		{name: "the longest span allowed", span: Range{From: from, To: from.AddDate(0, 0, maxRangeDays-1)}},
		{name: "no from", span: Range{To: from}, bad: true},
		{name: "no to", span: Range{From: from}, bad: true},
		{name: "backwards", span: Range{From: from, To: from.AddDate(0, 0, -1)}, bad: true},
		{name: "one day too long", span: Range{From: from, To: from.AddDate(0, 0, maxRangeDays)}, bad: true},
		{
			// Counted as calendar days, so the time of day either end carries cannot tip it over.
			name: "the longest span, with both ends mid-day",
			span: Range{From: from.Add(23 * time.Hour), To: from.AddDate(0, 0, maxRangeDays-1).Add(time.Hour)},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			span, err := CheckRange(test.span)
			if test.bad != apperr.IsValidation(err) {
				t.Fatalf("CheckRange(%v) error = %v, want validation = %v", test.span, err, test.bad)
			}
			if err == nil && (span.From.Hour() != 0 || span.To.Hour() != 0) {
				t.Errorf("CheckRange returned %v, want both ends at midnight", span)
			}
		})
	}
}

func TestRangeDays(t *testing.T) {
	t.Parallel()

	from := day(2026, time.September, 1)
	tests := []struct {
		span Range
		want int
	}{
		{span: Range{From: from, To: from}, want: 1},
		{span: Range{From: from, To: from.AddDate(0, 0, 6)}, want: 7},
		{span: Range{From: from, To: from.AddDate(0, 0, 29)}, want: 30},
	}

	for _, test := range tests {
		if got := test.span.days(); got != test.want {
			t.Errorf("days() = %d, want %d", got, test.want)
		}
	}
}

func TestCheckName(t *testing.T) {
	t.Parallel()

	got, err := checkName("  Shift Malam  ")
	if err != nil {
		t.Fatalf("checkName: %v", err)
	}
	if got != "Shift Malam" {
		t.Errorf("checkName trimmed to %q", got)
	}

	for _, name := range []string{"", "   ", strings.Repeat("a", 101)} {
		if _, err := checkName(name); !apperr.IsValidation(err) {
			t.Errorf("checkName(%d chars) error = %v, want a validation error", len(name), err)
		}
	}
}

func TestCheckWorkdays(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		workdays []int
		want     []int16
		bad      bool
	}{
		{name: "weekdays", workdays: []int{1, 2, 3, 4, 5}, want: []int16{1, 2, 3, 4, 5}},
		{name: "sorted and deduped", workdays: []int{5, 1, 5, 3}, want: []int16{1, 3, 5}},
		{name: "Sunday alone", workdays: []int{7}, want: []int16{7}},
		{name: "empty", workdays: nil, bad: true},
		{name: "zero is not a weekday", workdays: []int{0, 1}, bad: true},
		{name: "eight is not a weekday", workdays: []int{1, 8}, bad: true},
		// 65537 truncates to 1 in an int16: narrowing before the range check would accept it as Monday.
		{name: "a value that wraps into a weekday", workdays: []int{65537}, bad: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := checkWorkdays(test.workdays)
			if test.bad {
				if !apperr.IsValidation(err) {
					t.Fatalf("checkWorkdays(%v) error = %v, want a validation error", test.workdays, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("checkWorkdays(%v): %v", test.workdays, err)
			}
			if !slices.Equal(got, test.want) {
				t.Errorf("checkWorkdays(%v) = %v, want %v", test.workdays, got, test.want)
			}
		})
	}
}

func TestCheckSchedule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                       string
		start, end                 Clock
		graceMinutes, breakMinutes int
		bad                        bool
	}{
		{name: "an office day", start: 8 * 60, end: 17 * 60, graceMinutes: 15, breakMinutes: 60},
		{name: "a night shift", start: 22 * 60, end: 6 * 60, breakMinutes: 60},
		{name: "no grace and no break", start: 8 * 60, end: 17 * 60},
		{name: "the longest grace allowed", start: 8 * 60, end: 17 * 60, graceMinutes: 240},
		{name: "equal times", start: 8 * 60, end: 8 * 60, bad: true},
		{name: "negative grace", start: 8 * 60, end: 17 * 60, graceMinutes: -1, bad: true},
		{name: "grace beyond four hours", start: 8 * 60, end: 17 * 60, graceMinutes: 241, bad: true},
		{name: "negative break", start: 8 * 60, end: 17 * 60, breakMinutes: -1, bad: true},
		{name: "break beyond eight hours", start: 0, end: 23 * 60, breakMinutes: 481, bad: true},
		{name: "break as long as the shift", start: 8 * 60, end: 12 * 60, breakMinutes: 240, bad: true},
		{name: "break longer than a night shift", start: 22 * 60, end: 2 * 60, breakMinutes: 300, bad: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := checkSchedule(test.start, test.end, test.graceMinutes, test.breakMinutes)
			if test.bad != apperr.IsValidation(err) {
				t.Fatalf("checkSchedule error = %v, want validation = %v", err, test.bad)
			}
		})
	}
}

func TestCheckLocation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		lat, lng float64
		radiusM  int
		bad      bool
	}{
		{name: "Jakarta", lat: -6.2088, lng: 106.8456, radiusM: 100},
		{name: "the poles and the date line", lat: 90, lng: 180, radiusM: 10000},
		{name: "lat too far south", lat: -91, lng: 0, radiusM: 100, bad: true},
		{name: "lat too far north", lat: 91, lng: 0, radiusM: 100, bad: true},
		{name: "lng too far west", lat: 0, lng: -181, radiusM: 100, bad: true},
		{name: "lng too far east", lat: 0, lng: 181, radiusM: 100, bad: true},
		{name: "radius too small to be useful", lat: 0, lng: 0, radiusM: 9, bad: true},
		{name: "radius larger than a city block", lat: 0, lng: 0, radiusM: 10001, bad: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := checkLocation(test.lat, test.lng, test.radiusM)
			if test.bad != apperr.IsValidation(err) {
				t.Fatalf("checkLocation error = %v, want validation = %v", err, test.bad)
			}
		})
	}
}

func TestLength(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		start, end Clock
		want       Clock
	}{
		{name: "an office day", start: 8 * 60, end: 17 * 60, want: 9 * 60},
		{name: "a night shift wraps", start: 22 * 60, end: 6 * 60, want: 8 * 60},
		{name: "equal times are a whole day", start: 8 * 60, end: 8 * 60, want: minutesPerDay},
	}

	for _, test := range tests {
		if got := length(test.start, test.end); got != test.want {
			t.Errorf("%s: length = %d, want %d", test.name, got, test.want)
		}
	}
}

func TestCheckAssignments(t *testing.T) {
	t.Parallel()

	date := day(2026, time.September, 14)
	tooMany := make([]Assignment, maxBulkAssigned+1)
	for index := range tooMany {
		tooMany[index] = Assignment{UserID: int64(index + 1), WorkDate: date}
	}

	tests := []struct {
		name        string
		assignments []Assignment
		bad         bool
	}{
		{name: "one entry", assignments: []Assignment{{UserID: 1, WorkDate: date}}},
		{
			name: "the same person on two dates",
			assignments: []Assignment{
				{UserID: 1, WorkDate: date},
				{UserID: 1, WorkDate: date.AddDate(0, 0, 1)},
			},
		},
		{name: "the most allowed at once", assignments: tooMany[:maxBulkAssigned]},
		{name: "empty", assignments: nil, bad: true},
		{name: "more than allowed", assignments: tooMany, bad: true},
		{name: "no user", assignments: []Assignment{{WorkDate: date}}, bad: true},
		{name: "no date", assignments: []Assignment{{UserID: 1}}, bad: true},
		{
			name:        "a note longer than the column",
			assignments: []Assignment{{UserID: 1, WorkDate: date, Note: strings.Repeat("n", 201)}},
			bad:         true,
		},
		{
			name: "the same person twice on one date",
			assignments: []Assignment{
				{UserID: 1, WorkDate: date},
				{UserID: 1, WorkDate: date.Add(13 * time.Hour)}, // the same calendar day
			},
			bad: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := checkAssignments(test.assignments)
			if test.bad != apperr.IsValidation(err) {
				t.Fatalf("checkAssignments error = %v, want validation = %v", err, test.bad)
			}
		})
	}
}
