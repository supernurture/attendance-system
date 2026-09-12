package schedule

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/oapi-codegen/runtime/types"

	schedulecontract "attendance-system/internal/api/server/oapicodegen/schedule"
	"attendance-system/internal/middleware"
)

func TestHandlerGetMySchedule(t *testing.T) {
	s := newServer(t)

	schedule := s.weekdaySchedule(t)
	person := s.person(t, "employee", &schedule.ID)
	as := claimsOf(person, middleware.RoleEmployee)

	// 2026-09-14 is a Monday, 2026-09-13 a Sunday.
	path := "/me/schedule?from=2026-09-13&to=2026-09-14"
	days := decode[[]schedulecontract.ScheduledDay](t, s.do(t, http.MethodGet, path, as, nil), http.StatusOK)
	if len(days) != 2 {
		t.Fatalf("got %d days, want 2", len(days))
	}
	if days[0].Working || days[0].Reason != "not_a_workday" {
		t.Errorf("Sunday = %+v", days[0])
	}
	if !days[1].Working || days[1].Reason != "default" {
		t.Errorf("Monday = %+v", days[1])
	}
	if days[1].Schedule == nil || days[1].Schedule.StartTime != "08:00" {
		t.Errorf("Monday's schedule = %+v", days[1].Schedule)
	}
	if days[1].Schedule.CrossesMidnight {
		t.Error("08:00-17:00 does not cross midnight")
	}

	// The holiday's name comes back whether the schedule observes it or not.
	s.addHoliday(t, day(2026, time.September, 14), "Maulid Nabi")
	days = decode[[]schedulecontract.ScheduledDay](t, s.do(t, http.MethodGet, path, as, nil), http.StatusOK)
	if days[1].HolidayName == nil || *days[1].HolidayName != "Maulid Nabi" {
		t.Errorf("holiday_name = %v", days[1].HolidayName)
	}
	if !days[1].Working {
		t.Error("a schedule that does not observe holidays still works that day")
	}
}

func TestHandlerGetMyScheduleErrors(t *testing.T) {
	s := newServer(t)
	as := claimsOf(s.person(t, "employee", nil), middleware.RoleEmployee)

	// A span the other way around is the client's mistake.
	rec := s.do(t, http.MethodGet, "/me/schedule?from=2026-09-14&to=2026-09-13", as, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("backwards span = %d, want 400; body = %s", rec.Code, rec.Body)
	}

	// A malformed date never reaches the service.
	rec = s.do(t, http.MethodGet, "/me/schedule?from=yesterday&to=2026-09-13", as, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed date = %d, want 400", rec.Code)
	}

	// A token for somebody who is not there any more.
	stranger := middleware.Claims{UserID: 0, Role: middleware.RoleEmployee}
	rec = s.do(t, http.MethodGet, "/me/schedule?from=2026-09-13&to=2026-09-14", stranger, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown caller = %d, want 404", rec.Code)
	}

	// The span is required, so asking without one is rejected before the service runs.
	if rec = s.do(t, http.MethodGet, "/me/schedule", as, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("no span = %d, want 400", rec.Code)
	}
}

func TestHandlerWorkSchedules(t *testing.T) {
	s := newServer(t)
	admin := claimsOf(s.person(t, "hr_admin", nil), middleware.RoleHRAdmin)

	grace, breaks, observes := 15, 60, true
	body := schedulecontract.WorkScheduleRequest{
		Name: unique("schedule"), StartTime: "22:00", EndTime: "06:00",
		GraceMinutes: &grace, BreakMinutes: &breaks, ObservesHolidays: &observes,
		Workdays: []int{1, 2, 3, 4, 5, 6, 7},
	}
	created := decode[schedulecontract.WorkSchedule](t,
		s.do(t, http.MethodPost, "/work-schedules", admin, body), http.StatusCreated)
	s.trackSchedule(WorkSchedule{ID: created.Id})
	if !created.CrossesMidnight || created.StartTime != "22:00" {
		t.Errorf("created = %+v, want a night shift", created)
	}

	body.Name = unique("schedule")
	body.StartTime, body.EndTime = "08:00", "17:00"
	body.Workdays = []int{1, 2, 3, 4, 5}
	path := fmt.Sprintf("/work-schedules/%d", created.Id)
	replaced := decode[schedulecontract.WorkSchedule](t,
		s.do(t, http.MethodPut, path, admin, body), http.StatusOK)
	if replaced.CrossesMidnight || len(replaced.Workdays) != 5 {
		t.Errorf("replaced = %+v", replaced)
	}

	supervisor := claimsOf(s.person(t, "supervisor", nil), middleware.RoleSupervisor)
	listed := decode[[]schedulecontract.WorkSchedule](t,
		s.do(t, http.MethodGet, "/work-schedules", supervisor, nil), http.StatusOK)
	if len(listed) == 0 {
		t.Error("the list came back empty")
	}

	if rec := s.do(t, http.MethodDelete, path, admin, nil); rec.Code != http.StatusNoContent {
		t.Errorf("delete = %d, want 204; body = %s", rec.Code, rec.Body)
	}
}

