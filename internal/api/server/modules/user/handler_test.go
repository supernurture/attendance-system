package user

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/oapi-codegen/runtime/types"

	usercontract "attendance-system/internal/api/server/oapicodegen/user"
	"attendance-system/internal/middleware"
)

func TestGetMeAnswers200AndThen404(t *testing.T) {
	s := newServer(t)
	employee := s.addUser(t, middleware.RoleEmployee, nil)

	me := decode[usercontract.User](t, s.do(t, http.MethodGet, "/me", employee, nil), http.StatusOK)
	if me.Id != employee.ID || me.Role != usercontract.Employee {
		t.Errorf("GET /me = %+v, want %d as an employee", me, employee.ID)
	}

	// A token outlives the account by up to its 15 minutes, so the route has to answer something.
	if err := s.repo.SoftDelete(t.Context(), employee.ID, nil); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if rec := s.do(t, http.MethodGet, "/me", employee, nil); rec.Code != http.StatusNotFound {
		t.Errorf("GET /me after the account went: status = %d, want 404", rec.Code)
	}
}

func TestListUsersIsForbiddenForAnEmployee(t *testing.T) {
	s := newServer(t)
	employee := s.addUser(t, middleware.RoleEmployee, nil)

	if rec := s.do(t, http.MethodGet, "/users", employee, nil); rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403; body = %s", rec.Code, rec.Body)
	}
}

func TestListUsersGivesASupervisorTheirSubtree(t *testing.T) {
	s := newServer(t)
	lead, mid, junior, stranger := s.hierarchy(t)

	listed := decode[[]usercontract.User](t, s.do(t, http.MethodGet, "/users", lead, nil), http.StatusOK)
	got := names(listed)
	for _, want := range []string{mid.FullName, junior.FullName} {
		if !slices.Contains(got, want) {
			t.Errorf("%v is missing %q", got, want)
		}
	}
	for _, unwanted := range []string{lead.FullName, stranger.FullName} {
		if slices.Contains(got, unwanted) {
			t.Errorf("%v must not contain %q", got, unwanted)
		}
	}
}

func TestCreateUserAnswers201ThenConflictAndBadRequest(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	body := usercontract.CreateUserRequest{
		Email:    unique("new") + "@test.local",
		Password: "eight888",
		FullName: "Budi Santoso",
	}

	created := decode[usercontract.User](t, s.do(t, http.MethodPost, "/users", admin, body), http.StatusCreated)
	s.track(User{ID: created.Id})
	if created.Role != usercontract.Employee {
		t.Errorf("created = %+v, want an employee", created)
	}

	if rec := s.do(t, http.MethodPost, "/users", admin, body); rec.Code != http.StatusConflict {
		t.Errorf("the same email twice: status = %d, want 409", rec.Code)
	}

	body.Email = unique("other") + "@test.local"
	body.Password = "short"
	if rec := s.do(t, http.MethodPost, "/users", admin, body); rec.Code != http.StatusBadRequest {
		t.Errorf("a short password: status = %d, want 400", rec.Code)
	}
}

func TestGetUserAnswers403And404(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	employee := s.addUser(t, middleware.RoleEmployee, nil)
	stranger := s.addUser(t, middleware.RoleEmployee, nil)

	path := fmt.Sprintf("/users/%d", stranger.ID)
	if rec := s.do(t, http.MethodGet, path, employee, nil); rec.Code != http.StatusForbidden {
		t.Errorf("an employee reading someone else: status = %d, want 403", rec.Code)
	}
	if rec := s.do(t, http.MethodGet, "/users/0", admin, nil); rec.Code != http.StatusNotFound {
		t.Errorf("a missing user: status = %d, want 404", rec.Code)
	}
	if rec := s.do(t, http.MethodGet, path, admin, nil); rec.Code != http.StatusOK {
		t.Errorf("hr_admin reading anyone: status = %d, want 200", rec.Code)
	}
}

