package schedule

import (
	"context"
	"errors"
	"testing"
	"time"

	"attendance-system/internal/middleware"
	"attendance-system/internal/pkg/apperr"
)

func TestServiceDays(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()

	schedule := s.weekdaySchedule(t)
	schedule.ObservesHolidays = true
	if err := s.db.Save(&schedule).Error; err != nil {
		t.Fatalf("save schedule: %v", err)
	}
	guard := s.addSchedule(t, WorkSchedule{
		StartTime: 7 * 60, EndTime: 19 * 60, Workdays: pqInt16Array{1, 2, 3, 4, 5, 6, 7},
	})
	person := s.person(t, "employee", &schedule.ID)

	// Monday the 14th is a holiday the schedule observes; the 15th is rostered to the guard shift,
	// the 16th is a roster entry with no schedule, and the 19th is a Saturday.
	s.addHoliday(t, day(2026, time.September, 14), "Maulid Nabi")
	s.addAssignment(t, person.ID, day(2026, time.September, 15), &guard.ID)
	s.addAssignment(t, person.ID, day(2026, time.September, 16), nil)

	days, err := s.svc.Days(ctx, person.ID, Range{
		From: day(2026, time.September, 13), To: day(2026, time.September, 19),
	})
	if err != nil {
		t.Fatalf("Days: %v", err)
	}
	if len(days) != 7 {
		t.Fatalf("got %d days, want 7", len(days))
	}

	want := []struct {
		reason  Reason
		working bool
	}{
		{reason: ReasonNotAWorkday},            // Sunday
		{reason: ReasonHoliday},                // Monday, an observed holiday
		{reason: ReasonShift, working: true},   // Tuesday, rostered onto the guard shift
		{reason: ReasonAssignedOff},            // Wednesday, rostered off
		{reason: ReasonDefault, working: true}, // Thursday
		{reason: ReasonDefault, working: true}, // Friday
		{reason: ReasonNotAWorkday},            // Saturday
	}
	for index, expected := range want {
		got := days[index]
		if got.Reason != expected.reason || got.Working != expected.working {
			t.Errorf("%s: reason = %q working = %v, want %q and %v",
				got.Date.Format(time.DateOnly), got.Reason, got.Working, expected.reason, expected.working)
		}
	}
	if days[2].Schedule == nil || days[2].Schedule.ID != guard.ID {
		t.Errorf("Tuesday's schedule = %v, want the guard shift", days[2].Schedule)
	}
	if days[1].Holiday != "Maulid Nabi" {
		t.Errorf("Monday's holiday = %q", days[1].Holiday)
	}
}

// Someone with no schedule at all is simply never working, rather than an error.
func TestServiceDaysWithoutSchedule(t *testing.T) {
	s := newServer(t)

	person := s.person(t, "employee", nil)
	days, err := s.svc.Days(context.Background(), person.ID, Range{
		From: day(2026, time.September, 14), To: day(2026, time.September, 14),
	})
	if err != nil {
		t.Fatalf("Days: %v", err)
	}
	if len(days) != 1 || days[0].Working || days[0].Reason != ReasonNotAWorkday {
		t.Errorf("days = %+v", days)
	}
}

func TestServiceDaysRejected(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()

	if _, err := s.svc.Days(ctx, 1, Range{}); !apperr.IsValidation(err) {
		t.Errorf("Days without a span error = %v, want a validation error", err)
	}
	_, err := s.svc.Days(ctx, 0, Range{From: futureDate(0), To: futureDate(1)})
	if !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("Days for nobody error = %v, want ErrNotFound", err)
	}
}

func TestServiceDaysFaults(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()

	person := s.person(t, "employee", nil)
	span := Range{From: day(2026, time.September, 14), To: day(2026, time.September, 14)}

	for _, table := range []string{"users", "shift_assignments", "holidays", "work_schedules"} {
		// work_schedules is only read when something points at one, so give that case a default.
		userID := person.ID
		if table == "work_schedules" {
			schedule := s.weekdaySchedule(t)
			userID = s.person(t, "employee", &schedule.ID).ID
		}
		if _, err := s.failing(t, table).Days(ctx, userID, span); !errors.Is(err, errInjected) {
			t.Errorf("Days with %s failing: error = %v, want the injected failure", table, err)
		}
	}
}