func TestHandlerWorkScheduleErrors(t *testing.T) {
	s := newServer(t)
	admin := claimsOf(s.person(t, "hr_admin", nil), middleware.RoleHRAdmin)
	employee := claimsOf(s.person(t, "employee", nil), middleware.RoleEmployee)
	valid := schedulecontract.WorkScheduleRequest{
		Name: unique("schedule"), StartTime: "08:00", EndTime: "17:00", Workdays: []int{1},
	}

	// A schedule somebody keeps cannot be removed.
	kept := s.weekdaySchedule(t)
	s.person(t, "employee", &kept.ID)
	keptPath := fmt.Sprintf("/work-schedules/%d", kept.ID)

	tests := []struct {
		name   string
		method string
		path   string
		as     middleware.Claims
		body   any
		want   int
	}{
		{name: "list as an employee", method: http.MethodGet, path: "/work-schedules", as: employee, want: 403},
		{name: "create as an employee", method: http.MethodPost, path: "/work-schedules",
			as: employee, body: valid, want: 403},
		{name: "create with no name", method: http.MethodPost, path: "/work-schedules", as: admin,
			body: schedulecontract.WorkScheduleRequest{StartTime: "08:00", EndTime: "17:00", Workdays: []int{1}},
			want: 400},
		{name: "create with a malformed time", method: http.MethodPost, path: "/work-schedules", as: admin,
			body: schedulecontract.WorkScheduleRequest{Name: "x", StartTime: "8am", EndTime: "17:00",
				Workdays: []int{1}},
			want: 400},
		{name: "replace as an employee", method: http.MethodPut, path: keptPath,
			as: employee, body: valid, want: 403},
		{name: "replace with no workdays", method: http.MethodPut, path: keptPath, as: admin,
			body: schedulecontract.WorkScheduleRequest{Name: "x", StartTime: "08:00", EndTime: "17:00"},
			want: 400},
		{name: "replace one that is gone", method: http.MethodPut, path: "/work-schedules/0",
			as: admin, body: valid, want: 404},
		{name: "delete as an employee", method: http.MethodDelete, path: keptPath, as: employee, want: 403},
		{name: "delete one that is gone", method: http.MethodDelete, path: "/work-schedules/0",
			as: admin, want: 404},
		{name: "delete one still in use", method: http.MethodDelete, path: keptPath, as: admin, want: 409},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := s.do(t, test.method, test.path, test.as, test.body)
			if rec.Code != test.want {
				t.Errorf("status = %d, want %d; body = %s", rec.Code, test.want, rec.Body)
			}
		})
	}
}