func TestUpdateAndDeleteUser(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	employee := s.addUser(t, middleware.RoleEmployee, nil)
	path := fmt.Sprintf("/users/%d", employee.ID)
	body := usercontract.UpdateUserRequest{
		FullName: "Renamed", IsActive: true, JoinDate: types.Date{Time: time.Now()},
	}

	updated := decode[usercontract.User](t, s.do(t, http.MethodPut, path, admin, body), http.StatusOK)
	if updated.FullName != "Renamed" {
		t.Errorf("updated = %+v, want the new name", updated)
	}
	if rec := s.do(t, http.MethodPut, path, employee, body); rec.Code != http.StatusForbidden {
		t.Errorf("an employee editing: status = %d, want 403", rec.Code)
	}
	if rec := s.do(t, http.MethodPut, "/users/0", admin, body); rec.Code != http.StatusNotFound {
		t.Errorf("a missing user: status = %d, want 404", rec.Code)
	}

	own := fmt.Sprintf("/users/%d", admin.ID)
	if rec := s.do(t, http.MethodDelete, own, admin, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("an admin deleting themselves: status = %d, want 400", rec.Code)
	}
	if rec := s.do(t, http.MethodDelete, path, admin, nil); rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	if rec := s.do(t, http.MethodDelete, path, admin, nil); rec.Code != http.StatusNotFound {
		t.Errorf("deleting twice: status = %d, want 404", rec.Code)
	}
}

func TestUpdateUserRoleIsSuperAdminOnly(t *testing.T) {
	s := newServer(t)
	owner := s.addUser(t, middleware.RoleSuperAdmin, nil)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	employee := s.addUser(t, middleware.RoleEmployee, nil)
	path := fmt.Sprintf("/users/%d/role", employee.ID)
	body := usercontract.UpdateRoleRequest{Role: usercontract.Supervisor}

	if rec := s.do(t, http.MethodPatch, path, admin, body); rec.Code != http.StatusForbidden {
		t.Errorf("hr_admin granting a role: status = %d, want 403", rec.Code)
	}
	granted := decode[usercontract.User](t, s.do(t, http.MethodPatch, path, owner, body), http.StatusOK)
	if granted.Role != usercontract.Supervisor {
		t.Errorf("granted = %+v, want a supervisor", granted)
	}

	unknown := usercontract.UpdateRoleRequest{Role: "owner"}
	if rec := s.do(t, http.MethodPatch, path, owner, unknown); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown role: status = %d, want 400", rec.Code)
	}
	if rec := s.do(t, http.MethodPatch, "/users/0/role", owner, body); rec.Code != http.StatusNotFound {
		t.Errorf("a missing user: status = %d, want 404", rec.Code)
	}
}

func TestDepartmentEndpoints(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	employee := s.addUser(t, middleware.RoleEmployee, nil)
	body := usercontract.DepartmentRequest{Name: unique("Engineering")}

	if rec := s.do(t, http.MethodPost, "/departments", employee, body); rec.Code != http.StatusForbidden {
		t.Errorf("an employee adding a department: status = %d, want 403", rec.Code)
	}
	created := decode[usercontract.Department](t,
		s.do(t, http.MethodPost, "/departments", admin, body), http.StatusCreated)
	s.departmentIDs = append(s.departmentIDs, created.Id)

	if rec := s.do(t, http.MethodPost, "/departments", admin, body); rec.Code != http.StatusConflict {
		t.Errorf("the same name twice: status = %d, want 409", rec.Code)
	}
	if rec := s.do(t, http.MethodGet, "/departments", employee, nil); rec.Code != http.StatusForbidden {
		t.Errorf("an employee listing departments: status = %d, want 403", rec.Code)
	}
	if rec := s.do(t, http.MethodGet, "/departments", admin, nil); rec.Code != http.StatusOK {
		t.Errorf("hr_admin listing departments: status = %d, want 200", rec.Code)
	}

	path := fmt.Sprintf("/departments/%d", created.Id)
	renamed := decode[usercontract.Department](t,
		s.do(t, http.MethodPut, path, admin, usercontract.DepartmentRequest{Name: body.Name + " Platform"}),
		http.StatusOK)
	if renamed.Name != body.Name+" Platform" {
		t.Errorf("renamed = %+v, want the new name", renamed)
	}
	if rec := s.do(t, http.MethodPut, "/departments/0", admin, body); rec.Code != http.StatusNotFound {
		t.Errorf("a missing department: status = %d, want 404", rec.Code)
	}
	if rec := s.do(t, http.MethodDelete, path, admin, nil); rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	if rec := s.do(t, http.MethodDelete, path, admin, nil); rec.Code != http.StatusNotFound {
		t.Errorf("deleting twice: status = %d, want 404", rec.Code)
	}
}

