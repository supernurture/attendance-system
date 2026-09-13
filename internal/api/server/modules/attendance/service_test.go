package attendance

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"attendance-system/internal/api/server/modules/schedule"
	"attendance-system/internal/middleware"
	"attendance-system/internal/pkg/apperr"
	"attendance-system/internal/pkg/storage"
)

func TestCheckInMeasuresTheShift(t *testing.T) {
	s := newServer(t)
	hours := s.hours(t, 8*60, 17*60, 15)
	employee := s.person(t, middleware.RoleEmployee, nil, &hours.ID)
	officeID := s.office(t, officeLat, officeLng, 100)

	clockAt(t, local(2030, time.March, 4, 8, 20))
	row, err := s.svc.CheckIn(t.Context(), employee.UserID, s.mark(t, employee.UserID, 30))
	if err != nil {
		t.Fatalf("CheckIn: %v", err)
	}

	if row.LateMinutes != 5 {
		t.Errorf("late_minutes = %d, want 5 for 08:20 against 08:00 with 15 minutes grace", row.LateMinutes)
	}
	if !row.WorkDate.Equal(date(2030, time.March, 4)) || *row.ScheduleID != hours.ID {
		t.Errorf("work date %v, schedule %v; want 2030-03-04 on %d", row.WorkDate, *row.ScheduleID, hours.ID)
	}
	if !*row.CheckIn.WithinGeofence || *row.CheckIn.LocationID != officeID {
		t.Errorf("30 m from the office: within = %v at %v, want inside %d",
			*row.CheckIn.WithinGeofence, *row.CheckIn.LocationID, officeID)
	}

	// What was returned is what was stored.
	stored, err := s.repo.ByID(t.Context(), row.ID)
	if err != nil || stored.LateMinutes != 5 || !stored.CheckInAt.Equal(row.CheckInAt) ||
		stored.CheckIn.PhotoKey == nil {
		t.Errorf("stored = %+v, %v", stored, err)
	}
}

func TestCheckInOutsideTheGeofenceIsFlaggedNotRefused(t *testing.T) {
	s := newServer(t)
	employee := s.person(t, middleware.RoleEmployee, nil, nil)
	officeID := s.office(t, officeLat, officeLng, 100)

	clockAt(t, local(2030, time.March, 4, 9, 0))
	row, err := s.svc.CheckIn(t.Context(), employee.UserID, s.mark(t, employee.UserID, 200))
	if err != nil {
		t.Fatalf("CheckIn 200 m out: %v", err)
	}
	if *row.CheckIn.WithinGeofence || *row.CheckIn.LocationID != officeID {
		t.Errorf("200 m from a 100 m office: within = %v, office %v; want flagged against %d",
			*row.CheckIn.WithinGeofence, *row.CheckIn.LocationID, officeID)
	}
	if row.ScheduleID != nil || row.LateMinutes != 0 {
		t.Errorf("no schedule: schedule %v, late %d; want neither", row.ScheduleID, row.LateMinutes)
	}

	// With the geofence enforced the same check-in is refused.
	enforced := NewService(s.db, s.store, jakarta, true)
	other := s.person(t, middleware.RoleEmployee, nil, nil)
	if _, err := enforced.CheckIn(t.Context(), other.UserID, s.mark(t, other.UserID, 200)); !apperr.IsValidation(err) {
		t.Errorf("enforced geofence: err = %v, want a validation error", err)
	}
	if _, err := enforced.CheckIn(t.Context(), other.UserID, s.mark(t, other.UserID, 50)); err != nil {
		t.Errorf("enforced geofence, inside: err = %v", err)
	}
}