func TestHandlerHolidays(t *testing.T) {
	s := newServer(t)
	admin := claimsOf(s.person(t, "hr_admin", nil), middleware.RoleHRAdmin)

	date := futureDate(20)
	body := schedulecontract.HolidayRequest{Date: types.Date{Time: date}, Name: "Hari Raya"}
	created := decode[schedulecontract.Holiday](t,
		s.do(t, http.MethodPost, "/holidays", admin, body), http.StatusCreated)
	s.trackHoliday(Holiday{ID: created.Id})
	if !created.Date.Time.Equal(date) {
		t.Errorf("date = %s, want %s", created.Date.Time, date)
	}

	// The same date twice is a conflict, so resolution is never ambiguous.
	if rec := s.do(t, http.MethodPost, "/holidays", admin, body); rec.Code != http.StatusConflict {
		t.Errorf("duplicate date = %d, want 409; body = %s", rec.Code, rec.Body)
	}

	body.Name = "Hari Raya Kedua"
	path := fmt.Sprintf("/holidays/%d", created.Id)
	replaced := decode[schedulecontract.Holiday](t, s.do(t, http.MethodPut, path, admin, body), http.StatusOK)
	if replaced.Name != "Hari Raya Kedua" {
		t.Errorf("replaced = %+v", replaced)
	}

	supervisor := claimsOf(s.person(t, "supervisor", nil), middleware.RoleSupervisor)
	span := fmt.Sprintf("/holidays?from=%s&to=%s",
		futureDate(0).Format(time.DateOnly), futureDate(30).Format(time.DateOnly))
	listed := decode[[]schedulecontract.Holiday](t, s.do(t, http.MethodGet, span, supervisor, nil), http.StatusOK)
	if len(listed) != 1 {
		t.Errorf("listed = %+v", listed)
	}

	if rec := s.do(t, http.MethodDelete, path, admin, nil); rec.Code != http.StatusNoContent {
		t.Errorf("delete = %d, want 204; body = %s", rec.Code, rec.Body)
	}
}

func TestHandlerHolidayErrors(t *testing.T) {
	s := newServer(t)
	admin := claimsOf(s.person(t, "hr_admin", nil), middleware.RoleHRAdmin)
	employee := claimsOf(s.person(t, "employee", nil), middleware.RoleEmployee)

	existing := s.addHoliday(t, futureDate(40), "Planned")
	taken := s.addHoliday(t, futureDate(41), "Taken")
	valid := schedulecontract.HolidayRequest{Date: types.Date{Time: futureDate(42)}, Name: "x"}
	path := fmt.Sprintf("/holidays/%d", existing.ID)

	tests := []struct {
		name   string
		method string
		path   string
		as     middleware.Claims
		body   any
		want   int
	}{
		{name: "list as an employee", method: http.MethodGet, path: "/holidays?from=2026-01-01&to=2026-01-31",
			as: employee, want: 403},
		{name: "list with a backwards span", method: http.MethodGet,
			path: "/holidays?from=2026-02-01&to=2026-01-31", as: admin, want: 400},
		{name: "create as an employee", method: http.MethodPost, path: "/holidays",
			as: employee, body: valid, want: 403},
		{name: "create with no name", method: http.MethodPost, path: "/holidays", as: admin,
			body: schedulecontract.HolidayRequest{Date: types.Date{Time: futureDate(43)}}, want: 400},
		{name: "replace as an employee", method: http.MethodPut, path: path, as: employee, body: valid, want: 403},
		{name: "replace with no name", method: http.MethodPut, path: path, as: admin,
			body: schedulecontract.HolidayRequest{Date: types.Date{Time: futureDate(44)}}, want: 400},
		{name: "replace one that is gone", method: http.MethodPut, path: "/holidays/0",
			as: admin, body: valid, want: 404},
		{name: "replace onto a date already taken", method: http.MethodPut, path: path, as: admin,
			body: schedulecontract.HolidayRequest{Date: types.Date{Time: taken.Date}, Name: "x"}, want: 409},
		{name: "delete as an employee", method: http.MethodDelete, path: path, as: employee, want: 403},
		{name: "delete one that is gone", method: http.MethodDelete, path: "/holidays/0", as: admin, want: 404},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := s.do(t, test.method, test.path, test.as, test.body)
			if rec.Code != test.want {
				t.Errorf("status = %d, want %d; body = %s", rec.Code, test.want, rec.Body)
			}
		})
	}
}