func TestRoutesNeedAToken(t *testing.T) {
	s := newServer(t)

	for _, path := range []string{"/me", "/users", "/departments"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a token: status = %d, want 401", path, rec.Code)
		}
	}
}

func TestEveryRouteReports500WhenTheDatabaseFails(t *testing.T) {
	s := newServer(t)
	owner := s.addUser(t, middleware.RoleSuperAdmin, nil)
	router := s.failingRouter(t)
	date := types.Date{Time: time.Now()}

	calls := map[string]struct {
		method, path string
		body         any
	}{
		"GET /me":    {http.MethodGet, "/me", nil},
		"GET /users": {http.MethodGet, "/users", nil},
		"POST /users": {http.MethodPost, "/users", usercontract.CreateUserRequest{
			Email: "a@test.local", Password: "eight888", FullName: "A", JoinDate: &date,
		}},
		"GET /users/{id}": {http.MethodGet, "/users/1", nil},
		"PUT /users/{id}": {http.MethodPut, "/users/1", usercontract.UpdateUserRequest{
			FullName: "A", IsActive: true, JoinDate: date,
		}},
		"DELETE /users/{id}": {http.MethodDelete, "/users/1", nil},
		"PATCH /users/{id}/role": {http.MethodPatch, "/users/1/role",
			usercontract.UpdateRoleRequest{Role: usercontract.Supervisor}},
		"GET /departments":         {http.MethodGet, "/departments", nil},
		"POST /departments":        {http.MethodPost, "/departments", usercontract.DepartmentRequest{Name: "Ops"}},
		"PUT /departments/{id}":    {http.MethodPut, "/departments/1", usercontract.DepartmentRequest{Name: "Ops"}},
		"DELETE /departments/{id}": {http.MethodDelete, "/departments/1", nil},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			rec := s.doOn(t, router, call.method, call.path, owner, call.body)
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500; body = %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestRoutesRefuseAnEmployeeAndABadBody(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	employee := s.addUser(t, middleware.RoleEmployee, nil)
	department := s.addDepartment(t, unique("Finance"))
	other := s.addDepartment(t, unique("Legal"))
	path := fmt.Sprintf("/departments/%d", department.ID)
	date := types.Date{Time: time.Now()}

	forbidden := map[string]struct {
		method, path string
		body         any
	}{
		"POST /users": {http.MethodPost, "/users", usercontract.CreateUserRequest{
			Email: "x@test.local", Password: "eight888", FullName: "X",
		}},
		"PUT /departments/{id}":    {http.MethodPut, path, usercontract.DepartmentRequest{Name: "Nope"}},
		"DELETE /departments/{id}": {http.MethodDelete, path, nil},
	}
	for name, call := range forbidden {
		t.Run(name, func(t *testing.T) {
			if rec := s.do(t, call.method, call.path, employee, call.body); rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", rec.Code)
			}
		})
	}

	empty := usercontract.DepartmentRequest{Name: "  "}
	if rec := s.do(t, http.MethodPost, "/departments", admin, empty); rec.Code != http.StatusBadRequest {
		t.Errorf("POST /departments with no name: status = %d, want 400", rec.Code)
	}
	if rec := s.do(t, http.MethodPut, path, admin, empty); rec.Code != http.StatusBadRequest {
		t.Errorf("PUT /departments with no name: status = %d, want 400", rec.Code)
	}
	taken := usercontract.DepartmentRequest{Name: other.Name}
	if rec := s.do(t, http.MethodPut, path, admin, taken); rec.Code != http.StatusConflict {
		t.Errorf("renaming onto a taken name: status = %d, want 409", rec.Code)
	}

	noName := usercontract.UpdateUserRequest{FullName: " ", IsActive: true, JoinDate: date}
	userPath := fmt.Sprintf("/users/%d", employee.ID)
	if rec := s.do(t, http.MethodPut, userPath, admin, noName); rec.Code != http.StatusBadRequest {
		t.Errorf("PUT /users with no name: status = %d, want 400", rec.Code)
	}
}

func TestHandlerRefusesAnUnguardedRoute(t *testing.T) {
	h := NewHandler(NewService(nil, time.UTC))
	ctx := context.Background() // no claims: the route was mounted without middleware.Auth

	calls := map[string]func() error{
		"GetMe":      func() error { _, err := h.GetMe(ctx, usercontract.GetMeRequestObject{}); return err },
		"ListUsers":  func() error { _, err := h.ListUsers(ctx, usercontract.ListUsersRequestObject{}); return err },
		"CreateUser": func() error { _, err := h.CreateUser(ctx, usercontract.CreateUserRequestObject{}); return err },
		"GetUser":    func() error { _, err := h.GetUser(ctx, usercontract.GetUserRequestObject{}); return err },
		"UpdateUser": func() error { _, err := h.UpdateUser(ctx, usercontract.UpdateUserRequestObject{}); return err },
		"DeleteUser": func() error {
			_, err := h.DeleteUser(ctx, usercontract.DeleteUserRequestObject{})
			return err
		},
		"UpdateUserRole": func() error {
			_, err := h.UpdateUserRole(ctx, usercontract.UpdateUserRoleRequestObject{})
			return err
		},
		"ListDepartments": func() error {
			_, err := h.ListDepartments(ctx, usercontract.ListDepartmentsRequestObject{})
			return err
		},
		"CreateDepartment": func() error {
			_, err := h.CreateDepartment(ctx, usercontract.CreateDepartmentRequestObject{})
			return err
		},
		"UpdateDepartment": func() error {
			_, err := h.UpdateDepartment(ctx, usercontract.UpdateDepartmentRequestObject{})
			return err
		},
		"DeleteDepartment": func() error {
			_, err := h.DeleteDepartment(ctx, usercontract.DeleteDepartmentRequestObject{})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Error("the handler answered without claims; want an error")
			}
		})
	}
}

