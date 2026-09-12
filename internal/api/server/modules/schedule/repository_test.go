package schedule

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"attendance-system/internal/pkg/apperr"
)

func TestRepositorySchedules(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()

	created := WorkSchedule{
		Name: unique("schedule"), StartTime: 22 * 60, EndTime: 6 * 60,
		GraceMinutes: 15, BreakMinutes: 60, Workdays: pqInt16Array{1, 2, 3, 4, 5, 6, 7},
		ObservesHolidays: true,
	}
	if err := s.repo.CreateSchedule(ctx, &created); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	s.trackSchedule(created)

	read, err := s.repo.ScheduleByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("ScheduleByID: %v", err)
	}
	if read.StartTime != 22*60 || read.EndTime != 6*60 || !read.CrossesMidnight() {
		t.Errorf("read back %s-%s, crosses = %v", read.StartTime, read.EndTime, read.CrossesMidnight())
	}
	if len(read.Workdays) != 7 {
		t.Errorf("workdays = %v, want all seven", read.Workdays)
	}

	replaced, err := s.repo.ReplaceSchedule(ctx, WorkSchedule{
		ID: created.ID, Name: unique("schedule"), StartTime: 8 * 60, EndTime: 17 * 60,
		Workdays: pqInt16Array{1, 2, 3},
	})
	if err != nil {
		t.Fatalf("ReplaceSchedule: %v", err)
	}
	if replaced.ID != created.ID || replaced.StartTime != 8*60 || len(replaced.Workdays) != 3 {
		t.Errorf("replaced = %+v", replaced)
	}

	listed, err := s.repo.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	if !hasSchedule(listed, created.ID) {
		t.Error("ListSchedules did not return the schedule")
	}

	if err := s.repo.DeleteSchedule(ctx, created.ID); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}
	if _, err := s.repo.ScheduleByID(ctx, created.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("after delete, ScheduleByID error = %v, want ErrNotFound", err)
	}
}

func TestRepositoryScheduleMissing(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()

	if _, err := s.repo.ScheduleByID(ctx, 0); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("ScheduleByID error = %v, want ErrNotFound", err)
	}
	if _, err := s.repo.ReplaceSchedule(ctx, WorkSchedule{ID: 0, Name: "x"}); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("ReplaceSchedule error = %v, want ErrNotFound", err)
	}
	if err := s.repo.DeleteSchedule(ctx, 0); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("DeleteSchedule error = %v, want ErrNotFound", err)
	}
}

// A check constraint is the database refusing what validation should have caught; it still has to
// arrive as a rejected request rather than a 500.
func TestRepositoryScheduleCheckViolation(t *testing.T) {
	s := newServer(t)

	bad := WorkSchedule{Name: unique("schedule"), GraceMinutes: -1, Workdays: pqInt16Array{1}}
	err := s.repo.CreateSchedule(context.Background(), &bad)
	if !apperr.IsValidation(err) {
		t.Fatalf("CreateSchedule error = %v, want a validation error", err)
	}
}

func TestRepositoryScheduleInUse(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()

	schedule := s.weekdaySchedule(t)
	inUse, err := s.repo.ScheduleInUse(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("ScheduleInUse: %v", err)
	}
	if inUse {
		t.Error("a fresh schedule is not in use")
	}

	// Someone keeping it as their default.
	keeper := s.person(t, "employee", &schedule.ID)
	if inUse, err = s.repo.ScheduleInUse(ctx, schedule.ID); err != nil || !inUse {
		t.Fatalf("ScheduleInUse with a keeper = %v, %v", inUse, err)
	}

	// A removed employee no longer counts: nobody is left whose days would break.
	s.db.Exec("UPDATE users SET deleted_at = now() WHERE id = ?", keeper.ID)
	if inUse, err = s.repo.ScheduleInUse(ctx, schedule.ID); err != nil || inUse {
		t.Fatalf("ScheduleInUse with a deleted keeper = %v, %v", inUse, err)
	}

	// Roster that has already been worked does not count: resolution loads schedules Unscoped, so the
	// history reads back whether the schedule is retired or not.
	past := s.person(t, "employee", nil)
	s.addAssignment(t, past.ID, pastDate(30), &schedule.ID)
	if inUse, err = s.repo.ScheduleInUse(ctx, schedule.ID); err != nil || inUse {
		t.Fatalf("ScheduleInUse with only past roster = %v, %v", inUse, err)
	}

	// Roster from today on does count: those days are still to be worked.
	rostered := s.person(t, "employee", nil)
	s.addAssignment(t, rostered.ID, futureDate(2), &schedule.ID)
	if inUse, err = s.repo.ScheduleInUse(ctx, schedule.ID); err != nil || !inUse {
		t.Fatalf("ScheduleInUse with a roster entry = %v, %v", inUse, err)
	}
}

