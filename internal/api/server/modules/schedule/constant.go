package schedule

// Reason says why a date is or is not a working day, so a client never has to guess.
type Reason string

const (
	ReasonShift       Reason = "shift"         // the roster names a schedule for this date
	ReasonDefault     Reason = "default"       // the person's own schedule covers this weekday
	ReasonAssignedOff Reason = "assigned_off"  // the roster names no schedule: a planned day off
	ReasonHoliday     Reason = "holiday"       // a public holiday the schedule observes
	ReasonNotAWorkday Reason = "not_a_workday" // outside the schedule's weekdays, or no schedule
)

// Actions recorded in audit_logs. Only past dates are written: editing the future changes a plan,
// while editing the past changes what the reports already said about people's attendance.
const (
	actionHolidayChanged = "holiday.changed"
	actionHolidayDeleted = "holiday.deleted"
	actionShiftChanged   = "shift_assignment.changed"
	actionShiftDeleted   = "shift_assignment.deleted"

	entityHoliday = "holiday"
	// entityShift entries carry the user's id, not the row's: a roster entry is identified by
	// (user_id, work_date), both of which the payload holds, and its own id is replaced on re-upsert.
	entityShift = "shift_assignment"

	nullJSON = "null" // audit payload for a state that does not exist, before a create or after a delete
)

const (
	maxRangeDays    = 366 // one year per request; reports page by month anyway
	maxBulkAssigned = 500 // a month of roster for a decent-sized team in one call
)
