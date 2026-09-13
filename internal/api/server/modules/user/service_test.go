package user

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"attendance-system/internal/middleware"
	"attendance-system/internal/pkg/apperr"
)

func TestListFollowsTheHierarchy(t *testing.T) {
	s := newServer(t)
	lead, mid, junior, stranger := s.hierarchy(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)

	_, err := s.svc.List(t.Context(), claimsOf(junior), Page{Limit: defaultPageLimit})
	if !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("an employee listing everyone: err = %v, want apperr.ErrForbidden", err)
	}

	below, err := s.svc.List(t.Context(), claimsOf(lead), Page{Limit: defaultPageLimit})
	if err != nil {
		t.Fatalf("List as a supervisor: %v", err)
	}
	if got := ids(below); !slices.Contains(got, mid.ID) || !slices.Contains(got, junior.ID) ||
		slices.Contains(got, stranger.ID) || slices.Contains(got, lead.ID) {
		t.Errorf("a supervisor sees %v, want exactly their subtree (%d, %d)", got, mid.ID, junior.ID)
	}

	everyone, err := s.svc.List(t.Context(), claimsOf(admin), Page{Limit: defaultPageLimit})
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
		"someone else":           {junior, stranger, apperr.ErrForbidden},
		"their own subtree":      {lead, junior, nil},
		"outside their subtree":  {lead, stranger, apperr.ErrForbidden},
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
		Email:    " " + strings.ToUpper(unique("New")) + "@Example.com ",
		Password: "eight888",
		FullName: "  Budi Santoso ",
	}
	if _, err := s.svc.Create(t.Context(), claimsOf(supervisor), next); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("a supervisor adding an employee: err = %v, want apperr.ErrForbidden", err)
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
	if today := time.Now().UTC(); created.JoinDate.Format(time.DateOnly) != today.Format(time.DateOnly) {
		t.Errorf("join_date = %v, want today, %s, by default", created.JoinDate, today.Format(time.DateOnly))
	}
}

// UTC+14 and UTC-11 are 25 hours apart, so a join dated by the server's zone would match at most one of them.
func TestCreateDatesTheJoinInTheCompanyZone(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)

	for _, name := range []string{"Pacific/Kiritimati", "Pacific/Pago_Pago"} {
		zone, _ := time.LoadLocation(name)
		created, err := NewService(s.db, zone).Create(t.Context(), claimsOf(admin), NewUser{
			Email: unique("joiner") + "@test.local", Password: "eight888", FullName: "Joiner",
		})
		if err != nil {
			t.Fatalf("%s: Create: %v", name, err)
		}
		s.track(created)
		if want := time.Now().In(zone).Format(time.DateOnly); created.JoinDate.Format(time.DateOnly) != want {
			t.Errorf("%s: join_date = %v, want %s", name, created.JoinDate, want)
		}
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
			Email:    unique("m") + "@test.local",
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
	department := s.addDepartment(t, unique("Ops"))

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

	logs := s.auditLogs(t, employee.ID)
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
	if logs := s.auditLogs(t, employee.ID); len(logs) != 1 {
		t.Errorf("%d audit rows after reactivating, want the original 1", len(logs))
	}
}

func TestReplaceChecksRoleAndInput(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	employee := s.addUser(t, middleware.RoleEmployee, nil)
	details := Details{FullName: "Renamed", IsActive: true}

	_, err := s.svc.Replace(t.Context(), claimsOf(employee), employee.ID, details)
	if !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("an employee editing themselves: err = %v, want apperr.ErrForbidden", err)
	}
	if _, err := s.svc.Replace(t.Context(), claimsOf(admin), 0, details); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("a missing user: err = %v, want apperr.ErrNotFound", err)
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

	if err := s.svc.Delete(t.Context(), claimsOf(employee), employee.ID); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("an employee deleting themselves: err = %v, want apperr.ErrForbidden", err)
	}
	if err := s.svc.Delete(t.Context(), claimsOf(admin), admin.ID); !isValidationError(err) {
		t.Errorf("an admin deleting their own account: err = %v, want a ValidationError", err)
	}
	if err := s.svc.Delete(t.Context(), claimsOf(admin), employee.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.svc.Get(t.Context(), claimsOf(admin), employee.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("after the delete: err = %v, want apperr.ErrNotFound", err)
	}
}

