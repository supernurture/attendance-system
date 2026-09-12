package schedule

import (
	"testing"
	"time"
)

func TestResolve(t *testing.T) {
	t.Parallel()

	office := &WorkSchedule{
		ID: 1, Name: "Office", StartTime: 8 * 60, EndTime: 17 * 60,
		Workdays: pqInt16Array{1, 2, 3, 4, 5}, ObservesHolidays: true,
	}
	night := &WorkSchedule{
		ID: 2, Name: "Night", StartTime: 22 * 60, EndTime: 6 * 60,
		Workdays: pqInt16Array{1, 2, 3, 4, 5, 6, 7}, ObservesHolidays: true,
	}
	guard := &WorkSchedule{
		ID: 3, Name: "Guard", StartTime: 7 * 60, EndTime: 19 * 60,
		Workdays: pqInt16Array{1, 2, 3, 4, 5, 6, 7}, ObservesHolidays: false,
	}
	rostered := &ShiftAssignment{ID: 9, UserID: 7}

	// 2026-09-13 is a Sunday, 2026-09-14 a Monday.
	sunday, monday := day(2026, time.September, 13), day(2026, time.September, 14)

	tests := []struct {
		name     string
		date     time.Time
		in       Inputs
		working  bool
		reason   Reason
		schedule *WorkSchedule
	}{
		{
			name: "developer on a Sunday is off",
			date: sunday, in: Inputs{Default: office},
			reason: ReasonNotAWorkday,
		},
		{
			name: "developer on a Monday works their own schedule",
			date: monday, in: Inputs{Default: office},
			working: true, reason: ReasonDefault, schedule: office,
		},
		{
			name: "a guard rostered onto a public holiday still works",
			date: monday, in: Inputs{Assignment: rostered, Assigned: guard, Default: office, Holiday: "Idul Fitri"},
			working: true, reason: ReasonShift, schedule: guard,
		},
		{
			name: "an observed holiday turns the roster off",
			date: monday, in: Inputs{Assignment: rostered, Assigned: night, Default: office, Holiday: "Idul Fitri"},
			reason: ReasonHoliday,
		},
		{
			name: "an observed holiday turns the default schedule off",
			date: monday, in: Inputs{Default: office, Holiday: "Idul Fitri"},
			reason: ReasonHoliday,
		},
		{
			name: "a roster entry with no schedule is a day off, weekday or not",
			date: monday, in: Inputs{Assignment: rostered, Default: office},
			reason: ReasonAssignedOff,
		},
		{
			name: "a roster entry with no schedule wins over a holiday too",
			date: monday, in: Inputs{Assignment: rostered, Default: office, Holiday: "Idul Fitri"},
			reason: ReasonAssignedOff,
		},
		{
			name: "the roster works a Sunday the default schedule does not",
			date: sunday, in: Inputs{Assignment: rostered, Assigned: night, Default: office},
			working: true, reason: ReasonShift, schedule: night,
		},
		{
			name: "nobody with a schedule is never working",
			date: monday, in: Inputs{},
			reason: ReasonNotAWorkday,
		},
		{
			name: "an unobserved holiday on a day the schedule does not cover is still off",
			date: sunday, in: Inputs{Default: office, Holiday: "Idul Fitri"},
			reason: ReasonNotAWorkday,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := Resolve(test.date, test.in)
			if got.Working != test.working {
				t.Errorf("working = %v, want %v", got.Working, test.working)
			}
			if got.Reason != test.reason {
				t.Errorf("reason = %q, want %q", got.Reason, test.reason)
			}
			if got.Schedule != test.schedule {
				t.Errorf("schedule = %v, want %v", got.Schedule, test.schedule)
			}
			if !got.Date.Equal(test.date) {
				t.Errorf("date = %s, want %s", got.Date, test.date)
			}
			if got.Holiday != test.in.Holiday {
				t.Errorf("holiday = %q, want %q", got.Holiday, test.in.Holiday)
			}
		})
	}
}

func TestCrossesMidnight(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		start, end Clock
		want       bool
	}{
		{name: "a night shift wraps", start: 22 * 60, end: 6 * 60, want: true},
		{name: "an office day does not", start: 8 * 60, end: 17 * 60},
		{name: "ending exactly at the start wraps a full day", start: 8 * 60, end: 8 * 60, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			schedule := WorkSchedule{StartTime: test.start, EndTime: test.end}
			if got := schedule.CrossesMidnight(); got != test.want {
				t.Errorf("CrossesMidnight() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestWorksOn(t *testing.T) {
	t.Parallel()

	weekdays := &WorkSchedule{Workdays: pqInt16Array{1, 2, 3, 4, 5}}
	sundayOnly := &WorkSchedule{Workdays: pqInt16Array{7}}

	// Go counts Sunday as 0 while the column uses ISO, where Sunday is 7.
	if worksOn(weekdays, day(2026, time.September, 13)) {
		t.Error("Sunday is not one of workdays 1-5")
	}
	if !worksOn(sundayOnly, day(2026, time.September, 13)) {
		t.Error("Sunday should match ISO weekday 7")
	}
	if !worksOn(weekdays, day(2026, time.September, 14)) {
		t.Error("Monday is workday 1")
	}
}