func TestListUsersTakesLimitAndOffset(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	s.addUser(t, middleware.RoleEmployee, nil)

	page := decode[[]usercontract.User](t, s.do(t, http.MethodGet, "/users?limit=1", admin, nil), http.StatusOK)
	if len(page) != 1 {
		t.Errorf("limit=1 returned %d rows, want 1", len(page))
	}
	next := decode[[]usercontract.User](t,
		s.do(t, http.MethodGet, "/users?limit=1&offset=1", admin, nil), http.StatusOK)
	if len(next) != 1 || next[0].Id == page[0].Id {
		t.Errorf("offset=1 returned %+v, want a different row", next)
	}
	if rec := s.do(t, http.MethodGet, "/users?limit=501", admin, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("limit=501: status = %d, want 400", rec.Code)
	}
}

func TestDeleteUserAnswers403ForAHigherRole(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)
	owner := s.addUser(t, middleware.RoleSuperAdmin, nil)

	path := fmt.Sprintf("/users/%d", owner.ID)
	if rec := s.do(t, http.MethodDelete, path, admin, nil); rec.Code != http.StatusForbidden {
		t.Errorf("hr_admin deleting a super_admin: status = %d, want 403", rec.Code)
	}
}

func TestListUsersRefusesAnExplicitZeroLimit(t *testing.T) {
	s := newServer(t)
	admin := s.addUser(t, middleware.RoleHRAdmin, nil)

	// Absent means the default; sending zero is a mistake worth reporting.
	if rec := s.do(t, http.MethodGet, "/users?limit=0", admin, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("limit=0: status = %d, want 400", rec.Code)
	}
	if rec := s.do(t, http.MethodGet, "/users", admin, nil); rec.Code != http.StatusOK {
		t.Errorf("no limit: status = %d, want 200", rec.Code)
	}
}