func TestRepositoryScheduleInUseFails(t *testing.T) {
	ctx := context.Background()

	if _, err := NewRepository(failingDB(t, "users")).ScheduleInUse(ctx, 1); !errors.Is(err, errInjected) {
		t.Errorf("error = %v, want the injected failure", err)
	}
	_, err := NewRepository(failingDB(t, "shift_assignments")).ScheduleInUse(ctx, 1)
	if !errors.Is(err, errInjected) {
		t.Errorf("error = %v, want the injected failure", err)
	}
}

func TestRepositoryHolidays(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()

	date := day(2026, time.August, 17)
	holiday := Holiday{Date: date, Name: "Hari Kemerdekaan"}
	if err := s.repo.CreateHoliday(ctx, &holiday, nil); err != nil {
		t.Fatalf("CreateHoliday: %v", err)
	}
	s.trackHoliday(holiday)

	// One holiday per date: the unique index is what keeps resolution unambiguous.
	duplicate := Holiday{Date: date, Name: "Another"}
	if err := s.repo.CreateHoliday(ctx, &duplicate, nil); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("duplicate date error = %v, want ErrConflict", err)
	}

	listed, err := s.repo.ListHolidays(ctx, Range{From: date, To: date})
	if err != nil {
		t.Fatalf("ListHolidays: %v", err)
	}
	if len(listed) != 1 || listed[0].Name != "Hari Kemerdekaan" {
		t.Errorf("ListHolidays = %+v", listed)
	}

	// Outside the span it must not show up.
	listed, err = s.repo.ListHolidays(ctx, Range{From: date.AddDate(0, 0, 1), To: date.AddDate(0, 0, 2)})
	if err != nil || len(listed) != 0 {
		t.Fatalf("ListHolidays outside the span = %+v, %v", listed, err)
	}

	moved, err := s.repo.ReplaceHoliday(ctx, Holiday{ID: holiday.ID, Date: date.AddDate(0, 0, 1), Name: "Cuti"}, nil)
	if err != nil {
		t.Fatalf("ReplaceHoliday: %v", err)
	}
	if moved.Name != "Cuti" || !moved.Date.Equal(date.AddDate(0, 0, 1)) {
		t.Errorf("moved = %+v", moved)
	}

	if err := s.repo.DeleteHoliday(ctx, holiday.ID, nil); err != nil {
		t.Fatalf("DeleteHoliday: %v", err)
	}
	if _, err := s.repo.HolidayByID(ctx, holiday.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("after delete, HolidayByID error = %v, want ErrNotFound", err)
	}
}

func TestRepositoryHolidayMissing(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()

	if _, err := s.repo.HolidayByID(ctx, 0); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("HolidayByID error = %v, want ErrNotFound", err)
	}
	_, err := s.repo.ReplaceHoliday(ctx, Holiday{ID: 0, Date: day(2026, time.August, 17), Name: "x"}, nil)
	if !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("ReplaceHoliday error = %v, want ErrNotFound", err)
	}
	if err := s.repo.DeleteHoliday(ctx, 0, nil); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("DeleteHoliday error = %v, want ErrNotFound", err)
	}
}