func TestCheckInRefuses(t *testing.T) {
	s := newServer(t)
	hours := s.hours(t, 8*60, 17*60, 0)
	employee := s.person(t, middleware.RoleEmployee, nil, &hours.ID)
	ctx := t.Context()
	clockAt(t, local(2030, time.March, 4, 8, 0))

	if _, err := s.svc.CheckIn(ctx, employee.UserID, Mark{}); !apperr.IsValidation(err) {
		t.Errorf("empty mark: err = %v, want a validation error", err)
	}

	never := Mark{PhotoKey: neverPut(employee.UserID)}
	if _, err := s.svc.CheckIn(ctx, employee.UserID, never); !apperr.IsValidation(err) {
		t.Errorf("a photo_key never uploaded: err = %v, want a validation error", err)
	}

	other := s.person(t, middleware.RoleEmployee, nil, nil)
	if _, err := s.svc.CheckIn(ctx, employee.UserID, s.mark(t, other.UserID, 0)); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("someone else's photo: err = %v, want ErrForbidden", err)
	}

	if _, err := s.svc.CheckIn(ctx, 0, s.mark(t, employee.UserID, 0)); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("nobody: err = %v, want ErrNotFound", err)
	}

	first := s.mark(t, employee.UserID, 0)
	if _, err := s.svc.CheckIn(ctx, employee.UserID, first); err != nil {
		t.Fatalf("first CheckIn: %v", err)
	}
	if _, err := s.svc.CheckIn(ctx, employee.UserID, s.mark(t, employee.UserID, 0)); !errors.Is(err,
		apperr.ErrConflict) {
		t.Errorf("second CheckIn: err = %v, want ErrConflict", err)
	}
	if _, err := s.svc.CheckIn(ctx, other.UserID, first); !errors.Is(err, errPhotoKeyUsed) {
		t.Errorf("a key already used: err = %v, want errPhotoKeyUsed", err)
	}

	// Too early for today's shift, with nothing yesterday to belong to.
	clockAt(t, local(2030, time.March, 4, 3, 0))
	s.rostered(t, other.UserID, date(2030, time.March, 3), nil)
	s.rostered(t, other.UserID, date(2030, time.March, 4), &hours.ID)
	if _, err := s.svc.CheckIn(ctx, other.UserID, s.mark(t, other.UserID, 0)); !apperr.IsValidation(err) {
		t.Errorf("03:00 for 08:00: err = %v, want a validation error", err)
	}
}

func TestCheckInFaults(t *testing.T) {
	s := newServer(t)
	employee := s.person(t, middleware.RoleEmployee, nil, nil)
	clockAt(t, local(2030, time.March, 4, 9, 0))

	for _, table := range []string{"users", "office_locations"} {
		_, err := s.failing(t, table).CheckIn(t.Context(), employee.UserID, s.mark(t, employee.UserID, 0))
		if !errors.Is(err, errInjected) {
			t.Errorf("%s failing: err = %v, want the injected failure", table, err)
		}
	}

	_, err := s.failingAfter(t, "check_in_photo_key = ").CheckIn(t.Context(), employee.UserID,
		s.mark(t, employee.UserID, 0))
	if !errors.Is(err, errInjected) {
		t.Errorf("photo key lookup failing: err = %v, want the injected failure", err)
	}
}

func TestPhotoKeyIsUsedOnce(t *testing.T) {
	s := newServer(t)
	employee := s.person(t, middleware.RoleEmployee, nil, nil)

	clockAt(t, local(2030, time.March, 4, 9, 0))
	in := s.mark(t, employee.UserID, 0)
	if _, err := s.svc.CheckIn(t.Context(), employee.UserID, in); err != nil {
		t.Fatalf("CheckIn: %v", err)
	}

	// The check-in's selfie cannot serve as the check-out's, which the per-column UNIQUE alone would allow.
	clockAt(t, local(2030, time.March, 4, 17, 0))
	if _, err := s.svc.CheckOut(t.Context(), employee.UserID, in); !errors.Is(err, errPhotoKeyUsed) {
		t.Errorf("check-in key reused to check out: err = %v, want errPhotoKeyUsed", err)
	}
}

