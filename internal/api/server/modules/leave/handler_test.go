package leave

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/oapi-codegen/runtime/types"

	leavecontract "attendance-system/internal/api/server/oapicodegen/leave"
	"attendance-system/internal/middleware"
)

func leaveBody(code string, from, to time.Time, key *string) leavecontract.LeaveRequest {
	return leavecontract.LeaveRequest{Type: code, StartDate: types.Date{Time: from}, EndDate: types.Date{Time: to},
		Reason: "family matters", AttachmentKey: key}
}

func TestHandlerLeaveFlow(t *testing.T) {
	s := newServer(t)
	lead := s.person(t, middleware.RoleSupervisor, nil)
	employee := s.person(t, middleware.RoleEmployee, &lead.UserID)
	colleague := s.person(t, middleware.RoleEmployee, nil)
	admin := s.person(t, middleware.RoleHRAdmin, nil)

	key := s.attachment(t, employee.UserID)
	filed := decode[leavecontract.Leave](t, s.do(t, http.MethodPost, "/leaves", employee,
		leaveBody("sick", date(time.March, 3), date(time.March, 5), &key)), http.StatusCreated)
	if filed.WorkingDays != 3 || filed.Status != "pending" || filed.AttachmentMime == nil ||
		filed.StartDate.String() != "2031-03-03" {
		t.Errorf("filed = %+v", filed)
	}

	for name, test := range map[string]struct {
		as   middleware.Claims
		body leavecontract.LeaveRequest
		want int
	}{
		"13 days of annual leave": {employee, leaveBody("annual", date(time.March, 10), date(time.March, 26), nil),
			http.StatusBadRequest},
		"someone else's attachment": {colleague, leaveBody("sick", date(time.March, 3), date(time.March, 5),
			ptr(s.attachment(t, employee.UserID))), http.StatusForbidden},
		"overlapping dates": {employee, leaveBody("unpaid", date(time.March, 5), date(time.March, 5), nil),
			http.StatusConflict},
		"a removed account": {middleware.Claims{Role: middleware.RoleEmployee}, leaveBody("annual",
			date(time.March, 10), date(time.March, 10), nil), http.StatusNotFound}, // user id 0 matches no row
	} {
		if rec := s.do(t, http.MethodPost, "/leaves", test.as, test.body); rec.Code != test.want {
			t.Errorf("POST /leaves, %s = %d, want %d; body = %s", name, rec.Code, test.want, rec.Body)
		}
	}

	mine := decode[[]leavecontract.Leave](t, s.do(t, http.MethodGet, "/leaves/me", employee, nil), http.StatusOK)
	if len(mine) != 1 || mine[0].Id != filed.Id {
		t.Errorf("GET /leaves/me = %+v", mine)
	}
	pending := decode[[]leavecontract.Leave](t, s.do(t, http.MethodGet, "/leaves/pending", lead, nil), http.StatusOK)
	if len(pending) != 1 || pending[0].Id != filed.Id {
		t.Errorf("GET /leaves/pending = %+v", pending)
	}
	if rec := s.do(t, http.MethodGet, "/leaves/pending", employee, nil); rec.Code != http.StatusForbidden {
		t.Errorf("GET /leaves/pending as an employee = %d, want 403", rec.Code)
	}

	balances := decode[[]leavecontract.Balance](t, s.do(t, http.MethodGet, fmt.Sprintf("/leaves/me/balance?year=%d",
		year), employee, nil), http.StatusOK)
	annual := s.seeded(t, "annual")
	for _, balance := range balances {
		if balance.LeaveTypeId == annual.ID && (balance.RemainingDays != 12 || balance.Code != "annual") {
			t.Errorf("annual balance = %+v; want 12 left, since sick leave never touches it", balance)
		}
	}
	if rec := s.do(t, http.MethodGet, "/leaves/me/balance?year=1", employee, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("GET /leaves/me/balance?year=1 = %d, want 400", rec.Code)
	}

	attachment := fmt.Sprintf("/leaves/%d/attachment", filed.Id)
	rec := s.do(t, http.MethodGet, attachment, lead, nil)
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), key) {
		t.Errorf("GET %s = %d, Location %q; want 302 to the key", attachment, rec.Code, rec.Header().Get("Location"))
	}
	for _, step := range []struct {
		as   middleware.Claims
		path string
		want int
	}{
		{colleague, attachment, http.StatusForbidden},
		{employee, "/leaves/0/attachment", http.StatusNotFound},
	} {
		if rec := s.do(t, http.MethodGet, step.path, step.as, nil); rec.Code != step.want {
			t.Errorf("GET %s = %d, want %d", step.path, rec.Code, step.want)
		}
	}

	decision := fmt.Sprintf("/leaves/%d/decision", filed.Id)
	approve := leavecontract.DecisionRequest{Decision: "approve", Note: ptr("get well")}
	for _, step := range []struct {
		name string
		as   middleware.Claims
		path string
		body leavecontract.DecisionRequest
		want int
	}{
		{"by the employee", employee, decision, approve, http.StatusForbidden},
		{"an unknown decision", lead, decision, leavecontract.DecisionRequest{Decision: "later"},
			http.StatusBadRequest},
		{"no such request", lead, "/leaves/0/decision", approve, http.StatusNotFound},
		{"by their lead", lead, decision, approve, http.StatusOK},
		{"twice", lead, decision, approve, http.StatusConflict},
	} {
		if rec := s.do(t, http.MethodPost, step.path, step.as, step.body); rec.Code != step.want {
			t.Errorf("deciding %s = %d, want %d; body = %s", step.name, rec.Code, step.want, rec.Body)
		}
	}

	withdrawn := decode[leavecontract.Leave](t, s.do(t, http.MethodPost, "/leaves", employee,
		leaveBody("annual", date(time.April, 1), date(time.April, 1), nil)), http.StatusCreated)
	cancel := fmt.Sprintf("/leaves/%d", withdrawn.Id)
	for _, step := range []struct {
		name string
		as   middleware.Claims
		path string
		want int
	}{
		{"by their lead", lead, cancel, http.StatusForbidden},
		{"no such request", employee, "/leaves/0", http.StatusNotFound},
		{"by its owner", employee, cancel, http.StatusNoContent},
		{"twice", employee, cancel, http.StatusConflict},
		{"approved leave, by hr_admin", admin, fmt.Sprintf("/leaves/%d", filed.Id), http.StatusNoContent},
	} {
		if rec := s.do(t, http.MethodDelete, step.path, step.as, nil); rec.Code != step.want {
			t.Errorf("cancelling %s = %d, want %d", step.name, rec.Code, step.want)
		}
	}
}