// Moving a holiday onto a date that already has one is the same conflict as creating it there.
func TestRepositoryHolidayReplaceConflict(t *testing.T) {
	s := newServer(t)

	first := s.addHoliday(t, day(2026, time.August, 17), "Kemerdekaan")
	second := s.addHoliday(t, day(2026, time.December, 25), "Natal")

	_, err := s.repo.ReplaceHoliday(context.Background(),
		Holiday{ID: second.ID, Date: first.Date, Name: "Natal"}, nil)
	if !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("ReplaceHoliday error = %v, want ErrConflict", err)
	}
}

func TestRepositoryLocations(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()

	location := OfficeLocation{Name: unique("office"), Lat: -6.2, Lng: 106.8, RadiusM: 150}
	if err := s.repo.CreateLocation(ctx, &location); err != nil {
		t.Fatalf("CreateLocation: %v", err)
	}
	s.trackLocation(location)

	listed, err := s.repo.ListLocations(ctx)
	if err != nil {
		t.Fatalf("ListLocations: %v", err)
	}
	if !hasLocation(listed, location.ID) {
		t.Error("ListLocations did not return the office")
	}

	replaced, err := s.repo.ReplaceLocation(ctx, OfficeLocation{
		ID: location.ID, Name: "Cabang", Lat: 1, Lng: 2, RadiusM: 200,
	})
	if err != nil {
		t.Fatalf("ReplaceLocation: %v", err)
	}
	if replaced.Name != "Cabang" || replaced.RadiusM != 200 {
		t.Errorf("replaced = %+v", replaced)
	}

	if err := s.repo.DeleteLocation(ctx, location.ID); err != nil {
		t.Fatalf("DeleteLocation: %v", err)
	}
	if _, err := s.repo.LocationByID(ctx, location.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("after delete, LocationByID error = %v, want ErrNotFound", err)
	}
}

func TestRepositoryLocationMissing(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()

	if _, err := s.repo.LocationByID(ctx, 0); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("LocationByID error = %v, want ErrNotFound", err)
	}
	if _, err := s.repo.ReplaceLocation(ctx, OfficeLocation{ID: 0}); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("ReplaceLocation error = %v, want ErrNotFound", err)
	}
	if err := s.repo.DeleteLocation(ctx, 0); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("DeleteLocation error = %v, want ErrNotFound", err)
	}
	bad := OfficeLocation{Name: unique("office"), RadiusM: -1}
	if err := s.repo.CreateLocation(ctx, &bad); !apperr.IsValidation(err) {
		t.Errorf("CreateLocation with a negative radius error = %v, want a validation error", err)
	}
}

func TestRepositoryUpsertAssignments(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()

	schedule := s.weekdaySchedule(t)
	person := s.person(t, "employee", nil)
	date := day(2026, time.September, 14)

	rows := []ShiftAssignment{{UserID: person.ID, WorkDate: date, ScheduleID: &schedule.ID, Note: "first"}}
	if err := s.repo.UpsertAssignments(ctx, rows, nil); err != nil {
		t.Fatalf("UpsertAssignments: %v", err)
	}

	// The same person and date again replaces the row rather than adding one.
	again := []ShiftAssignment{{UserID: person.ID, WorkDate: date, Note: "day off"}}
	if err := s.repo.UpsertAssignments(ctx, again, nil); err != nil {
		t.Fatalf("UpsertAssignments again: %v", err)
	}

	listed, err := s.repo.ListAssignments(ctx, Range{From: date, To: date}, &person.ID)
	if err != nil {
		t.Fatalf("ListAssignments: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed %d rows, want the one upserted row", len(listed))
	}
	if listed[0].ScheduleID != nil || listed[0].Note != "day off" {
		t.Errorf("row = %+v, want the replaced values", listed[0])
	}

	// Listing without a user returns the same row, since the span still covers it.
	all, err := s.repo.ListAssignments(ctx, Range{From: date, To: date}, nil)
	if err != nil || len(all) == 0 {
		t.Fatalf("ListAssignments for everyone = %d rows, %v", len(all), err)
	}

	read, err := s.repo.AssignmentByID(ctx, listed[0].ID)
	if err != nil {
		t.Fatalf("AssignmentByID: %v", err)
	}
	if read.UserID != person.ID {
		t.Errorf("read = %+v", read)
	}

	if err := s.repo.DeleteAssignment(ctx, read.ID, nil); err != nil {
		t.Fatalf("DeleteAssignment: %v", err)
	}
	if _, err := s.repo.AssignmentByID(ctx, read.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("after delete, AssignmentByID error = %v, want ErrNotFound", err)
	}
	if err := s.repo.DeleteAssignment(ctx, 0, nil); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("DeleteAssignment of nothing error = %v, want ErrNotFound", err)
	}
}

