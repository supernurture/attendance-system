package leave

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"attendance-system/internal/middleware"
	"attendance-system/internal/pkg/apperr"
	"attendance-system/internal/pkg/storage"
)

func TestNowIsUTCToTheMicrosecond(t *testing.T) {
	// Every other test pins the clock, so this is the one place the real one runs.
	if at := now(); at.Location() != time.UTC || at.Nanosecond()%int(time.Microsecond) != 0 {
		t.Errorf("now() = %v; want UTC with nothing finer than Postgres keeps", at)
	}
}

func TestRequestCountsOnlyWorkingDays(t *testing.T) {
	s := newServer(t)
	employee := s.person(t, middleware.RoleEmployee, nil)
	s.holiday(t, date(time.March, 7))

	// Thursday to Monday: the Friday holiday and the weekend between are free, which leaves 2 of the 5 days.
	row := s.file(t, employee.UserID, "annual", date(time.March, 6), date(time.March, 10), nil)
	if row.WorkingDays != 2 || row.Status != statusPending || row.ReviewedAt != nil || row.ID == 0 {
		t.Errorf("row = %+v; want 2 working days, pending", row)
	}
	if held := s.balance(t, employee.UserID, s.seeded(t, "annual").ID); held.PendingDays != 2 ||
		held.RemainingDays() != 10 {
		t.Errorf("balance = %+v, remaining %d; want 2 pending and 10 left", held, held.RemainingDays())
	}

	weekend := Filing{Type: "annual", StartDate: date(time.March, 8), EndDate: date(time.March, 9), Reason: "x"}
	if _, err := s.svc.Request(t.Context(), employee.UserID, weekend); !apperr.IsValidation(err) {
		t.Errorf("a weekend alone: err = %v, want a validation error", err)
	}
}

func TestAnnualLeaveCannotExceedItsBalance(t *testing.T) {
	s := newServer(t)
	employee := s.person(t, middleware.RoleEmployee, nil)
	annual := s.seeded(t, "annual")
	ctx := t.Context()

	// Monday 3 March to Wednesday 19 March is 13 working days, one more than the 12 a year.
	tooMany := Filing{Type: "annual", StartDate: date(time.March, 3), EndDate: date(time.March, 19), Reason: "x"}
	if _, err := s.svc.Request(ctx, employee.UserID, tooMany); !apperr.IsValidation(err) ||
		!strings.Contains(err.Error(), "only 12 remain") {
		t.Errorf("13 days of annual leave: err = %v, want a validation error naming the 12 left", err)
	}

	whole := s.file(t, employee.UserID, "annual", date(time.March, 3), date(time.March, 18), nil)
	oneMore := Filing{Type: "annual", StartDate: date(time.April, 1), EndDate: date(time.April, 1), Reason: "x"}
	if _, err := s.svc.Request(ctx, employee.UserID, oneMore); !apperr.IsValidation(err) {
		t.Errorf("a day past a quota spent by a pending request: err = %v, want a validation error", err)
	}

	// Rejecting gives the days back without anything to update: the balance is summed from the requests.
	admin := s.person(t, middleware.RoleHRAdmin, nil)
	if _, err := s.svc.Decide(ctx, admin, whole.ID, DecisionReject, "busy season"); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if held := s.balance(t, employee.UserID, annual.ID); held.RemainingDays() != 12 || held.PendingDays != 0 {
		t.Errorf("balance after rejecting = %+v; want all 12 back", held)
	}
	if _, err := s.svc.Request(ctx, employee.UserID, oneMore); err != nil {
		t.Errorf("a day once the quota is free again: %v", err)
	}
}

func TestSpecialLeaveDoesNotReduceAnnualLeave(t *testing.T) {
	s := newServer(t)
	lead := s.person(t, middleware.RoleSupervisor, nil)
	employee := s.person(t, middleware.RoleEmployee, &lead.UserID)
	annual := s.seeded(t, "annual")

	// Article 93: two days of bereavement, approved, and the annual quota does not move.
	key := s.attachment(t, employee.UserID)
	row := s.file(t, employee.UserID, "bereavement_core", date(time.March, 3), date(time.March, 4), &key)
	if _, err := s.svc.Decide(t.Context(), lead, row.ID, DecisionApprove, ""); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	held := s.balance(t, employee.UserID, annual.ID)
	if held.UsedDays != 0 || held.PendingDays != 0 || held.RemainingDays() != 12 {
		t.Errorf("annual balance after approved bereavement leave = %+v; want untouched", held)
	}
	balances, _ := s.repo.Balances(t.Context(), employee.UserID, year)
	for _, balance := range balances {
		if balance.Code == "bereavement_core" {
			t.Error("bereavement leave has no yearly quota, so it has no balance to list")
		}
	}
}

