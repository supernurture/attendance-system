package leave

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"attendance-system/internal/middleware"
	"attendance-system/internal/pkg/apperr"
)

func TestLeaveTypeLifecycle(t *testing.T) {
	s := newServer(t)
	admin := s.person(t, middleware.RoleHRAdmin, nil)
	employee := s.person(t, middleware.RoleEmployee, nil)
	ctx := t.Context()

	code := strings.ReplaceAll(unique("study"), "-", "_")
	fields := LeaveType{Code: "  " + code + " ", Name: " Study leave ", QuotaDaysPerYear: ptr(5)}
	created, err := s.svc.CreateType(ctx, admin, fields)
	if err != nil {
		t.Fatalf("CreateType: %v", err)
	}
	s.typeIDs = append(s.typeIDs, created.ID)
	if created.ID == 0 || created.Code != code || created.Name != "Study leave" {
		t.Errorf("created = %+v; want it stored trimmed", created)
	}
	if _, err := s.svc.CreateType(ctx, admin, fields); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("a second live type with that code: err = %v, want ErrConflict", err)
	}

	kinds, err := s.svc.Types(ctx)
	if err != nil || !hasType(kinds, created.ID) || !hasType(kinds, s.seeded(t, "annual").ID) {
		t.Errorf("Types = %d types, %v; want the new one and the seeded ones", len(kinds), err)
	}

	// Renaming leaves the quota alone, so nothing is audited; changing the quota is.
	renamed := created
	renamed.Name = "Study"
	if _, err := s.svc.ReplaceType(ctx, admin, created.ID, renamed); err != nil {
		t.Fatalf("ReplaceType renaming: %v", err)
	}
	if entries := s.audits(t, entityLeaveType, created.ID); len(entries) != 0 {
		t.Errorf("renaming wrote %d audit entries, want none", len(entries))
	}
	renamed.QuotaDaysPerYear, renamed.AutoApprove = nil, true
	replaced, err := s.svc.ReplaceType(ctx, admin, created.ID, renamed)
	if err != nil || replaced.QuotaDaysPerYear != nil || !replaced.AutoApprove || replaced.Name != "Study" {
		t.Errorf("ReplaceType = %+v, %v", replaced, err)
	}
	entries := s.audits(t, entityLeaveType, created.ID)
	if len(entries) != 1 || entries[0].Action != actionTypeQuotaChanged || entries[0].ActorID != admin.UserID ||
		string(entries[0].Before) != `{"quota_days_per_year": 5}` ||
		string(entries[0].After) != `{"quota_days_per_year": null}` {
		t.Errorf("audit entries = %+v; want one from 5 to null", entries)
	}

	if _, err := s.svc.ReplaceType(ctx, admin, 0, renamed); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("replacing no type: err = %v, want ErrNotFound", err)
	}
	if _, err := s.svc.ReplaceType(ctx, admin, created.ID, LeaveType{}); !apperr.IsValidation(err) {
		t.Errorf("replacing with no code: err = %v, want a validation error", err)
	}
	seeded := s.seeded(t, "annual")
	if _, err := s.svc.ReplaceType(ctx, admin, created.ID, seeded); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("taking another type's code: err = %v, want ErrConflict", err)
	}

	for name, call := range map[string]func() error{
		"CreateType":  func() error { _, err := s.svc.CreateType(ctx, employee, fields); return err },
		"ReplaceType": func() error { _, err := s.svc.ReplaceType(ctx, employee, created.ID, renamed); return err },
		"DeleteType":  func() error { return s.svc.DeleteType(ctx, employee, created.ID) },
		"SetQuota":    func() error { _, err := s.svc.SetQuota(ctx, employee, Quota{}, nil); return err },
	} {
		if err := call(); !errors.Is(err, apperr.ErrForbidden) {
			t.Errorf("%s as an employee: err = %v, want ErrForbidden", name, err)
		}
	}
	if _, err := s.svc.CreateType(ctx, admin, LeaveType{Code: "x", Name: ""}); !apperr.IsValidation(err) {
		t.Errorf("creating with no name: err = %v, want a validation error", err)
	}

	if err := s.svc.DeleteType(ctx, admin, created.ID); err != nil {
		t.Fatalf("DeleteType: %v", err)
	}
	if err := s.svc.DeleteType(ctx, admin, created.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("deleting twice: err = %v, want ErrNotFound", err)
	}
	reused, err := s.svc.CreateType(ctx, admin, fields)
	if err != nil {
		t.Fatalf("reusing a retired type's code: %v", err)
	}
	s.typeIDs = append(s.typeIDs, reused.ID)
}