func TestHandlerLeaveTypesAndQuotas(t *testing.T) {
	s := newServer(t)
	admin := s.person(t, middleware.RoleHRAdmin, nil)
	employee := s.person(t, middleware.RoleEmployee, nil)

	kinds := decode[[]leavecontract.LeaveType](t, s.do(t, http.MethodGet, "/leave-types", employee, nil), http.StatusOK)
	if len(kinds) < 11 {
		t.Errorf("GET /leave-types = %d types, want at least the 11 statutory ones", len(kinds))
	}

	body := leavecontract.LeaveTypeRequest{Code: strings.ReplaceAll(unique("study"), "-", "_"), Name: "Study",
		QuotaDaysPerYear: ptr(4), MaxWorkingDaysPerRequest: ptr(2), IsPaid: true}
	created := decode[leavecontract.LeaveType](t, s.do(t, http.MethodPost, "/leave-types", admin, body),
		http.StatusCreated)
	s.typeIDs = append(s.typeIDs, created.Id)
	if created.Code != body.Code || *created.QuotaDaysPerYear != 4 || *created.MaxWorkingDaysPerRequest != 2 {
		t.Errorf("created = %+v", created)
	}

	path := fmt.Sprintf("/leave-types/%d", created.Id)
	body.Name = "Study days"
	updated := decode[leavecontract.LeaveType](t, s.do(t, http.MethodPut, path, admin, body), http.StatusOK)
	if updated.Name != "Study days" {
		t.Errorf("updated = %+v", updated)
	}

	quota := leavecontract.QuotaRequest{UserId: employee.UserID, LeaveTypeId: created.Id, Year: year, QuotaDays: 6,
		CarriedOverDays: ptr(1)}
	set := decode[leavecontract.Quota](t, s.do(t, http.MethodPost, "/leave-balances", admin, quota), http.StatusOK)
	if set.QuotaDays != 6 || set.CarriedOverDays != 1 || set.Year != year {
		t.Errorf("set = %+v", set)
	}

	taken := body
	taken.Code = "annual"
	for _, call := range []struct {
		name, method, path string
		as                 middleware.Claims
		body               any
		want               int
	}{
		{"create as an employee", http.MethodPost, "/leave-types", employee, body, http.StatusForbidden},
		{"create with a bad code", http.MethodPost, "/leave-types", admin, leavecontract.LeaveTypeRequest{Code: "X",
			Name: "x"}, http.StatusBadRequest},
		{"create a taken code", http.MethodPost, "/leave-types", admin, taken, http.StatusConflict},
		{"update as an employee", http.MethodPut, path, employee, body, http.StatusForbidden},
		{"update with no name", http.MethodPut, path, admin, leavecontract.LeaveTypeRequest{Code: "x"},
			http.StatusBadRequest},
		{"update no such type", http.MethodPut, "/leave-types/0", admin, body, http.StatusNotFound},
		{"update to a taken code", http.MethodPut, path, admin, taken, http.StatusConflict},
		{"set a quota as an employee", http.MethodPost, "/leave-balances", employee, quota, http.StatusForbidden},
		{"set a quota for no type", http.MethodPost, "/leave-balances", admin, leavecontract.QuotaRequest{
			UserId: employee.UserID, Year: year}, http.StatusBadRequest},
		{"delete as an employee", http.MethodDelete, path, employee, nil, http.StatusForbidden},
		{"delete", http.MethodDelete, path, admin, nil, http.StatusNoContent},
		{"delete twice", http.MethodDelete, path, admin, nil, http.StatusNotFound},
	} {
		if rec := s.do(t, call.method, call.path, call.as, call.body); rec.Code != call.want {
			t.Errorf("%s = %d, want %d; body = %s", call.name, rec.Code, call.want, rec.Body)
		}
	}
}