func TestLeaveTypeRules(t *testing.T) {
	s := newServer(t)
	employee := s.person(t, middleware.RoleEmployee, nil)
	ctx := t.Context()

	sickDay := s.file(t, employee.UserID, "sick", date(time.March, 3), date(time.March, 3), nil)
	if sickDay.AttachmentKey != nil {
		t.Errorf("a day of sick leave = %+v; want it accepted with no attachment", sickDay)
	}

	nextYear := time.Date(year+1, time.January, 2, 0, 0, 0, 0, time.UTC)
	lastYear := time.Date(year-1, time.March, 4, 0, 0, 0, 0, time.UTC) // a Monday
	refused := map[string]Filing{
		"3 days sick with no note": {Type: "sick", StartDate: date(time.March, 10), EndDate: date(time.March, 12),
			Reason: "x"},
		"5 days of marriage leave": {Type: "marriage", StartDate: date(time.March, 17), EndDate: date(time.March, 21),
			Reason: "x"},
		"annual across a new year": {Type: "annual", StartDate: date(time.December, 31), EndDate: nextYear,
			Reason: "x"},
		"an unknown type": {Type: "sabbatical", StartDate: date(time.March, 3), EndDate: date(time.March, 3),
			Reason: "x"},
		"a key never put": {Type: "sick", StartDate: date(time.May, 5), EndDate: date(time.May, 5), Reason: "x",
			AttachmentKey: ptr(neverPut(employee.UserID))},
		"no reason at all":     {Type: "sick", StartDate: date(time.May, 6), EndDate: date(time.May, 6)},
		"annual for last year": {Type: "annual", StartDate: lastYear, EndDate: lastYear, Reason: "x"},
	}
	for name, filing := range refused {
		if _, err := s.svc.Request(ctx, employee.UserID, filing); !apperr.IsValidation(err) {
			t.Errorf("%s: err = %v, want a validation error", name, err)
		}
	}
	if _, err := s.svc.Request(ctx, employee.UserID, refused["5 days of marriage leave"]); err == nil ||
		!strings.Contains(err.Error(), "at most 3 working days") {
		t.Errorf("marriage leave past its cap: err = %v, want max_working_days_per_request named", err)
	}

	// With a note, the same three days go through, and the note cannot back a second request.
	key := s.attachment(t, employee.UserID)
	sick := s.file(t, employee.UserID, "sick", date(time.March, 10), date(time.March, 12), &key)
	if sick.WorkingDays != 3 || sick.AttachmentMime == nil || *sick.AttachmentMime != "application/pdf" {
		t.Errorf("3 days sick with a note = %+v", sick)
	}
	again := Filing{Type: "sick", StartDate: date(time.April, 1), EndDate: date(time.April, 3), Reason: "x",
		AttachmentKey: &key}
	if _, err := s.svc.Request(ctx, employee.UserID, again); !errors.Is(err, errAttachmentUsed) {
		t.Errorf("reusing an attachment: err = %v, want errAttachmentUsed", err)
	}
	colleague := s.person(t, middleware.RoleEmployee, nil)
	again.AttachmentKey = ptr(s.attachment(t, employee.UserID))
	if _, err := s.svc.Request(ctx, colleague.UserID, again); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("someone else's attachment: err = %v, want ErrForbidden", err)
	}

	// Sick leave has no quota to close with the year, so it may be filed late for last year.
	s.file(t, employee.UserID, "sick", lastYear, lastYear, nil)

	// Sick leave has no yearly quota, so it may run into the next year.
	newYear := Filing{Type: "sick", StartDate: date(time.December, 31), EndDate: nextYear, Reason: "x",
		AttachmentKey: ptr(s.attachment(t, employee.UserID))}
	if _, err := s.svc.Request(ctx, employee.UserID, newYear); err != nil {
		t.Errorf("sick leave over the new year: %v", err)
	}
}

