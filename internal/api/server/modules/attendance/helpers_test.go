package attendance

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"attendance-system/internal/api/server/modules/schedule"
	"attendance-system/internal/api/server/modules/upload"
	attendancecontract "attendance-system/internal/api/server/oapicodegen/attendance"
	"attendance-system/internal/middleware"
	"attendance-system/internal/pkg/storage"
	"attendance-system/pkg/database"
)

// Assembled rather than written out, so secret scanners do not read the fixture as a leaked key.
var testSecret = []byte(strings.Repeat("fixture-", 5))

var jakarta = mustZone("Asia/Jakarta")

// The offices sit far from the ones other packages' tests create, which every check-in here also sees.
const (
	officeLat = 10.0
	officeLng = 20.0

	metersPerDegree = 2 * 3.141592653589793 * earthRadiusM / 360 // along a meridian
)

// Minimal JPEG: the magic bytes http.DetectContentType looks for.
var jpeg = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte("j"), 1000)...)

var errInjected = errors.New("injected failure")

func mustZone(name string) *time.Location {
	zone, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return zone
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func testDB(t *testing.T) *gorm.DB {
	t.Helper()

	// Built the way production builds it, so the tests run with the same pinned session timezone.
	port, _ := strconv.Atoi(envOr("POSTGRES_TEST_PORT", "5432"))
	dsn := database.PostgresDSN(
		envOr("POSTGRES_TEST_HOST", "localhost"), port,
		envOr("POSTGRES_TEST_USER", "postgres"), envOr("POSTGRES_TEST_PASSWORD", "postgres"),
		envOr("POSTGRES_TEST_DB", "attendance"), "sslmode=disable connect_timeout=2")
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err == nil {
		err = db.Exec("SELECT 1").Error
	}
	if err != nil {
		if os.Getenv("POSTGRES_TEST_REQUIRED") != "" {
			t.Fatalf("POSTGRES_TEST_REQUIRED is set but no postgres is reachable: %v", err)
		}
		t.Skipf("no postgres reachable (run `docker compose up -d`): %v", err)
	}
	if !db.Migrator().HasTable("attendance_corrections") {
		t.Fatal("postgres is up but not migrated: run `make migrate-up`")
	}

	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func testStore(t *testing.T, ttl time.Duration) *storage.Storage {
	t.Helper()

	store, err := storage.New(storage.Config{
		Endpoint:        envOr("STORAGE_TEST_ENDPOINT", "http://localhost:9000"),
		Region:          envOr("STORAGE_TEST_REGION", "auto"),
		Bucket:          envOr("STORAGE_TEST_BUCKET", "attendance"),
		AccessKeyID:     envOr("STORAGE_TEST_ACCESS_KEY_ID", "minioadmin"),
		SecretAccessKey: envOr("STORAGE_TEST_SECRET_ACCESS_KEY", "minioadmin"),
		ForcePathStyle:  true,
		PresignTTL:      ttl,
	})
	if err != nil {
		if os.Getenv("STORAGE_TEST_REQUIRED") != "" {
			t.Fatalf("STORAGE_TEST_REQUIRED is set but no object storage is reachable: %v", err)
		}
		t.Skipf("no object storage reachable (run `docker compose up -d`): %v", err)
	}
	return store
}

// server is the module wired the way router.go wires it, behind middleware.Auth.
type server struct {
	router *gin.Engine
	db     *gorm.DB
	store  *storage.Storage
	svc    *Service
	repo   *Repository

	userIDs     []int64
	scheduleIDs []int64
	holidayIDs  []int64
	locationIDs []int64
	keys        []string
}

func newServer(t *testing.T) *server {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := testDB(t)
	s := &server{db: db, store: testStore(t, time.Minute)}
	s.svc = NewService(db, s.store, jakarta, false)
	s.repo = s.svc.repo
	s.router = routerFor(s.svc)

	// Corrections and attendance point at users, offices and schedules, so they go first.
	t.Cleanup(func() {
		s.db.Exec("DELETE FROM attendance_corrections WHERE user_id IN ?", s.userIDs)
		s.db.Exec("DELETE FROM attendances WHERE user_id IN ?", s.userIDs)
		s.db.Exec("DELETE FROM leave_requests WHERE user_id IN ?", s.userIDs)
		s.db.Exec("DELETE FROM shift_assignments WHERE user_id IN ?", s.userIDs)
		s.db.Exec("UPDATE users SET manager_id = NULL, default_schedule_id = NULL WHERE id IN ?", s.userIDs)
		s.db.Exec("DELETE FROM users WHERE id IN ?", s.userIDs)
		s.db.Exec("DELETE FROM holidays WHERE id IN ?", s.holidayIDs)
		s.db.Exec("DELETE FROM office_locations WHERE id IN ?", s.locationIDs)
		s.db.Exec("DELETE FROM work_schedules WHERE id IN ?", s.scheduleIDs)
		for _, key := range s.keys {
			_ = s.store.Delete(context.Background(), key)
		}
	})
	return s
}

func routerFor(svc *Service) *gin.Engine {
	router := gin.New()
	router.ContextWithFallback = true
	attendancecontract.RegisterHandlers(router.Group("", middleware.Auth(testSecret)),
		attendancecontract.NewStrictHandler(NewHandler(svc), nil))
	return router
}

// person inserts an employee, since this module only reads users, and removes it afterwards.
func (s *server) person(t *testing.T, role middleware.Role, managerID, scheduleID *int64) middleware.Claims {
	t.Helper()

	var id int64
	err := s.db.Raw(`INSERT INTO users (email, password_hash, full_name, role, join_date, manager_id,
			default_schedule_id)
		VALUES (?, 'not used: tests sign their own tokens', ?, ?, '2020-01-01', ?, ?) RETURNING id`,
		unique("user")+"@test.local", fmt.Sprintf("User %02d", len(s.userIDs)+1), string(role),
		managerID, scheduleID).Scan(&id).Error
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	s.userIDs = append(s.userIDs, id)
	return middleware.Claims{UserID: id, Role: role}
}

// hours stores a schedule worked every day of the week that ignores public holidays.
func (s *server) hours(t *testing.T, start, end schedule.Clock, grace int) schedule.WorkSchedule {
	t.Helper()

	row := schedule.WorkSchedule{
		Name: unique("schedule"), StartTime: start, EndTime: end, GraceMinutes: grace,
	}
	err := s.db.Raw(`INSERT INTO work_schedules (name, start_time, end_time, grace_minutes, workdays,
			observes_holidays) VALUES (?, ?, ?, ?, '{1,2,3,4,5,6,7}', false) RETURNING id`,
		row.Name, start, end, grace).Scan(&row.ID).Error
	if err != nil {
		t.Fatalf("create work schedule: %v", err)
	}

	s.scheduleIDs = append(s.scheduleIDs, row.ID)
	return row
}

func (s *server) office(t *testing.T, lat, lng float64, radiusM int) int64 {
	t.Helper()

	var id int64
	err := s.db.Raw(`INSERT INTO office_locations (name, lat, lng, radius_m) VALUES (?, ?, ?, ?) RETURNING id`,
		unique("office"), lat, lng, radiusM).Scan(&id).Error
	if err != nil {
		t.Fatalf("create office location: %v", err)
	}

	s.locationIDs = append(s.locationIDs, id)
	return id
}

func (s *server) holiday(t *testing.T, date time.Time, name string) {
	t.Helper()

	var id int64
	if err := s.db.Raw(`INSERT INTO holidays (date, name) VALUES (?, ?) RETURNING id`, date, name).
		Scan(&id).Error; err != nil {
		t.Fatalf("create holiday: %v", err)
	}
	s.holidayIDs = append(s.holidayIDs, id)
}

func (s *server) rostered(t *testing.T, userID int64, date time.Time, scheduleID *int64) {
	t.Helper()

	if err := s.db.Exec(`INSERT INTO shift_assignments (user_id, work_date, schedule_id) VALUES (?, ?, ?)`,
		userID, date, scheduleID).Error; err != nil {
		t.Fatalf("create shift assignment: %v", err)
	}
}

// selfie uploads a JPEG the way a phone would, intent then PUT, and returns its key.
func (s *server) selfie(t *testing.T, userID int64) string {
	t.Helper()

	intent, err := upload.NewService(s.store).Intent(t.Context(), userID, upload.AttendancePhoto, "image/jpeg",
		int64(len(jpeg)))
	if err != nil {
		t.Fatalf("upload intent: %v", err)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPut, intent.URL, bytes.NewReader(jpeg))
	for name, value := range intent.Headers {
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT selfie: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT selfie status = %d", resp.StatusCode)
	}

	s.keys = append(s.keys, intent.Key)
	return intent.Key
}

// mark is a check-in or check-out at the test office, meters north of its center, with a fresh selfie.
func (s *server) mark(t *testing.T, userID int64, metersNorth float64) Mark {
	t.Helper()
	return Mark{PhotoKey: s.selfie(t, userID), Lat: officeLat + metersNorth/metersPerDegree, Lng: officeLng,
		AccuracyM: 8}
}

// checkedIn stores an attendance directly, with evidence, for tests that are not about checking in.
func (s *server) checkedIn(t *testing.T, userID int64, date, at time.Time, scheduleID *int64) Attendance {
	t.Helper()

	lat, lng, accuracy, within, mock := officeLat, officeLng, 5.0, true, false
	key := s.selfie(t, userID)
	row := Attendance{UserID: userID, WorkDate: date, ScheduleID: scheduleID, CheckInAt: at, CheckIn: Evidence{
		Lat: &lat, Lng: &lng, AccuracyM: &accuracy, PhotoKey: &key, WithinGeofence: &within, MockLocation: &mock,
	}}
	if err := s.repo.Create(t.Context(), &row); err != nil {
		t.Fatalf("create attendance: %v", err)
	}
	return row
}

// onLeave stores a leave request directly, in the status given.
func (s *server) onLeave(t *testing.T, userID int64, from, to time.Time, status string) {
	t.Helper()

	if err := s.db.Exec(`INSERT INTO leave_requests (user_id, leave_type_id, start_date, end_date, working_days,
			reason, status) SELECT ?, id, ?, ?, 1, 'away', ? FROM leave_types WHERE code = 'annual'`,
		userID, from, to, status).Error; err != nil {
		t.Fatalf("create leave request: %v", err)
	}
}

// fileCorrection stores a pending correction directly.
func (s *server) fileCorrection(t *testing.T, userID int64, date time.Time, in, out *time.Time) Correction {
	t.Helper()

	correction := Correction{UserID: userID, WorkDate: date, RequestedBy: userID, ProposedCheckInAt: in,
		ProposedCheckOutAt: out, Reason: "forgot", Status: correctionPending}
	if err := s.repo.CreateCorrection(t.Context(), &correction); err != nil {
		t.Fatalf("create correction: %v", err)
	}
	return correction
}

// clockAt pins now() to a moment for the rest of the test.
func clockAt(t *testing.T, moment time.Time) {
	t.Helper()

	original := now
	now = func() time.Time { return moment.UTC() }
	t.Cleanup(func() { now = original })
}

// do calls the API as that person would: through the router, with a signed token.
func (s *server) do(t *testing.T, method, path string, as middleware.Claims, body any) *httptest.ResponseRecorder {
	t.Helper()
	return doOn(t, s.router, method, path, as, body)
}

func doOn(
	t *testing.T, router *gin.Engine, method, path string, as middleware.Claims, body any,
) *httptest.ResponseRecorder {
	t.Helper()

	payload := []byte{}
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+middleware.SignAccessToken(testSecret, as, time.Minute))

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder, want int) T {
	t.Helper()

	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, want, rec.Body)
	}
	var decoded T
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode %T: %v", decoded, err)
	}
	return decoded
}

