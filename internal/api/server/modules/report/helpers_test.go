package report

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"attendance-system/internal/api/server/modules/attendance"
	reportcontract "attendance-system/internal/api/server/oapicodegen/report"
	"attendance-system/internal/middleware"
	"attendance-system/pkg/database"
)

// Assembled rather than written out, so secret scanners do not read the fixture as a leaked key.
var testSecret = []byte(strings.Repeat("fixture-", 5))

var jakarta = mustZone("Asia/Jakarta")

var errInjected = errors.New("injected failure")

// The week the tests report on: long past, so no day is still not_yet, and far from other packages' dates.
var (
	monday    = date(2025, time.June, 2)
	tuesday   = date(2025, time.June, 3)
	wednesday = date(2025, time.June, 4)
	thursday  = date(2025, time.June, 5)
)

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
	if !db.Migrator().HasTable("leave_requests") {
		t.Fatal("postgres is up but not migrated: run `make migrate-up`")
	}

	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// failingDB is a fresh handle on the test database where every query on match fails before it runs.
func failingDB(t *testing.T, match string) *gorm.DB {
	t.Helper()

	db := testDB(t)
	err := db.Callback().Query().Before("gorm:query").Register("test:fail", func(tx *gorm.DB) {
		if tx.Statement.Table == match {
			_ = tx.AddError(errInjected)
		}
	})
	if err != nil {
		t.Fatalf("register failure callback: %v", err)
	}
	return db
}

// server is the module wired the way router.go wires it, behind middleware.Auth. Reports never touch the store.
type server struct {
	router *gin.Engine
	db     *gorm.DB
	svc    *Service

	userIDs     []int64
	scheduleIDs []int64
	holidayIDs  []int64
}

func newServer(t *testing.T) *server {
	t.Helper()
	gin.SetMode(gin.TestMode)

	s := &server{db: testDB(t)}
	s.svc = serviceOn(s.db)
	s.router = routerFor(s.svc)

	t.Cleanup(func() {
		s.db.Exec("DELETE FROM attendances WHERE user_id IN ?", s.userIDs)
		s.db.Exec("DELETE FROM leave_requests WHERE user_id IN ?", s.userIDs)
		s.db.Exec("DELETE FROM shift_assignments WHERE user_id IN ?", s.userIDs)
		s.db.Exec("UPDATE users SET manager_id = NULL, default_schedule_id = NULL WHERE id IN ?", s.userIDs)
		s.db.Exec("DELETE FROM users WHERE id IN ?", s.userIDs)
		s.db.Exec("DELETE FROM holidays WHERE id IN ?", s.holidayIDs)
		s.db.Exec("DELETE FROM work_schedules WHERE id IN ?", s.scheduleIDs)
	})
	return s
}

func serviceOn(db *gorm.DB) *Service {
	return NewService(attendance.NewService(db, nil, jakarta, false))
}

func routerFor(svc *Service) *gin.Engine {
	router := gin.New()
	router.ContextWithFallback = true
	reportcontract.RegisterHandlers(router.Group("", middleware.Auth(testSecret)),
		reportcontract.NewStrictHandler(NewHandler(svc), nil))
	return router
}

// person inserts someone named name, since this module only reads users.
func (s *server) person(
	t *testing.T, name string, role middleware.Role, managerID, scheduleID *int64,
) middleware.Claims {
	t.Helper()

	var id int64
	err := s.db.Raw(`INSERT INTO users (email, password_hash, full_name, role, join_date, manager_id,
			default_schedule_id)
		VALUES (?, 'not used: tests sign their own tokens', ?, ?, '2020-01-01', ?, ?) RETURNING id`,
		unique("user")+"@test.local", name, string(role), managerID, scheduleID).Scan(&id).Error
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	s.userIDs = append(s.userIDs, id)
	return middleware.Claims{UserID: id, Role: role}
}

// hours stores a Monday-to-Friday 08:00-17:00 schedule; observes says whether it takes public holidays off.
func (s *server) hours(t *testing.T, observes bool) int64 {
	t.Helper()

	var id int64
	err := s.db.Raw(`INSERT INTO work_schedules (name, start_time, end_time, grace_minutes, workdays,
			observes_holidays) VALUES (?, '08:00', '17:00', 0, '{1,2,3,4,5}', ?) RETURNING id`,
		unique("schedule"), observes).Scan(&id).Error
	if err != nil {
		t.Fatalf("create work schedule: %v", err)
	}

	s.scheduleIDs = append(s.scheduleIDs, id)
	return id
}

func (s *server) holiday(t *testing.T, date time.Time) {
	t.Helper()

	var id int64
	if err := s.db.Raw(`INSERT INTO holidays (date, name) VALUES (?, 'Test holiday') RETURNING id`, date).
		Scan(&id).Error; err != nil {
		t.Fatalf("create holiday: %v", err)
	}
	s.holidayIDs = append(s.holidayIDs, id)
}

func (s *server) exec(t *testing.T, sql string, values ...any) {
	t.Helper()

	if err := s.db.Exec(sql, values...).Error; err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// worked stores a day checked in and out at those Jakarta wall-clock hours, late by late minutes.
func (s *server) worked(t *testing.T, userID int64, date time.Time, in, out string, late int) {
	t.Helper()

	at := func(clock string) time.Time {
		moment, _ := time.ParseInLocation(time.DateTime, date.Format(time.DateOnly)+" "+clock+":00", jakarta)
		return moment
	}
	s.exec(t, `INSERT INTO attendances (user_id, work_date, check_in_at, check_out_at, late_minutes)
		VALUES (?, ?, ?, ?, ?)`, userID, date, at(in), at(out), late)
}

func (s *server) onLeave(t *testing.T, userID int64, from, to time.Time) {
	t.Helper()
	s.exec(t, `INSERT INTO leave_requests (user_id, leave_type_id, start_date, end_date, working_days, reason,
			status) SELECT ?, id, ?, ?, 1, 'away', 'approved' FROM leave_types WHERE code = 'annual'`,
		userID, from, to)
}

func (s *server) rosteredOff(t *testing.T, userID int64, date time.Time) {
	t.Helper()
	s.exec(t, `INSERT INTO shift_assignments (user_id, work_date, schedule_id) VALUES (?, ?, NULL)`, userID, date)
}

// get calls the API as that person would: through the router, with a signed token.
func get(t *testing.T, router *gin.Engine, path string, as middleware.Claims) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
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
	if err := json.NewDecoder(bytes.NewReader(rec.Body.Bytes())).Decode(&decoded); err != nil {
		t.Fatalf("decode %T: %v", decoded, err)
	}
	return decoded
}

// unique builds a value no other test or package will repeat; the Windows clock is too coarse for that.
func unique(prefix string) string {
	return prefix + "-" + strings.ToLower(rand.Text())
}

func date(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

var shownText = regexp.MustCompile(`\(((?:\.|[^\)])*)\) Tj`)

// pdfText is every string the PDF shows, in drawing order. Maroto leaves its content streams uncompressed.
func pdfText(pdf []byte) []string {
	var shown []string
	for _, match := range shownText.FindAllSubmatch(pdf, -1) {
		shown = append(shown, strings.NewReplacer(`\(`, "(", `\)`, ")", `\`, `\`).Replace(string(match[1])))
	}
	return shown
}
