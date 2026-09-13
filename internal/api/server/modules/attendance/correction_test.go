package attendance

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"attendance-system/internal/middleware"
	"attendance-system/internal/pkg/apperr"
)

func TestCorrected(t *testing.T) {
	workDate := date(2030, time.March, 4)
	in, out := local(2030, 3, 4, 8, 0), local(2030, 3, 4, 17, 0)
	held := &Attendance{ID: 9, UserID: 1, WorkDate: workDate, CheckInAt: in}

	next, err := corrected(held, Correction{UserID: 1, WorkDate: workDate, ProposedCheckOutAt: &out})
	if err != nil || next.ID != 9 || !next.CheckInAt.Equal(in) || !next.CheckOutAt.Equal(out) {
		t.Errorf("a check-out laid over a held check-in = %+v, %v", next, err)
	}

	earlier := local(2030, 3, 4, 7, 0)
	next, err = corrected(nil, Correction{UserID: 1, WorkDate: workDate, ProposedCheckInAt: &earlier})
	if err != nil || next.ID != 0 || next.UserID != 1 || !next.CheckInAt.Equal(earlier) || next.CheckOutAt != nil {
		t.Errorf("a new day = %+v, %v", next, err)
	}

	if _, err := corrected(nil, Correction{WorkDate: workDate, ProposedCheckOutAt: &out}); !apperr.IsValidation(err) {
		t.Errorf("a check-out with no check-in anywhere: err = %v, want a validation error", err)
	}
	before := local(2030, 3, 4, 7, 59)
	if _, err := corrected(held, Correction{ProposedCheckOutAt: &before}); !apperr.IsValidation(err) {
		t.Errorf("a check-out before the check-in: err = %v, want a validation error", err)
	}
}

func TestRequestCorrection(t *testing.T) {
	s := newServer(t)
	employee := s.person(t, middleware.RoleEmployee, nil, nil)
	colleague := s.person(t, middleware.RoleEmployee, nil, nil)
	admin := s.person(t, middleware.RoleHRAdmin, nil, nil)
	ctx := t.Context()
	workDate := date(2030, time.March, 4)
	s.checkedIn(t, employee.UserID, workDate, local(2030, 3, 4, 8, 0), nil)
	clockAt(t, local(2030, time.March, 5, 9, 0))

	out := local(2030, 3, 4, 17, 0)
	forgot := Proposal{WorkDate: workDate, CheckOutAt: &out, Reason: "forgot to check out"}
	correction, err := s.svc.RequestCorrection(ctx, employee, forgot)
	if err != nil {
		t.Fatalf("RequestCorrection: %v", err)
	}
	if correction.ID == 0 || correction.Status != correctionPending || correction.RequestedBy != employee.UserID ||
		correction.UserID != employee.UserID {
		t.Errorf("correction = %+v", correction)
	}
	if _, err := s.svc.RequestCorrection(ctx, employee, forgot); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("a second pending request for the day: err = %v, want ErrConflict", err)
	}

	forColleague := Proposal{UserID: &colleague.UserID, WorkDate: workDate, CheckInAt: ptr(local(2030, 3, 4, 8, 5)),
		Reason: "the phone died"}
	if _, err := s.svc.RequestCorrection(ctx, employee, forColleague); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("an employee filing for someone else: err = %v, want ErrForbidden", err)
	}
	onBehalf, err := s.svc.RequestCorrection(ctx, admin, forColleague)
	if err != nil || onBehalf.UserID != colleague.UserID || onBehalf.RequestedBy != admin.UserID {
		t.Errorf("hr_admin filing on someone's behalf = %+v, %v", onBehalf, err)
	}

	nobody := int64(0)
	ghost := Proposal{UserID: &nobody, WorkDate: workDate, CheckInAt: ptr(local(2030, 3, 4, 8, 5)), Reason: "x"}
	if _, err := s.svc.RequestCorrection(ctx, admin, ghost); !apperr.IsValidation(err) {
		t.Errorf("filing for a user that does not exist: err = %v, want a validation error", err)
	}

	checkOutOnly := Proposal{WorkDate: date(2030, time.March, 3), CheckOutAt: ptr(local(2030, 3, 3, 17, 0)),
		Reason: "x"}
	if _, err := s.svc.RequestCorrection(ctx, colleague, checkOutOnly); !apperr.IsValidation(err) {
		t.Errorf("a check-out for a day with no check-in: err = %v, want a validation error", err)
	}
	if _, err := s.svc.RequestCorrection(ctx, colleague, Proposal{WorkDate: workDate}); !apperr.IsValidation(err) {
		t.Errorf("no times and no reason: err = %v, want a validation error", err)
	}
	if _, err := s.failing(t, "attendances").RequestCorrection(ctx, colleague, forColleague); !errors.Is(err,
		errInjected) {
		t.Errorf("attendance lookup failing: err = %v, want the injected failure", err)
	}
}