func TestChangeRoleIsForSuperAdminAndIsAudited(t *testing.T) {
	s := newServer(t)
	owner := s.addUser(t, middleware.RoleSuperAdmin, nil)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	employee := s.addUser(t, middleware.RoleEmployee, nil)

	// Segregation of duties: whoever approves corrections cannot hand out roles.
	_, err := s.svc.ChangeRole(t.Context(), claimsOf(admin), employee.ID, middleware.RoleSupervisor)
	if !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("hr_admin granting a role: err = %v, want apperr.ErrForbidden", err)
	}

	promoted, err := s.svc.ChangeRole(t.Context(), claimsOf(owner), employee.ID, middleware.RoleSupervisor)
	if err != nil || promoted.Role != string(middleware.RoleSupervisor) {
		t.Fatalf("ChangeRole = %+v, %v; want a supervisor", promoted, err)
	}

	logs := s.auditLogs(t, employee.ID)
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
	if logs := s.auditLogs(t, employee.ID); len(logs) != 1 {
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
	if !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("a missing user: err = %v, want apperr.ErrNotFound", err)
	}
}

func TestDepartmentsNeedTheRightRole(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	supervisor := s.addUser(t, middleware.RoleSupervisor, nil)
	employee := s.addUser(t, middleware.RoleEmployee, nil)
	name := unique("Security")

	if _, err := s.svc.CreateDepartment(t.Context(), claimsOf(supervisor), name); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("a supervisor adding a department: err = %v, want apperr.ErrForbidden", err)
	}
	created, err := s.svc.CreateDepartment(t.Context(), claimsOf(admin), "  "+name+" ")
	if err != nil || created.Name != name {
		t.Fatalf("CreateDepartment = %+v, %v; want the name trimmed", created, err)
	}
	s.departmentIDs = append(s.departmentIDs, created.ID)

	if _, err := s.svc.ListDepartments(t.Context(), claimsOf(employee)); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("an employee listing departments: err = %v, want apperr.ErrForbidden", err)
	}
	if _, err := s.svc.ListDepartments(t.Context(), claimsOf(supervisor)); err != nil {
		t.Errorf("a supervisor listing departments: err = %v, want none", err)
	}

	_, err = s.svc.RenameDepartment(t.Context(), claimsOf(supervisor), created.ID, "Nope")
	if !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("a supervisor renaming: err = %v, want apperr.ErrForbidden", err)
	}
	if _, err := s.svc.RenameDepartment(t.Context(), claimsOf(admin), created.ID, " "); !isValidationError(err) {
		t.Errorf("an empty name: err = %v, want a ValidationError", err)
	}
	err = s.svc.DeleteDepartment(t.Context(), claimsOf(supervisor), created.ID)
	if !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("a supervisor deleting: err = %v, want apperr.ErrForbidden", err)
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
		Email:    unique("long") + "@test.local",
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
	svc := &Service{repo: NewRepository(failingDB(t, "users")), zone: time.UTC}

	_, err := svc.Get(t.Context(), claimsOf(lead), other.ID)
	if !errors.Is(err, errInjected) {
		t.Errorf("err = %v, want the database failure, not a quiet 403", err)
	}
}

func TestHRAdminCannotTouchASuperAdmin(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	peer := s.addUser(t, middleware.RoleHRAdmin, nil)
	owner := s.addUser(t, middleware.RoleSuperAdmin, nil)
	details := Details{FullName: "Renamed", IsActive: false}

	// Only super_admin grants roles, so letting hr_admin remove them would lock the system.
	if _, err := s.svc.Replace(t.Context(), claimsOf(admin), owner.ID, details); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("hr_admin deactivating a super_admin: err = %v, want apperr.ErrForbidden", err)
	}
	if err := s.svc.Delete(t.Context(), claimsOf(admin), owner.ID); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("hr_admin deleting a super_admin: err = %v, want apperr.ErrForbidden", err)
	}

	// Same rank is still ordinary admin work, and a super_admin reaches everyone.
	if _, err := s.svc.Replace(t.Context(), claimsOf(admin), peer.ID, details); err != nil {
		t.Errorf("hr_admin editing another hr_admin: err = %v, want none", err)
	}
	if err := s.svc.Delete(t.Context(), claimsOf(owner), admin.ID); err != nil {
		t.Errorf("super_admin deleting an hr_admin: err = %v, want none", err)
	}
}