// failing is the service against a database where every statement touching match fails before it runs.
func (s *server) failing(t *testing.T, match string) *Service {
	t.Helper()
	return NewService(failingDB(t, match), s.store, jakarta, false)
}

// failingAfter fails only the statements whose built SQL contains match, so one query can be singled out.
func (s *server) failingAfter(t *testing.T, match string) *Service {
	t.Helper()
	return NewService(failingAfterDB(t, match), s.store, jakarta, false)
}

// failingDB is a fresh handle on the test database where every statement touching match fails before it runs,
// the way a dropped connection would.
func failingDB(t *testing.T, match string) *gorm.DB {
	t.Helper()

	db := testDB(t)
	fail := func(tx *gorm.DB) {
		if tx.Statement.Table == match || strings.Contains(tx.Statement.SQL.String(), match) {
			_ = tx.AddError(errInjected)
		}
	}
	callbacks := db.Callback()
	for _, err := range []error{
		callbacks.Create().Before("gorm:create").Register("test:fail", fail),
		callbacks.Query().Before("gorm:query").Register("test:fail", fail),
		callbacks.Row().Before("gorm:row").Register("test:fail", fail),
		callbacks.Raw().Before("gorm:raw").Register("test:fail", fail),
		callbacks.Update().Before("gorm:update").Register("test:fail", fail),
		callbacks.Delete().Before("gorm:delete").Register("test:fail", fail),
	} {
		if err != nil {
			t.Fatalf("register failure callback: %v", err)
		}
	}
	return db
}

