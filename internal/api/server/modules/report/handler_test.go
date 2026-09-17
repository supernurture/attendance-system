package report

import (
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	reportcontract "attendance-system/internal/api/server/oapicodegen/report"
	"attendance-system/internal/middleware"
)

const week = "?from=2025-06-02&to=2025-06-05"

func TestFormatsAgree(t *testing.T) {
	s := newServer(t)
	w := s.week(t)

	report := decode[reportcontract.AttendanceReport](t, get(t, s.router, "/reports/attendance"+week, w.lead),
		http.StatusOK)
	if len(report.Days) != 12 || len(report.People) != 3 || report.People[0].WorkedMinutes != 1050 {
		t.Fatalf("report = %+v, want the team's 12 days and 3 totals", report)
	}

	csvRec := get(t, s.router, "/reports/attendance.csv"+week, w.lead)
	if csvRec.Code != http.StatusOK || csvRec.Header().Get("Content-Type") != "text/csv" ||
		csvRec.Header().Get("Content-Disposition") != `attachment; filename="attendance_2025-06-02_2025-06-05.csv"` {
		t.Fatalf("CSV: status %d, headers %v", csvRec.Code, csvRec.Header())
	}
	rows, err := csv.NewReader(csvRec.Body).ReadAll()
	if err != nil {
		t.Fatalf("read CSV: %v", err)
	}
	wantRows := [][]string{csvHeader}
	for _, day := range report.Days {
		wantRows = append(wantRows, []string{
			strconv.FormatInt(day.UserId, 10), day.FullName, "", day.WorkDate.Format(time.DateOnly), string(day.Status),
			timestamp(day.CheckInAt), timestamp(day.CheckOutAt), strconv.Itoa(day.LateMinutes),
			strconv.Itoa(day.EarlyLeaveMinutes), strconv.Itoa(day.WorkedMinutes),
		})
	}
	if !slices.EqualFunc(rows, wantRows, slices.Equal) {
		t.Errorf("CSV rows = %q\nwant the JSON's days %q", rows, wantRows)
	}

	pdfRec := get(t, s.router, "/reports/attendance.pdf"+week, w.lead)
	if pdfRec.Code != http.StatusOK || pdfRec.Header().Get("Content-Type") != "application/pdf" ||
		pdfRec.Header().Get("Content-Disposition") != `attachment; filename="attendance_2025-06-02_2025-06-05.pdf"` {
		t.Fatalf("PDF: status %d, headers %v", pdfRec.Code, pdfRec.Header())
	}
	shown := pdfText(pdfRec.Body.Bytes())
	wantShown := []string{"Attendance summary", "2025-06-02 to 2025-06-05"}
	wantShown = append(wantShown, pdfHeader...)
	for _, total := range report.People {
		wantShown = append(wantShown, total.FullName, strconv.Itoa(total.Present), strconv.Itoa(total.Late),
			strconv.Itoa(total.OnLeave), strconv.Itoa(total.Absent), hours(total.WorkedMinutes))
	}
	wantShown = append(wantShown, "1 / 1") // the page number
	if !slices.Equal(shown, wantShown) {
		t.Errorf("PDF shows %q\nwant the JSON's totals %q", shown, wantShown)
	}
}

func TestReportErrors(t *testing.T) {
	s := newServer(t)
	lead := s.person(t, "Lead", middleware.RoleSupervisor, nil, nil)
	employee := s.person(t, "Employee", middleware.RoleEmployee, &lead.UserID, nil)
	failing := routerFor(serviceOn(failingDB(t, "users")))

	// Straight to the handlers, past middleware.Auth: no claims is a wiring bug, not the client's.
	unguarded := gin.New()
	reportcontract.RegisterHandlers(unguarded, reportcontract.NewStrictHandler(NewHandler(s.svc), nil))

	for _, path := range []string{"/reports/attendance", "/reports/attendance.csv", "/reports/attendance.pdf"} {
		checks := []struct {
			name   string
			rec    *httptest.ResponseRecorder
			status int
		}{
			{"an employee", get(t, s.router, path+week, employee), http.StatusForbidden},
			{"an unknown status", get(t, s.router, path+week+"&status=asleep", lead), http.StatusBadRequest},
			{"a failing database", get(t, failing, path+week, lead), http.StatusInternalServerError},
			{"no claims", get(t, unguarded, path+week, lead), http.StatusInternalServerError},
		}
		for _, check := range checks {
			if check.rec.Code != check.status {
				t.Errorf("%s, %s: status = %d, want %d; body = %s", path, check.name, check.rec.Code, check.status,
					check.rec.Body)
			}
		}
	}
}

func timestamp(at *time.Time) string {
	if at == nil {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}