func TestOverlappingRequestsConflict(t *testing.T) {
	s := newServer(t)
	employee := s.person(t, middleware.RoleEmployee, nil)
	ctx := t.Context()

	first := s.file(t, employee.UserID, "annual", date(time.March, 3), date(time.March, 4), nil)
	overlap := Filing{Type: "unpaid", StartDate: date(time.March, 4), EndDate: date(time.March, 5), Reason: "x"}
	if _, err := s.svc.Request(ctx, employee.UserID, overlap); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("dates overlapping a pending request of another type: err = %v, want ErrConflict", err)
	}

	if err := s.svc.Cancel(ctx, employee, first.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if _, err := s.svc.Request(ctx, employee.UserID, overlap); err != nil {
		t.Errorf("the same dates once the first is cancelled: %v", err)
	}
}

func TestConcurrentRequestsCannotOverbook(t *testing.T) {
	s := newServer(t)
	employee := s.person(t, middleware.RoleEmployee, nil)
	kind := s.kind(t, LeaveType{Name: "Short quota", QuotaDaysPerYear: ptr(3)})

	// Six single days at once against a quota of three: exactly three may get through.
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for x := range 6 {
		wg.Go(func() {
			day := date(time.March, 3+x) // Monday to Saturday; the Saturday is refused on its own
			_, err := s.svc.Request(context.Background(), employee.UserID, Filing{Type: kind.Code, StartDate: day,
				EndDate: day, Reason: "x"})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)

	filed := 0
	for err := range errs {
		if err == nil {
			filed++
		}
	}
	if held := s.balance(t, employee.UserID, kind.ID); filed != 3 || held.PendingDays != 3 {
		t.Errorf("%d filed, balance %+v; want exactly 3 days held", filed, held)
	}
}

func TestAutoApprovedType(t *testing.T) {
	s := newServer(t)
	employee := s.person(t, middleware.RoleEmployee, nil)
	kind := s.kind(t, LeaveType{Name: "Birthday", QuotaDaysPerYear: ptr(1), AutoApprove: true})
	clockAt(t, time.Date(year, time.March, 1, 9, 0, 0, 0, jakarta))

	row := s.file(t, employee.UserID, kind.Code, date(time.March, 3), date(time.March, 3), nil)
	if row.Status != statusApproved || row.ReviewedAt == nil || row.ReviewedBy != nil {
		t.Errorf("row = %+v; want approved on filing, by nobody", row)
	}
	if held := s.balance(t, employee.UserID, kind.ID); held.UsedDays != 1 || held.RemainingDays() != 0 {
		t.Errorf("balance = %+v; want the one day used", held)
	}
}

func TestDecide(t *testing.T) {
	s := newServer(t)
	lead := s.person(t, middleware.RoleSupervisor, nil)
	employee := s.person(t, middleware.RoleEmployee, &lead.UserID)
	stranger := s.person(t, middleware.RoleSupervisor, nil)
	admin := s.person(t, middleware.RoleHRAdmin, nil)
	ctx := t.Context()
	clockAt(t, time.Date(year, time.March, 1, 9, 0, 0, 0, jakarta))

	row := s.file(t, employee.UserID, "annual", date(time.March, 3), date(time.March, 3), nil)
	own := s.file(t, lead.UserID, "annual", date(time.March, 3), date(time.March, 3), nil)

	forbidden := map[string]struct {
		as middleware.Claims
		id int64
	}{
		"an employee":                     {employee, row.ID},
		"a supervisor above someone else": {stranger, row.ID},
		"a supervisor for their own":      {lead, own.ID},
	}
	for name, test := range forbidden {
		if _, err := s.svc.Decide(ctx, test.as, test.id, DecisionApprove, ""); !errors.Is(err, apperr.ErrForbidden) {
			t.Errorf("%s: err = %v, want ErrForbidden", name, err)
		}
	}
	if _, err := s.svc.Decide(ctx, lead, row.ID, "maybe", ""); !apperr.IsValidation(err) {
		t.Errorf("an unknown decision: err = %v, want a validation error", err)
	}
	if _, err := s.svc.Decide(ctx, lead, row.ID, DecisionApprove, strings.Repeat("x", 501)); !apperr.IsValidation(err) {
		t.Errorf("a note too long: err = %v, want a validation error", err)
	}
	if _, err := s.svc.Decide(ctx, lead, 0, DecisionApprove, ""); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("no such request: err = %v, want ErrNotFound", err)
	}

	approved, err := s.svc.Decide(ctx, lead, row.ID, DecisionApprove, "enjoy")
	if err != nil || approved.Status != statusApproved || *approved.ReviewedBy != lead.UserID ||
		!approved.ReviewedAt.Equal(time.Date(year, time.March, 1, 9, 0, 0, 0, jakarta)) ||
		*approved.ReviewNote != "enjoy" {
		t.Errorf("approved = %+v, %v", approved, err)
	}
	if _, err := s.svc.Decide(ctx, admin, row.ID, DecisionReject, ""); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("deciding twice: err = %v, want ErrConflict", err)
	}
	rejected, err := s.svc.Decide(ctx, admin, own.ID, DecisionReject, "")
	if err != nil || rejected.Status != statusRejected || rejected.ReviewNote != nil {
		t.Errorf("hr_admin rejecting a supervisor's own = %+v, %v", rejected, err)
	}
}