func TestCheckOut(t *testing.T) {
	s := newServer(t)
	night := s.hours(t, 22*60, 6*60, 0)
	employee := s.person(t, middleware.RoleEmployee, nil, &night.ID)
	ctx := t.Context()

	clockAt(t, local(2030, time.March, 4, 9, 0))
	if _, err := s.svc.CheckOut(ctx, employee.UserID, s.mark(t, employee.UserID, 0)); !errors.Is(err,
		errNoOpenCheckIn) {
		t.Fatalf("check-out without a check-in: err = %v, want errNoOpenCheckIn", err)
	}
	if _, err := s.svc.CheckOut(ctx, employee.UserID, Mark{}); !apperr.IsValidation(err) {
		t.Errorf("empty mark: err = %v, want a validation error", err)
	}

	// In at 21:55, out at 05:30 the next morning: one work date, 30 minutes early.
	clockAt(t, local(2030, time.March, 4, 21, 55))
	in, err := s.svc.CheckIn(ctx, employee.UserID, s.mark(t, employee.UserID, 0))
	if err != nil {
		t.Fatalf("CheckIn: %v", err)
	}
	clockAt(t, local(2030, time.March, 5, 5, 30))

	if _, err := s.svc.CheckOut(ctx, employee.UserID, Mark{PhotoKey: "attendance/1/x.jpg"}); !errors.Is(err,
		apperr.ErrForbidden) {
		t.Errorf("someone else's photo: err = %v, want ErrForbidden", err)
	}

	out, err := s.svc.CheckOut(ctx, employee.UserID, s.mark(t, employee.UserID, 500))
	if err != nil {
		t.Fatalf("CheckOut: %v", err)
	}
	if out.ID != in.ID || out.EarlyLeaveMinutes != 30 || !out.WorkDate.Equal(date(2030, time.March, 4)) {
		t.Errorf("check-out = id %d, early %d, date %v; want %d, 30, 2030-03-04",
			out.ID, out.EarlyLeaveMinutes, out.WorkDate, in.ID)
	}

	stored, _ := s.repo.ByID(ctx, in.ID)
	if stored.CheckOutAt == nil || stored.EarlyLeaveMinutes != 30 || *stored.CheckOut.WithinGeofence {
		t.Errorf("stored check-out = %+v", stored)
	}

	// Closed now, so a second check-out finds nothing to close.
	if _, err := s.svc.CheckOut(ctx, employee.UserID, s.mark(t, employee.UserID, 0)); !errors.Is(err,
		errNoOpenCheckIn) {
		t.Errorf("second check-out: err = %v, want errNoOpenCheckIn", err)
	}
}

func TestCheckOutLeavesAnOldCheckInToACorrection(t *testing.T) {
	s := newServer(t)
	employee := s.person(t, middleware.RoleEmployee, nil, nil)
	s.checkedIn(t, employee.UserID, date(2030, time.March, 4), local(2030, time.March, 4, 8, 0), nil)

	clockAt(t, local(2030, time.March, 5, 8, 1)) // 24 hours and a minute later
	if _, err := s.svc.CheckOut(t.Context(), employee.UserID, s.mark(t, employee.UserID, 0)); !errors.Is(err,
		errNoOpenCheckIn) {
		t.Errorf("a day-old check-in: err = %v, want errNoOpenCheckIn", err)
	}
}

func TestCheckOutFaults(t *testing.T) {
	s := newServer(t)
	hours := s.hours(t, 8*60, 17*60, 0)
	clockAt(t, local(2030, time.March, 4, 17, 0))

	// Each case gets its own open check-in: a write failed after it ran has still closed the one it hit.
	faults := map[string]*Service{
		"open check-in lookup": s.failing(t, "attendances"),
		"schedule lookup":      s.failing(t, "work_schedules"),
		"check-out write":      s.failingAfter(t, `UPDATE "attendances"`),
	}
	for name, svc := range faults {
		employee := s.person(t, middleware.RoleEmployee, nil, nil)
		s.checkedIn(t, employee.UserID, date(2030, time.March, 4), local(2030, time.March, 4, 8, 0), &hours.ID)
		if _, err := svc.CheckOut(t.Context(), employee.UserID, s.mark(t, employee.UserID, 0)); !errors.Is(err,
			errInjected) {
			t.Errorf("%s failing: err = %v, want the injected failure", name, err)
		}
	}
}