func TestServiceSchedules(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()
	admin := claimsOf(s.person(t, "hr_admin", nil), middleware.RoleHRAdmin)

	created, err := s.svc.CreateSchedule(ctx, admin, Hours{
		Name: unique("schedule"), StartTime: "22:00", EndTime: "06:00",
		GraceMinutes: 15, BreakMinutes: 60, Workdays: []int{5, 1, 1}, ObservesHolidays: true,
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	s.trackSchedule(created)
	if created.StartTime != 22*60 || !created.CrossesMidnight() {
		t.Errorf("created = %+v, want a night shift", created)
	}
	if len(created.Workdays) != 2 {
		t.Errorf("workdays = %v, want them deduped", created.Workdays)
	}

	replaced, err := s.svc.ReplaceSchedule(ctx, admin, created.ID, Hours{
		Name: unique("schedule"), StartTime: "08:00", EndTime: "17:00", Workdays: []int{1, 2, 3, 4, 5},
	})
	if err != nil {
		t.Fatalf("ReplaceSchedule: %v", err)
	}
	if replaced.CrossesMidnight() {
		t.Error("08:00-17:00 does not cross midnight")
	}

	listed, err := s.svc.ListSchedules(ctx, claimsOf(s.person(t, "supervisor", nil), middleware.RoleSupervisor))
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	if !hasSchedule(listed, created.ID) {
		t.Error("ListSchedules did not return the schedule")
	}

	if err := s.svc.DeleteSchedule(ctx, admin, created.ID); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}
}

func TestServiceSchedulesForbidden(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()

	employee := claimsOf(s.person(t, "employee", nil), middleware.RoleEmployee)
	supervisor := claimsOf(s.person(t, "supervisor", nil), middleware.RoleSupervisor)
	hours := Hours{Name: "x", StartTime: "08:00", EndTime: "17:00", Workdays: []int{1}}

	if _, err := s.svc.ListSchedules(ctx, employee); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("ListSchedules as an employee error = %v, want ErrForbidden", err)
	}
	if _, err := s.svc.CreateSchedule(ctx, supervisor, hours); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("CreateSchedule as a supervisor error = %v, want ErrForbidden", err)
	}
	if _, err := s.svc.ReplaceSchedule(ctx, supervisor, 1, hours); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("ReplaceSchedule as a supervisor error = %v, want ErrForbidden", err)
	}
	if err := s.svc.DeleteSchedule(ctx, supervisor, 1); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("DeleteSchedule as a supervisor error = %v, want ErrForbidden", err)
	}
}

func TestServiceScheduleRejected(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()
	admin := claimsOf(s.person(t, "hr_admin", nil), middleware.RoleHRAdmin)

	valid := Hours{Name: unique("schedule"), StartTime: "08:00", EndTime: "17:00", Workdays: []int{1}}
	tests := []struct {
		name  string
		hours Hours
	}{
		{name: "no name", hours: with(valid, func(h *Hours) { h.Name = " " })},
		{name: "no workdays", hours: with(valid, func(h *Hours) { h.Workdays = nil })},
		{name: "a malformed start", hours: with(valid, func(h *Hours) { h.StartTime = "8am" })},
		{name: "a malformed end", hours: with(valid, func(h *Hours) { h.EndTime = "25:00" })},
		{name: "equal times", hours: with(valid, func(h *Hours) { h.EndTime = h.StartTime })},
		{
			name:  "a break as long as the shift",
			hours: with(valid, func(h *Hours) { h.EndTime, h.BreakMinutes = "12:00", 240 }),
		},
	}

	for _, test := range tests {
		if _, err := s.svc.CreateSchedule(ctx, admin, test.hours); !apperr.IsValidation(err) {
			t.Errorf("CreateSchedule with %s: error = %v, want a validation error", test.name, err)
		}
	}
}

