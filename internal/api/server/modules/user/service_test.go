package user

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"attendance-system/internal/middleware"
)

func TestListFollowsTheHierarchy(t *testing.T) {
	s := newServer(t)
	lead, mid, junior, stranger := s.hierarchy(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)

	if _, err := s.svc.List(t.Context(), claimsOf(junior)); !errors.Is(err, ErrForbidden) {
		t.Errorf("an employee listing everyone: err = %v, want ErrForbidden", err)
	}

	below, err := s.svc.List(t.Context(), claimsOf(lead))
	if err != nil {
		t.Fatalf("List as a supervisor: %v", err)
	}
	if got := ids(below); !slices.Contains(got, mid.ID) || !slices.Contains(got, junior.ID) ||
		slices.Contains(got, stranger.ID) || slices.Contains(got, lead.ID) {
		t.Errorf("a supervisor sees %v, want exactly their subtree (%d, %d)", got, mid.ID, junior.ID)
	}

	everyone, err := s.svc.List(t.Context(), claimsOf(admin))
	if err != nil {
		t.Fatalf("List as hr_admin: %v", err)
	}
	for _, want := range []int64{lead.ID, stranger.ID} {
		if !slices.Contains(ids(everyone), want) {
			t.Errorf("hr_admin cannot see %d, want everyone", want)
		}
	}
}

func TestGetReachesSelfSubtreeAndEveryoneForAdmins(t *testing.T) {
	s := newServer(t)
	lead, _, junior, stranger := s.hierarchy(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)

	tests := map[string]struct {
		as, target User
		wantErr    error
	}{
		"themselves":             {junior, junior, nil},
		"someone else":           {junior, stranger, ErrForbidden},
		"their own subtree":      {lead, junior, nil},
		"outside their subtree":  {lead, stranger, ErrForbidden},
		"anyone, as an hr_admin": {admin, stranger, nil},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := s.svc.Get(t.Context(), claimsOf(test.as), test.target.ID)
			if !errors.Is(err, test.wantErr) {
				t.Errorf("err = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestCreateIsForHRAdminAndStartsAsAnEmployee(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	supervisor := s.addUser(t, middleware.RoleSupervisor, nil)

	next := NewUser{
		Email:    fmt.Sprintf(" New-%d@Example.com ", time.Now().UnixNano()),
		Password: "eight888",
		FullName: "  Budi Santoso ",
	}
	if _, err := s.svc.Create(t.Context(), claimsOf(supervisor), next); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a supervisor adding an employee: err = %v, want ErrForbidden", err)
	}

	created, err := s.svc.Create(t.Context(), claimsOf(admin), next)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	s.track(created)

	if created.Role != string(middleware.RoleEmployee) || !created.IsActive {
		t.Errorf("created = %+v, want an active employee", created)
	}
	if created.Email != normalizeEmail(next.Email) || created.FullName != "Budi Santoso" {
		t.Errorf("created = %+v, want the email and name trimmed", created)
	}
	if bcrypt.CompareHashAndPassword([]byte(created.PasswordHash), []byte(next.Password)) != nil {
		t.Error("the stored hash does not match the password")
	}
	if created.JoinDate.IsZero() {
		t.Error("join_date is empty, want today by default")
	}
}

func TestCreateValidatesItsInput(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	unknown := int64(0)

	tests := map[string]NewUser{
		"not an email":       {Email: "budi", Password: "eight888", FullName: "Budi"},
		"password too short": {Email: "b@test.local", Password: "seven77", FullName: "Budi"},
		"no name":            {Email: "b@test.local", Password: "eight888", FullName: "  "},
		"unknown manager": {
			Email:    fmt.Sprintf("m-%d@test.local", time.Now().UnixNano()),
			Password: "eight888", FullName: "Budi", ManagerID: &unknown,
		},
	}
	for name, next := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := s.svc.Create(t.Context(), claimsOf(admin), next); !isValidationError(err) {
				t.Errorf("err = %v, want a ValidationError", err)
			}
		})
	}
}

