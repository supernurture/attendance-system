package schedule

import (
	"slices"
	"strings"
	"time"

	"attendance-system/internal/pkg/apperr"
)

// Range is an inclusive span of dates.
type Range struct {
	From time.Time
	To   time.Time
}

// days is how many dates the span covers, both ends included.
func (r Range) days() int {
	return int(r.To.Sub(r.From).Hours()/24) + 1
}

// CheckRange refuses a span the database should not be asked for, and returns it as calendar days so
// the count cannot depend on the time of day either end carries.
func CheckRange(span Range) (Range, error) {
	if span.From.IsZero() || span.To.IsZero() {
		return Range{}, apperr.Invalid("from and to are both required")
	}

	span = Range{From: dateOnly(span.From), To: dateOnly(span.To)}
	switch {
	case span.To.Before(span.From):
		return Range{}, apperr.Invalid("to cannot be before from")
	case span.days() > maxRangeDays:
		return Range{}, apperr.Invalid("the range must be at most %d days", maxRangeDays)
	}
	return span, nil
}

// checkName trims a name and rejects what the column cannot hold.
func checkName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", apperr.Invalid("name is required")
	case len(name) > 100:
		return "", apperr.Invalid("name must be at most 100 characters")
	}
	return name, nil
}

// checkWorkdays keeps the ISO weekday list the column's CHECK would refuse anyway, sorted and without
// repeats so two equal schedules read the same. It takes int and narrows afterwards: narrowing first
// would wrap 65537 into a valid Monday.
func checkWorkdays(workdays []int) ([]int16, error) {
	if len(workdays) == 0 {
		return nil, apperr.Invalid("workdays needs at least one day, 1 (Monday) to 7 (Sunday)")
	}

	sorted := slices.Clone(workdays)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)

	narrowed := make([]int16, 0, len(sorted))
	for _, day := range sorted {
		if day < 1 || day > 7 {
			return nil, apperr.Invalid("workdays must be between 1 (Monday) and 7 (Sunday), got %d", day)
		}
		narrowed = append(narrowed, int16(day))
	}
	return narrowed, nil
}

// checkSchedule validates the hours a schedule keeps. A shift whose end is at or before its start
// crosses midnight, which is allowed; equal times would be a zero-length or 24-hour shift, which is not.
func checkSchedule(start, end Clock, graceMinutes, breakMinutes int) error {
	switch {
	case start == end:
		return apperr.Invalid("start_time and end_time cannot be the same")
	case graceMinutes < 0 || graceMinutes > 240:
		return apperr.Invalid("grace_minutes must be between 0 and 240")
	case breakMinutes < 0 || breakMinutes > 480:
		return apperr.Invalid("break_minutes must be between 0 and 480")
	case breakMinutes >= int(length(start, end)):
		return apperr.Invalid("break_minutes must be shorter than the shift")
	}
	return nil
}

// checkLocation validates one office's circle on the map.
func checkLocation(lat, lng float64, radiusM int) error {
	switch {
	case lat < -90 || lat > 90:
		return apperr.Invalid("lat must be between -90 and 90")
	case lng < -180 || lng > 180:
		return apperr.Invalid("lng must be between -180 and 180")
	case radiusM < 10 || radiusM > 10000:
		return apperr.Invalid("radius_m must be between 10 and 10000")
	}
	return nil
}

// checkAssignments validates one bulk roster write, including the same person twice on one date, which
// the upsert would otherwise silently collapse into whichever row landed last.
func checkAssignments(assignments []Assignment) error {
	switch {
	case len(assignments) == 0:
		return apperr.Invalid("assignments is required")
	case len(assignments) > maxBulkAssigned:
		return apperr.Invalid("at most %d assignments per request", maxBulkAssigned)
	}

	seen := make(map[string]struct{}, len(assignments))
	for _, assignment := range assignments {
		switch {
		case assignment.UserID < 1:
			return apperr.Invalid("user_id is required")
		case assignment.WorkDate.IsZero():
			return apperr.Invalid("work_date is required")
		case len(assignment.Note) > 200:
			return apperr.Invalid("note must be at most 200 characters")
		}

		taken := slot(assignment.UserID, assignment.WorkDate)
		if _, repeated := seen[taken]; repeated {
			return apperr.Invalid("user %d is assigned twice on %s",
				assignment.UserID, key(assignment.WorkDate))
		}
		seen[taken] = struct{}{}
	}
	return nil
}

// length is how long a shift runs, counting the wrap past midnight.
func length(start, end Clock) Clock {
	if end > start {
		return end - start
	}
	return minutesPerDay - start + end
}
