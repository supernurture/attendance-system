package report

import (
	"errors"
	"testing"
	"time"

	"attendance-system/internal/api/server/modules/attendance"
	"attendance-system/internal/api/server/modules/schedule"
	"attendance-system/internal/middleware"
	"attendance-system/internal/pkg/apperr"
)

// team is a supervisor's week: a developer who came twice, a guard rostered off on Thursday, someone on leave all
// week, and an outsider beyond the supervisor.
type team struct {
	lead, developer, guard, away, outsider middleware.Claims
}

func (s *server) week(t *testing.T) team {
	t.Helper()

	office := s.hours(t, true)
	shifts := s.hours(t, false)
	s.holiday(t, tuesday)

	var w team
	w.lead = s.person(t, "Lead", middleware.RoleSupervisor, nil, nil)
	w.developer = s.person(t, "Ani Developer", middleware.RoleEmployee, &w.lead.UserID, &office)
	w.guard = s.person(t, "Budi Guard", middleware.RoleEmployee, &w.lead.UserID, &shifts)
	w.away = s.person(t, "Citra Away", middleware.RoleEmployee, &w.lead.UserID, &office)
	w.outsider = s.person(t, "Dodi Outsider", middleware.RoleEmployee, nil, &office)

	s.worked(t, w.developer.UserID, monday, "08:00", "17:00", 0)
	s.worked(t, w.developer.UserID, wednesday, "08:30", "17:00", 30)
	s.rosteredOff(t, w.guard.UserID, thursday)
	s.onLeave(t, w.away.UserID, monday, thursday)
	return w
}

func TestAttendance(t *testing.T) {
	s := newServer(t)
	w := s.week(t)
	span := schedule.Range{From: monday, To: thursday}

	report, err := s.svc.Attendance(t.Context(), w.lead, Query{Span: span})
	if err != nil {
		t.Fatalf("Attendance: %v", err)
	}

	type day struct {
		userID int64
		date   time.Time
	}
	want := map[day]attendance.Status{
		{w.developer.UserID, monday}:    attendance.StatusPresent,
		{w.developer.UserID, tuesday}:   attendance.StatusHoliday,
		{w.developer.UserID, wednesday}: attendance.StatusLate,
		{w.developer.UserID, thursday}:  attendance.StatusAbsent, // a workday with no check-in
		{w.guard.UserID, monday}:        attendance.StatusAbsent,
		{w.guard.UserID, tuesday}:       attendance.StatusAbsent, // the guard's hours work through holidays
		{w.guard.UserID, wednesday}:     attendance.StatusAbsent,
		{w.guard.UserID, thursday}:      attendance.StatusOff, // rostered off, not absent
		{w.away.UserID, monday}:         attendance.StatusOnLeave,
		{w.away.UserID, tuesday}:        attendance.StatusOnLeave,
		{w.away.UserID, wednesday}:      attendance.StatusOnLeave,
		{w.away.UserID, thursday}:       attendance.StatusOnLeave,
	}
	if len(report.Days) != len(want) {
		t.Errorf("got %d days, want the subtree's %d and nobody else's: %+v", len(report.Days), len(want), report.Days)
	}
	for _, got := range report.Days {
		if status := want[day{got.UserID, got.Date}]; got.Status != status {
			t.Errorf("user %d on %s: status = %q, want %q", got.UserID, got.Date, got.Status, status)
		}
	}

	wantTotals := []Total{
		{UserID: w.developer.UserID, FullName: "Ani Developer", Present: 1, Late: 1, Absent: 1,
			WorkedMinutes: 540 + 510},
		{UserID: w.guard.UserID, FullName: "Budi Guard", Absent: 3},
		{UserID: w.away.UserID, FullName: "Citra Away", OnLeave: 4},
	}
	if len(report.People) != len(wantTotals) {
		t.Fatalf("People = %+v, want %+v", report.People, wantTotals)
	}
	for i, total := range wantTotals {
		if report.People[i] != total {
			t.Errorf("People[%d] = %+v, want %+v", i, report.People[i], total)
		}
	}

	absent := attendance.StatusAbsent
	onlyAbsent, err := s.svc.Attendance(t.Context(), w.lead, Query{Span: span, Status: &absent})
	if err != nil || len(onlyAbsent.Days) != 4 || len(onlyAbsent.People) != 2 || onlyAbsent.People[0].Present != 0 {
		t.Errorf("only absent days = %+v, %v; want the developer's one and the guard's three", onlyAbsent, err)
	}

	unknown := attendance.Status("asleep")
	if _, err := s.svc.Attendance(t.Context(), w.lead, Query{Span: span, Status: &unknown}); !apperr.IsValidation(err) {
		t.Errorf("an unknown status: err = %v, want a validation error", err)
	}
	if _, err := s.svc.Attendance(t.Context(), w.developer, Query{Span: span}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("an employee: err = %v, want ErrForbidden", err)
	}
}

func TestFormatsPassErrorsOn(t *testing.T) {
	s := newServer(t)
	lead := s.person(t, "Lead", middleware.RoleSupervisor, nil, nil)
	query := Query{Span: schedule.Range{From: monday, To: monday}}

	if _, err := s.svc.CSV(t.Context(), lead, query); err != nil {
		t.Errorf("CSV: %v", err)
	}
	failing := serviceOn(failingDB(t, "users"))
	if _, err := failing.CSV(t.Context(), lead, query); !errors.Is(err, errInjected) {
		t.Errorf("CSV with users failing: err = %v, want the injected failure", err)
	}
	if _, err := failing.PDF(t.Context(), lead, query); !errors.Is(err, errInjected) {
		t.Errorf("PDF with users failing: err = %v, want the injected failure", err)
	}
}

func TestWorked(t *testing.T) {
	in := time.Date(2025, 6, 2, 1, 0, 0, 0, time.UTC)
	checkedOut := in.Add(8*time.Hour + 59*time.Second)
	tests := map[string]struct {
		day  attendance.Presence
		want int
	}{
		"no attendance":   {attendance.Presence{}, 0},
		"not checked out": {attendance.Presence{Attendance: &attendance.Attendance{CheckInAt: in}}, 0},
		"whole minutes": {
			attendance.Presence{Attendance: &attendance.Attendance{CheckInAt: in, CheckOutAt: &checkedOut}}, 480,
		},
	}
	for name, test := range tests {
		if got := worked(test.day); got != test.want {
			t.Errorf("%s: worked = %d, want %d", name, got, test.want)
		}
	}
}