func TestApproveCorrectionRewritesTheDay(t *testing.T) {
	s := newServer(t)
	hours := s.hours(t, 8*60, 17*60, 15)
	lead := s.person(t, middleware.RoleSupervisor, nil, nil)
	employee := s.person(t, middleware.RoleEmployee, &lead.UserID, &hours.ID)
	ctx := t.Context()
	workDate := date(2030, time.March, 4)
	in := local(2030, 3, 4, 8, 40)
	row := s.checkedIn(t, employee.UserID, workDate, in, &hours.ID)
	clockAt(t, local(2030, time.March, 5, 9, 0))

	// The phone checked in 40 minutes late and never checked out; both times get corrected.
	correction := s.fileCorrection(t, employee.UserID, workDate, ptr(local(2030, 3, 4, 8, 20)),
		ptr(local(2030, 3, 4, 16, 30)))
	decided, err := s.svc.DecideCorrection(ctx, lead, correction.ID, DecisionApprove, "confirmed at the gate")
	if err != nil {
		t.Fatalf("DecideCorrection: %v", err)
	}

	if decided.Status != correctionApproved || *decided.ReviewedBy != lead.UserID || decided.ReviewedAt == nil ||
		*decided.ReviewNote != "confirmed at the gate" {
		t.Errorf("decided = %+v", decided)
	}
	if decided.OldCheckInAt == nil || !decided.OldCheckInAt.Equal(in) || decided.OldCheckOutAt != nil {
		t.Errorf("snapshot = %v, %v; want the old check-in and no check-out", decided.OldCheckInAt,
			decided.OldCheckOutAt)
	}

	stored, _ := s.repo.ByID(ctx, row.ID)
	if !stored.CheckInAt.Equal(local(2030, 3, 4, 8, 20)) || stored.CheckOutAt == nil ||
		!stored.CheckOutAt.Equal(local(2030, 3, 4, 16, 30)) {
		t.Errorf("stored times = %v to %v", stored.CheckInAt, stored.CheckOutAt)
	}
	if stored.LateMinutes != 5 || stored.EarlyLeaveMinutes != 30 || stored.CheckIn.PhotoKey == nil {
		t.Errorf("stored = late %d, early %d; want 5 and 30 with the check-in evidence kept",
			stored.LateMinutes, stored.EarlyLeaveMinutes)
	}

	if _, err := s.svc.DecideCorrection(ctx, lead, correction.ID, DecisionReject, ""); !errors.Is(err,
		apperr.ErrConflict) {
		t.Errorf("deciding twice: err = %v, want ErrConflict", err)
	}
}

func TestApproveCorrectionCreatesAMissingDay(t *testing.T) {
	s := newServer(t)
	hours := s.hours(t, 8*60, 17*60, 15)
	employee := s.person(t, middleware.RoleEmployee, nil, &hours.ID)
	admin := s.person(t, middleware.RoleHRAdmin, nil, nil)
	workDate := date(2030, time.March, 4)
	clockAt(t, local(2030, time.March, 5, 9, 0))

	correction := s.fileCorrection(t, employee.UserID, workDate, ptr(local(2030, 3, 4, 8, 20)), nil)
	decided, err := s.svc.DecideCorrection(t.Context(), admin, correction.ID, DecisionApprove, "")
	if err != nil {
		t.Fatalf("DecideCorrection: %v", err)
	}
	if decided.OldCheckInAt != nil || decided.ReviewNote != nil {
		t.Errorf("a day that had nothing: snapshot %v, note %v; want neither", decided.OldCheckInAt, decided.ReviewNote)
	}

	created, err := s.repo.Held(t.Context(), employee.UserID, workDate)
	if err != nil || created == nil {
		t.Fatalf("Held = %v, %v; want the approved day", created, err)
	}
	if created.ScheduleID == nil || *created.ScheduleID != hours.ID || created.LateMinutes != 5 ||
		created.CheckIn.PhotoKey != nil {
		t.Errorf("created = %+v; want rule A's schedule, 5 minutes late, and no evidence", created)
	}
}