func TestSetQuota(t *testing.T) {
	s := newServer(t)
	admin := s.person(t, middleware.RoleHRAdmin, nil)
	employee := s.person(t, middleware.RoleEmployee, nil)
	annual := s.seeded(t, "annual")
	ctx := t.Context()

	quota := Quota{UserID: employee.UserID, LeaveTypeID: annual.ID, Year: year, QuotaDays: 10}
	set, err := s.svc.SetQuota(ctx, admin, quota, nil)
	if err != nil || set.QuotaDays != 10 || set.CarriedOverDays != 0 {
		t.Fatalf("SetQuota = %+v, %v", set, err)
	}
	if _, err := s.svc.SetQuota(ctx, admin, quota, ptr(4)); err != nil {
		t.Fatalf("SetQuota with carried-over days: %v", err)
	}

	// Leaving the carried-over days out keeps them rather than resetting them to 0.
	quota.QuotaDays = 12
	kept, err := s.svc.SetQuota(ctx, admin, quota, nil)
	if err != nil || kept.CarriedOverDays != 4 {
		t.Fatalf("SetQuota without carried-over days = %+v, %v; want the 4 kept", kept, err)
	}
	if held := s.balance(t, employee.UserID, annual.ID); held.QuotaDays != 12 || held.CarriedOverDays != 4 {
		t.Errorf("balance = %+v; want the replaced quota", held)
	}

	entries := s.audits(t, entityUser, employee.UserID)
	if len(entries) != 3 || entries[0].Action != actionQuotaSet || string(entries[0].Before) != nullJSON ||
		string(entries[2].Before) != string(entries[1].After) {
		t.Fatalf("audit entries = %+v; want three, each starting where the last ended", entries)
	}
	// jsonb keeps its keys shortest first.
	want := `{"year": 2031, "quota_days": 12, "leave_type_id": ` + strconv.FormatInt(annual.ID, 10) +
		`, "carried_over_days": 4}`
	if string(entries[2].After) != want {
		t.Errorf("after = %s, want %s", entries[2].After, want)
	}

	refused := map[string]Quota{
		"a year out of range":  {UserID: employee.UserID, LeaveTypeID: annual.ID, Year: 1999},
		"negative days":        {UserID: employee.UserID, LeaveTypeID: annual.ID, Year: year, QuotaDays: -1},
		"no such type":         {UserID: employee.UserID, LeaveTypeID: 0, Year: year},
		"a type with no quota": {UserID: employee.UserID, LeaveTypeID: s.seeded(t, "sick").ID, Year: year},
		"no such user":         {UserID: 0, LeaveTypeID: annual.ID, Year: year},
		"too many carried over": {UserID: employee.UserID, LeaveTypeID: annual.ID, Year: year,
			CarriedOverDays: 367},
	}
	for name, quota := range refused {
		if _, err := s.svc.SetQuota(ctx, admin, quota, ptr(quota.CarriedOverDays)); !apperr.IsValidation(err) {
			t.Errorf("%s: err = %v, want a validation error", name, err)
		}
	}
	own := Quota{UserID: admin.UserID, LeaveTypeID: annual.ID, Year: year, QuotaDays: 366}
	if _, err := s.svc.SetQuota(ctx, admin, own, nil); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("hr_admin setting their own quota: err = %v, want ErrForbidden", err)
	}
	if _, err := s.failing(t, "leave_types").SetQuota(ctx, admin, quota, nil); !errors.Is(err, errInjected) {
		t.Errorf("SetQuota with the type lookup failing: err = %v, want the injected failure", err)
	}
}

// audits reads the audit entries about one entity, oldest first.
func (s *server) audits(t *testing.T, entityType string, entityID int64) []AuditLog {
	t.Helper()

	var entries []AuditLog
	if err := s.db.Where("entity_type = ? AND entity_id = ?", entityType, entityID).Order("id").
		Find(&entries).Error; err != nil {
		t.Fatalf("read audit_logs: %v", err)
	}
	return entries
}

func hasType(kinds []LeaveType, id int64) bool {
	for _, kind := range kinds {
		if kind.ID == id {
			return true
		}
	}
	return false
}
