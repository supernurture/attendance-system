package attendance

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"attendance-system/internal/api/server/modules/schedule"
	"attendance-system/internal/api/server/modules/upload"
	"attendance-system/internal/middleware"
	"attendance-system/internal/pkg/apperr"
	"attendance-system/internal/pkg/storage"
)

// now is a seam, so a test can check in at a chosen moment. Postgres keeps microseconds, so nothing finer.
var now = func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// Mark is what the phone sends with a check-in or a check-out.
type Mark struct {
	PhotoKey     string
	Lat          float64
	Lng          float64
	AccuracyM    float64
	MockLocation bool
}

type Service struct {
	repo    *Repository
	plan    *schedule.Service    // rule A for one person
	roster  *schedule.Repository // offices, roster, holidays, and schedules by id
	uploads *upload.Service
	zone    *time.Location
	enforce bool
}

// NewService reads every schedule's times in zone. geofenceEnforce refuses a mark outside every office instead of
// flagging it.
func NewService(db *gorm.DB, store *storage.Storage, zone *time.Location, geofenceEnforce bool) *Service {
	return &Service{
		repo:    NewRepository(db, store),
		plan:    schedule.NewService(db, zone),
		roster:  schedule.NewRepository(db),
		uploads: upload.NewService(store),
		zone:    zone,
		enforce: geofenceEnforce,
	}
}

// CheckIn opens the attendance of the shift this moment belongs to, with that shift's schedule and lateness.
func (s *Service) CheckIn(ctx context.Context, userID int64, mark Mark) (Attendance, error) {
	if err := checkMark(mark); err != nil {
		return Attendance{}, err
	}

	at := now()
	today := dateIn(at, s.zone)
	days, err := s.plan.Days(ctx, userID, schedule.Range{From: today.AddDate(0, 0, -1), To: today.AddDate(0, 0, 1)})
	if err != nil {
		return Attendance{}, err
	}
	picked, err := pickShift(at, s.zone, days)
	if err != nil {
		return Attendance{}, err
	}
	evidence, err := s.evidence(ctx, userID, mark)
	if err != nil {
		return Attendance{}, err
	}

	row := Attendance{
		UserID:      userID,
		WorkDate:    picked.date,
		ScheduleID:  picked.scheduleID(),
		CheckInAt:   at,
		CheckIn:     evidence,
		LateMinutes: picked.lateMinutes(at),
	}
	if err := s.repo.Create(ctx, &row); err != nil {
		return Attendance{}, err
	}
	return row, nil
}

// CheckOut closes the caller's latest open check-in and counts how early they left. A check-in stops being
// closable checkOutClosesAfter past its shift's end, or maxOpenCheckIn after it on a day with no hours.
func (s *Service) CheckOut(ctx context.Context, userID int64, mark Mark) (Attendance, error) {
	if err := checkMark(mark); err != nil {
		return Attendance{}, err
	}

	at := now()
	row, err := s.repo.OpenCheckIn(ctx, userID, at.Add(-maxOpenCheckIn))
	if err != nil {
		return Attendance{}, err
	}
	hours, err := scheduleOf(ctx, s.roster, row.ScheduleID)
	if err != nil {
		return Attendance{}, err
	}
	measured := place(row.WorkDate, hours, s.zone)
	if hours != nil && at.After(measured.end.Add(checkOutClosesAfter)) {
		return Attendance{}, errNoOpenCheckIn
	}
	evidence, err := s.evidence(ctx, userID, mark)
	if err != nil {
		return Attendance{}, err
	}

	row.CheckOutAt, row.CheckOut = &at, evidence
	row.EarlyLeaveMinutes = measured.earlyLeaveMinutes(at)
	if err := s.repo.CheckOut(ctx, row); err != nil {
		return Attendance{}, err
	}
	return row, nil
}

