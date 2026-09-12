package user

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"attendance-system/internal/middleware"
	"attendance-system/internal/pkg/apperr"
)

func TestByIDReportsAMiss(t *testing.T) {
	s := newServer(t)

	if _, err := s.repo.ByID(t.Context(), 0); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("err = %v, want apperr.ErrNotFound", err)
	}
}

// hierarchy builds lead -> mid -> junior, plus a stranger nobody manages.
func (s *server) hierarchy(t *testing.T) (lead, mid, junior, stranger User) {
	t.Helper()

	lead = s.addUser(t, middleware.RoleSupervisor, nil)
	mid = s.addUser(t, middleware.RoleSupervisor, &lead.ID)
	junior = s.addUser(t, middleware.RoleEmployee, &mid.ID)
	stranger = s.addUser(t, middleware.RoleEmployee, nil)
	return lead, mid, junior, stranger
}

func TestListSubtreeWalksEveryLevel(t *testing.T) {
	s := newServer(t)
	lead, mid, junior, stranger := s.hierarchy(t)

	below, err := s.repo.ListSubtree(t.Context(), lead.ID, Page{Limit: 100})
	if err != nil {
		t.Fatalf("ListSubtree: %v", err)
	}
	got := ids(below)
	for _, want := range []int64{mid.ID, junior.ID} {
		if !slices.Contains(got, want) {
			t.Errorf("subtree %v is missing %d", got, want)
		}
	}
	for _, unwanted := range []int64{lead.ID, stranger.ID} {
		if slices.Contains(got, unwanted) {
			t.Errorf("subtree %v must not contain %d", got, unwanted)
		}
	}

	oneDown, err := s.repo.ListSubtree(t.Context(), mid.ID, Page{Limit: 100})
	if err != nil {
		t.Fatalf("ListSubtree: %v", err)
	}
	if got := ids(oneDown); len(got) != 1 || got[0] != junior.ID {
		t.Errorf("mid's subtree = %v, want just %d", got, junior.ID)
	}
}

func TestInSubtree(t *testing.T) {
	s := newServer(t)
	lead, _, junior, stranger := s.hierarchy(t)

	for name, test := range map[string]struct {
		userID int64
		want   bool
	}{
		"two levels down": {junior.ID, true},
		"unrelated":       {stranger.ID, false},
		"themselves":      {lead.ID, false},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := s.repo.InSubtree(t.Context(), lead.ID, test.userID)
			if err != nil || got != test.want {
				t.Errorf("InSubtree = %v, %v; want %v", got, err, test.want)
			}
		})
	}
}

func TestSubtreeSurvivesCircularManagers(t *testing.T) {
	s := newServer(t)
	first := s.addUser(t, middleware.RoleSupervisor, nil)
	second := s.addUser(t, middleware.RoleSupervisor, &first.ID)
	// first now reports to second, which reports to first: without the CYCLE clause this never ends.
	s.db.Exec("UPDATE users SET manager_id = ? WHERE id = ?", second.ID, first.ID)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	below, err := s.repo.ListSubtree(ctx, first.ID, Page{Limit: 100})
	if err != nil {
		t.Fatalf("ListSubtree on circular data: %v", err)
	}
	if got := ids(below); !slices.Contains(got, second.ID) {
		t.Errorf("subtree %v is missing %d", got, second.ID)
	}
}

func TestCreateRejectsATakenEmailButReusesADeletedOne(t *testing.T) {
	s := newServer(t)
	taken := s.addUser(t, middleware.RoleEmployee, nil)

	again := &User{Email: taken.Email, PasswordHash: "x", FullName: "Twin", Role: "employee", JoinDate: time.Now()}
	if err := s.repo.Create(t.Context(), again); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("err = %v, want apperr.ErrConflict", err)
	}

	if err := s.repo.SoftDelete(t.Context(), taken.ID, nil); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if err := s.repo.Create(t.Context(), again); err != nil {
		t.Errorf("after the first was removed: err = %v, want the address free again", err)
	}
	s.track(*again)
}

func TestCreateReportsAnUnknownManagerOrDepartment(t *testing.T) {
	s := newServer(t)
	missing := int64(0)

	err := s.repo.Create(t.Context(), &User{
		Email: unique("fk") + "@test.local", PasswordHash: "x",
		FullName: "No Manager", Role: "employee", JoinDate: time.Now(), ManagerID: &missing,
	})
	if !isValidationError(err) {
		t.Errorf("err = %v, want a ValidationError the handler can turn into 400", err)
	}
}