func TestPendingAndCancel(t *testing.T) {
	s := newServer(t)
	lead := s.person(t, middleware.RoleSupervisor, nil)
	employee := s.person(t, middleware.RoleEmployee, &lead.UserID)
	outsider := s.person(t, middleware.RoleEmployee, nil)
	admin := s.person(t, middleware.RoleHRAdmin, nil)
	ctx := t.Context()

	below := s.file(t, employee.UserID, "annual", date(time.March, 3), date(time.March, 3), nil)
	s.file(t, lead.UserID, "annual", date(time.March, 3), date(time.March, 3), nil)
	away := s.file(t, outsider.UserID, "annual", date(time.March, 3), date(time.March, 3), nil)

	pending, err := s.svc.Pending(ctx, lead)
	if err != nil || len(pending) != 1 || pending[0].ID != below.ID {
		t.Errorf("Pending for the lead = %+v, %v; want only their report's", pending, err)
	}
	everyone, err := s.svc.Pending(ctx, admin)
	if err != nil || !has(everyone, away.ID) || !has(everyone, below.ID) {
		t.Errorf("Pending for hr_admin = %+v, %v; want everyone's", everyone, err)
	}
	if _, err := s.svc.Pending(ctx, employee); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("Pending for an employee: err = %v, want ErrForbidden", err)
	}

	mine, err := s.svc.Mine(ctx, employee.UserID)
	if err != nil || len(mine) != 1 || mine[0].ID != below.ID {
		t.Errorf("Mine = %+v, %v", mine, err)
	}

	if err := s.svc.Cancel(ctx, lead, below.ID); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("cancelling a report's request: err = %v, want ErrForbidden", err)
	}
	if err := s.svc.Cancel(ctx, employee, 0); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("cancelling nothing: err = %v, want ErrNotFound", err)
	}
	if err := s.svc.Cancel(ctx, employee, below.ID); err != nil {
		t.Errorf("Cancel: %v", err)
	}
	if err := s.svc.Cancel(ctx, employee, below.ID); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("cancelling twice: err = %v, want ErrConflict", err)
	}
}