// failingAfterDB fails the statements whose SQL contains match, hooked after the SQL is built.
func failingAfterDB(t *testing.T, match string) *gorm.DB {
	t.Helper()

	db := testDB(t)
	fail := func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), match) {
			_ = tx.AddError(errInjected)
		}
	}
	callbacks := db.Callback()
	for _, err := range []error{
		callbacks.Query().After("gorm:query").Register("test:fail-after", fail),
		callbacks.Row().After("gorm:row").Register("test:fail-after", fail),
		callbacks.Create().After("gorm:create").Register("test:fail-after", fail),
		callbacks.Update().After("gorm:update").Register("test:fail-after", fail),
	} {
		if err != nil {
			t.Fatalf("register failure callback: %v", err)
		}
	}
	return db
}

// unique builds a value no other test or package will repeat; the Windows clock is too coarse for that.
func unique(prefix string) string {
	return prefix + "-" + strings.ToLower(rand.Text())
}

func date(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// local is a wall-clock moment in Jakarta, the zone the tests' schedules are read in.
func local(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, jakarta)
}

func ptr[T any](value T) *T { return &value }

// sameID reports whether an optional id is want, where 0 stands for none.
func sameID(got *int64, want int64) bool {
	if got == nil {
		return want == 0
	}
	return *got == want
}

// neverPut is a well-formed key under the user's prefix that nothing was uploaded to.
func neverPut(userID int64) string {
	return fmt.Sprintf("attendance/%d/00000000-0000-0000-0000-000000000000.jpg", userID)
}
