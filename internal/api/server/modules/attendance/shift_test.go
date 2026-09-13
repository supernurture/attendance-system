package attendance

import (
	"testing"
	"time"

	"attendance-system/internal/api/server/modules/schedule"
	"attendance-system/internal/pkg/apperr"
)

var (
	dayShift      = &schedule.WorkSchedule{ID: 1, StartTime: 8 * 60, EndTime: 17 * 60, GraceMinutes: 15}
	nightShift    = &schedule.WorkSchedule{ID: 2, StartTime: 22 * 60, EndTime: 6 * 60}
	midnightShift = &schedule.WorkSchedule{ID: 3} // 00:00 to 00:00, all day from midnight
)

func TestPlace(t *testing.T) {
	day := place(date(2030, time.March, 4), dayShift, jakarta)
	if !day.start.Equal(local(2030, time.March, 4, 8, 0)) || !day.end.Equal(local(2030, time.March, 4, 17, 0)) {
		t.Errorf("08:00-17:00 placed at %v to %v", day.start, day.end)
	}

	night := place(date(2030, time.March, 4), nightShift, jakarta)
	if !night.start.Equal(local(2030, time.March, 4, 22, 0)) || !night.end.Equal(local(2030, time.March, 5, 6, 0)) {
		t.Errorf("22:00-06:00 placed at %v to %v, want the end on the next day", night.start, night.end)
	}
	if *night.scheduleID() != nightShift.ID {
		t.Errorf("scheduleID = %d, want %d", *night.scheduleID(), nightShift.ID)
	}

	none := place(date(2030, time.March, 4), nil, jakarta)
	if none.scheduleID() != nil || none.lateMinutes(local(2030, time.March, 4, 23, 0)) != 0 ||
		none.earlyLeaveMinutes(local(2030, time.March, 4, 1, 0)) != 0 {
		t.Errorf("a day with no hours has a schedule or counts minutes: %+v", none)
	}
}

func TestLateAndEarlyLeaveMinutes(t *testing.T) {
	day := place(date(2030, time.March, 4), dayShift, jakarta)
	at := func(hour, minute, second int) time.Time {
		return time.Date(2030, time.March, 4, hour, minute, second, 0, jakarta)
	}

	late := map[time.Time]int{
		at(8, 20, 0):  5, // start 08:00, grace 15 minutes
		at(8, 15, 59): 0, // still inside the grace: only whole minutes count
		at(7, 30, 0):  0,
		at(9, 0, 0):   45,
	}
	for moment, want := range late {
		if got := day.lateMinutes(moment); got != want {
			t.Errorf("check-in at %s: late = %d, want %d", moment.Format(time.TimeOnly), got, want)
		}
	}

	// Neither count runs past the 540-minute shift: arriving after it ended, or leaving before it began.
	if got := day.lateMinutes(at(21, 0, 0)); got != 540 {
		t.Errorf("check-in at 21:00: late = %d, want the whole shift, 540", got)
	}

	early := map[time.Time]int{
		at(5, 0, 0):    540,
		at(16, 0, 0):   60,
		at(16, 59, 30): 0,
		at(17, 30, 0):  0,
	}
	for moment, want := range early {
		if got := day.earlyLeaveMinutes(moment); got != want {
			t.Errorf("check-out at %s: early = %d, want %d", moment.Format(time.TimeOnly), got, want)
		}
	}
}

func TestPickShift(t *testing.T) {
	yesterday, today, tomorrow := date(2030, time.March, 3), date(2030, time.March, 4), date(2030, time.March, 5)
	working := func(day time.Time, hours *schedule.WorkSchedule) schedule.Day {
		return schedule.Day{Date: day, Working: true, Schedule: hours}
	}
	days := func(hours ...*schedule.WorkSchedule) []schedule.Day {
		listed := []schedule.Day{}
		for index, day := range []time.Time{yesterday, today, tomorrow} {
			if hours[index] == nil {
				listed = append(listed, schedule.Day{Date: day})
				continue
			}
			listed = append(listed, working(day, hours[index]))
		}
		return listed
	}

	tests := map[string]struct {
		days     []schedule.Day
		at       time.Time
		wantDate time.Time
		wantID   int64 // 0 for no schedule
	}{
		"on time":                 {days(dayShift, dayShift, dayShift), local(2030, 3, 4, 8, 20), today, 1},
		"inside the early window": {days(dayShift, dayShift, dayShift), local(2030, 3, 4, 4, 0), today, 1},
		"after the shift ended":   {days(dayShift, dayShift, dayShift), local(2030, 3, 4, 21, 0), today, 1},
		"night shift after midnight": {days(nightShift, nightShift, nightShift), local(2030, 3, 4, 1, 0), yesterday,
			2},
		"night shift before its start":  {days(nightShift, nightShift, nightShift), local(2030, 3, 4, 19, 0), today, 2},
		"late evening for tomorrow":     {days(nil, nil, midnightShift), local(2030, 3, 4, 21, 0), tomorrow, 3},
		"nearest start of two windows":  {days(nightShift, dayShift, dayShift), local(2030, 3, 4, 5, 0), today, 1},
		"a day off":                     {days(nil, nil, nil), local(2030, 3, 4, 10, 0), today, 0},
		"a day off after a night shift": {days(nightShift, nil, nil), local(2030, 3, 4, 9, 0), today, 0},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := pickShift(test.at, jakarta, test.days)
			if err != nil {
				t.Fatalf("pickShift: %v", err)
			}
			if !got.date.Equal(test.wantDate) {
				t.Errorf("work date = %s, want %s", got.date.Format(time.DateOnly), test.wantDate.Format(time.DateOnly))
			}
			if gotID := got.scheduleID(); !sameID(gotID, test.wantID) {
				t.Errorf("schedule = %v, want %d", gotID, test.wantID)
			}
		})
	}

	// Nothing in reach, and today's shift has not opened yet.
	_, err := pickShift(local(2030, 3, 4, 3, 0), jakarta, days(nil, dayShift, dayShift))
	if !apperr.IsValidation(err) {
		t.Errorf("03:00 for an 08:00 shift: err = %v, want a validation error", err)
	}
}

func TestDateIn(t *testing.T) {
	// 23:30 UTC is already the next morning in Jakarta.
	if got := dateIn(time.Date(2030, time.March, 4, 23, 30, 0, 0, time.UTC), jakarta); !got.Equal(date(2030, 3, 5)) {
		t.Errorf("dateIn = %v, want 2030-03-05", got)
	}
}
