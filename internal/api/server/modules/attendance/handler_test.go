package attendance

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	attendancecontract "attendance-system/internal/api/server/oapicodegen/attendance"
	"attendance-system/internal/middleware"
)

func markBody(mark Mark) attendancecontract.MarkRequest {
	return attendancecontract.MarkRequest{PhotoKey: mark.PhotoKey, Lat: mark.Lat, Lng: mark.Lng,
		AccuracyM: mark.AccuracyM, MockLocation: mark.MockLocation}
}

func TestHandlerCheckInAndOut(t *testing.T) {
	s := newServer(t)
	hours := s.hours(t, 8*60, 17*60, 15)
	employee := s.person(t, middleware.RoleEmployee, nil, &hours.ID)
	other := s.person(t, middleware.RoleEmployee, nil, nil)
	officeID := s.office(t, officeLat, officeLng, 100)
	clockAt(t, local(2030, time.March, 4, 8, 20))

	// Checking out before checking in is the client's mistake.
	rec := s.do(t, http.MethodPost, "/attendance/check-out", employee, markBody(s.mark(t, employee.UserID, 0)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("check-out without a check-in = %d, want 400; body = %s", rec.Code, rec.Body)
	}

	// 200 m from a 100 m office: flagged, not refused.
	far := s.mark(t, employee.UserID, 200)
	far.MockLocation = true
	in := decode[attendancecontract.Attendance](t,
		s.do(t, http.MethodPost, "/attendance/check-in", employee, markBody(far)), http.StatusCreated)
	if in.LateMinutes != 5 || in.CheckIn == nil || in.CheckIn.WithinGeofence || !in.CheckIn.MockLocation ||
		*in.CheckIn.OfficeLocationId != officeID || in.CheckOut != nil || in.WorkDate.String() != "2030-03-04" {
		t.Errorf("check-in = %+v, evidence %+v", in, in.CheckIn)
	}

	removed := middleware.Claims{Role: middleware.RoleEmployee} // user id 0 matches no row
	statuses := map[string]struct {
		as   middleware.Claims
		mark Mark
		want int
	}{
		"twice on one day":   {employee, s.mark(t, employee.UserID, 0), http.StatusConflict},
		"a key never put":    {employee, Mark{PhotoKey: neverPut(employee.UserID)}, http.StatusBadRequest},
		"someone else's key": {employee, s.mark(t, other.UserID, 0), http.StatusForbidden},
		"a removed account":  {removed, s.mark(t, other.UserID, 0), http.StatusNotFound},
	}
	for name, test := range statuses {
		rec := s.do(t, http.MethodPost, "/attendance/check-in", test.as, markBody(test.mark))
		if rec.Code != test.want {
			t.Errorf("check-in, %s = %d, want %d; body = %s", name, rec.Code, test.want, rec.Body)
		}
	}

	clockAt(t, local(2030, time.March, 4, 16, 50))
	rec = s.do(t, http.MethodPost, "/attendance/check-out", employee, markBody(s.mark(t, other.UserID, 0)))
	if rec.Code != http.StatusForbidden {
		t.Errorf("check-out with someone else's key = %d, want 403", rec.Code)
	}
	out := decode[attendancecontract.Attendance](t,
		s.do(t, http.MethodPost, "/attendance/check-out", employee, markBody(s.mark(t, employee.UserID, 20))),
		http.StatusOK)
	if out.Id != in.Id || out.EarlyLeaveMinutes != 10 || out.CheckOut == nil || !out.CheckOut.WithinGeofence {
		t.Errorf("check-out = %+v, evidence %+v", out, out.CheckOut)
	}
}

func TestHandlerAttendanceReads(t *testing.T) {
	s := newServer(t)
	lead := s.person(t, middleware.RoleSupervisor, nil, nil)
	employee := s.person(t, middleware.RoleEmployee, &lead.UserID, nil)
	outsider := s.person(t, middleware.RoleEmployee, nil, nil)
	row := s.checkedIn(t, employee.UserID, date(2030, 3, 4), local(2030, 3, 4, 8, 0), nil)
	clockAt(t, local(2030, time.March, 4, 12, 0))

	listed := decode[[]attendancecontract.Attendance](t,
		s.do(t, http.MethodGet, "/attendance/me?from=2030-03-01&to=2030-03-31", employee, nil), http.StatusOK)
	if len(listed) != 1 || listed[0].Id != row.ID || listed[0].CheckIn == nil {
		t.Errorf("GET /attendance/me = %+v", listed)
	}
	if rec := s.do(t, http.MethodGet, "/attendance/me?from=2030-03-31&to=2030-03-01", employee, nil); rec.Code !=
		http.StatusBadRequest {
		t.Errorf("a backwards span = %d, want 400", rec.Code)
	}

	report := attendancecontract.DailyReportRequest{Content: "racked 3 servers"}
	report.WorkDate.Time = date(2030, 3, 4)
	saved := decode[attendancecontract.Attendance](t,
		s.do(t, http.MethodPut, "/attendance/me/daily-report", employee, report), http.StatusOK)
	if saved.DailyReport == nil || *saved.DailyReport != "racked 3 servers" || saved.DailyReportUpdatedAt == nil {
		t.Errorf("saved report = %+v", saved)
	}
	if rec := s.do(t, http.MethodPut, "/attendance/me/daily-report", outsider, report); rec.Code != 404 {
		t.Errorf("a report with no attendance = %d, want 404", rec.Code)
	}
	report.Content = " "
	if rec := s.do(t, http.MethodPut, "/attendance/me/daily-report", employee, report); rec.Code !=
		http.StatusBadRequest {
		t.Errorf("an empty report = %d, want 400", rec.Code)
	}

	photo := fmt.Sprintf("/attendance/%d/photo/", row.ID)
	rec := s.do(t, http.MethodGet, photo+"in", lead, nil)
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), *row.CheckIn.PhotoKey) {
		t.Errorf("GET photo/in = %d, Location %q; want 302 to the object", rec.Code, rec.Header().Get("Location"))
	}
	for path, want := range map[string]int{photo + "out": 404, photo + "sideways": 400} {
		if rec := s.do(t, http.MethodGet, path, employee, nil); rec.Code != want {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, want)
		}
	}
	if rec := s.do(t, http.MethodGet, photo+"in", outsider, nil); rec.Code != http.StatusForbidden {
		t.Errorf("someone else's photo = %d, want 403", rec.Code)
	}

	whosIn := decode[[]attendancecontract.Presence](t,
		s.do(t, http.MethodGet, "/attendance/whos-in?date=2030-03-04", lead, nil), http.StatusOK)
	if len(whosIn) != 1 || whosIn[0].UserId != employee.UserID || whosIn[0].Status != "present" ||
		whosIn[0].Attendance == nil {
		t.Errorf("whos-in = %+v; want only the lead's report, present", whosIn)
	}
	if rec := s.do(t, http.MethodGet, "/attendance/whos-in?date=2030-03-04", employee, nil); rec.Code !=
		http.StatusForbidden {
		t.Errorf("whos-in as an employee = %d, want 403", rec.Code)
	}
}