func TestReplaceRecordsADeactivation(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	employee := s.addUser(t, middleware.RoleEmployee, nil)
	department := s.addDepartment(t, fmt.Sprintf("Ops %d", time.Now().UnixNano()))

	details := Details{FullName: "Renamed", IsActive: false, DepartmentID: &department.ID}
	updated, err := s.svc.Replace(t.Context(), claimsOf(admin), employee.ID, details)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if updated.FullName != "Renamed" || updated.IsActive || updated.DepartmentID == nil {
		t.Errorf("updated = %+v, want the new details", updated)
	}
	if updated.JoinDate.IsZero() {
		t.Error("join_date was cleared, want the stored one kept when none is sent")
	}

	logs, err := s.repo.auditLogs(t.Context(), entityUser, employee.ID)
	if err != nil {
		t.Fatalf("auditLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("%d audit rows, want exactly 1 for the deactivation", len(logs))
	}
	if logs[0].Action != actionDeactivated || logs[0].ActorID != admin.ID {
		t.Errorf("audit = %+v, want %q by %d", logs[0], actionDeactivated, admin.ID)
	}
	if string(logs[0].Before) != `{"is_active": true}` || string(logs[0].After) != `{"is_active": false}` {
		t.Errorf("audit before/after = %s / %s", logs[0].Before, logs[0].After)
	}

	// Reactivating is not a loss of access, so it is not audited.
	details.IsActive = true
	if _, err := s.svc.Replace(t.Context(), claimsOf(admin), employee.ID, details); err != nil {
		t.Fatalf("Replace again: %v", err)
	}
	if logs, _ := s.repo.auditLogs(t.Context(), entityUser, employee.ID); len(logs) != 1 {
		t.Errorf("%d audit rows after reactivating, want the original 1", len(logs))
	}
}

func TestReplaceChecksRoleAndInput(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	employee := s.addUser(t, middleware.RoleEmployee, nil)
	details := Details{FullName: "Renamed", IsActive: true}

	if _, err := s.svc.Replace(t.Context(), claimsOf(employee), employee.ID, details); !errors.Is(err, ErrForbidden) {
		t.Errorf("an employee editing themselves: err = %v, want ErrForbidden", err)
	}
	if _, err := s.svc.Replace(t.Context(), claimsOf(admin), 0, details); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing user: err = %v, want ErrNotFound", err)
	}

	own := Details{FullName: "Renamed", IsActive: true, ManagerID: &employee.ID}
	if _, err := s.svc.Replace(t.Context(), claimsOf(admin), employee.ID, own); !isValidationError(err) {
		t.Errorf("managing themselves: err = %v, want a ValidationError", err)
	}
}

func TestDeleteIsForHRAdminAndNotForThemselves(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	employee := s.addUser(t, middleware.RoleEmployee, nil)

	if err := s.svc.Delete(t.Context(), claimsOf(employee), employee.ID); !errors.Is(err, ErrForbidden) {
		t.Errorf("an employee deleting themselves: err = %v, want ErrForbidden", err)
	}
	if err := s.svc.Delete(t.Context(), claimsOf(admin), admin.ID); !isValidationError(err) {
		t.Errorf("an admin deleting their own account: err = %v, want a ValidationError", err)
	}
	if err := s.svc.Delete(t.Context(), claimsOf(admin), employee.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.svc.Get(t.Context(), claimsOf(admin), employee.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("after the delete: err = %v, want ErrNotFound", err)
	}
}

func TestChangeRoleIsForSuperAdminAndIsAudited(t *testing.T) {
	s := newServer(t)
	owner := s.addUser(t, middleware.RoleSuperAdmin, nil)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	employee := s.addUser(t, middleware.RoleEmployee, nil)

	// Segregation of duties: whoever approves corrections cannot hand out roles.
	_, err := s.svc.ChangeRole(t.Context(), claimsOf(admin), employee.ID, middleware.RoleSupervisor)
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("hr_admin granting a role: err = %v, want ErrForbidden", err)
	}

	promoted, err := s.svc.ChangeRole(t.Context(), claimsOf(owner), employee.ID, middleware.RoleSupervisor)
	if err != nil || promoted.Role != string(middleware.RoleSupervisor) {
		t.Fatalf("ChangeRole = %+v, %v; want a supervisor", promoted, err)
	}

	logs, err := s.repo.auditLogs(t.Context(), entityUser, employee.ID)
	if err != nil {
		t.Fatalf("auditLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("%d audit rows, want exactly 1", len(logs))
	}
	if logs[0].Action != actionRoleChanged || logs[0].ActorID != owner.ID {
		t.Errorf("audit = %+v, want %q by %d", logs[0], actionRoleChanged, owner.ID)
	}
	if string(logs[0].Before) != `{"role": "employee"}` || string(logs[0].After) != `{"role": "supervisor"}` {
		t.Errorf("audit before/after = %s / %s", logs[0].Before, logs[0].After)
	}

	// Granting the role they already hold changes nothing, so it is not worth a row.
	if _, err := s.svc.ChangeRole(t.Context(), claimsOf(owner), employee.ID, middleware.RoleSupervisor); err != nil {
		t.Fatalf("ChangeRole again: %v", err)
	}
	if logs, _ := s.repo.auditLogs(t.Context(), entityUser, employee.ID); len(logs) != 1 {
		t.Errorf("%d audit rows after granting the same role, want 1", len(logs))
	}
}

func TestChangeRoleRefusesUnknownRolesAndThemselves(t *testing.T) {
	s := newServer(t)
	owner := s.addUser(t, middleware.RoleSuperAdmin, nil)
	employee := s.addUser(t, middleware.RoleEmployee, nil)

	if _, err := s.svc.ChangeRole(t.Context(), claimsOf(owner), employee.ID, "owner"); !isValidationError(err) {
		t.Errorf("an unknown role: err = %v, want a ValidationError", err)
	}
	_, err := s.svc.ChangeRole(t.Context(), claimsOf(owner), owner.ID, middleware.RoleEmployee)
	if !isValidationError(err) {
		t.Errorf("demoting themselves: err = %v, want a ValidationError", err)
	}
	_, err = s.svc.ChangeRole(t.Context(), claimsOf(owner), 0, middleware.RoleEmployee)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing user: err = %v, want ErrNotFound", err)
	}
}

