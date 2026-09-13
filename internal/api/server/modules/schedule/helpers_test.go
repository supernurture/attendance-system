package schedule

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"attendance-system/pkg/database"

	schedulecontract "attendance-system/internal/api/server/oapicodegen/schedule"
	"attendance-system/internal/middleware"
)

// testZone is far enough from UTC that a test measuring "today" in the wrong zone fails for 7 hours a day.
var testZone, _ = time.LoadLocation("Asia/Jakarta")

// Assembled rather than written out, so secret scanners do not read the fixture as a leaked key.
var testSecret = []byte(strings.Repeat("fixture-", 5))

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
	if !db.Migrator().HasTable("work_schedules") {
		t.Fatal("postgres is up but not migrated: run `make migrate-up`")
	}

	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// server is the module wired the way router.go wires it, behind middleware.Auth.
type server struct {
	router *gin.Engine
	db     *gorm.DB
	svc    *Service
	repo   *Repository

	userIDs     []int64
	scheduleIDs []int64
	holidayIDs  []int64
	locationIDs []int64
}

func newServer(t *testing.T) *server {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := testDB(t)
	s := &server{db: db, svc: NewService(db, testZone), repo: NewRepository(db)}

	router := gin.New()
	router.ContextWithFallback = true
	schedulecontract.RegisterHandlers(router.Group("", middleware.Auth(testSecret)),
		schedulecontract.NewStrictHandler(NewHandler(s.svc), nil))
	s.router = router

	// Assignments and default_schedule_id point at the other rows, so the order matters.
	t.Cleanup(func() {
		s.db.Exec("DELETE FROM audit_logs WHERE actor_id IN ? OR entity_id IN ?", s.userIDs, s.userIDs)
		s.db.Exec("DELETE FROM audit_logs WHERE entity_type = ? AND entity_id IN ?", entityHoliday, s.holidayIDs)
		s.db.Exec("DELETE FROM shift_assignments WHERE user_id IN ?", s.userIDs)
		s.db.Exec("UPDATE users SET default_schedule_id = NULL WHERE id IN ?", s.userIDs)
		s.db.Exec("DELETE FROM users WHERE id IN ?", s.userIDs)
		s.db.Exec("DELETE FROM holidays WHERE id IN ?", s.holidayIDs)
		s.db.Exec("DELETE FROM office_locations WHERE id IN ?", s.locationIDs)
		s.db.Exec("DELETE FROM work_schedules WHERE id IN ?", s.scheduleIDs)
	})
	return s
}

// person inserts an employee, since this module only ever reads users, and removes it afterwards.
func (s *server) person(t *testing.T, role middleware.Role, defaultScheduleID *int64) employee {
	t.Helper()

	var id int64
	err := s.db.Raw(`INSERT INTO users (email, password_hash, full_name, role, is_active, join_date,
			default_schedule_id)
		VALUES (?, ?, ?, ?, true, CURRENT_DATE, ?) RETURNING id`,
		unique("user")+"@test.local", "not used: tests sign their own tokens",
		fmt.Sprintf("User %d", len(s.userIDs)+1), string(role), defaultScheduleID).Scan(&id).Error
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	s.userIDs = append(s.userIDs, id)
	return employee{ID: id, DefaultScheduleID: defaultScheduleID}
}

// weekdaySchedule is the common fixture: Monday to Friday, 08:00 to 17:00.
func (s *server) weekdaySchedule(t *testing.T) WorkSchedule {
	t.Helper()
	return s.addSchedule(t, WorkSchedule{
		StartTime: 8 * 60, EndTime: 17 * 60, Workdays: pqInt16Array{1, 2, 3, 4, 5},
	})
}

// addSchedule stores a work schedule, filling in the fields the test left out.
func (s *server) addSchedule(t *testing.T, schedule WorkSchedule) WorkSchedule {
	t.Helper()

	if schedule.Name == "" {
		schedule.Name = unique("schedule")
	}
	if len(schedule.Workdays) == 0 {
		schedule.Workdays = pqInt16Array{1, 2, 3, 4, 5}
	}
	if err := s.db.Create(&schedule).Error; err != nil {
		t.Fatalf("create work schedule: %v", err)
	}

	s.scheduleIDs = append(s.scheduleIDs, schedule.ID)
	return schedule
}

func (s *server) addHoliday(t *testing.T, date time.Time, name string) Holiday {
	t.Helper()

	holiday := Holiday{Date: dateOnly(date), Name: name}
	if err := s.db.Create(&holiday).Error; err != nil {
		t.Fatalf("create holiday: %v", err)
	}

	s.holidayIDs = append(s.holidayIDs, holiday.ID)
	return holiday
}

func (s *server) addLocation(t *testing.T) OfficeLocation {
	t.Helper()

	location := OfficeLocation{Name: unique("office"), Lat: -6.2088, Lng: 106.8456, RadiusM: 100}
	if err := s.db.Create(&location).Error; err != nil {
		t.Fatalf("create office location: %v", err)
	}

	s.locationIDs = append(s.locationIDs, location.ID)
	return location
}