func TestHandlerCorrections(t *testing.T) {
	s := newServer(t)
	lead := s.person(t, middleware.RoleSupervisor, nil, nil)
	employee := s.person(t, middleware.RoleEmployee, &lead.UserID, nil)
	colleague := s.person(t, middleware.RoleEmployee, nil, nil)
	s.checkedIn(t, employee.UserID, date(2030, 3, 4), local(2030, 3, 4, 8, 0), nil)
	clockAt(t, local(2030, time.March, 5, 9, 0))

	request := attendancecontract.CorrectionRequest{ProposedCheckOutAt: ptr(local(2030, 3, 4, 17, 0)),
		Reason: "forgot to check out"}
	request.WorkDate.Time = date(2030, 3, 4)
	filed := decode[attendancecontract.Correction](t,
		s.do(t, http.MethodPost, "/attendance/corrections", employee, request), http.StatusCreated)
	if filed.Status != "pending" || filed.RequestedBy != employee.UserID {
		t.Errorf("filed = %+v", filed)
	}
	if rec := s.do(t, http.MethodPost, "/attendance/corrections", employee, request); rec.Code != http.StatusConflict {
		t.Errorf("a second pending request = %d, want 409", rec.Code)
	}
	forColleague := request
	forColleague.UserId = &colleague.UserID
	if rec := s.do(t, http.MethodPost, "/attendance/corrections", employee, forColleague); rec.Code !=
		http.StatusForbidden {
		t.Errorf("filing for someone else = %d, want 403", rec.Code)
	}
	if rec := s.do(t, http.MethodPost, "/attendance/corrections", employee,
		attendancecontract.CorrectionRequest{}); rec.Code != http.StatusBadRequest {
		t.Errorf("an empty request = %d, want 400", rec.Code)
	}

	mine := decode[[]attendancecontract.Correction](t,
		s.do(t, http.MethodGet, "/attendance/corrections/me", employee, nil), http.StatusOK)
	if len(mine) != 1 || mine[0].Id != filed.Id {
		t.Errorf("my corrections = %+v", mine)
	}
	pending := decode[[]attendancecontract.Correction](t,
		s.do(t, http.MethodGet, "/attendance/corrections/pending", lead, nil), http.StatusOK)
	if len(pending) != 1 || pending[0].Id != filed.Id {
		t.Errorf("the lead's queue = %+v", pending)
	}
	if rec := s.do(t, http.MethodGet, "/attendance/corrections/pending", employee, nil); rec.Code !=
		http.StatusForbidden {
		t.Errorf("an employee's queue = %d, want 403", rec.Code)
	}

	decision := func(id int64) string { return fmt.Sprintf("/attendance/corrections/%d/decision", id) }
	approve := attendancecontract.DecisionRequest{Decision: "approve", Note: ptr("seen on camera")}
	for name, test := range map[string]struct {
		as   middleware.Claims
		path string
		body attendancecontract.DecisionRequest
		want int
	}{
		"by a non-supervisor": {employee, decision(filed.Id), approve, http.StatusForbidden},
		"an unknown decision": {lead, decision(filed.Id), attendancecontract.DecisionRequest{Decision: "maybe"},
			http.StatusBadRequest},
		"no such correction": {lead, decision(0), approve, http.StatusNotFound},
	} {
		if rec := s.do(t, http.MethodPost, test.path, test.as, test.body); rec.Code != test.want {
			t.Errorf("deciding %s = %d, want %d; body = %s", name, rec.Code, test.want, rec.Body)
		}
	}

	approved := decode[attendancecontract.Correction](t,
		s.do(t, http.MethodPost, decision(filed.Id), lead, approve), http.StatusOK)
	if approved.Status != "approved" || approved.OldCheckInAt == nil || *approved.ReviewNote != "seen on camera" {
		t.Errorf("approved = %+v", approved)
	}
	reject := attendancecontract.DecisionRequest{Decision: "reject"}
	if rec := s.do(t, http.MethodPost, decision(filed.Id), lead, reject); rec.Code != http.StatusConflict {
		t.Errorf("deciding twice = %d, want 409", rec.Code)
	}

	request.WorkDate.Time, request.ProposedCheckInAt = date(2030, 3, 3), ptr(local(2030, 3, 3, 8, 0))
	request.ProposedCheckOutAt = nil
	withdrawn := decode[attendancecontract.Correction](t,
		s.do(t, http.MethodPost, "/attendance/corrections", employee, request), http.StatusCreated)
	cancel := fmt.Sprintf("/attendance/corrections/%d", withdrawn.Id)
	for _, step := range []struct {
		name string
		as   middleware.Claims
		path string
		want int
	}{
		{"by a stranger", colleague, cancel, http.StatusForbidden},
		{"no such correction", employee, "/attendance/corrections/0", http.StatusNotFound},
		{"by its employee", employee, cancel, http.StatusNoContent},
		{"twice", employee, cancel, http.StatusConflict},
	} {
		if rec := s.do(t, http.MethodDelete, step.path, step.as, nil); rec.Code != step.want {
			t.Errorf("cancelling %s = %d, want %d", step.name, rec.Code, step.want)
		}
	}
}