// A schedule somebody still keeps or is rostered onto cannot be removed: their days would stop
// resolving. Only the repository can tell, so it is a conflict rather than a validation error.
func TestServiceDeleteScheduleInUse(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()
	admin := claimsOf(s.person(t, "hr_admin", nil), middleware.RoleHRAdmin)

	schedule := s.weekdaySchedule(t)
	s.person(t, "employee", &schedule.ID)

	if err := s.svc.DeleteSchedule(ctx, admin, schedule.ID); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("DeleteSchedule error = %v, want ErrConflict", err)
	}
	if err := s.svc.DeleteSchedule(ctx, admin, 0); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("DeleteSchedule of nothing error = %v, want ErrNotFound", err)
	}

	// Only roster still to be worked blocks it; a schedule used last month can be retired.
	retiring := s.weekdaySchedule(t)
	s.addAssignment(t, s.person(t, "employee", nil).ID, pastDate(30), &retiring.ID)
	if err := s.svc.DeleteSchedule(ctx, admin, retiring.ID); err != nil {
		t.Errorf("DeleteSchedule with only past roster: %v", err)
	}
	if err := s.failing(t, "users").DeleteSchedule(ctx, admin, schedule.ID); !errors.Is(err, errInjected) {
		t.Errorf("DeleteSchedule with the lookup failing: error = %v, want the injected failure", err)
	}
}