func TestHandlerOfficeLocations(t *testing.T) {
	s := newServer(t)
	admin := claimsOf(s.person(t, "hr_admin", nil), middleware.RoleHRAdmin)

	body := schedulecontract.OfficeLocationRequest{
		Name: unique("office"), Lat: -6.2088, Lng: 106.8456, RadiusM: 100,
	}
	created := decode[schedulecontract.OfficeLocation](t,
		s.do(t, http.MethodPost, "/office-locations", admin, body), http.StatusCreated)
	s.trackLocation(OfficeLocation{ID: created.Id})
	if created.RadiusM != 100 {
		t.Errorf("created = %+v", created)
	}

	body.RadiusM = 250
	path := fmt.Sprintf("/office-locations/%d", created.Id)
	replaced := decode[schedulecontract.OfficeLocation](t,
		s.do(t, http.MethodPut, path, admin, body), http.StatusOK)
	if replaced.RadiusM != 250 {
		t.Errorf("replaced = %+v", replaced)
	}

	supervisor := claimsOf(s.person(t, "supervisor", nil), middleware.RoleSupervisor)
	listed := decode[[]schedulecontract.OfficeLocation](t,
		s.do(t, http.MethodGet, "/office-locations", supervisor, nil), http.StatusOK)
	if !hasContractLocation(listed, created.Id) {
		t.Error("the office is not in the list")
	}

	if rec := s.do(t, http.MethodDelete, path, admin, nil); rec.Code != http.StatusNoContent {
		t.Errorf("delete = %d, want 204; body = %s", rec.Code, rec.Body)
	}
}

func TestHandlerOfficeLocationErrors(t *testing.T) {
	s := newServer(t)
	admin := claimsOf(s.person(t, "hr_admin", nil), middleware.RoleHRAdmin)
	employee := claimsOf(s.person(t, "employee", nil), middleware.RoleEmployee)

	existing := s.addLocation(t)
	path := fmt.Sprintf("/office-locations/%d", existing.ID)
	valid := schedulecontract.OfficeLocationRequest{Name: "Kantor", Lat: 0, Lng: 0, RadiusM: 100}
	tooSmall := schedulecontract.OfficeLocationRequest{Name: "Kantor", RadiusM: 1}

	tests := []struct {
		name   string
		method string
		path   string
		as     middleware.Claims
		body   any
		want   int
	}{
		{name: "list as an employee", method: http.MethodGet, path: "/office-locations", as: employee, want: 403},
		{name: "create as an employee", method: http.MethodPost, path: "/office-locations",
			as: employee, body: valid, want: 403},
		{name: "create with too small a radius", method: http.MethodPost, path: "/office-locations",
			as: admin, body: tooSmall, want: 400},
		{name: "replace as an employee", method: http.MethodPut, path: path, as: employee, body: valid, want: 403},
		{name: "replace with too small a radius", method: http.MethodPut, path: path,
			as: admin, body: tooSmall, want: 400},
		{name: "replace one that is gone", method: http.MethodPut, path: "/office-locations/0",
			as: admin, body: valid, want: 404},
		{name: "delete as an employee", method: http.MethodDelete, path: path, as: employee, want: 403},
		{name: "delete one that is gone", method: http.MethodDelete, path: "/office-locations/0",
			as: admin, want: 404},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := s.do(t, test.method, test.path, test.as, test.body)
			if rec.Code != test.want {
				t.Errorf("status = %d, want %d; body = %s", rec.Code, test.want, rec.Body)
			}
		})
	}
}

func TestHandlerShiftAssignments(t *testing.T) {
	s := newServer(t)
	admin := claimsOf(s.person(t, "hr_admin", nil), middleware.RoleHRAdmin)

	schedule := s.weekdaySchedule(t)
	person := s.person(t, "employee", nil)
	note := "covering for Budi"
	body := schedulecontract.AssignShiftsRequest{Assignments: []schedulecontract.ShiftAssignmentRequest{
		{UserId: person.ID, WorkDate: types.Date{Time: futureDate(1)}, ScheduleId: &schedule.ID, Note: &note},
		{UserId: person.ID, WorkDate: types.Date{Time: futureDate(2)}},
	}}

	written := decode[[]schedulecontract.ShiftAssignment](t,
		s.do(t, http.MethodPost, "/shift-assignments", admin, body), http.StatusOK)
	if len(written) != 2 {
		t.Fatalf("wrote %d rows, want 2", len(written))
	}
	if written[0].Note == nil || *written[0].Note != note {
		t.Errorf("note = %v", written[0].Note)
	}
	if written[1].ScheduleId != nil {
		t.Error("the second entry is a planned day off")
	}

	span := fmt.Sprintf("/shift-assignments?from=%s&to=%s&user_id=%d",
		futureDate(0).Format(time.DateOnly), futureDate(3).Format(time.DateOnly), person.ID)
	listed := decode[[]schedulecontract.ShiftAssignment](t,
		s.do(t, http.MethodGet, span, admin, nil), http.StatusOK)
	if len(listed) != 2 || listed[0].Id == nil {
		t.Fatalf("listed = %+v", listed)
	}

	// The roster wins over the employee's own schedule, even on a day it does not cover.
	as := claimsOf(person, middleware.RoleEmployee)
	path := fmt.Sprintf("/me/schedule?from=%s&to=%s",
		futureDate(1).Format(time.DateOnly), futureDate(1).Format(time.DateOnly))
	days := decode[[]schedulecontract.ScheduledDay](t, s.do(t, http.MethodGet, path, as, nil), http.StatusOK)
	if len(days) != 1 || days[0].Reason != "shift" {
		t.Errorf("days = %+v", days)
	}

	remove := fmt.Sprintf("/shift-assignments/%d", *listed[0].Id)
	if rec := s.do(t, http.MethodDelete, remove, admin, nil); rec.Code != http.StatusNoContent {
		t.Errorf("delete = %d, want 204; body = %s", rec.Code, rec.Body)
	}
}