func TestHandlerFailuresAre500(t *testing.T) {
	s := newServer(t)
	router := routerFor(s.failing(t, ""))
	lead := middleware.Claims{UserID: 1, Role: middleware.RoleSupervisor}
	clockAt(t, local(2030, time.March, 5, 9, 0))

	mark := attendancecontract.MarkRequest{PhotoKey: "attendance/1/x.jpg"}
	report := attendancecontract.DailyReportRequest{Content: "x"}
	report.WorkDate.Time = date(2030, 3, 4)
	request := attendancecontract.CorrectionRequest{ProposedCheckInAt: ptr(local(2030, 3, 4, 8, 0)), Reason: "x"}
	request.WorkDate.Time = date(2030, 3, 4)

	for _, call := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/attendance/check-in", mark},
		{http.MethodPost, "/attendance/check-out", mark},
		{http.MethodGet, "/attendance/me?from=2030-03-01&to=2030-03-31", nil},
		{http.MethodPut, "/attendance/me/daily-report", report},
		{http.MethodGet, "/attendance/1/photo/in", nil},
		{http.MethodGet, "/attendance/whos-in?date=2030-03-04", nil},
		{http.MethodPost, "/attendance/corrections", request},
		{http.MethodGet, "/attendance/corrections/me", nil},
		{http.MethodGet, "/attendance/corrections/pending", nil},
		{http.MethodDelete, "/attendance/corrections/1", nil},
		{http.MethodPost, "/attendance/corrections/1/decision", attendancecontract.DecisionRequest{Decision: "reject"}},
	} {
		if rec := doOn(t, router, call.method, call.path, lead, call.body); rec.Code != http.StatusInternalServerError {
			t.Errorf("%s %s with the database down = %d, want 500; body = %s", call.method, call.path, rec.Code,
				rec.Body)
		}
	}
}

