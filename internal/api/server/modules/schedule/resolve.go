package schedule

import (
	"slices"
	"time"
)

// Day is what rule A decides for one person on one date.
type Day struct {
	Date     time.Time
	Working  bool
	Reason   Reason
	Schedule *WorkSchedule // the hours that apply, nil when the day is off
	Holiday  string        // the holiday's name when one falls on this date, whether observed or not
}

// Inputs are the rows rule A needs for one date, already loaded.
type Inputs struct {
	Assignment *ShiftAssignment // the roster's entry for this date, nil when there is none
	Assigned   *WorkSchedule    // the schedule that assignment names, nil when it names none
	Default    *WorkSchedule    // the user's own schedule, nil when they have none
	Holiday    string           // the holiday's name, empty when the date is an ordinary one
}

// Resolve is rule A: the roster wins, then the user's own schedule, and an observed holiday turns
// either of them off. Everything else is simply not a working day.
func Resolve(date time.Time, in Inputs) Day {
	day := Day{Date: date, Holiday: in.Holiday}

	switch {
	case in.Assignment != nil && in.Assigned == nil:
		// A roster row naming no schedule is how it says "not this day".
		day.Reason = ReasonAssignedOff
		return day
	case in.Assignment != nil:
		day.Schedule, day.Reason = in.Assigned, ReasonShift
	case in.Default != nil && worksOn(in.Default, date):
		day.Schedule, day.Reason = in.Default, ReasonDefault
	default:
		day.Reason = ReasonNotAWorkday
		return day
	}

	if in.Holiday != "" && day.Schedule.ObservesHolidays {
		return Day{Date: date, Reason: ReasonHoliday, Holiday: in.Holiday}
	}
	day.Working = true
	return day
}

// worksOn reports whether the schedule covers that weekday, in ISO numbering (1=Mon..7=Sun).
func worksOn(schedule *WorkSchedule, date time.Time) bool {
	weekday := int16(date.Weekday())
	if weekday == 0 {
		weekday = 7 // Go counts Sunday as 0; the column uses ISO, where Sunday is 7
	}
	return slices.Contains(schedule.Workdays, weekday)
}