func TestReplaceRefusesAManagerFromTheirOwnSubtree(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	lead, _, junior, stranger := s.hierarchy(t)

	loop := Details{FullName: lead.FullName, IsActive: true, ManagerID: &junior.ID}
	if _, err := s.svc.Replace(t.Context(), claimsOf(admin), lead.ID, loop); !isValidationError(err) {
		t.Errorf("a manager from their own subtree: err = %v, want a ValidationError", err)
	}

	fine := Details{FullName: lead.FullName, IsActive: true, ManagerID: &stranger.ID}
	if _, err := s.svc.Replace(t.Context(), claimsOf(admin), lead.ID, fine); err != nil {
		t.Errorf("a manager from outside: err = %v, want none", err)
	}
	inOwn, err := s.repo.InSubtree(t.Context(), lead.ID, lead.ID)
	if err != nil || inOwn {
		t.Errorf("lead is inside their own subtree = %v, %v; want false", inOwn, err)
	}
}

func TestARemovedDepartmentCannotBeAssigned(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	employee := s.addUser(t, middleware.RoleEmployee, nil)
	department := s.addDepartment(t, unique("Gone"))
	if err := s.svc.DeleteDepartment(t.Context(), claimsOf(admin), department.ID); err != nil {
		t.Fatalf("DeleteDepartment: %v", err)
	}

	_, err := s.svc.Create(t.Context(), claimsOf(admin), NewUser{
		Email: unique("dept") + "@test.local", Password: "eight888",
		FullName: "Assigned", DepartmentID: &department.ID,
	})
	if !isValidationError(err) {
		t.Errorf("Create into a removed department: err = %v, want a ValidationError", err)
	}

	details := Details{FullName: "Renamed", IsActive: true, DepartmentID: &department.ID}
	if _, err := s.svc.Replace(t.Context(), claimsOf(admin), employee.ID, details); !isValidationError(err) {
		t.Errorf("Replace into a removed department: err = %v, want a ValidationError", err)
	}
}

func TestDeleteIsAudited(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	employee := s.addUser(t, middleware.RoleEmployee, nil)

	if err := s.svc.Delete(t.Context(), claimsOf(admin), employee.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	logs := s.auditLogs(t, employee.ID)
	if len(logs) != 1 || logs[0].Action != actionDeleted || logs[0].ActorID != admin.ID {
		t.Errorf("audit rows = %+v, want one %q by %d: deleted_at says when, not who",
			logs, actionDeleted, admin.ID)
	}
}

func TestListPagesThroughTheDirectory(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	s.addUser(t, middleware.RoleEmployee, nil)

	first, err := s.svc.List(t.Context(), claimsOf(admin), Page{Limit: 1})
	if err != nil || len(first) != 1 {
		t.Fatalf("List(limit 1) = %d rows, %v; want 1", len(first), err)
	}
	second, err := s.svc.List(t.Context(), claimsOf(admin), Page{Limit: 1, Offset: 1})
	if err != nil || len(second) != 1 {
		t.Fatalf("List(offset 1) = %d rows, %v; want 1", len(second), err)
	}
	if first[0].ID == second[0].ID {
		t.Error("the second page repeats the first row")
	}
	if _, err := s.svc.List(t.Context(), claimsOf(admin), Page{Limit: maxPageLimit + 1}); !isValidationError(err) {
		t.Errorf("over the cap: err = %v, want a ValidationError", err)
	}
}

func TestTheChecksReportDatabaseFailures(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	employee := s.addUser(t, middleware.RoleEmployee, nil)
	department := s.addDepartment(t, unique("Faulty"))

	departments := &Service{repo: NewRepository(failingDB(t, "departments")), zone: time.UTC}
	_, err := departments.Create(t.Context(), claimsOf(admin), NewUser{
		Email: "dept@test.local", Password: "eight888", FullName: "Budi", DepartmentID: &department.ID,
	})
	if !errors.Is(err, errInjected) {
		t.Errorf("department lookup: err = %v, want the database failure", err)
	}

	subtree := &Service{repo: NewRepository(failingAfterDB(t, "subordinates")), zone: time.UTC}
	_, err = subtree.Replace(t.Context(), claimsOf(admin), employee.ID, Details{
		FullName: "Renamed", IsActive: true, ManagerID: &admin.ID,
	})
	if !errors.Is(err, errInjected) {
		t.Errorf("subtree lookup: err = %v, want the database failure", err)
	}

	lookups := &Service{repo: NewRepository(failingAfterDB(t, "count(")), zone: time.UTC}
	_, err = lookups.Replace(t.Context(), claimsOf(admin), employee.ID, Details{
		FullName: "Renamed", IsActive: true, ManagerID: &admin.ID,
	})
	if !errors.Is(err, errInjected) {
		t.Errorf("manager lookup: err = %v, want the database failure", err)
	}
}

func TestNobodyDeactivatesTheirOwnAccount(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	owner := s.addUser(t, middleware.RoleSuperAdmin, nil)
	off := Details{FullName: "Self", IsActive: false, JoinDate: time.Now()}

	// The last super_admin locking themselves out would leave nobody able to grant roles.
	for name, as := range map[string]User{"hr_admin": admin, "super_admin": owner} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.svc.Replace(t.Context(), claimsOf(as), as.ID, off); !isValidationError(err) {
				t.Errorf("err = %v, want a ValidationError", err)
			}
		})
	}

	stillOn := Details{FullName: "Self", IsActive: true, JoinDate: time.Now()}
	if _, err := s.svc.Replace(t.Context(), claimsOf(admin), admin.ID, stillOn); err != nil {
		t.Errorf("editing their own details: err = %v, want none", err)
	}
	if _, err := s.svc.Replace(t.Context(), claimsOf(owner), admin.ID, off); err != nil {
		t.Errorf("deactivating someone else: err = %v, want none", err)
	}
}

