package attendance

import (
	"errors"
	"testing"
	"time"

	"attendance-system/internal/api/server/modules/schedule"
	"attendance-system/internal/middleware"
	"attendance-system/internal/pkg/apperr"
)

func TestWhosIn(t *testing.T) {
	s := newServer(t)
	day := s.hours(t, 8*60, 17*60, 15)
	night := s.hours(t, 22*60, 6*60, 0)
	observing := s.hours(t, 8*60, 17*60, 0)
	s.db.Exec("UPDATE work_schedules SET observes_holidays = true WHERE id = ?", observing.ID)
	today := date(2030, time.March, 6)
	s.holiday(t, today, "Hari Raya Nyepi")

	lead := s.person(t, middleware.RoleSupervisor, nil, nil)
	onTime := s.person(t, middleware.RoleEmployee, &lead.UserID, &day.ID)
	late := s.person(t, middleware.RoleEmployee, &lead.UserID, &day.ID)
	missing := s.person(t, middleware.RoleEmployee, &lead.UserID, &day.ID)
	guard := s.person(t, middleware.RoleEmployee, &lead.UserID, &night.ID)
	onHoliday := s.person(t, middleware.RoleEmployee, &lead.UserID, &observing.ID)
	rosteredOff := s.person(t, middleware.RoleEmployee, &lead.UserID, &day.ID)
	unscheduled := s.person(t, middleware.RoleEmployee, &lead.UserID, nil)
	vacationing := s.person(t, middleware.RoleEmployee, &lead.UserID, &observing.ID)
	awaiting := s.person(t, middleware.RoleEmployee, &lead.UserID, &day.ID)
	outsider := s.person(t, middleware.RoleEmployee, nil, &day.ID)

	// Neither the inactive nor someone who joins later is expected in.
	inactive := s.person(t, middleware.RoleEmployee, &lead.UserID, &day.ID)
	joinsLater := s.person(t, middleware.RoleEmployee, &lead.UserID, &day.ID)
	s.db.Exec("UPDATE users SET is_active = false WHERE id = ?", inactive.UserID)
	s.db.Exec("UPDATE users SET join_date = ? WHERE id = ?", today.AddDate(0, 0, 1), joinsLater.UserID)

	s.checkedIn(t, onTime.UserID, today, local(2030, time.March, 6, 8, 0), &day.ID)
	lateRow := s.checkedIn(t, late.UserID, today, local(2030, time.March, 6, 9, 0), &day.ID)
	s.db.Exec("UPDATE attendances SET late_minutes = 45 WHERE id = ?", lateRow.ID)
	s.rostered(t, rosteredOff.UserID, today, nil)
	s.onLeave(t, vacationing.UserID, today.AddDate(0, 0, -1), today, "approved")
	s.onLeave(t, awaiting.UserID, today, today, "pending")

	clockAt(t, local(2030, time.March, 6, 10, 0))
	presences, err := s.svc.WhosIn(t.Context(), lead, today)
	if err != nil {
		t.Fatalf("WhosIn: %v", err)
	}

	want := map[int64]Status{
		onTime.UserID:      StatusPresent,
		late.UserID:        StatusLate,
		missing.UserID:     StatusAbsent,
		guard.UserID:       StatusNotYet, // the night shift starts at 22:00
		onHoliday.UserID:   StatusHoliday,
		rosteredOff.UserID: StatusHoliday, // rule B: a holiday outranks a day off
		unscheduled.UserID: StatusHoliday,
		vacationing.UserID: StatusOnLeave, // rule B: leave outranks the holiday
		awaiting.UserID:    StatusAbsent,  // pending leave does not excuse the day
	}
	got := map[int64]Presence{}
	for _, presence := range presences {
		got[presence.UserID] = presence
	}
	if len(got) != len(want) {
		t.Errorf("got %d people, want exactly the %d active in the subtree: %+v", len(got), len(want), presences)
	}
	for userID, status := range want {
		if got[userID].Status != status {
			t.Errorf("user %d: status = %q, want %q", userID, got[userID].Status, status)
		}
	}
	if got[onTime.UserID].Attendance == nil || got[missing.UserID].Attendance != nil {
		t.Error("the attendance must come with present people only")
	}
	if _, seen := got[outsider.UserID]; seen {
		t.Error("someone outside the supervisor's subtree is listed")
	}

	// hr_admin sees everyone, the outsider included.
	admin := s.person(t, middleware.RoleHRAdmin, nil, nil)
	everyone, err := s.svc.WhosIn(t.Context(), admin, today)
	if err != nil {
		t.Fatalf("WhosIn as hr_admin: %v", err)
	}
	if !listed(everyone, outsider.UserID) {
		t.Error("hr_admin must see people outside any one subtree")
	}

	if _, err := s.svc.WhosIn(t.Context(), onTime, today); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("an employee: err = %v, want ErrForbidden", err)
	}
}

func TestWhosInOnAnOrdinaryDay(t *testing.T) {
	s := newServer(t)
	lead := s.person(t, middleware.RoleSupervisor, nil, nil)
	unscheduled := s.person(t, middleware.RoleEmployee, &lead.UserID, nil)

	clockAt(t, local(2030, time.March, 7, 10, 0))
	presences, err := s.svc.WhosIn(t.Context(), lead, date(2030, time.March, 7))
	if err != nil || len(presences) != 1 || presences[0].Status != StatusOff ||
		presences[0].UserID != unscheduled.UserID {
		t.Errorf("WhosIn = %+v, %v; want the one unscheduled report off", presences, err)
	}
}

func TestWhosInFaults(t *testing.T) {
	s := newServer(t)
	hours := s.hours(t, 8*60, 17*60, 0)
	lead := s.person(t, middleware.RoleSupervisor, nil, nil)
	s.person(t, middleware.RoleEmployee, &lead.UserID, &hours.ID)

	tables := []string{"users", "attendances", "leave_requests", "shift_assignments", "holidays", "work_schedules"}
	for _, table := range tables {
		if _, err := s.failing(t, table).WhosIn(t.Context(), lead, date(2030, 3, 7)); !errors.Is(err, errInjected) {
			t.Errorf("%s failing: err = %v, want the injected failure", table, err)
		}
	}
}

func TestStanding(t *testing.T) {
	hours := &schedule.WorkSchedule{StartTime: 8 * 60, EndTime: 17 * 60, GraceMinutes: 15}
	working := schedule.Day{Date: date(2030, time.March, 4), Working: true, Schedule: hours}

	tests := map[string]struct {
		day  schedule.Day
		at   time.Time
		want Status
	}{
		"before the grace ends": {working, local(2030, 3, 4, 8, 14), StatusNotYet},
		"once the grace ends":   {working, local(2030, 3, 4, 8, 15), StatusAbsent},
		"a holiday":             {schedule.Day{Holiday: "Nyepi"}, local(2030, 3, 4, 10, 0), StatusHoliday},
		"not a workday":         {schedule.Day{}, local(2030, 3, 4, 10, 0), StatusOff},
	}
	for name, test := range tests {
		if got := standing(test.day, test.at, jakarta); got != test.want {
			t.Errorf("%s: standing = %q, want %q", name, got, test.want)
		}
	}
}

func listed(presences []Presence, userID int64) bool {
	for _, presence := range presences {
		if presence.UserID == userID {
			return true
		}
	}
	return false
}