func TestHandlerShiftAssignmentErrors(t *testing.T) {
	s := newServer(t)
	admin := claimsOf(s.person(t, "hr_admin", nil), middleware.RoleHRAdmin)
	supervisor := claimsOf(s.person(t, "supervisor", nil), middleware.RoleSupervisor)
	person := s.person(t, "employee", nil)

	one := schedulecontract.AssignShiftsRequest{Assignments: []schedulecontract.ShiftAssignmentRequest{
		{UserId: person.ID, WorkDate: types.Date{Time: futureDate(1)}},
	}}
	twice := schedulecontract.AssignShiftsRequest{Assignments: []schedulecontract.ShiftAssignmentRequest{
		{UserId: person.ID, WorkDate: types.Date{Time: futureDate(1)}},
		{UserId: person.ID, WorkDate: types.Date{Time: futureDate(1)}},
	}}

	tests := []struct {
		name   string
		method string
		path   string
		as     middleware.Claims
		body   any
		want   int
	}{
		{name: "list as a supervisor", method: http.MethodGet,
			path: "/shift-assignments?from=2026-09-01&to=2026-09-30", as: supervisor, want: 403},
		{name: "list with a backwards span", method: http.MethodGet,
			path: "/shift-assignments?from=2026-09-30&to=2026-09-01", as: admin, want: 400},
		{name: "assign as a supervisor", method: http.MethodPost, path: "/shift-assignments",
			as: supervisor, body: one, want: 403},
		{name: "assign nothing", method: http.MethodPost, path: "/shift-assignments", as: admin,
			body: schedulecontract.AssignShiftsRequest{}, want: 400},
		{name: "assign the same person twice on one date", method: http.MethodPost,
			path: "/shift-assignments", as: admin, body: twice, want: 400},
		{name: "assign somebody who does not exist", method: http.MethodPost, path: "/shift-assignments",
			as: admin, body: schedulecontract.AssignShiftsRequest{
				Assignments: []schedulecontract.ShiftAssignmentRequest{
					{UserId: -1, WorkDate: types.Date{Time: futureDate(1)}},
				},
			}, want: 400},
		{name: "delete as a supervisor", method: http.MethodDelete, path: "/shift-assignments/1",
			as: supervisor, want: 403},
		{name: "delete one that is gone", method: http.MethodDelete, path: "/shift-assignments/0",
			as: admin, want: 404},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := s.do(t, test.method, test.path, test.as, test.body)
			if rec.Code != test.want {
				t.Errorf("status = %d, want %d; body = %s", rec.Code, test.want, rec.Body)
			}
		})
	}
}