// Mine lists the caller's attendance over a span.
func (s *Service) Mine(ctx context.Context, userID int64, span schedule.Range) ([]Attendance, error) {
	span, err := schedule.CheckRange(span)
	if err != nil {
		return nil, err
	}
	return s.repo.ListByUser(ctx, userID, span)
}

// SaveDailyReport replaces the report on the caller's attendance for that work date.
func (s *Service) SaveDailyReport(
	ctx context.Context, userID int64, workDate time.Time, content string,
) (Attendance, error) {
	content, err := checkReport(workDate, content)
	if err != nil {
		return Attendance{}, err
	}
	return s.repo.SaveReport(ctx, userID, dateOnly(workDate), content)
}

// PhotoURL signs a download of one side's selfie for its owner, a supervisor above them, or hr_admin.
func (s *Service) PhotoURL(ctx context.Context, claims middleware.Claims, id int64, side Side) (string, error) {
	if side != SideIn && side != SideOut {
		return "", apperr.Invalid("side must be %s or %s", SideIn, SideOut)
	}

	row, err := s.repo.ByID(ctx, id)
	if err != nil {
		return "", err
	}
	if err := s.reach(ctx, claims, row.UserID); err != nil {
		return "", err
	}

	key := row.CheckIn.PhotoKey
	if side == SideOut {
		key = row.CheckOut.PhotoKey
	}
	if key == nil {
		return "", fmt.Errorf("%w: attendance %d has no %s photo", apperr.ErrNotFound, id, side)
	}
	return s.repo.PhotoURL(ctx, *key)
}

// evidence places the phone against the offices and verifies its selfie. Outside every office is only flagged,
// unless the geofence is enforced.
func (s *Service) evidence(ctx context.Context, userID int64, mark Mark) (Evidence, error) {
	offices, err := s.roster.ListLocations(ctx)
	if err != nil {
		return Evidence{}, err
	}
	officeID, within := locate(offices, mark.Lat, mark.Lng)
	if s.enforce && !within {
		return Evidence{}, apperr.Invalid("the location is outside every office's geofence")
	}

	used, err := s.repo.PhotoKeyUsed(ctx, mark.PhotoKey)
	if err != nil {
		return Evidence{}, err
	}
	if used {
		return Evidence{}, errPhotoKeyUsed
	}
	if _, err := s.uploads.Verify(ctx, userID, upload.AttendancePhoto, mark.PhotoKey); err != nil {
		return Evidence{}, err
	}

	return Evidence{
		Lat: &mark.Lat, Lng: &mark.Lng, AccuracyM: &mark.AccuracyM, PhotoKey: &mark.PhotoKey,
		LocationID: officeID, WithinGeofence: &within, MockLocation: &mark.MockLocation,
	}, nil
}

// scheduleOf loads the hours an attendance kept, retired ones included; nil when it kept none.
func scheduleOf(ctx context.Context, roster *schedule.Repository, id *int64) (*schedule.WorkSchedule, error) {
	if id == nil {
		return nil, nil
	}
	found, err := roster.SchedulesByID(ctx, []int64{*id})
	if err != nil {
		return nil, err
	}
	hours := found[*id] // the foreign key keeps it there
	return &hours, nil
}

// reach is rule D for one person: the caller themselves, anyone below a supervisor, anyone at all for hr_admin.
func (s *Service) reach(ctx context.Context, claims middleware.Claims, userID int64) error {
	if claims.UserID == userID || claims.Role.AtLeast(middleware.RoleHRAdmin) {
		return nil
	}
	if !claims.Role.AtLeast(middleware.RoleSupervisor) {
		return apperr.ErrForbidden
	}

	below, err := s.repo.InSubtree(ctx, claims.UserID, userID)
	if err != nil {
		return err
	}
	if !below {
		return apperr.ErrForbidden
	}
	return nil
}

// scope is whose records a supervisor-and-up caller lists: nil for everyone, else their own subtree.
func scope(claims middleware.Claims) *int64 {
	if claims.Role.AtLeast(middleware.RoleHRAdmin) {
		return nil
	}
	return &claims.UserID
}