func TestServiceHolidays(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()
	admin := claimsOf(s.person(t, "hr_admin", nil), middleware.RoleHRAdmin)

	created, err := s.svc.CreateHoliday(ctx, admin, futureDate(30), "  Hari Raya  ")
	if err != nil {
		t.Fatalf("CreateHoliday: %v", err)
	}
	s.trackHoliday(created)
	if created.Name != "Hari Raya" {
		t.Errorf("name = %q, want it trimmed", created.Name)
	}

	replaced, err := s.svc.ReplaceHoliday(ctx, admin, created.ID, futureDate(31), "Hari Raya Kedua")
	if err != nil {
		t.Fatalf("ReplaceHoliday: %v", err)
	}
	if !replaced.Date.Equal(futureDate(31)) {
		t.Errorf("date = %s, want it moved", replaced.Date)
	}

	listed, err := s.svc.ListHolidays(ctx, claimsOf(s.person(t, "supervisor", nil), middleware.RoleSupervisor),
		Range{From: futureDate(0), To: futureDate(60)})
	if err != nil {
		t.Fatalf("ListHolidays: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Errorf("ListHolidays = %+v", listed)
	}

	// Editing the future changes a plan, so nothing is audited.
	if logs := s.auditLogs(t, entityHoliday, created.ID); len(logs) != 0 {
		t.Errorf("a future holiday wrote %d audit rows", len(logs))
	}
	if err := s.svc.DeleteHoliday(ctx, admin, created.ID); err != nil {
		t.Fatalf("DeleteHoliday: %v", err)
	}
	if logs := s.auditLogs(t, entityHoliday, created.ID); len(logs) != 0 {
		t.Errorf("deleting a future holiday wrote %d audit rows", len(logs))
	}
}

// Editing or removing a holiday that has passed changes whether people were off or absent, which the
// reports have already answered; that is what audit_logs is for.
func TestServiceHolidaysAuditThePast(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()
	admin := claimsOf(s.person(t, "hr_admin", nil), middleware.RoleHRAdmin)

	past := s.addHoliday(t, pastDate(30), "Last Month")
	if _, err := s.svc.ReplaceHoliday(ctx, admin, past.ID, pastDate(29), "Renamed"); err != nil {
		t.Fatalf("ReplaceHoliday: %v", err)
	}

	logs := s.auditLogs(t, entityHoliday, past.ID)
	if len(logs) != 1 {
		t.Fatalf("editing a past holiday wrote %d audit rows, want 1", len(logs))
	}
	if logs[0].Action != actionHolidayChanged || logs[0].ActorID != admin.UserID {
		t.Errorf("audit row = %+v", logs[0])
	}
	if !sameJSON(t, logs[0].Before, holidayJSON(pastDate(30), "Last Month")) {
		t.Errorf("before = %s", logs[0].Before)
	}
	if !sameJSON(t, logs[0].After, holidayJSON(pastDate(29), "Renamed")) {
		t.Errorf("after = %s", logs[0].After)
	}

	if err := s.svc.DeleteHoliday(ctx, admin, past.ID); err != nil {
		t.Fatalf("DeleteHoliday: %v", err)
	}
	logs = s.auditLogs(t, entityHoliday, past.ID)
	if len(logs) != 2 || logs[1].Action != actionHolidayDeleted {
		t.Fatalf("after deleting, audit rows = %+v", logs)
	}
	if !sameJSON(t, logs[1].After, nullJSON) {
		t.Errorf("after = %s, want null", logs[1].After)
	}
}

// Declaring a holiday on a date that has passed changes the past exactly as editing one does.
func TestServiceHolidayCreatedInThePastIsAudited(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()
	admin := claimsOf(s.person(t, "hr_admin", nil), middleware.RoleHRAdmin)

	created, err := s.svc.CreateHoliday(ctx, admin, pastDate(20), "Backdated")
	if err != nil {
		t.Fatalf("CreateHoliday: %v", err)
	}
	s.trackHoliday(created)

	logs := s.auditLogs(t, entityHoliday, created.ID)
	if len(logs) != 1 || logs[0].Action != actionHolidayChanged {
		t.Fatalf("audit rows = %+v, want one change", logs)
	}
	if !sameJSON(t, logs[0].Before, nullJSON) {
		t.Errorf("before = %s, want null", logs[0].Before)
	}
	if !sameJSON(t, logs[0].After, holidayJSON(pastDate(20), "Backdated")) {
		t.Errorf("after = %s", logs[0].After)
	}
}

// Moving a future holiday onto a past date is just as much a change to the past.
func TestServiceHolidayMovedIntoThePast(t *testing.T) {
	s := newServer(t)
	admin := claimsOf(s.person(t, "hr_admin", nil), middleware.RoleHRAdmin)

	future := s.addHoliday(t, futureDate(10), "Planned")
	if _, err := s.svc.ReplaceHoliday(context.Background(), admin, future.ID, pastDate(10), "Planned"); err != nil {
		t.Fatalf("ReplaceHoliday: %v", err)
	}
	if logs := s.auditLogs(t, entityHoliday, future.ID); len(logs) != 1 {
		t.Fatalf("moving a holiday into the past wrote %d audit rows, want 1", len(logs))
	}
}

func TestServiceHolidaysRejected(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()
	admin := claimsOf(s.person(t, "hr_admin", nil), middleware.RoleHRAdmin)
	employee := claimsOf(s.person(t, "employee", nil), middleware.RoleEmployee)
	supervisor := claimsOf(s.person(t, "supervisor", nil), middleware.RoleSupervisor)

	if _, err := s.svc.ListHolidays(ctx, employee, Range{}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("ListHolidays as an employee error = %v, want ErrForbidden", err)
	}
	if _, err := s.svc.ListHolidays(ctx, supervisor, Range{}); !apperr.IsValidation(err) {
		t.Errorf("ListHolidays without a span error = %v, want a validation error", err)
	}
	if _, err := s.svc.CreateHoliday(ctx, supervisor, futureDate(1), "x"); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("CreateHoliday as a supervisor error = %v, want ErrForbidden", err)
	}
	if _, err := s.svc.CreateHoliday(ctx, admin, futureDate(1), " "); !apperr.IsValidation(err) {
		t.Errorf("CreateHoliday without a name error = %v, want a validation error", err)
	}
	if _, err := s.svc.CreateHoliday(ctx, admin, time.Time{}, "x"); !apperr.IsValidation(err) {
		t.Errorf("CreateHoliday without a date error = %v, want a validation error", err)
	}
	if _, err := s.svc.ReplaceHoliday(ctx, supervisor, 1, futureDate(1), "x"); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("ReplaceHoliday as a supervisor error = %v, want ErrForbidden", err)
	}
	if _, err := s.svc.ReplaceHoliday(ctx, admin, 1, time.Time{}, "x"); !apperr.IsValidation(err) {
		t.Errorf("ReplaceHoliday without a date error = %v, want a validation error", err)
	}
	if _, err := s.svc.ReplaceHoliday(ctx, admin, 0, futureDate(1), "x"); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("ReplaceHoliday of nothing error = %v, want ErrNotFound", err)
	}
	if err := s.svc.DeleteHoliday(ctx, supervisor, 1); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("DeleteHoliday as a supervisor error = %v, want ErrForbidden", err)
	}
	if err := s.svc.DeleteHoliday(ctx, admin, 0); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("DeleteHoliday of nothing error = %v, want ErrNotFound", err)
	}
}