func TestApproveCorrectionIsOneTransaction(t *testing.T) {
	s := newServer(t)
	employee := s.person(t, middleware.RoleEmployee, nil, nil)
	admin := s.person(t, middleware.RoleHRAdmin, nil, nil)
	workDate := date(2030, time.March, 4)
	row := s.checkedIn(t, employee.UserID, workDate, local(2030, 3, 4, 8, 0), nil)
	clockAt(t, local(2030, time.March, 5, 9, 0))
	correction := s.fileCorrection(t, employee.UserID, workDate, nil, ptr(local(2030, 3, 4, 17, 0)))

	// The attendance is written first; failing the correction's own update must take that write back.
	failing := s.failingAfter(t, `UPDATE "attendance_corrections"`)
	if _, err := failing.DecideCorrection(t.Context(), admin, correction.ID, DecisionApprove, ""); !errors.Is(err,
		errInjected) {
		t.Fatalf("err = %v, want the injected failure", err)
	}
	if stored, _ := s.repo.ByID(t.Context(), row.ID); stored.CheckOutAt != nil {
		t.Errorf("check-out = %v after a failed approval, want the attendance untouched", stored.CheckOutAt)
	}
	if stored, _ := s.repo.CorrectionByID(t.Context(), correction.ID); stored.Status != correctionPending {
		t.Errorf("status = %q after a failed approval, want pending", stored.Status)
	}
}

func TestDecideCorrectionRefuses(t *testing.T) {
	s := newServer(t)
	lead := s.person(t, middleware.RoleSupervisor, nil, nil)
	employee := s.person(t, middleware.RoleEmployee, &lead.UserID, nil)
	peer := s.person(t, middleware.RoleSupervisor, nil, nil)
	ctx := t.Context()
	workDate := date(2030, time.March, 4)
	row := s.checkedIn(t, employee.UserID, workDate, local(2030, 3, 4, 8, 0), nil)
	clockAt(t, local(2030, time.March, 5, 9, 0))
	correction := s.fileCorrection(t, employee.UserID, workDate, nil, ptr(local(2030, 3, 4, 17, 0)))
	own := s.fileCorrection(t, lead.UserID, workDate, ptr(local(2030, 3, 4, 8, 0)), nil)

	forbidden := map[string]struct {
		claims middleware.Claims
		id     int64
	}{
		"an employee":                     {employee, correction.ID},
		"a supervisor above someone else": {peer, correction.ID},
		"a supervisor on their own":       {lead, own.ID},
		"a colleague with a manager's role": {middleware.Claims{UserID: peer.UserID, Role: middleware.RoleEmployee},
			correction.ID},
	}
	for name, test := range forbidden {
		if _, err := s.svc.DecideCorrection(ctx, test.claims, test.id, DecisionApprove, ""); !errors.Is(err,
			apperr.ErrForbidden) {
			t.Errorf("%s: err = %v, want ErrForbidden", name, err)
		}
	}

	if _, err := s.svc.DecideCorrection(ctx, lead, correction.ID, "maybe", ""); !apperr.IsValidation(err) {
		t.Errorf("an unknown decision: err = %v, want a validation error", err)
	}
	if _, err := s.svc.DecideCorrection(ctx, lead, correction.ID, DecisionReject, strings.Repeat("a", 501)); !apperr.
		IsValidation(err) {
		t.Errorf("a note too long: err = %v, want a validation error", err)
	}
	if _, err := s.svc.DecideCorrection(ctx, lead, 0, DecisionApprove, ""); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("no such correction: err = %v, want ErrNotFound", err)
	}
	if _, err := s.failingAfter(t, "subordinates").DecideCorrection(ctx, lead, correction.ID, DecisionApprove,
		""); !errors.Is(err, errInjected) {
		t.Errorf("subtree lookup failing: err = %v, want the injected failure", err)
	}

	// The day moved on since it was filed: a check-in after the proposed check-out cannot be approved.
	s.db.Exec("UPDATE attendances SET check_in_at = ? WHERE id = ?", local(2030, 3, 4, 18, 0), row.ID)
	if _, err := s.svc.DecideCorrection(ctx, lead, correction.ID, DecisionApprove, ""); !apperr.IsValidation(err) {
		t.Errorf("an approval that no longer fits: err = %v, want a validation error", err)
	}

	rejected, err := s.svc.DecideCorrection(ctx, lead, correction.ID, DecisionReject, "not what the gate log says")
	if err != nil || rejected.Status != correctionRejected || *rejected.ReviewNote != "not what the gate log says" {
		t.Errorf("reject = %+v, %v", rejected, err)
	}
	if stored, _ := s.repo.ByID(ctx, row.ID); stored.CheckOutAt != nil {
		t.Error("rejecting touched the attendance")
	}
}