func TestCancelApprovedLeave(t *testing.T) {
	s := newServer(t) // today is Wednesday 15 January
	lead := s.person(t, middleware.RoleSupervisor, nil)
	employee := s.person(t, middleware.RoleEmployee, &lead.UserID)
	admin := s.person(t, middleware.RoleHRAdmin, nil)
	otherAdmin := s.person(t, middleware.RoleHRAdmin, nil)
	annual := s.seeded(t, "annual")
	ctx := t.Context()

	approved := func(as middleware.Claims, by middleware.Claims, from, to time.Time) Leave {
		t.Helper()
		row := s.file(t, as.UserID, "annual", from, to, nil)
		if _, err := s.svc.Decide(ctx, by, row.ID, DecisionApprove, ""); err != nil {
			t.Fatalf("Decide: %v", err)
		}
		return row
	}
	upcoming := approved(employee, lead, date(time.March, 3), date(time.March, 4))
	started := approved(employee, lead, date(time.January, 15), date(time.January, 16))
	adminsOwn := approved(admin, otherAdmin, date(time.January, 15), date(time.January, 15))
	rejected := s.file(t, employee.UserID, "annual", date(time.April, 1), date(time.April, 1), nil)
	if _, err := s.svc.Decide(ctx, lead, rejected.ID, DecisionReject, ""); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	refused := []struct {
		name string
		as   middleware.Claims
		id   int64
		want error
	}{
		{"by a supervisor above the owner", lead, upcoming.ID, apperr.ErrForbidden},
		{"by the owner once it has started", employee, started.ID, apperr.ErrForbidden},
		{"by hr_admin for their own started leave", admin, adminsOwn.ID, apperr.ErrForbidden},
		{"a rejected request", employee, rejected.ID, apperr.ErrConflict},
	}
	for _, test := range refused {
		if err := s.svc.Cancel(ctx, test.as, test.id); !errors.Is(err, test.want) {
			t.Errorf("cancelling %s: err = %v, want %v", test.name, err, test.want)
		}
	}

	if err := s.svc.Cancel(ctx, employee, upcoming.ID); err != nil {
		t.Fatalf("the owner cancelling approved leave not yet started: %v", err)
	}
	if err := s.svc.Cancel(ctx, admin, started.ID); err != nil {
		t.Fatalf("hr_admin cancelling someone's started leave: %v", err)
	}
	if err := s.svc.Cancel(ctx, otherAdmin, adminsOwn.ID); err != nil {
		t.Fatalf("hr_admin cancelling another hr_admin's started leave: %v", err)
	}

	withdrawn, _ := s.repo.ByID(ctx, started.ID)
	if withdrawn.Status != statusCancelled || withdrawn.CancelledBy == nil || *withdrawn.CancelledBy != admin.UserID ||
		!withdrawn.CancelledAt.Equal(time.Date(year, time.January, 15, 9, 0, 0, 0, jakarta)) ||
		withdrawn.ReviewedBy == nil || *withdrawn.ReviewedBy != lead.UserID {
		t.Errorf("withdrawn = %+v; want cancelled by hr_admin, keeping who approved it", withdrawn)
	}
	if held := s.balance(t, employee.UserID, annual.ID); held.UsedDays != 0 || held.RemainingDays() != 12 {
		t.Errorf("balance after both were cancelled = %+v; want every day back", held)
	}
	s.file(t, employee.UserID, "annual", date(time.March, 3), date(time.March, 4), nil) // the dates are free again
}

func TestAttachmentURL(t *testing.T) {
	s := newServer(t)
	lead := s.person(t, middleware.RoleSupervisor, nil)
	employee := s.person(t, middleware.RoleEmployee, &lead.UserID)
	colleague := s.person(t, middleware.RoleEmployee, nil)
	stranger := s.person(t, middleware.RoleSupervisor, nil)
	admin := s.person(t, middleware.RoleHRAdmin, nil)
	ctx := t.Context()

	key := s.attachment(t, employee.UserID)
	noted := s.file(t, employee.UserID, "sick", date(time.March, 3), date(time.March, 5), &key)
	bare := s.file(t, employee.UserID, "annual", date(time.March, 10), date(time.March, 10), nil)

	for _, as := range []middleware.Claims{employee, lead, admin} {
		if url, err := s.svc.AttachmentURL(ctx, as, noted.ID); err != nil || !strings.Contains(url, key) {
			t.Errorf("AttachmentURL as %s = %q, %v; want a URL to the key", as.Role, url, err)
		}
	}
	for _, as := range []middleware.Claims{colleague, stranger} {
		if _, err := s.svc.AttachmentURL(ctx, as, noted.ID); !errors.Is(err, apperr.ErrForbidden) {
			t.Errorf("AttachmentURL as an unrelated %s: err = %v, want ErrForbidden", as.Role, err)
		}
	}
	if _, err := s.svc.AttachmentURL(ctx, employee, bare.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("a request with no attachment: err = %v, want ErrNotFound", err)
	}
	if _, err := s.svc.AttachmentURL(ctx, employee, 0); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("no such request: err = %v, want ErrNotFound", err)
	}

	original := presignGet
	presignGet = func(*storage.Storage, context.Context, string) (storage.PresignedURL, error) {
		return storage.PresignedURL{}, errInjected
	}
	t.Cleanup(func() { presignGet = original })
	if _, err := s.svc.AttachmentURL(ctx, employee, noted.ID); !errors.Is(err, errInjected) {
		t.Errorf("presigning failing: err = %v, want the injected failure", err)
	}
}