func TestServiceLocations(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()
	admin := claimsOf(s.person(t, "hr_admin", nil), middleware.RoleHRAdmin)

	created, err := s.svc.CreateLocation(ctx, admin, Place{
		Name: unique("office"), Lat: -6.2088, Lng: 106.8456, RadiusM: 100,
	})
	if err != nil {
		t.Fatalf("CreateLocation: %v", err)
	}
	s.trackLocation(created)

	replaced, err := s.svc.ReplaceLocation(ctx, admin, created.ID, Place{
		Name: "Cabang", Lat: 1, Lng: 2, RadiusM: 250,
	})
	if err != nil {
		t.Fatalf("ReplaceLocation: %v", err)
	}
	if replaced.RadiusM != 250 {
		t.Errorf("replaced = %+v", replaced)
	}

	listed, err := s.svc.ListLocations(ctx, claimsOf(s.person(t, "supervisor", nil), middleware.RoleSupervisor))
	if err != nil {
		t.Fatalf("ListLocations: %v", err)
	}
	if !hasLocation(listed, created.ID) {
		t.Error("ListLocations did not return the office")
	}

	if err := s.svc.DeleteLocation(ctx, admin, created.ID); err != nil {
		t.Fatalf("DeleteLocation: %v", err)
	}
}

func TestServiceLocationsRejected(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()
	admin := claimsOf(s.person(t, "hr_admin", nil), middleware.RoleHRAdmin)
	employee := claimsOf(s.person(t, "employee", nil), middleware.RoleEmployee)
	supervisor := claimsOf(s.person(t, "supervisor", nil), middleware.RoleSupervisor)
	place := Place{Name: "Kantor", Lat: 0, Lng: 0, RadiusM: 100}

	if _, err := s.svc.ListLocations(ctx, employee); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("ListLocations as an employee error = %v, want ErrForbidden", err)
	}
	if _, err := s.svc.CreateLocation(ctx, supervisor, place); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("CreateLocation as a supervisor error = %v, want ErrForbidden", err)
	}
	if _, err := s.svc.CreateLocation(ctx, admin, Place{Name: " ", RadiusM: 100}); !apperr.IsValidation(err) {
		t.Errorf("CreateLocation without a name error = %v, want a validation error", err)
	}
	if _, err := s.svc.CreateLocation(ctx, admin, Place{Name: "x", RadiusM: 1}); !apperr.IsValidation(err) {
		t.Errorf("CreateLocation with too small a radius error = %v, want a validation error", err)
	}
	if _, err := s.svc.ReplaceLocation(ctx, supervisor, 1, place); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("ReplaceLocation as a supervisor error = %v, want ErrForbidden", err)
	}
	if _, err := s.svc.ReplaceLocation(ctx, admin, 1, Place{Name: "x", RadiusM: 1}); !apperr.IsValidation(err) {
		t.Errorf("ReplaceLocation with too small a radius error = %v, want a validation error", err)
	}
	if _, err := s.svc.ReplaceLocation(ctx, admin, 0, place); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("ReplaceLocation of nothing error = %v, want ErrNotFound", err)
	}
	if err := s.svc.DeleteLocation(ctx, supervisor, 1); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("DeleteLocation as a supervisor error = %v, want ErrForbidden", err)
	}
	if err := s.svc.DeleteLocation(ctx, admin, 0); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("DeleteLocation of nothing error = %v, want ErrNotFound", err)
	}
}

func TestServiceAssign(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()
	admin := claimsOf(s.person(t, "hr_admin", nil), middleware.RoleHRAdmin)

	schedule := s.weekdaySchedule(t)
	person := s.person(t, "employee", nil)
	note := "covering for Budi"

	written, err := s.svc.Assign(ctx, admin, []Assignment{
		{UserID: person.ID, WorkDate: futureDate(1), ScheduleID: &schedule.ID, Note: note},
		{UserID: person.ID, WorkDate: futureDate(2)},
	})
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if len(written) != 2 || written[0].Note != note || written[1].ScheduleID != nil {
		t.Errorf("written = %+v", written)
	}

	listed, err := s.svc.ListAssignments(ctx, admin, Range{From: futureDate(0), To: futureDate(3)}, &person.ID)
	if err != nil {
		t.Fatalf("ListAssignments: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed %d rows, want 2", len(listed))
	}

	// Future roster is a plan, so nothing is audited.
	if logs := s.auditLogs(t, entityShift, person.ID); len(logs) != 0 {
		t.Errorf("future roster wrote %d audit rows", len(logs))
	}

	if err := s.svc.DeleteAssignment(ctx, admin, listed[0].ID); err != nil {
		t.Fatalf("DeleteAssignment: %v", err)
	}
	if logs := s.auditLogs(t, entityShift, person.ID); len(logs) != 0 {
		t.Errorf("deleting future roster wrote %d audit rows", len(logs))
	}
}