func TestARemovedManagerCannotBeAssigned(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	employee := s.addUser(t, middleware.RoleEmployee, nil)
	gone := s.addUser(t, middleware.RoleSupervisor, nil)
	if err := s.svc.Delete(t.Context(), claimsOf(admin), gone.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Nobody can log in as a removed manager, so their reports would be out of everyone's reach.
	details := Details{FullName: "Orphan", IsActive: true, JoinDate: time.Now(), ManagerID: &gone.ID}
	if _, err := s.svc.Replace(t.Context(), claimsOf(admin), employee.ID, details); !isValidationError(err) {
		t.Errorf("Replace under a removed manager: err = %v, want a ValidationError", err)
	}

	_, err := s.svc.Create(t.Context(), claimsOf(admin), NewUser{
		Email: unique("mgr") + "@test.local", Password: "eight888",
		FullName: "New Hire", ManagerID: &gone.ID,
	})
	if !isValidationError(err) {
		t.Errorf("Create under a removed manager: err = %v, want a ValidationError", err)
	}
}

func TestADefaultScheduleMustBeLive(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	employee := s.addUser(t, middleware.RoleEmployee, nil)
	schedule := s.addSchedule(t)

	created, err := s.svc.Create(t.Context(), claimsOf(admin), NewUser{
		Email: unique("sched") + "@test.local", Password: "eight888",
		FullName: "Budi", DefaultScheduleID: &schedule.ID,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	s.track(created)
	if created.DefaultScheduleID == nil || *created.DefaultScheduleID != schedule.ID {
		t.Errorf("created = %+v, want the schedule kept", created)
	}

	updated, err := s.svc.Replace(t.Context(), claimsOf(admin), employee.ID, Details{
		FullName: "Renamed", IsActive: true, DefaultScheduleID: &schedule.ID,
	})
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if updated.DefaultScheduleID == nil {
		t.Errorf("updated = %+v, want the schedule kept", updated)
	}

	// A retired schedule would leave the employee with hours nobody maintains; the foreign key
	// cannot see deleted_at, so the check has to.
	s.db.Exec("UPDATE work_schedules SET deleted_at = now() WHERE id = ?", schedule.ID)
	_, err = s.svc.Create(t.Context(), claimsOf(admin), NewUser{
		Email: unique("sched") + "@test.local", Password: "eight888",
		FullName: "Budi", DefaultScheduleID: &schedule.ID,
	})
	if !isValidationError(err) {
		t.Errorf("Create onto a retired schedule: err = %v, want a ValidationError", err)
	}
	_, err = s.svc.Replace(t.Context(), claimsOf(admin), employee.ID, Details{
		FullName: "Renamed", IsActive: true, DefaultScheduleID: &schedule.ID,
	})
	if !isValidationError(err) {
		t.Errorf("Replace onto a retired schedule: err = %v, want a ValidationError", err)
	}

	schedules := &Service{repo: NewRepository(failingDB(t, "work_schedules")), zone: time.UTC}
	_, err = schedules.Create(t.Context(), claimsOf(admin), NewUser{
		Email: unique("sched") + "@test.local", Password: "eight888",
		FullName: "Budi", DefaultScheduleID: &schedule.ID,
	})
	if !errors.Is(err, errInjected) {
		t.Errorf("schedule lookup: err = %v, want the database failure", err)
	}
}