func TestSoftDeleteKeepsTheRowForReports(t *testing.T) {
	s := newServer(t)
	actor := s.addUser(t, middleware.RoleSuperAdmin, nil)
	leaver := s.addUser(t, middleware.RoleEmployee, nil)
	audit := AuditLog{
		ActorID: actor.ID, Action: actionRoleChanged, EntityType: entityUser, EntityID: leaver.ID,
		Before: []byte(`{"role":"employee"}`), After: []byte(`{"role":"supervisor"}`),
	}
	if err := s.db.Create(&audit).Error; err != nil {
		t.Fatalf("create audit row: %v", err)
	}

	if err := s.repo.SoftDelete(t.Context(), leaver.ID, nil); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	if _, err := s.repo.ByID(t.Context(), leaver.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("ByID after the delete: err = %v, want apperr.ErrNotFound", err)
	}
	var stored User
	if err := s.db.Unscoped().Take(&stored, leaver.ID).Error; err != nil || !stored.DeletedAt.Valid {
		t.Errorf("stored = %+v, %v; want the row kept with deleted_at set", stored, err)
	}

	// What a report does: join the history back to the name, deleted or not.
	var name string
	err := s.db.Raw(`SELECT u.full_name FROM audit_logs a JOIN users u ON u.id = a.entity_id WHERE a.id = ?`,
		audit.ID).Scan(&name).Error
	if err != nil || name != leaver.FullName {
		t.Errorf("joined name = %q, %v; want %q", name, err, leaver.FullName)
	}
}

func TestSoftDeleteReportsAMiss(t *testing.T) {
	s := newServer(t)

	if err := s.repo.SoftDelete(t.Context(), 0, nil); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("err = %v, want apperr.ErrNotFound", err)
	}
}

func TestTheDatabaseRefusesAnUnknownRole(t *testing.T) {
	s := newServer(t)
	user := s.addUser(t, middleware.RoleEmployee, nil)

	err := s.db.Exec("UPDATE users SET role = 'owner' WHERE id = ?", user.ID).Error
	if err == nil {
		t.Fatal("the database accepted role 'owner'; the CHECK constraint is the last line of defence")
	}
}

func TestReplaceAndChangeRoleReportAMiss(t *testing.T) {
	s := newServer(t)

	if _, err := s.repo.Replace(t.Context(), User{ID: 0, FullName: "Ghost"}, nil); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("Replace: err = %v, want apperr.ErrNotFound", err)
	}
	if _, err := s.repo.ChangeRole(t.Context(), 0, "employee", AuditLog{}); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("ChangeRole: err = %v, want apperr.ErrNotFound", err)
	}
}

func TestDepartments(t *testing.T) {
	s := newServer(t)
	name := unique("Engineering")

	department := Department{Name: name}
	if err := s.repo.CreateDepartment(t.Context(), &department); err != nil {
		t.Fatalf("CreateDepartment: %v", err)
	}
	s.departmentIDs = append(s.departmentIDs, department.ID)

	// The unique index ignores case, so this is the same name.
	twin := Department{Name: strings.ToUpper(name)}
	if err := s.repo.CreateDepartment(t.Context(), &twin); !errors.Is(err, apperr.ErrConflict) {
		t.Errorf("duplicate name: err = %v, want apperr.ErrConflict", err)
	}

	renamed, err := s.repo.RenameDepartment(t.Context(), department.ID, name+" Platform")
	if err != nil || renamed.Name != name+" Platform" {
		t.Errorf("RenameDepartment = %+v, %v", renamed, err)
	}
	if _, err := s.repo.RenameDepartment(t.Context(), 0, "Ghost"); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("renaming a missing department: err = %v, want apperr.ErrNotFound", err)
	}

	listed, err := s.repo.ListDepartments(t.Context())
	if err != nil || len(listed) == 0 {
		t.Fatalf("ListDepartments = %v, %v", listed, err)
	}

	if err := s.repo.DeleteDepartment(t.Context(), department.ID); err != nil {
		t.Fatalf("DeleteDepartment: %v", err)
	}
	if err := s.repo.DeleteDepartment(t.Context(), 0); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("deleting a missing department: err = %v, want apperr.ErrNotFound", err)
	}
}

func TestTranslateLeavesOtherFailuresAlone(t *testing.T) {
	s := newServer(t)

	// A CHECK violation is neither a taken email nor a missing reference: it stays a 500.
	err := s.repo.Create(t.Context(), &User{
		Email: unique("role") + "@test.local", PasswordHash: "x",
		FullName: "Bad Role", Role: "owner", JoinDate: time.Now(),
	})
	if err == nil || errors.Is(err, apperr.ErrConflict) || isValidationError(err) {
		t.Errorf("err = %v, want the raw database error", err)
	}
}