func TestDecideCorrectionScheduleFaults(t *testing.T) {
	s := newServer(t)
	hours := s.hours(t, 8*60, 17*60, 0)
	employee := s.person(t, middleware.RoleEmployee, nil, &hours.ID)
	admin := s.person(t, middleware.RoleHRAdmin, nil, nil)
	clockAt(t, local(2030, time.March, 5, 9, 0))

	// A day that exists is measured by the schedule it kept; one that does not, by rule A.
	s.checkedIn(t, employee.UserID, date(2030, 3, 4), local(2030, 3, 4, 8, 0), &hours.ID)
	kept := s.fileCorrection(t, employee.UserID, date(2030, 3, 4), nil, ptr(local(2030, 3, 4, 17, 0)))
	missing := s.fileCorrection(t, employee.UserID, date(2030, 3, 3), ptr(local(2030, 3, 3, 8, 0)), nil)

	faults := map[string]struct {
		svc *Service
		id  int64
	}{
		"the kept schedule":  {s.failing(t, "work_schedules"), kept.ID},
		"rule A for the day": {s.failing(t, "users"), missing.ID},
	}
	for name, test := range faults {
		if _, err := test.svc.DecideCorrection(t.Context(), admin, test.id, DecisionApprove, ""); !errors.Is(err,
			errInjected) {
			t.Errorf("%s failing: err = %v, want the injected failure", name, err)
		}
	}
}

func TestCancelCorrection(t *testing.T) {
	s := newServer(t)
	employee := s.person(t, middleware.RoleEmployee, nil, nil)
	stranger := s.person(t, middleware.RoleSupervisor, nil, nil)
	admin := s.person(t, middleware.RoleHRAdmin, nil, nil)
	ctx := t.Context()
	clockAt(t, local(2030, time.March, 5, 9, 0))

	mine := s.fileCorrection(t, employee.UserID, date(2030, 3, 4), ptr(local(2030, 3, 4, 8, 0)), nil)
	if err := s.svc.CancelCorrection(ctx, stranger, mine.ID); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("a stranger: err = %v, want ErrForbidden", err)
	}
	if err := s.svc.CancelCorrection(ctx, employee, mine.ID); err != nil {
		t.Fatalf("the employee: err = %v", err)
	}
	if stored, _ := s.repo.CorrectionByID(ctx, mine.ID); stored.Status != correctionCancelled {
		t.Errorf("status = %q, want cancelled", stored.Status)
	}
	if err := s.svc.CancelCorrection(ctx, employee, mine.ID); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("cancelling twice: err = %v, want ErrConflict", err)
	}

	// Cancelled frees the day for a new request, which whoever filed it on their behalf may withdraw.
	onBehalf, err := s.svc.RequestCorrection(ctx, admin, Proposal{UserID: &employee.UserID, WorkDate: date(2030, 3, 4),
		CheckInAt: ptr(local(2030, 3, 4, 8, 0)), Reason: "badge log"})
	if err != nil {
		t.Fatalf("RequestCorrection after a cancel: %v", err)
	}
	if err := s.svc.CancelCorrection(ctx, admin, onBehalf.ID); err != nil {
		t.Errorf("whoever filed it: err = %v", err)
	}
	if err := s.svc.CancelCorrection(ctx, employee, 0); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("no such correction: err = %v, want ErrNotFound", err)
	}
}