// addAssignment rosters one person onto one date, or onto a day off when scheduleID is nil.
func (s *server) addAssignment(t *testing.T, userID int64, date time.Time, scheduleID *int64) ShiftAssignment {
	t.Helper()

	assignment := ShiftAssignment{UserID: userID, WorkDate: dateOnly(date), ScheduleID: scheduleID}
	if err := s.db.Create(&assignment).Error; err != nil {
		t.Fatalf("create shift assignment: %v", err)
	}
	return assignment
}

// track registers rows the service created, so the cleanup reaches them too.
func (s *server) trackSchedule(schedule WorkSchedule) WorkSchedule {
	s.scheduleIDs = append(s.scheduleIDs, schedule.ID)
	return schedule
}

func (s *server) trackHoliday(holiday Holiday) Holiday {
	s.holidayIDs = append(s.holidayIDs, holiday.ID)
	return holiday
}

func (s *server) trackLocation(location OfficeLocation) OfficeLocation {
	s.locationIDs = append(s.locationIDs, location.ID)
	return location
}

// auditLogs reads what the module wrote; nothing in production reads audit_logs yet.
func (s *server) auditLogs(t *testing.T, entityType string, entityID int64) []AuditLog {
	t.Helper()

	var logs []AuditLog
	err := s.db.Where("entity_type = ? AND entity_id = ?", entityType, entityID).Order("id").Find(&logs).Error
	if err != nil {
		t.Fatalf("read audit_logs: %v", err)
	}
	return logs
}

// sameJSON compares an audit payload by content: jsonb reformats what it stores, so the bytes that
// come back are never byte-for-byte what was written.
func sameJSON(t *testing.T, got []byte, want string) bool {
	t.Helper()

	var decodedGot, decodedWant any
	if err := json.Unmarshal(got, &decodedGot); err != nil {
		t.Fatalf("decode %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &decodedWant); err != nil {
		t.Fatalf("decode %s: %v", want, err)
	}
	return reflect.DeepEqual(decodedGot, decodedWant)
}

func claimsOf(person employee, role middleware.Role) middleware.Claims {
	return middleware.Claims{UserID: person.ID, Role: role}
}

// do calls the API as that person would: through the router, with a signed token.
func (s *server) do(
	t *testing.T, method, path string, as middleware.Claims, body any,
) *httptest.ResponseRecorder {
	t.Helper()

	payload := strings.NewReader("")
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		payload = strings.NewReader(string(encoded))
	}

	req := httptest.NewRequest(method, path, payload)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+middleware.SignAccessToken(testSecret, as, time.Minute))

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
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

var errInjected = errors.New("injected failure")

// failingDB is a fresh handle on the test database where every statement touching match fails before
// it runs, the way a dropped connection would.
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

// failingAfterDB fails the statements whose SQL contains match, hooked after the SQL is built so a
// single query can be singled out. Matching before it runs only sees the table name.
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
		callbacks.Create().After("gorm:create").Register("test:fail-after", fail),
		callbacks.Update().After("gorm:update").Register("test:fail-after", fail),
		callbacks.Delete().After("gorm:delete").Register("test:fail-after", fail),
	} {
		if err != nil {
			t.Fatalf("register failure callback: %v", err)
		}
	}
	return db
}

// failing is the service against a database where every statement touching match fails.
func (s *server) failing(t *testing.T, match string) *Service {
	t.Helper()
	return NewService(failingDB(t, match), testZone)
}

// failingRouter serves the module against a database that fails every statement.
func (s *server) failingRouter(t *testing.T) *gin.Engine {
	t.Helper()

	router := gin.New()
	router.ContextWithFallback = true
	schedulecontract.RegisterHandlers(router.Group("", middleware.Auth(testSecret)),
		schedulecontract.NewStrictHandler(NewHandler(s.failing(t, "")), nil))
	return router
}

// doOn is do against another router, for the fault cases.
func (s *server) doOn(
	t *testing.T, router *gin.Engine, method, path string, as middleware.Claims, body any,
) *httptest.ResponseRecorder {
	t.Helper()

	original := s.router
	s.router = router
	defer func() { s.router = original }()
	return s.do(t, method, path, as, body)
}

// unique builds a value no other test or package will repeat. time.Now().UnixNano() is not enough:
// the Windows clock is coarse, so two packages running at once collided on the unique index.
func unique(prefix string) string {
	return prefix + "-" + strings.ToLower(rand.Text()) // rand.Text: 26 random base32 characters
}

// day is a fixed calendar date, so a test never depends on what today happens to be.
func day(year int, month time.Month, date int) time.Time {
	return time.Date(year, month, date, 0, 0, 0, 0, time.UTC)
}

// pastDate and futureDate sit either side of today, which is what decides whether an edit is audited.
func pastDate(days int) time.Time {
	return dateOnly(time.Now().In(testZone)).AddDate(0, 0, -days)
}

func futureDate(days int) time.Time {
	return dateOnly(time.Now().In(testZone)).AddDate(0, 0, days)
}