func TestMineAndDailyReport(t *testing.T) {
	s := newServer(t)
	employee := s.person(t, middleware.RoleEmployee, nil, nil)
	ctx := t.Context()
	s.checkedIn(t, employee.UserID, date(2030, time.March, 4), local(2030, time.March, 4, 8, 0), nil)
	s.checkedIn(t, employee.UserID, date(2030, time.March, 6), local(2030, time.March, 6, 8, 0), nil)

	rows, err := s.svc.Mine(ctx, employee.UserID, schedule.Range{From: date(2030, 3, 1), To: date(2030, 3, 5)})
	if err != nil || len(rows) != 1 || !rows[0].WorkDate.Equal(date(2030, time.March, 4)) {
		t.Errorf("Mine = %+v, %v; want only 2030-03-04", rows, err)
	}
	if _, err := s.svc.Mine(ctx, employee.UserID, schedule.Range{}); !apperr.IsValidation(err) {
		t.Errorf("Mine without a span: err = %v, want a validation error", err)
	}

	row, err := s.svc.SaveDailyReport(ctx, employee.UserID, date(2030, time.March, 4), " racked 3 servers ")
	if err != nil || row.DailyReport == nil || *row.DailyReport != "racked 3 servers" ||
		row.DailyReportUpdatedAt == nil {
		t.Fatalf("SaveDailyReport = %+v, %v", row, err)
	}
	if row.CheckIn.PhotoKey == nil {
		t.Error("the report's response lost the rest of the row")
	}

	if _, err := s.svc.SaveDailyReport(ctx, employee.UserID, date(2030, time.March, 5), "x"); !errors.Is(err,
		apperr.ErrNotFound) {
		t.Errorf("a date without attendance: err = %v, want ErrNotFound", err)
	}
	if _, err := s.svc.SaveDailyReport(ctx, employee.UserID, date(2030, time.March, 4), ""); !apperr.IsValidation(err) {
		t.Errorf("an empty report: err = %v, want a validation error", err)
	}
	if _, err := s.failing(t, "attendances").SaveDailyReport(ctx, employee.UserID, date(2030, 3, 4), "x"); !errors.Is(
		err, errInjected) {
		t.Errorf("report write failing: err = %v, want the injected failure", err)
	}
}

func TestPhotoURL(t *testing.T) {
	s := newServer(t)
	lead := s.person(t, middleware.RoleSupervisor, nil, nil)
	employee := s.person(t, middleware.RoleEmployee, &lead.UserID, nil)
	peer := s.person(t, middleware.RoleSupervisor, nil, nil)
	admin := s.person(t, middleware.RoleHRAdmin, nil, nil)
	ctx := t.Context()
	row := s.checkedIn(t, employee.UserID, date(2030, time.March, 4), local(2030, time.March, 4, 8, 0), nil)

	for name, claims := range map[string]middleware.Claims{"owner": employee, "their supervisor": lead, "hr": admin} {
		url, err := s.svc.PhotoURL(ctx, claims, row.ID, SideIn)
		if err != nil || url == "" {
			t.Errorf("%s: PhotoURL = %q, %v", name, url, err)
		}
	}
	for name, claims := range map[string]middleware.Claims{
		"another supervisor": peer, "a colleague": {UserID: peer.UserID, Role: middleware.RoleEmployee},
	} {
		if _, err := s.svc.PhotoURL(ctx, claims, row.ID, SideIn); !errors.Is(err, apperr.ErrForbidden) {
			t.Errorf("%s: err = %v, want ErrForbidden", name, err)
		}
	}

	if _, err := s.svc.PhotoURL(ctx, employee, row.ID, SideOut); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("no check-out yet: err = %v, want ErrNotFound", err)
	}
	if _, err := s.svc.PhotoURL(ctx, employee, 0, SideIn); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("no such attendance: err = %v, want ErrNotFound", err)
	}
	if _, err := s.svc.PhotoURL(ctx, employee, row.ID, "sideways"); !apperr.IsValidation(err) {
		t.Errorf("a side that is not in or out: err = %v, want a validation error", err)
	}
	if _, err := s.failingAfter(t, "subordinates").PhotoURL(ctx, lead, row.ID, SideIn); !errors.Is(err, errInjected) {
		t.Errorf("subtree lookup failing: err = %v, want the injected failure", err)
	}

	boom := errors.New("signer broke")
	original := presignGet
	presignGet = func(*storage.Storage, context.Context, string) (storage.PresignedURL, error) {
		return storage.PresignedURL{}, boom
	}
	t.Cleanup(func() { presignGet = original })
	if _, err := s.svc.PhotoURL(ctx, employee, row.ID, SideIn); !errors.Is(err, boom) {
		t.Errorf("presign failing: err = %v, want it passed on", err)
	}
}

