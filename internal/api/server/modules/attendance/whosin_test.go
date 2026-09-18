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

func TestDaily(t *testing.T) {
	s := newServer(t)
	office := s.hours(t, 8*60, 17*60, 0)
	s.db.Exec("UPDATE work_schedules SET observes_holidays = true, workdays = '{1,2,3,4,5}' WHERE id = ?", office.ID)
	night := s.hours(t, 22*60, 6*60, 0)
	monday, tuesday, wednesday, thursday := date(2030, 3, 4), date(2030, 3, 5), date(2030, 3, 6), date(2030, 3, 7)
	s.holiday(t, tuesday, "Hari Raya Nyepi")

	lead := s.person(t, middleware.RoleSupervisor, nil, nil)
	developer := s.person(t, middleware.RoleEmployee, &lead.UserID, &office.ID)
	guard := s.person(t, middleware.RoleEmployee, &lead.UserID, &night.ID)
	joiner := s.person(t, middleware.RoleEmployee, &lead.UserID, &office.ID)
	inactive := s.person(t, middleware.RoleEmployee, &lead.UserID, &office.ID)
	leaver := s.person(t, middleware.RoleEmployee, &lead.UserID, &office.ID)
	outsider := s.person(t, middleware.RoleEmployee, nil, &office.ID)
	s.db.Exec("UPDATE users SET join_date = ? WHERE id = ?", wednesday, joiner.UserID)
	s.db.Exec("UPDATE users SET is_active = false WHERE id = ?", inactive.UserID)
	// Removed on Tuesday morning in Jakarta, still Monday in UTC: Tuesday is their last day.
	s.db.Exec("UPDATE users SET deleted_at = ? WHERE id = ?", local(2030, 3, 5, 6, 0), leaver.UserID)

	s.checkedIn(t, developer.UserID, monday, local(2030, 3, 4, 8, 0), &office.ID)
	s.onLeave(t, developer.UserID, wednesday, wednesday, "approved")
	s.rostered(t, guard.UserID, thursday, nil)

	clockAt(t, local(2030, time.March, 10, 12, 0))
	span := schedule.Range{From: monday, To: thursday}
	days, err := s.svc.Daily(t.Context(), lead, span, Filter{})
	if err != nil {
		t.Fatalf("Daily: %v", err)
	}

	want := map[dayKey]Status{
		keyOf(developer.UserID, monday):    StatusPresent,
		keyOf(developer.UserID, tuesday):   StatusHoliday,
		keyOf(developer.UserID, wednesday): StatusOnLeave,
		keyOf(developer.UserID, thursday):  StatusAbsent,
		keyOf(guard.UserID, monday):        StatusAbsent,
		keyOf(guard.UserID, tuesday):       StatusAbsent, // the guard's schedule works through holidays
		keyOf(guard.UserID, wednesday):     StatusAbsent,
		keyOf(guard.UserID, thursday):      StatusOff, // rostered off, not absent
		keyOf(joiner.UserID, wednesday):    StatusAbsent,
		keyOf(joiner.UserID, thursday):     StatusAbsent,
		keyOf(inactive.UserID, monday):     StatusAbsent,
		keyOf(inactive.UserID, tuesday):    StatusHoliday,
		keyOf(inactive.UserID, wednesday):  StatusAbsent,
		keyOf(inactive.UserID, thursday):   StatusAbsent,
	}
	got := statuses(days)
	if len(got) != len(want) || len(days) != len(want) {
		t.Errorf("got %d days, want %d: the subtree's, from each join date, without the removed: %+v",
			len(days), len(want), days)
	}
	for key, status := range want {
		if got[key] != status {
			t.Errorf("user %d on %s: status = %q, want %q", key.userID, time.Unix(key.date, 0).UTC(), got[key], status)
		}
	}
	if days[0].Attendance == nil || days[0].UserID != developer.UserID || !days[0].Date.Equal(monday) {
		t.Errorf("first day = %+v, want the developer's Monday with its attendance", days[0])
	}

	// hr_admin reaches the removed and the outsider, and the filters narrow to one person or department.
	admin := s.person(t, middleware.RoleHRAdmin, nil, nil)
	removed, err := s.svc.Daily(t.Context(), admin, span, Filter{UserID: &leaver.UserID})
	if err != nil || len(removed) != 2 || !removed[1].Date.Equal(tuesday) {
		t.Errorf("Daily for the removed = %+v, %v; want Monday and Tuesday", removed, err)
	}
	var department int64
	s.db.Raw("INSERT INTO departments (name) VALUES (?) RETURNING id", unique("department")).Scan(&department)
	t.Cleanup(func() {
		s.db.Exec("UPDATE users SET department_id = NULL WHERE department_id = ?", department)
		s.db.Exec("DELETE FROM departments WHERE id = ?", department)
	})
	s.db.Exec("UPDATE users SET department_id = ? WHERE id = ?", department, outsider.UserID)
	inDepartment, err := s.svc.Daily(t.Context(), admin, span, Filter{DepartmentID: &department})
	if err != nil || len(inDepartment) != 4 || inDepartment[0].UserID != outsider.UserID {
		t.Errorf("Daily for one department = %+v, %v; want the outsider's four days", inDepartment, err)
	}

	if _, err := s.svc.Daily(t.Context(), developer, span, Filter{}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("an employee: err = %v, want ErrForbidden", err)
	}
	backwards := schedule.Range{From: thursday, To: monday}
	if _, err := s.svc.Daily(t.Context(), lead, backwards, Filter{}); !apperr.IsValidation(err) {
		t.Errorf("a backwards range: err = %v, want a validation error", err)
	}
	if _, err := s.failing(t, "users").Daily(t.Context(), lead, span, Filter{}); !errors.Is(err, errInjected) {
		t.Errorf("users failing: err = %v, want the injected failure", err)
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

func statuses(presences []Presence) map[dayKey]Status {
	byDay := make(map[dayKey]Status, len(presences))
	for _, presence := range presences {
		byDay[keyOf(presence.UserID, presence.Date)] = presence.Status
	}
	return byDay
}

func listed(presences []Presence, userID int64) bool {
	for _, presence := range presences {
		if presence.UserID == userID {
			return true
		}
	}
	return false
}