func TestRepositoryReportsDatabaseFailures(t *testing.T) {
	s := newServer(t)
	employee := s.addUser(t, middleware.RoleEmployee, nil)
	department := s.addDepartment(t, unique("Fault"))
	users := NewRepository(failingDB(t, "users"))
	departments := NewRepository(failingDB(t, "departments"))

	calls := map[string]func() error{
		"SoftDelete": func() error { return users.SoftDelete(t.Context(), employee.ID, nil) },
		"ChangeRole": func() error {
			_, err := users.ChangeRole(t.Context(), employee.ID, "supervisor", AuditLog{})
			return err
		},
		"Replace": func() error {
			_, err := users.Replace(t.Context(), User{ID: employee.ID, FullName: "Renamed"}, nil)
			return err
		},
		"RenameDepartment": func() error {
			_, err := departments.RenameDepartment(t.Context(), department.ID, "Renamed")
			return err
		},
		"DeleteDepartment": func() error { return departments.DeleteDepartment(t.Context(), department.ID) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, errInjected) {
				t.Errorf("err = %v, want the injected failure", err)
			}
		})
	}
}

func TestSoftDeleteMovesTheReportsUp(t *testing.T) {
	s := newServer(t)
	lead, mid, junior, _ := s.hierarchy(t)

	if err := s.repo.SoftDelete(t.Context(), mid.ID, nil); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	moved, err := s.repo.ByID(t.Context(), junior.ID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if moved.ManagerID == nil || *moved.ManagerID != lead.ID {
		t.Errorf("junior's manager = %v, want the removed manager's own manager (%d)", moved.ManagerID, lead.ID)
	}
	below, err := s.repo.ListSubtree(t.Context(), lead.ID, Page{Limit: 100})
	if err != nil {
		t.Fatalf("ListSubtree: %v", err)
	}
	if got := ids(below); !slices.Contains(got, junior.ID) {
		t.Errorf("lead's subtree = %v, want it to still reach %d", got, junior.ID)
	}

	// Removing a root leaves its reports without a manager, which only hr_admin can then see.
	if err := s.repo.SoftDelete(t.Context(), lead.ID, nil); err != nil {
		t.Fatalf("SoftDelete the root: %v", err)
	}
	orphan, err := s.repo.ByID(t.Context(), junior.ID)
	if err != nil || orphan.ManagerID != nil {
		t.Errorf("junior's manager = %v, %v; want none", orphan.ManagerID, err)
	}
}

func TestSoftDeleteReportsAFailedReparent(t *testing.T) {
	s := newServer(t)
	_, mid, _, _ := s.hierarchy(t)
	repo := NewRepository(failingAfterDB(t, "manager_id"))

	if err := repo.SoftDelete(t.Context(), mid.ID, nil); !errors.Is(err, errInjected) {
		t.Errorf("err = %v, want the failed reparent reported, not a half-done delete", err)
	}
	if _, err := s.repo.ByID(t.Context(), mid.ID); err != nil {
		t.Errorf("the user was removed anyway: %v", err)
	}
}

func TestSoftDeleteReportsAFailedDelete(t *testing.T) {
	s := newServer(t)
	employee := s.addUser(t, middleware.RoleEmployee, nil)
	repo := NewRepository(failingAfterDB(t, `SET "deleted_at"`))

	if err := repo.SoftDelete(t.Context(), employee.ID, nil); !errors.Is(err, errInjected) {
		t.Errorf("err = %v, want the failed delete reported", err)
	}
}

func TestPagingWalksNamesakesExactlyOnce(t *testing.T) {
	s := newServer(t)
	const namesakes = 4
	for range namesakes {
		user := s.addUser(t, middleware.RoleEmployee, nil)
		s.db.Model(&User{}).Where("id = ?", user.ID).Update("full_name", "Budi")
	}

	seen := map[int64]int{}
	for offset := range namesakes {
		page, err := s.repo.List(t.Context(), Page{Limit: 1, Offset: offset})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(page) != 1 {
			t.Fatalf("offset %d returned %d rows, want 1", offset, len(page))
		}
		seen[page[0].ID]++
	}
	if len(seen) != namesakes {
		t.Errorf("%d pages over %d rows named alike showed %d distinct users; paging repeats or skips",
			namesakes, namesakes, len(seen))
	}
}