// A database that is down is never the client's fault. What the 500 body says is router.go's doing,
// and router_test.go is where that is checked.
func TestHandlerInternalErrors(t *testing.T) {
	s := newServer(t)
	router := s.failingRouter(t)
	admin := middleware.Claims{UserID: 1, Role: middleware.RoleHRAdmin}

	schedule := schedulecontract.WorkScheduleRequest{
		Name: "x", StartTime: "08:00", EndTime: "17:00", Workdays: []int{1},
	}
	holiday := schedulecontract.HolidayRequest{Date: types.Date{Time: futureDate(1)}, Name: "x"}
	location := schedulecontract.OfficeLocationRequest{Name: "x", RadiusM: 100}
	assignments := schedulecontract.AssignShiftsRequest{Assignments: []schedulecontract.ShiftAssignmentRequest{
		{UserId: 1, WorkDate: types.Date{Time: futureDate(1)}},
	}}

	calls := []struct {
		method string
		path   string
		body   any
	}{
		{method: http.MethodGet, path: "/me/schedule?from=2026-09-01&to=2026-09-02"},
		{method: http.MethodGet, path: "/work-schedules"},
		{method: http.MethodPost, path: "/work-schedules", body: schedule},
		{method: http.MethodPut, path: "/work-schedules/1", body: schedule},
		{method: http.MethodDelete, path: "/work-schedules/1"},
		{method: http.MethodGet, path: "/holidays?from=2026-09-01&to=2026-09-02"},
		{method: http.MethodPost, path: "/holidays", body: holiday},
		{method: http.MethodPut, path: "/holidays/1", body: holiday},
		{method: http.MethodDelete, path: "/holidays/1"},
		{method: http.MethodGet, path: "/office-locations"},
		{method: http.MethodPost, path: "/office-locations", body: location},
		{method: http.MethodPut, path: "/office-locations/1", body: location},
		{method: http.MethodDelete, path: "/office-locations/1"},
		{method: http.MethodGet, path: "/shift-assignments?from=2026-09-01&to=2026-09-02"},
		{method: http.MethodPost, path: "/shift-assignments", body: assignments},
		{method: http.MethodDelete, path: "/shift-assignments/1"},
	}

	for _, call := range calls {
		rec := s.doOn(t, router, call.method, call.path, admin, call.body)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s %s = %d, want 500; body = %s", call.method, call.path, rec.Code, rec.Body)
		}
	}
}

// Without middleware.Auth there is nobody to act as, which is a server mistake, not a 401.
func TestHandlerWithoutAuth(t *testing.T) {
	s := newServer(t)

	router := gin.New()
	router.ContextWithFallback = true
	schedulecontract.RegisterHandlers(router,
		schedulecontract.NewStrictHandlerWithOptions(NewHandler(s.svc), nil,
			schedulecontract.StrictGinServerOptions{
				HandlerErrorFunc: func(c *gin.Context, _ error) { c.Status(http.StatusInternalServerError) },
			}))

	paths := []struct {
		method string
		path   string
		body   any
	}{
		{method: http.MethodGet, path: "/me/schedule?from=2026-09-01&to=2026-09-02"},
		{method: http.MethodGet, path: "/work-schedules"},
		{method: http.MethodPost, path: "/work-schedules", body: schedulecontract.WorkScheduleRequest{}},
		{method: http.MethodPut, path: "/work-schedules/1", body: schedulecontract.WorkScheduleRequest{}},
		{method: http.MethodDelete, path: "/work-schedules/1"},
		{method: http.MethodGet, path: "/holidays?from=2026-09-01&to=2026-09-02"},
		{method: http.MethodPost, path: "/holidays", body: schedulecontract.HolidayRequest{}},
		{method: http.MethodPut, path: "/holidays/1", body: schedulecontract.HolidayRequest{}},
		{method: http.MethodDelete, path: "/holidays/1"},
		{method: http.MethodGet, path: "/office-locations"},
		{method: http.MethodPost, path: "/office-locations", body: schedulecontract.OfficeLocationRequest{}},
		{method: http.MethodPut, path: "/office-locations/1", body: schedulecontract.OfficeLocationRequest{}},
		{method: http.MethodDelete, path: "/office-locations/1"},
		{method: http.MethodGet, path: "/shift-assignments?from=2026-09-01&to=2026-09-02"},
		{method: http.MethodPost, path: "/shift-assignments", body: schedulecontract.AssignShiftsRequest{}},
		{method: http.MethodDelete, path: "/shift-assignments/1"},
	}

	for _, call := range paths {
		rec := s.doOn(t, router, call.method, call.path, middleware.Claims{UserID: 1}, call.body)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s %s without auth = %d, want 500", call.method, call.path, rec.Code)
		}
	}
}

func hasContractLocation(locations []schedulecontract.OfficeLocation, id int64) bool {
	for _, location := range locations {
		if location.Id == id {
			return true
		}
	}
	return false
}