// A roster entry for somebody who does not exist is the client's mistake, not a 500.
func TestRepositoryUpsertAssignmentsForeignKey(t *testing.T) {
	s := newServer(t)

	rows := []ShiftAssignment{{UserID: -1, WorkDate: day(2026, time.September, 14)}}
	err := s.repo.UpsertAssignments(context.Background(), rows, nil)
	if !apperr.IsValidation(err) {
		t.Fatalf("UpsertAssignments error = %v, want a validation error", err)
	}
}

func TestRepositoryUpsertAssignmentsWritesAudit(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()

	actor := s.person(t, "hr_admin", nil)
	person := s.person(t, "employee", nil)
	date := pastDate(3)

	// The row it replaces is read inside the transaction and handed back, so the entry cannot describe
	// a state something else changed in between.
	s.addAssignment(t, person.ID, date, nil)
	rows := []ShiftAssignment{{UserID: person.ID, WorkDate: date}}

	var handed []ShiftAssignment
	audits := func(before []ShiftAssignment) []AuditLog {
		handed = before
		return []AuditLog{*auditRow(actor.ID, actionShiftChanged, entityShift, person.ID,
			nullJSON, assignmentJSON(date, nil))}
	}
	if err := s.repo.UpsertAssignments(ctx, rows, audits); err != nil {
		t.Fatalf("UpsertAssignments: %v", err)
	}
	if len(handed) != 1 || handed[0].UserID != person.ID {
		t.Errorf("rows handed to the audit callback = %+v, want the one being replaced", handed)
	}

	logs := s.auditLogs(t, entityShift, person.ID)
	if len(logs) != 1 || logs[0].Action != actionShiftChanged {
		t.Fatalf("audit rows = %+v", logs)
	}
}

// No callback means no audit, and then the write must not pay for reading what it replaces.
func TestRepositoryUpsertAssignmentsWithoutAudit(t *testing.T) {
	s := newServer(t)

	person := s.person(t, "employee", nil)
	rows := []ShiftAssignment{{UserID: person.ID, WorkDate: futureDate(1)}}
	if err := s.repo.UpsertAssignments(context.Background(), rows, nil); err != nil {
		t.Fatalf("UpsertAssignments: %v", err)
	}
	if logs := s.auditLogs(t, entityShift, person.ID); len(logs) != 0 {
		t.Errorf("audit rows = %+v, want none", logs)
	}
}

// The lookup of what is being replaced happens in the transaction, so it can fail there.
func TestRepositoryUpsertAssignmentsLookupFails(t *testing.T) {
	s := newServer(t)

	person := s.person(t, "employee", nil)
	rows := []ShiftAssignment{{UserID: person.ID, WorkDate: pastDate(1)}}
	audits := func([]ShiftAssignment) []AuditLog { return nil }

	repo := NewRepository(failingAfterDB(t, "FOR UPDATE"))
	if err := repo.UpsertAssignments(context.Background(), rows, audits); !errors.Is(err, errInjected) {
		t.Fatalf("error = %v, want the injected failure", err)
	}
}