func TestListCorrections(t *testing.T) {
	s := newServer(t)
	lead := s.person(t, middleware.RoleSupervisor, nil, nil)
	employee := s.person(t, middleware.RoleEmployee, &lead.UserID, nil)
	outsider := s.person(t, middleware.RoleEmployee, nil, nil)
	admin := s.person(t, middleware.RoleHRAdmin, nil, nil)
	ctx := t.Context()
	clockAt(t, local(2030, time.March, 9, 9, 0))

	first := s.fileCorrection(t, employee.UserID, date(2030, 3, 4), ptr(local(2030, 3, 4, 8, 0)), nil)
	second := s.fileCorrection(t, employee.UserID, date(2030, 3, 5), ptr(local(2030, 3, 5, 8, 0)), nil)
	theirs := s.fileCorrection(t, outsider.UserID, date(2030, 3, 4), ptr(local(2030, 3, 4, 8, 0)), nil)
	own := s.fileCorrection(t, lead.UserID, date(2030, 3, 4), ptr(local(2030, 3, 4, 8, 0)), nil)

	pending, err := s.svc.PendingCorrections(ctx, lead)
	if err != nil || len(pending) != 2 || pending[0].ID != first.ID || pending[1].ID != second.ID {
		t.Errorf("the lead's queue = %+v, %v; want their report's two, oldest first", pending, err)
	}

	everything, err := s.svc.PendingCorrections(ctx, admin)
	if err != nil || !hasCorrection(everything, theirs.ID) || !hasCorrection(everything, own.ID) {
		t.Errorf("hr_admin's queue = %+v, %v; want every subtree's", everything, err)
	}
	if _, err := s.svc.PendingCorrections(ctx, employee); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("an employee's queue: err = %v, want ErrForbidden", err)
	}

	mine, err := s.svc.MyCorrections(ctx, employee.UserID)
	if err != nil || len(mine) != 2 || mine[0].ID != second.ID {
		t.Errorf("MyCorrections = %+v, %v; want both, newest first", mine, err)
	}
}

func hasCorrection(corrections []Correction, id int64) bool {
	for _, correction := range corrections {
		if correction.ID == id {
			return true
		}
	}
	return false
}

// Approval reads schedules on its own transaction; asking the pool for a second connection hung it once.
func TestApproveCorrectionNeedsOneConnection(t *testing.T) {
	s := newServer(t)
	hours := s.hours(t, 8*60, 17*60, 0)
	employee := s.person(t, middleware.RoleEmployee, nil, &hours.ID)
	admin := s.person(t, middleware.RoleHRAdmin, nil, nil)
	clockAt(t, local(2030, time.March, 5, 9, 0))
	s.checkedIn(t, employee.UserID, date(2030, 3, 4), local(2030, 3, 4, 8, 0), &hours.ID)
	kept := s.fileCorrection(t, employee.UserID, date(2030, 3, 4), nil, ptr(local(2030, 3, 4, 17, 0)))
	missing := s.fileCorrection(t, employee.UserID, date(2030, 3, 3), ptr(local(2030, 3, 3, 8, 0)), nil)

	db := testDB(t)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	svc := NewService(db, s.store, jakarta, false)

	for name, id := range map[string]int64{"the kept schedule": kept.ID, "rule A for the day": missing.ID} {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		if _, err := svc.DecideCorrection(ctx, admin, id, DecisionApprove, ""); err != nil {
			t.Errorf("%s on one connection: err = %v", name, err)
		}
		cancel()
	}
}
