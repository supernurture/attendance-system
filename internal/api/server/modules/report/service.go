package report

import (
	"context"
	"slices"
	"time"

	"attendance-system/internal/api/server/modules/attendance"
	"attendance-system/internal/api/server/modules/schedule"
	"attendance-system/internal/middleware"
)

// Query is what a report covers: a span, whom, and optionally only the days with one status.
type Query struct {
	Span   schedule.Range
	Filter attendance.Filter
	Status *attendance.Status
}

// Total is one person's days over a report, counted by status.
type Total struct {
	UserID        int64
	FullName      string
	DepartmentID  *int64
	Present       int // on time
	Late          int
	OnLeave       int
	Absent        int
	WorkedMinutes int
}

// Report is every day a query covers and each person's totals over those same days.
type Report struct {
	Span   schedule.Range
	Days   []attendance.Presence
	People []Total
}

type Service struct {
	attendance *attendance.Service
}

// NewService builds reports on the daily statuses the attendance service resolves.
func NewService(attendance *attendance.Service) *Service {
	return &Service{attendance: attendance}
}

// Attendance is the report behind all three formats, for supervisors and up.
func (s *Service) Attendance(ctx context.Context, claims middleware.Claims, query Query) (Report, error) {
	if err := checkStatus(query.Status); err != nil {
		return Report{}, err
	}

	days, err := s.attendance.Daily(ctx, claims, query.Span, query.Filter)
	if err != nil {
		return Report{}, err
	}
	if query.Status != nil {
		days = slices.DeleteFunc(days, func(day attendance.Presence) bool { return day.Status != *query.Status })
	}
	return Report{Span: query.Span, Days: days, People: totals(days)}, nil
}

// CSV is the report's days, one row each.
func (s *Service) CSV(ctx context.Context, claims middleware.Claims, query Query) ([]byte, error) {
	report, err := s.Attendance(ctx, claims, query)
	if err != nil {
		return nil, err
	}
	return writeCSV(report), nil
}

// PDF is the report's totals, one line per person.
func (s *Service) PDF(ctx context.Context, claims middleware.Claims, query Query) ([]byte, error) {
	report, err := s.Attendance(ctx, claims, query)
	if err != nil {
		return nil, err
	}
	return writePDF(report)
}

// totals counts each person's days; days come grouped by person, so each total follows the last.
func totals(days []attendance.Presence) []Total {
	var people []Total
	for _, day := range days {
		if len(people) == 0 || people[len(people)-1].UserID != day.UserID {
			people = append(people, Total{UserID: day.UserID, FullName: day.FullName, DepartmentID: day.DepartmentID})
		}

		total := &people[len(people)-1]
		switch day.Status {
		case attendance.StatusPresent:
			total.Present++
		case attendance.StatusLate:
			total.Late++
		case attendance.StatusOnLeave:
			total.OnLeave++
		case attendance.StatusAbsent:
			total.Absent++
		}
		total.WorkedMinutes += worked(day)
	}
	return people
}

// worked is whole minutes from check-in to check-out, 0 without both.
func worked(day attendance.Presence) int {
	if day.Attendance == nil || day.Attendance.CheckOutAt == nil {
		return 0
	}
	return int(day.Attendance.CheckOutAt.Sub(day.Attendance.CheckInAt) / time.Minute)
}