// Roster on a date that has passed is audited, and the entry says what it replaced.
func TestServiceAssignAuditsThePast(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()
	admin := claimsOf(s.person(t, "hr_admin", nil), middleware.RoleHRAdmin)

	schedule := s.weekdaySchedule(t)
	person := s.person(t, "employee", nil)
	date := pastDate(5)
	s.addAssignment(t, person.ID, date, nil)

	if _, err := s.svc.Assign(ctx, admin, []Assignment{
		{UserID: person.ID, WorkDate: date, ScheduleID: &schedule.ID},
	}); err != nil {
		t.Fatalf("Assign: %v", err)
	}

	logs := s.auditLogs(t, entityShift, person.ID)
	if len(logs) != 1 || logs[0].Action != actionShiftChanged {
		t.Fatalf("audit rows = %+v", logs)
	}
	if !sameJSON(t, logs[0].Before, assignmentJSON(date, nil)) {
		t.Errorf("before = %s, want the day off it replaced", logs[0].Before)
	}
	if !sameJSON(t, logs[0].After, assignmentJSON(date, &schedule.ID)) {
		t.Errorf("after = %s", logs[0].After)
	}

	// Removing it is audited too, with the row as it stood.
	listed, err := s.svc.ListAssignments(ctx, admin, Range{From: date, To: date}, &person.ID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("ListAssignments = %+v, %v", listed, err)
	}
	if err := s.svc.DeleteAssignment(ctx, admin, listed[0].ID); err != nil {
		t.Fatalf("DeleteAssignment: %v", err)
	}
	logs = s.auditLogs(t, entityShift, person.ID)
	if len(logs) != 2 || logs[1].Action != actionShiftDeleted {
		t.Fatalf("after deleting, audit rows = %+v", logs)
	}
	if !sameJSON(t, logs[1].Before, assignmentJSON(date, &schedule.ID)) || !sameJSON(t, logs[1].After, nullJSON) {
		t.Errorf("audit row = %s -> %s", logs[1].Before, logs[1].After)
	}
}

// A batch spanning both sides of today audits only the half that changes the past.
func TestServiceAssignAuditsOnlyThePastHalf(t *testing.T) {
	s := newServer(t)

	admin := claimsOf(s.person(t, "hr_admin", nil), middleware.RoleHRAdmin)
	person := s.person(t, "employee", nil)

	_, err := s.svc.Assign(context.Background(), admin, []Assignment{
		{UserID: person.ID, WorkDate: pastDate(4)},
		{UserID: person.ID, WorkDate: futureDate(4)},
	})
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}

	logs := s.auditLogs(t, entityShift, person.ID)
	if len(logs) != 1 {
		t.Fatalf("audit rows = %d, want only the past-dated one", len(logs))
	}
	if !sameJSON(t, logs[0].After, assignmentJSON(pastDate(4), nil)) {
		t.Errorf("after = %s, want the past-dated row", logs[0].After)
	}
}

// A past-dated entry on a date nobody was rostered for has nothing to report as its before state.
func TestServiceAssignAuditsWithoutABefore(t *testing.T) {
	s := newServer(t)

	admin := claimsOf(s.person(t, "hr_admin", nil), middleware.RoleHRAdmin)
	person := s.person(t, "employee", nil)
	date := pastDate(6)

	if _, err := s.svc.Assign(context.Background(), admin, []Assignment{
		{UserID: person.ID, WorkDate: date},
	}); err != nil {
		t.Fatalf("Assign: %v", err)
	}

	logs := s.auditLogs(t, entityShift, person.ID)
	if len(logs) != 1 || !sameJSON(t, logs[0].Before, nullJSON) {
		t.Fatalf("audit rows = %+v", logs)
	}
}