func TestDepartmentsNeedTheRightRole(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	supervisor := s.addUser(t, middleware.RoleSupervisor, nil)
	employee := s.addUser(t, middleware.RoleEmployee, nil)
	name := fmt.Sprintf("Security %d", time.Now().UnixNano())

	if _, err := s.svc.CreateDepartment(t.Context(), claimsOf(supervisor), name); !errors.Is(err, ErrForbidden) {
		t.Errorf("a supervisor adding a department: err = %v, want ErrForbidden", err)
	}
	created, err := s.svc.CreateDepartment(t.Context(), claimsOf(admin), "  "+name+" ")
	if err != nil || created.Name != name {
		t.Fatalf("CreateDepartment = %+v, %v; want the name trimmed", created, err)
	}
	s.departmentIDs = append(s.departmentIDs, created.ID)

	if _, err := s.svc.ListDepartments(t.Context(), claimsOf(employee)); !errors.Is(err, ErrForbidden) {
		t.Errorf("an employee listing departments: err = %v, want ErrForbidden", err)
	}
	if _, err := s.svc.ListDepartments(t.Context(), claimsOf(supervisor)); err != nil {
		t.Errorf("a supervisor listing departments: err = %v, want none", err)
	}

	_, err = s.svc.RenameDepartment(t.Context(), claimsOf(supervisor), created.ID, "Nope")
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("a supervisor renaming: err = %v, want ErrForbidden", err)
	}
	if _, err := s.svc.RenameDepartment(t.Context(), claimsOf(admin), created.ID, " "); !isValidationError(err) {
		t.Errorf("an empty name: err = %v, want a ValidationError", err)
	}
	if err := s.svc.DeleteDepartment(t.Context(), claimsOf(supervisor), created.ID); !errors.Is(err, ErrForbidden) {
		t.Errorf("a supervisor deleting: err = %v, want ErrForbidden", err)
	}
	if err := s.svc.DeleteDepartment(t.Context(), claimsOf(admin), created.ID); err != nil {
		t.Errorf("DeleteDepartment: %v", err)
	}
}

func TestMeReturnsTheCaller(t *testing.T) {
	s := newServer(t)
	employee := s.addUser(t, middleware.RoleEmployee, nil)

	me, err := s.svc.Me(t.Context(), claimsOf(employee))
	if err != nil || me.ID != employee.ID {
		t.Errorf("Me = %+v, %v; want %d", me, err, employee.ID)
	}
}

func TestCreateRefusesAPasswordBcryptCannotHash(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)

	_, err := s.svc.Create(t.Context(), claimsOf(admin), NewUser{
		Email:    fmt.Sprintf("long-%d@test.local", time.Now().UnixNano()),
		Password: strings.Repeat("x", 73), // bcrypt stops at 72 bytes
		FullName: "Budi",
	})
	if !isValidationError(err) {
		t.Errorf("err = %v, want a ValidationError, so the client gets 400 and not 500", err)
	}
}

func TestCreateDepartmentNeedsAName(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)

	if _, err := s.svc.CreateDepartment(t.Context(), claimsOf(admin), "   "); !isValidationError(err) {
		t.Errorf("err = %v, want a ValidationError", err)
	}
}

func TestSubtreeLookupFailureSurfaces(t *testing.T) {
	s := newServer(t)
	lead := s.addUser(t, middleware.RoleSupervisor, nil)
	other := s.addUser(t, middleware.RoleEmployee, nil)
	svc := &Service{repo: NewRepository(failingDB(t, "users"))}

	_, err := svc.Get(t.Context(), claimsOf(lead), other.ID)
	if !errors.Is(err, errInjected) {
		t.Errorf("err = %v, want the database failure, not a quiet 403", err)
	}
}
