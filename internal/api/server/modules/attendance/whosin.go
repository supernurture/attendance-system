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
	Status       Status
	Attendance   *Attendance
}

// WhosIn lists everyone the caller reaches who is employed and active on date, and where each of them stands.
// Rule A runs once over rows loaded for all of them, not once per person.
func (s *Service) WhosIn(ctx context.Context, claims middleware.Claims, date time.Time) ([]Presence, error) {
	if !claims.Role.AtLeast(middleware.RoleSupervisor) {
		return nil, apperr.ErrForbidden
	}

	date = dateOnly(date)
	people, err := s.repo.People(ctx, scope(claims), date)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.OnDate(ctx, scope(claims), date)
	if err != nil {
		return nil, err
	}

	span := schedule.Range{From: date, To: date}
	roster, err := s.roster.ListAssignments(ctx, span, nil)
	if err != nil {
		return nil, err
	}
	holidays, err := s.roster.ListHolidays(ctx, span)
	if err != nil {
		return nil, err
	}

	attended := make(map[int64]Attendance, len(rows))
	for _, row := range rows {
		attended[row.UserID] = row
	}
	assigned := make(map[int64]schedule.ShiftAssignment, len(roster))
	wanted := make([]int64, 0, len(people)+len(roster))
	for _, assignment := range roster {
		assigned[assignment.UserID] = assignment
		wanted = appendID(wanted, assignment.ScheduleID)
	}
	for _, member := range people {
		wanted = appendID(wanted, member.DefaultScheduleID)
	}
	schedules, err := s.roster.SchedulesByID(ctx, wanted)
	if err != nil {
		return nil, err
	}

	holiday := ""
	if len(holidays) > 0 {
		holiday = holidays[0].Name
	}
	at := now()
	presences := make([]Presence, 0, len(people))
	for _, member := range people {
		presence := Presence{UserID: member.ID, FullName: member.FullName, DepartmentID: member.DepartmentID}
		if row, ok := attended[member.ID]; ok {
			presence.Attendance, presence.Status = &row, StatusPresent
			if row.LateMinutes > 0 {
				presence.Status = StatusLate
			}
		} else {
			in := schedule.Inputs{Default: pick(schedules, member.DefaultScheduleID), Holiday: holiday}
			if assignment, ok := assigned[member.ID]; ok {
				in.Assignment, in.Assigned = &assignment, pick(schedules, assignment.ScheduleID)
			}
			presence.Status = standing(schedule.Resolve(date, in), at, s.zone)
		}
		presences = append(presences, presence)
	}
	return presences, nil
}

// standing is rule B for a day with no attendance; leave joins it in phase 5. A working day counts as absent only
// once its shift and grace have started.
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