func TestBalances(t *testing.T) {
	s := newServer(t)
	employee := s.person(t, middleware.RoleEmployee, nil)
	admin := s.person(t, middleware.RoleHRAdmin, nil)
	annual := s.seeded(t, "annual")
	ctx := t.Context()

	if _, err := s.svc.SetQuota(ctx, admin, Quota{UserID: employee.UserID, LeaveTypeID: annual.ID, Year: year,
		QuotaDays: 14}, ptr(3)); err != nil {
		t.Fatalf("SetQuota: %v", err)
	}
	s.file(t, employee.UserID, "annual", date(time.March, 3), date(time.March, 4), nil)

	// 31 December in UTC is already 1 January in Jakarta.
	clockAt(t, time.Date(year-1, time.December, 31, 18, 0, 0, 0, time.UTC))
	balances, err := s.svc.Balances(ctx, employee.UserID, nil)
	if err != nil {
		t.Fatalf("Balances: %v", err)
	}
	found := false
	for _, balance := range balances {
		if balance.LeaveTypeID != annual.ID {
			continue
		}
		found = true
		if balance.Year != year || balance.QuotaDays != 14 || balance.CarriedOverDays != 3 ||
			balance.PendingDays != 2 || balance.RemainingDays() != 15 {
			t.Errorf("annual balance = %+v; want 14 + 3 - 2 in %d", balance, year)
		}
	}
	if !found {
		t.Errorf("Balances = %+v; want annual listed", balances)
	}

	if other, err := s.svc.Balances(ctx, employee.UserID, ptr(year+1)); err != nil ||
		!hasBalance(other, annual.ID, 12) {
		t.Errorf("next year's balances = %+v, %v; want the type's own 12", other, err)
	}
	if _, err := s.svc.Balances(ctx, employee.UserID, ptr(1999)); !apperr.IsValidation(err) {
		t.Errorf("year 1999: err = %v, want a validation error", err)
	}
}

func TestServiceFaults(t *testing.T) {
	s := newServer(t)
	lead := s.person(t, middleware.RoleSupervisor, nil)
	employee := s.person(t, middleware.RoleEmployee, &lead.UserID)
	colleague := s.person(t, middleware.RoleSupervisor, nil)
	ctx := t.Context()

	key := s.attachment(t, employee.UserID)
	row := s.file(t, employee.UserID, "sick", date(time.March, 3), date(time.March, 5), &key)
	annual := Filing{Type: "annual", StartDate: date(time.April, 1), EndDate: date(time.April, 1), Reason: "x"}
	noted := Filing{Type: "sick", StartDate: date(time.April, 7), EndDate: date(time.April, 7), Reason: "x",
		AttachmentKey: ptr(s.attachment(t, employee.UserID))}

	requests := map[string]struct {
		svc    *Service
		filing Filing
	}{
		"looking up the type":        {s.failing(t, "leave_types"), annual},
		"resolving the working days": {s.failing(t, "work_schedules"), annual},
		"looking up the attachment":  {s.failing(t, "leave_requests"), noted},
		"locking the user":           {NewService(failingAfterDB(t, "FOR NO KEY UPDATE"), s.store, jakarta), annual},
		"reading the balance":        {NewService(failingAfterDB(t, "leave_balances b"), s.store, jakarta), annual},
		"inserting the request": {NewService(failingAfterDB(t, `INSERT INTO "leave_requests"`), s.store, jakarta),
			annual},
	}
	for name, test := range requests {
		if _, err := test.svc.Request(ctx, employee.UserID, test.filing); !errors.Is(err, errInjected) {
			t.Errorf("Request with %s failing: err = %v, want the injected failure", name, err)
		}
	}

	broken := s.failing(t, "leave_requests")
	if err := broken.Cancel(ctx, employee, row.ID); !errors.Is(err, errInjected) {
		t.Errorf("Cancel failing: err = %v, want the injected failure", err)
	}
	if _, err := broken.AttachmentURL(ctx, employee, row.ID); !errors.Is(err, errInjected) {
		t.Errorf("AttachmentURL failing: err = %v, want the injected failure", err)
	}
	if _, err := s.failing(t, "users").Decide(ctx, colleague, row.ID, DecisionApprove, ""); !errors.Is(err,
		errInjected) {
		t.Errorf("Decide with the hierarchy failing: err = %v, want the injected failure", err)
	}
	if _, err := broken.Decide(ctx, lead, row.ID, DecisionApprove, ""); !errors.Is(err, errInjected) {
		t.Errorf("Decide failing: err = %v, want the injected failure", err)
	}
}

func has(rows []Leave, id int64) bool {
	for _, row := range rows {
		if row.ID == id {
			return true
		}
	}
	return false
}

func hasBalance(balances []Balance, typeID int64, remaining int) bool {
	for _, balance := range balances {
		if balance.LeaveTypeID == typeID {
			return balance.RemainingDays() == remaining
		}
	}
	return false
}