func TestServiceAssignRejected(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()
	admin := claimsOf(s.person(t, "hr_admin", nil), middleware.RoleHRAdmin)
	supervisor := claimsOf(s.person(t, "supervisor", nil), middleware.RoleSupervisor)
	person := s.person(t, "employee", nil)

	one := []Assignment{{UserID: person.ID, WorkDate: futureDate(1)}}
	if _, err := s.svc.Assign(ctx, supervisor, one); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("Assign as a supervisor error = %v, want ErrForbidden", err)
	}
	if _, err := s.svc.Assign(ctx, admin, nil); !apperr.IsValidation(err) {
		t.Errorf("Assign with nothing error = %v, want a validation error", err)
	}
	stranger := []Assignment{{UserID: -1, WorkDate: futureDate(1)}}
	if _, err := s.svc.Assign(ctx, admin, stranger); !apperr.IsValidation(err) {
		t.Errorf("Assign for a stranger error = %v, want a validation error", err)
	}
	if _, err := s.svc.ListAssignments(ctx, supervisor, Range{}, nil); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("ListAssignments as a supervisor error = %v, want ErrForbidden", err)
	}
	if _, err := s.svc.ListAssignments(ctx, admin, Range{}, nil); !apperr.IsValidation(err) {
		t.Errorf("ListAssignments without a span error = %v, want a validation error", err)
	}
	if err := s.svc.DeleteAssignment(ctx, supervisor, 1); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("DeleteAssignment as a supervisor error = %v, want ErrForbidden", err)
	}
	if err := s.svc.DeleteAssignment(ctx, admin, 0); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("DeleteAssignment of nothing error = %v, want ErrNotFound", err)
	}
}

// Reading what the past-dated dates already held is a query of its own, so it can fail on its own.
func TestServiceAssignAuditLookupFails(t *testing.T) {
	s := newServer(t)

	admin := claimsOf(s.person(t, "hr_admin", nil), middleware.RoleHRAdmin)
	person := s.person(t, "employee", nil)
	// Out of order on purpose: the span the lookup asks for has to cover both ends either way.
	rows := []Assignment{
		{UserID: person.ID, WorkDate: pastDate(2)},
		{UserID: person.ID, WorkDate: pastDate(3)},
		{UserID: person.ID, WorkDate: pastDate(1)},
	}

	_, err := s.failing(t, "shift_assignments").Assign(context.Background(), admin, rows)
	if !errors.Is(err, errInjected) {
		t.Fatalf("error = %v, want the injected failure", err)
	}
}

func TestLookup(t *testing.T) {
	t.Parallel()

	id := int64(7)
	schedules := map[int64]WorkSchedule{7: {ID: 7}}

	if lookup(schedules, nil) != nil {
		t.Error("no id means no schedule")
	}
	if got := lookup(schedules, &id); got == nil || got.ID != 7 {
		t.Errorf("lookup = %v, want the schedule", got)
	}
	missing := int64(8)
	if lookup(schedules, &missing) != nil {
		t.Error("an id that was not loaded means no schedule")
	}
}

func TestIsPast(t *testing.T) {
	t.Parallel()

	today := day(2030, time.March, 4)
	if !isPast(day(2030, time.March, 3), today) {
		t.Error("yesterday is past")
	}
	if isPast(time.Date(2030, time.March, 4, 23, 0, 0, 0, time.UTC), today) {
		t.Error("today is not past, whatever its time of day: the day is still being worked")
	}
	if isPast(day(2030, time.March, 5), today) {
		t.Error("tomorrow is not past")
	}
}

// UTC+14 and UTC-11 are 25 hours apart, so their dates differ at every moment; the server's own zone must not
// decide either.
func TestTodayIsTheCompanyDate(t *testing.T) {
	t.Parallel()

	ahead, _ := time.LoadLocation("Pacific/Kiritimati")
	behind, _ := time.LoadLocation("Pacific/Pago_Pago")
	if early, late := NewService(nil, behind).today(), NewService(nil, ahead).today(); !late.After(early) {
		t.Errorf("today in UTC-11 = %v, in UTC+14 = %v; want the later zone a day on", early, late)
	}
	if got, want := NewService(nil, ahead).today(), dateOnly(time.Now().In(ahead)); !got.Equal(want) {
		t.Errorf("today = %v, want %v", got, want)
	}
}

// with copies hours and applies one change, so each case reads as the one field it is testing.
func with(hours Hours, change func(*Hours)) Hours {
	change(&hours)
	return hours
}