// The roster row and its audit row are one transaction: an audit log that can go missing is worse
// than none, because it looks complete.
func TestRepositoryUpsertAssignmentsAuditFails(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()

	person := s.person(t, "employee", nil)
	date := pastDate(3)
	rows := []ShiftAssignment{{UserID: person.ID, WorkDate: date}}
	audits := func([]ShiftAssignment) []AuditLog {
		return []AuditLog{*auditRow(person.ID, actionShiftChanged, entityShift, person.ID, nullJSON, nullJSON)}
	}

	repo := NewRepository(failingDB(t, "audit_logs"))
	if err := repo.UpsertAssignments(ctx, rows, audits); !errors.Is(err, errInjected) {
		t.Fatalf("error = %v, want the injected failure", err)
	}

	listed, err := s.repo.ListAssignments(ctx, Range{From: date, To: date}, &person.ID)
	if err != nil {
		t.Fatalf("ListAssignments: %v", err)
	}
	if len(listed) != 0 {
		t.Errorf("the roster row survived a failed audit write: %+v", listed)
	}
}

func TestRepositoryDefaultScheduleID(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()

	schedule := s.weekdaySchedule(t)
	with := s.person(t, "employee", &schedule.ID)
	without := s.person(t, "employee", nil)

	got, err := s.repo.DefaultScheduleID(ctx, with.ID)
	if err != nil {
		t.Fatalf("DefaultScheduleID: %v", err)
	}
	if got == nil || *got != schedule.ID {
		t.Errorf("DefaultScheduleID = %v, want %d", got, schedule.ID)
	}

	if got, err = s.repo.DefaultScheduleID(ctx, without.ID); err != nil || got != nil {
		t.Errorf("DefaultScheduleID without one = %v, %v", got, err)
	}
	if _, err = s.repo.DefaultScheduleID(ctx, 0); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("DefaultScheduleID of nobody error = %v, want ErrNotFound", err)
	}

	// A removed employee is nobody, even though the row is still there.
	s.db.Exec("UPDATE users SET deleted_at = now() WHERE id = ?", without.ID)
	if _, err = s.repo.DefaultScheduleID(ctx, without.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("DefaultScheduleID of a deleted employee error = %v, want ErrNotFound", err)
	}
}

// A retired schedule still has to load: the roster and defaults that pointed at it have not changed.
func TestRepositorySchedulesByID(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()

	schedule := s.weekdaySchedule(t)
	if err := s.repo.DeleteSchedule(ctx, schedule.ID); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}

	byID, err := s.repo.SchedulesByID(ctx, []int64{schedule.ID})
	if err != nil {
		t.Fatalf("SchedulesByID: %v", err)
	}
	if _, ok := byID[schedule.ID]; !ok {
		t.Error("SchedulesByID skipped a retired schedule")
	}

	empty, err := s.repo.SchedulesByID(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Errorf("SchedulesByID(nil) = %v, %v", empty, err)
	}
}

