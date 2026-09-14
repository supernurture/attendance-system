package leave

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"attendance-system/internal/middleware"
	"attendance-system/internal/pkg/apperr"
)

func TestOverlapIsRefusedByTheConstraint(t *testing.T) {
	s := newServer(t)
	employee := s.person(t, middleware.RoleEmployee, nil)
	annual := s.seeded(t, "annual")
	ctx := t.Context()

	// Straight to the repository, past every check in the service: the EXCLUDE constraint is what holds.
	row := func(from, to time.Time) *Leave {
		return &Leave{UserID: employee.UserID, LeaveTypeID: annual.ID, StartDate: from, EndDate: to, WorkingDays: 1,
			Reason: "x", Status: statusPending}
	}
	first := row(date(time.March, 3), date(time.March, 5))
	if err := s.repo.Create(ctx, first, nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.repo.Create(ctx, row(date(time.March, 5), date(time.March, 5)), nil); !errors.Is(err, errOverlap) {
		t.Errorf("sharing the last day: err = %v, want errOverlap", err)
	}
	if err := s.repo.Create(ctx, row(date(time.March, 6), date(time.March, 6)), nil); err != nil {
		t.Errorf("the day after: %v", err)
	}

	// A rejected request frees its dates.
	if _, err := s.repo.Settle(ctx, first.ID, statusPending, map[string]any{"status": statusRejected}); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if err := s.repo.Create(ctx, row(date(time.March, 4), date(time.March, 4)), nil); err != nil {
		t.Errorf("dates a rejected request held: %v", err)
	}

	// The attachment column's UNIQUE settles a race the service's lookup cannot see.
	keyed := row(date(time.April, 1), date(time.April, 1))
	keyed.AttachmentKey, keyed.AttachmentMime = ptr(neverPut(employee.UserID)), ptr("application/pdf")
	if err := s.repo.Create(ctx, keyed, nil); err != nil {
		t.Fatalf("Create with an attachment: %v", err)
	}
	reused := row(date(time.April, 2), date(time.April, 2))
	reused.AttachmentKey, reused.AttachmentMime = keyed.AttachmentKey, keyed.AttachmentMime
	if err := s.repo.Create(ctx, reused, nil); !errors.Is(err, errAttachmentUsed) {
		t.Errorf("a reused attachment key: err = %v, want errAttachmentUsed", err)
	}

	nobody := row(date(time.May, 1), date(time.May, 1))
	nobody.UserID = 0
	if err := s.repo.Create(ctx, nobody, nil); !apperr.IsValidation(err) {
		t.Errorf("a request for no user: err = %v, want a validation error", err)
	}
}

func TestRepositoryFaults(t *testing.T) {
	s := newServer(t)
	employee := s.person(t, middleware.RoleEmployee, nil)
	admin := s.person(t, middleware.RoleHRAdmin, nil)
	kind := s.kind(t, LeaveType{Name: "Faulty", QuotaDaysPerYear: ptr(3)})
	ctx := t.Context()

	quota := Quota{UserID: employee.UserID, LeaveTypeID: kind.ID, Year: year, QuotaDays: 2}
	audit := func(*Quota, Quota) AuditLog { return AuditLog{ActorID: admin.UserID, Action: "x", EntityType: "x"} }
	for name, match := range map[string]string{
		"locking the quota": `"leave_balances" WHERE user_id`,
		"writing the quota": `INSERT INTO "leave_balances"`,
		"writing its audit": `INSERT INTO "audit_logs"`,
	} {
		repo := NewRepository(failingAfterDB(t, match), s.store)
		if _, err := repo.SetQuota(ctx, quota, false, audit); !errors.Is(err, errInjected) {
			t.Errorf("SetQuota with %s failing: err = %v, want the injected failure", name, err)
		}
	}

	quotaChanged := func(LeaveType) *AuditLog {
		return &AuditLog{ActorID: admin.UserID, Action: "x", EntityType: "x", EntityID: kind.ID}
	}
	for name, match := range map[string]string{
		"writing the type":  `UPDATE "leave_types"`,
		"writing its audit": `INSERT INTO "audit_logs"`,
	} {
		if _, err := NewRepository(failingAfterDB(t, match), s.store).ReplaceType(ctx, kind, quotaChanged); !errors.Is(
			err, errInjected) {
			t.Errorf("ReplaceType with %s failing: err = %v, want the injected failure", name, err)
		}
	}
	if err := NewRepository(failingDB(t, "leave_types"), s.store).DeleteType(ctx, kind.ID); !errors.Is(err,
		errInjected) {
		t.Errorf("DeleteType failing: err = %v, want the injected failure", err)
	}

	// Settling fails outright, not only by finding nothing pending.
	row := s.file(t, employee.UserID, kind.Code, date(time.March, 3), date(time.March, 3), nil)
	if _, err := NewRepository(failingDB(t, "leave_requests"), s.store).Settle(ctx, row.ID, statusPending,
		map[string]any{"status": statusRejected}); !errors.Is(err, errInjected) {
		t.Errorf("Settle failing: err = %v, want the injected failure", err)
	}
	if _, err := NewRepository(failingDB(t, "users"), s.store).InSubtree(ctx, admin.UserID,
		employee.UserID); !errors.Is(err, errInjected) {
		t.Errorf("InSubtree failing: err = %v, want the injected failure", err)
	}
}

func TestTranslate(t *testing.T) {
	if err := translate(errInjected); !errors.Is(err, errInjected) {
		t.Errorf("translate = %v, want a non-Postgres error unchanged", err)
	}
	checkViolation := &pgconn.PgError{Code: "23514"}
	if err := translate(checkViolation); !errors.Is(err, checkViolation) {
		t.Errorf("translate = %v, want a violation nothing maps passed on as it is", err)
	}
}
