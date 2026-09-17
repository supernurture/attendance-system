package attendance

import (
	"context"
	"time"

	"attendance-system/internal/api/server/modules/schedule"
	"attendance-system/internal/middleware"
	"attendance-system/internal/pkg/apperr"
)

// Presence is where one person stands on a date, with their attendance when they have one.
type Presence struct {
	UserID       int64
	FullName     string
	DepartmentID *int64
	Date         time.Time
	Status       Status
	Attendance   *Attendance
}

// Filter narrows Daily to one person or one department; nil fields do not narrow.
type Filter struct {
	UserID       *int64
	DepartmentID *int64
}

// WhosIn lists everyone the caller reaches who is employed and active on date, and where each of them stands.
func (s *Service) WhosIn(ctx context.Context, claims middleware.Claims, date time.Time) ([]Presence, error) {
	if !claims.Role.AtLeast(middleware.RoleSupervisor) {
		return nil, apperr.ErrForbidden
	}

	date = dateOnly(date)
	people, err := s.repo.People(ctx, scope(claims), date)
	if err != nil {
		return nil, err
	}
	return s.standings(ctx, scope(claims), people, schedule.Range{From: date, To: date})
}

// Daily is rule B over a span: one entry per person the caller reaches per date they were employed on, active or
// not, by name then date.
func (s *Service) Daily(
	ctx context.Context, claims middleware.Claims, span schedule.Range, filter Filter,
) ([]Presence, error) {
	if !claims.Role.AtLeast(middleware.RoleSupervisor) {
		return nil, apperr.ErrForbidden
	}

	span, err := schedule.CheckRange(span)
	if err != nil {
		return nil, err
	}
	people, err := s.repo.Staff(ctx, scope(claims), span, filter)
	if err != nil {
		return nil, err
	}
	return s.standings(ctx, scope(claims), people, span)
}

// dayKey is one person on one date.
type dayKey struct {
	userID int64
	date   int64 // Unix seconds of the date's UTC midnight
}

func keyOf(userID int64, date time.Time) dayKey { return dayKey{userID, date.Unix()} }

// standings resolves every person on every date of span they were employed on. Rule A runs over rows loaded once
// for all of them, not once per person or per day.
func (s *Service) standings(
	ctx context.Context, managerID *int64, people []person, span schedule.Range,
) ([]Presence, error) {
	rows, err := s.repo.Within(ctx, managerID, span)
	if err != nil {
		return nil, err
	}
	leaves, err := s.repo.LeaveWithin(ctx, managerID, span)
	if err != nil {
		return nil, err
	}
	roster, err := s.roster.ListAssignments(ctx, span, nil)
	if err != nil {
		return nil, err
	}
	holidays, err := s.roster.ListHolidays(ctx, span)
	if err != nil {
		return nil, err
	}

	attended := make(map[dayKey]Attendance, len(rows))
	for _, row := range rows {
		attended[keyOf(row.UserID, row.WorkDate)] = row
	}
	away := make(map[int64][]leave, len(leaves))
	for _, taken := range leaves {
		away[taken.UserID] = append(away[taken.UserID], taken)
	}
	named := make(map[int64]string, len(holidays))
	for _, holiday := range holidays {
		named[holiday.Date.Unix()] = holiday.Name
	}
	assigned := make(map[dayKey]schedule.ShiftAssignment, len(roster))
	wanted := make([]int64, 0, len(people)+len(roster))
	for _, assignment := range roster {
		assigned[keyOf(assignment.UserID, assignment.WorkDate)] = assignment
		wanted = appendID(wanted, assignment.ScheduleID)
	}
	for _, member := range people {
		wanted = appendID(wanted, member.DefaultScheduleID)
	}
	schedules, err := s.roster.SchedulesByID(ctx, wanted)
	if err != nil {
		return nil, err
	}

	at := now()
	presences := make([]Presence, 0, len(people))
	for _, member := range people {
		for date := span.From; !date.After(span.To); date = date.AddDate(0, 0, 1) {
			gone := member.DeletedAt != nil && date.After(dateIn(*member.DeletedAt, s.zone))
			if date.Before(member.JoinDate) || gone {
				continue
			}

			presence := Presence{UserID: member.ID, FullName: member.FullName, DepartmentID: member.DepartmentID,
				Date: date}
			if row, ok := attended[keyOf(member.ID, date)]; ok {
				presence.Attendance, presence.Status = &row, StatusPresent
				if row.LateMinutes > 0 {
					presence.Status = StatusLate
				}
			} else if covered(away[member.ID], date) {
				presence.Status = StatusOnLeave
			} else {
				in := schedule.Inputs{Default: pick(schedules, member.DefaultScheduleID), Holiday: named[date.Unix()]}
				if assignment, ok := assigned[keyOf(member.ID, date)]; ok {
					in.Assignment, in.Assigned = &assignment, pick(schedules, assignment.ScheduleID)
				}
				presence.Status = standing(schedule.Resolve(date, in), at, s.zone)
			}
			presences = append(presences, presence)
		}
	}
	return presences, nil
}

// standing is rule B for a day with neither attendance nor approved leave. A working day counts as absent only once
// its shift and grace have started.
func standing(day schedule.Day, at time.Time, zone *time.Location) Status {
	switch {
	case !day.Working && day.Holiday != "":
		return StatusHoliday
	case !day.Working:
		return StatusOff
	}

	start := place(day.Date, day.Schedule, zone).start
	if at.Before(start.Add(time.Duration(day.Schedule.GraceMinutes) * time.Minute)) {
		return StatusNotYet
	}
	return StatusAbsent
}

// covered reports whether any of the leaves includes date.
func covered(leaves []leave, date time.Time) bool {
	for _, taken := range leaves {
		if !date.Before(taken.StartDate) && !date.After(taken.EndDate) {
			return true
		}
	}
	return false
}

func appendID(ids []int64, id *int64) []int64 {
	if id == nil {
		return ids
	}
	return append(ids, *id)
}

func pick(schedules map[int64]schedule.WorkSchedule, id *int64) *schedule.WorkSchedule {
	if id == nil {
		return nil
	}
	hours := schedules[*id]
	return &hours
}