func TestRepositoryFaults(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()

	schedule := s.weekdaySchedule(t)
	holiday := s.addHoliday(t, day(2026, time.August, 17), "Kemerdekaan")
	location := s.addLocation(t)

	broken := NewRepository(failingDB(t, ""))
	calls := map[string]func() error{
		"ListSchedules": func() error { _, err := broken.ListSchedules(ctx); return err },
		"ScheduleByID":  func() error { _, err := broken.ScheduleByID(ctx, schedule.ID); return err },
		"CreateSchedule": func() error {
			return broken.CreateSchedule(ctx, &WorkSchedule{Name: unique("schedule")})
		},
		"ReplaceSchedule": func() error {
			_, err := broken.ReplaceSchedule(ctx, WorkSchedule{ID: schedule.ID, Name: "x"})
			return err
		},
		"DeleteSchedule": func() error { return broken.DeleteSchedule(ctx, schedule.ID) },
		"ListHolidays": func() error {
			_, err := broken.ListHolidays(ctx, Range{From: holiday.Date, To: holiday.Date})
			return err
		},
		"HolidayByID":   func() error { _, err := broken.HolidayByID(ctx, holiday.ID); return err },
		"CreateHoliday": func() error { return broken.CreateHoliday(ctx, &Holiday{Date: futureDate(400)}, nil) },
		"ReplaceHoliday": func() error {
			_, err := broken.ReplaceHoliday(ctx, Holiday{ID: holiday.ID, Date: holiday.Date, Name: "x"}, nil)
			return err
		},
		"DeleteHoliday":  func() error { return broken.DeleteHoliday(ctx, holiday.ID, nil) },
		"ListLocations":  func() error { _, err := broken.ListLocations(ctx); return err },
		"LocationByID":   func() error { _, err := broken.LocationByID(ctx, location.ID); return err },
		"CreateLocation": func() error { return broken.CreateLocation(ctx, &OfficeLocation{Name: "x"}) },
		"ReplaceLocation": func() error {
			_, err := broken.ReplaceLocation(ctx, OfficeLocation{ID: location.ID, Name: "x", RadiusM: 100})
			return err
		},
		"DeleteLocation": func() error { return broken.DeleteLocation(ctx, location.ID) },
		"UpsertAssignments": func() error {
			return broken.UpsertAssignments(ctx, []ShiftAssignment{{UserID: 1, WorkDate: futureDate(1)}}, nil)
		},
		"AssignmentByID":    func() error { _, err := broken.AssignmentByID(ctx, 1); return err },
		"DeleteAssignment":  func() error { return broken.DeleteAssignment(ctx, 1, nil) },
		"ListAssignments":   func() error { _, err := broken.ListAssignments(ctx, Range{}, nil); return err },
		"DefaultScheduleID": func() error { _, err := broken.DefaultScheduleID(ctx, 1); return err },
		"SchedulesByID":     func() error { _, err := broken.SchedulesByID(ctx, []int64{1}); return err },
	}

	for name, call := range calls {
		if err := call(); !errors.Is(err, errInjected) {
			t.Errorf("%s error = %v, want the injected failure", name, err)
		}
	}
}

// The read after a successful write is its own statement, so it can fail on its own.
func TestRepositoryReadBackFails(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()

	schedule := s.weekdaySchedule(t)
	holiday := s.addHoliday(t, day(2026, time.August, 17), "Kemerdekaan")
	location := s.addLocation(t)

	broken := NewRepository(failingAfterDB(t, "SELECT"))
	_, err := broken.ReplaceSchedule(ctx, WorkSchedule{ID: schedule.ID, Name: "x", Workdays: pqInt16Array{1}})
	if !errors.Is(err, errInjected) {
		t.Errorf("ReplaceSchedule error = %v, want the injected failure", err)
	}
	_, err = broken.ReplaceHoliday(ctx, Holiday{ID: holiday.ID, Date: holiday.Date, Name: "x"}, nil)
	if !errors.Is(err, errInjected) {
		t.Errorf("ReplaceHoliday error = %v, want the injected failure", err)
	}
	_, err = broken.ReplaceLocation(ctx, OfficeLocation{ID: location.ID, Name: "x", RadiusM: 100})
	if !errors.Is(err, errInjected) {
		t.Errorf("ReplaceLocation error = %v, want the injected failure", err)
	}
}

// translate only maps the constraint violations this module can provoke; anything else passes through.
func TestTranslate(t *testing.T) {
	if err := translate(nil); err != nil {
		t.Errorf("translate(nil) = %v", err)
	}
	if err := translate(errInjected); !errors.Is(err, errInjected) {
		t.Errorf("translate(other) = %v, want it unchanged", err)
	}

	// A name too long for the column is a database error this module does not map, so it stays a 500:
	// validation catches it first, and anything reaching here is a bug rather than a bad request.
	s := newServer(t)
	long := WorkSchedule{Name: strings.Repeat("a", 101), Workdays: pqInt16Array{1}}
	err := s.repo.CreateSchedule(context.Background(), &long)
	if err == nil || apperr.IsValidation(err) {
		t.Errorf("CreateSchedule with an over-long name error = %v, want it unmapped", err)
	}
}

func hasSchedule(schedules []WorkSchedule, id int64) bool {
	for _, schedule := range schedules {
		if schedule.ID == id {
			return true
		}
	}
	return false
}

func hasLocation(locations []OfficeLocation, id int64) bool {
	for _, location := range locations {
		if location.ID == id {
			return true
		}
	}
	return false
}