func TestHandlerFailuresAre500(t *testing.T) {
	s := newServer(t)
	router := routerFor(s.failing(t, ""))
	admin := middleware.Claims{UserID: 1, Role: middleware.RoleHRAdmin}

	kind := leavecontract.LeaveTypeRequest{Code: "x", Name: "x"}
	for _, call := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/leaves", leaveBody("annual", date(time.March, 3), date(time.March, 3), nil)},
		{http.MethodGet, "/leaves/me", nil},
		{http.MethodGet, "/leaves/me/balance", nil},
		{http.MethodGet, "/leaves/pending", nil},
		{http.MethodDelete, "/leaves/1", nil},
		{http.MethodPost, "/leaves/1/decision", leavecontract.DecisionRequest{Decision: "reject"}},
		{http.MethodGet, "/leaves/1/attachment", nil},
		{http.MethodGet, "/leave-types", nil},
		{http.MethodPost, "/leave-types", kind},
		{http.MethodPut, "/leave-types/1", kind},
		{http.MethodDelete, "/leave-types/1", nil},
		{http.MethodPost, "/leave-balances", leavecontract.QuotaRequest{Year: year}},
	} {
		rec := doOn(t, router, call.method, call.path, admin, call.body)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s %s with the database down = %d, want 500; body = %s", call.method, call.path, rec.Code,
				rec.Body)
		}
	}
}

func TestHandlerWithoutClaimsFails(t *testing.T) {
	h := NewHandler(nil)
	ctx := context.Background()

	calls := map[string]func() error{
		"RequestLeave": func() error {
			_, err := h.RequestLeave(ctx, leavecontract.RequestLeaveRequestObject{})
			return err
		},
		"ListMyLeaves": func() error {
			_, err := h.ListMyLeaves(ctx, leavecontract.ListMyLeavesRequestObject{})
			return err
		},
		"GetMyLeaveBalance": func() error {
			_, err := h.GetMyLeaveBalance(ctx, leavecontract.GetMyLeaveBalanceRequestObject{})
			return err
		},
		"ListPendingLeaves": func() error {
			_, err := h.ListPendingLeaves(ctx, leavecontract.ListPendingLeavesRequestObject{})
			return err
		},
		"CancelLeave": func() error {
			_, err := h.CancelLeave(ctx, leavecontract.CancelLeaveRequestObject{})
			return err
		},
		"DecideLeave": func() error {
			_, err := h.DecideLeave(ctx, leavecontract.DecideLeaveRequestObject{})
			return err
		},
		"GetLeaveAttachment": func() error {
			_, err := h.GetLeaveAttachment(ctx, leavecontract.GetLeaveAttachmentRequestObject{})
			return err
		},
		"CreateLeaveType": func() error {
			_, err := h.CreateLeaveType(ctx, leavecontract.CreateLeaveTypeRequestObject{})
			return err
		},
		"UpdateLeaveType": func() error {
			_, err := h.UpdateLeaveType(ctx, leavecontract.UpdateLeaveTypeRequestObject{})
			return err
		},
		"DeleteLeaveType": func() error {
			_, err := h.DeleteLeaveType(ctx, leavecontract.DeleteLeaveTypeRequestObject{})
			return err
		},
		"SetLeaveQuota": func() error {
			_, err := h.SetLeaveQuota(ctx, leavecontract.SetLeaveQuotaRequestObject{})
			return err
		},
	}
	for name, call := range calls {
		if call() == nil {
			t.Errorf("%s reached without middleware.Auth succeeded, want an error", name)
		}
	}
}