func TestHandlerWithoutClaimsFails(t *testing.T) {
	h := NewHandler(nil)
	ctx := context.Background()

	calls := map[string]func() error{
		"CheckIn":  func() error { _, err := h.CheckIn(ctx, attendancecontract.CheckInRequestObject{}); return err },
		"CheckOut": func() error { _, err := h.CheckOut(ctx, attendancecontract.CheckOutRequestObject{}); return err },
		"ListMyAttendance": func() error {
			_, err := h.ListMyAttendance(ctx, attendancecontract.ListMyAttendanceRequestObject{})
			return err
		},
		"SaveDailyReport": func() error {
			_, err := h.SaveDailyReport(ctx, attendancecontract.SaveDailyReportRequestObject{})
			return err
		},
		"GetAttendancePhoto": func() error {
			_, err := h.GetAttendancePhoto(ctx, attendancecontract.GetAttendancePhotoRequestObject{})
			return err
		},
		"GetWhosIn": func() error {
			_, err := h.GetWhosIn(ctx, attendancecontract.GetWhosInRequestObject{})
			return err
		},
		"RequestCorrection": func() error {
			_, err := h.RequestCorrection(ctx, attendancecontract.RequestCorrectionRequestObject{})
			return err
		},
		"ListMyCorrections": func() error {
			_, err := h.ListMyCorrections(ctx, attendancecontract.ListMyCorrectionsRequestObject{})
			return err
		},
		"ListPendingCorrections": func() error {
			_, err := h.ListPendingCorrections(ctx, attendancecontract.ListPendingCorrectionsRequestObject{})
			return err
		},
		"CancelCorrection": func() error {
			_, err := h.CancelCorrection(ctx, attendancecontract.CancelCorrectionRequestObject{})
			return err
		},
		"DecideCorrection": func() error {
			_, err := h.DecideCorrection(ctx, attendancecontract.DecideCorrectionRequestObject{})
			return err
		},
	}
	for name, call := range calls {
		if call() == nil {
			t.Errorf("%s reached without middleware.Auth succeeded, want an error", name)
		}
	}
}