// The redirect works while it is signed for and stops once it expires, which is the store's doing, not ours.
func TestPhotoURLExpires(t *testing.T) {
	s := newServer(t)
	employee := s.person(t, middleware.RoleEmployee, nil, nil)
	row := s.checkedIn(t, employee.UserID, date(2030, time.March, 4), local(2030, time.March, 4, 8, 0), nil)

	shortLived := NewService(s.db, testStore(t, 2*time.Second), jakarta, false)
	url, err := shortLived.PhotoURL(t.Context(), employee, row.ID, SideIn)
	if err != nil {
		t.Fatalf("PhotoURL: %v", err)
	}
	if status := fetch(t, url); status != http.StatusOK {
		t.Fatalf("GET the fresh URL = %d, want 200", status)
	}

	deadline := time.Now().Add(20 * time.Second) // generous: the store's clock need not match ours
	status := http.StatusOK
	for status == http.StatusOK && time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		status = fetch(t, url)
	}
	if status != http.StatusForbidden {
		t.Errorf("GET after the TTL = %d, want 403 from the store", status)
	}
}

func TestReachAndScope(t *testing.T) {
	s := newServer(t)
	lead := s.person(t, middleware.RoleSupervisor, nil, nil)
	mid := s.person(t, middleware.RoleSupervisor, &lead.UserID, nil)
	junior := s.person(t, middleware.RoleEmployee, &mid.UserID, nil)

	if err := s.svc.reach(t.Context(), lead, junior.UserID); err != nil {
		t.Errorf("two levels down: err = %v", err)
	}
	if err := s.svc.reach(t.Context(), mid, lead.UserID); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("upwards: err = %v, want ErrForbidden", err)
	}
	if got := scope(lead); got == nil || *got != lead.UserID {
		t.Errorf("a supervisor's scope = %v, want their own id", got)
	}
	if got := scope(middleware.Claims{UserID: 1, Role: middleware.RoleSuperAdmin}); got != nil {
		t.Errorf("a super_admin's scope = %v, want everyone", *got)
	}
}

func fetch(t *testing.T, url string) int {
	t.Helper()

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// Postgres keeps microseconds, so a moment returned to the client must not carry more than the row does.
func TestNowIsWhatPostgresStores(t *testing.T) {
	if got := now(); got.Location() != time.UTC || got.Nanosecond()%1000 != 0 {
		t.Errorf("now() = %v, want UTC truncated to microseconds", got)
	}
}

// A forgotten check-out stays closable into the evening, but not into the next morning's shift.
func TestCheckOutClosesOnlyNearTheShiftEnd(t *testing.T) {
	s := newServer(t)
	hours := s.hours(t, 8*60, 17*60, 0)
	late, early := s.person(t, middleware.RoleEmployee, nil, nil), s.person(t, middleware.RoleEmployee, nil, nil)
	for _, claims := range []middleware.Claims{late, early} {
		s.checkedIn(t, claims.UserID, date(2030, 3, 4), local(2030, 3, 4, 8, 0), &hours.ID)
	}

	clockAt(t, local(2030, time.March, 5, 1, 1)) // 8 hours and a minute past 17:00
	if _, err := s.svc.CheckOut(t.Context(), late.UserID, s.mark(t, late.UserID, 0)); !errors.Is(err,
		errNoOpenCheckIn) {
		t.Errorf("the next morning: err = %v, want errNoOpenCheckIn", err)
	}

	clockAt(t, local(2030, time.March, 5, 1, 0))
	row, err := s.svc.CheckOut(t.Context(), early.UserID, s.mark(t, early.UserID, 0))
	if err != nil || row.EarlyLeaveMinutes != 0 {
		t.Errorf("8 hours past the end = %+v, %v; want it closed with no early leave", row, err)
	}
}
