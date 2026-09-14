package leave

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

	"attendance-system/internal/api/server/modules/upload"
	leavecontract "attendance-system/internal/api/server/oapicodegen/leave"
	"attendance-system/internal/middleware"
	"attendance-system/internal/pkg/storage"
	"attendance-system/pkg/database"
)

// Assembled rather than written out, so secret scanners do not read the fixture as a leaked key.
var testSecret = []byte(strings.Repeat("fixture-", 5))

var jakarta = mustZone("Asia/Jakarta")

// A doctor's note, as far as http.DetectContentType can tell.
var pdf = append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("p"), 1000)...)

var errInjected = errors.New("injected failure")

// The tests plan in 2031, a year no other package's tests put holidays in: holidays.date is unique for everyone.
// 2031-03-03 is a Monday.
const year = 2031

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

func testStore(t *testing.T) *storage.Storage {
	t.Helper()

	store, err := storage.New(storage.Config{
		Endpoint:        envOr("STORAGE_TEST_ENDPOINT", "http://localhost:9000"),
		Region:          envOr("STORAGE_TEST_REGION", "auto"),
		Bucket:          envOr("STORAGE_TEST_BUCKET", "attendance"),
		AccessKeyID:     envOr("STORAGE_TEST_ACCESS_KEY_ID", "minioadmin"),
		SecretAccessKey: envOr("STORAGE_TEST_SECRET_ACCESS_KEY", "minioadmin"),
		ForcePathStyle:  true,
		PresignTTL:      time.Minute,
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
	typeIDs     []int64
	keys        []string
}

func newServer(t *testing.T) *server {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := testDB(t)
	s := &server{db: db, store: testStore(t)}
	s.svc = NewService(db, s.store, jakarta)
	s.repo = s.svc.repo
	s.router = routerFor(s.svc)
	// Pinned, or the rule refusing a past year would fail every 2031 request once the real clock reaches 2032.
	clockAt(t, time.Date(year, time.January, 15, 9, 0, 0, 0, jakarta))

	// Requests, quotas and audit entries point at users and types, so they go first.
	t.Cleanup(func() {
		s.db.Exec("DELETE FROM audit_logs WHERE actor_id IN ?", s.userIDs)
		s.db.Exec("DELETE FROM leave_requests WHERE user_id IN ?", s.userIDs)
		s.db.Exec("DELETE FROM leave_balances WHERE user_id IN ?", s.userIDs)
		s.db.Exec("UPDATE users SET manager_id = NULL, default_schedule_id = NULL WHERE id IN ?", s.userIDs)
		s.db.Exec("DELETE FROM users WHERE id IN ?", s.userIDs)
		s.db.Exec("DELETE FROM leave_types WHERE id IN ?", s.typeIDs)
		s.db.Exec("DELETE FROM holidays WHERE id IN ?", s.holidayIDs)
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
	leavecontract.RegisterHandlers(router.Group("", middleware.Auth(testSecret)),
		leavecontract.NewStrictHandler(NewHandler(svc), nil))
	return router
}

// person inserts someone who works Monday to Friday, observing public holidays, and removes them afterwards.
func (s *server) person(t *testing.T, role middleware.Role, managerID *int64) middleware.Claims {
	t.Helper()

	if len(s.scheduleIDs) == 0 {
		var id int64
		err := s.db.Raw(`INSERT INTO work_schedules (name, start_time, end_time, workdays, observes_holidays)
			VALUES (?, '08:00', '17:00', '{1,2,3,4,5}', true) RETURNING id`, unique("office hours")).Scan(&id).Error
		if err != nil {
			t.Fatalf("create work schedule: %v", err)
		}
		s.scheduleIDs = append(s.scheduleIDs, id)
	}

	var id int64
	err := s.db.Raw(`INSERT INTO users (email, password_hash, full_name, role, join_date, manager_id,
			default_schedule_id)
		VALUES (?, 'not used: tests sign their own tokens', 'Someone', ?, '2020-01-01', ?, ?) RETURNING id`,
		unique("user")+"@test.local", string(role), managerID, s.scheduleIDs[0]).Scan(&id).Error
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	s.userIDs = append(s.userIDs, id)
	return middleware.Claims{UserID: id, Role: role}
}

func (s *server) holiday(t *testing.T, date time.Time) {
	t.Helper()

	var id int64
	if err := s.db.Raw(`INSERT INTO holidays (date, name) VALUES (?, 'Hari Raya') RETURNING id`, date).
		Scan(&id).Error; err != nil {
		t.Fatalf("create holiday: %v", err)
	}
	s.holidayIDs = append(s.holidayIDs, id)
}

// kind stores a leave type of the test's own under a unique code, so the seeded ones stay untouched.
func (s *server) kind(t *testing.T, kind LeaveType) LeaveType {
	t.Helper()

	kind.Code = strings.ReplaceAll(unique("test"), "-", "_")
	if err := s.repo.CreateType(t.Context(), &kind); err != nil {
		t.Fatalf("create leave type: %v", err)
	}
	s.typeIDs = append(s.typeIDs, kind.ID)
	return kind
}

// seeded is one of the statutory types the migration inserts; tests only read them.
func (s *server) seeded(t *testing.T, code string) LeaveType {
	t.Helper()

	kind, err := s.repo.TypeByCode(t.Context(), code)
	if err != nil {
		t.Fatalf("seeded leave type %s: %v", code, err)
	}
	return kind
}

// attachment uploads a PDF the way a phone would, intent then PUT, and returns its key.
func (s *server) attachment(t *testing.T, userID int64) string {
	t.Helper()

	intent, err := upload.NewService(s.store).Intent(t.Context(), userID, upload.LeaveAttachment, "application/pdf",
		int64(len(pdf)))
	if err != nil {
		t.Fatalf("upload intent: %v", err)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPut, intent.URL, bytes.NewReader(pdf))
	for name, value := range intent.Headers {
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT attachment: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT attachment status = %d", resp.StatusCode)
	}

	s.keys = append(s.keys, intent.Key)
	return intent.Key
}

// file requests leave through the service and fails the test when it is refused.
func (s *server) file(t *testing.T, userID int64, code string, from, to time.Time, key *string) Leave {
	t.Helper()

	row, err := s.svc.Request(t.Context(), userID, Filing{Type: code, StartDate: from, EndDate: to,
		Reason: "family matters", AttachmentKey: key})
	if err != nil {
		t.Fatalf("request %s leave %s to %s: %v", code, day(from), day(to), err)
	}
	return row
}

// balance is the person's balance of one type in the test year.
func (s *server) balance(t *testing.T, userID, typeID int64) Balance {
	t.Helper()

	balances, err := s.repo.Balances(t.Context(), userID, year)
	if err != nil {
		t.Fatalf("Balances: %v", err)
	}
	for _, balance := range balances {
		if balance.LeaveTypeID == typeID {
			return balance
		}
	}
	t.Fatalf("no balance for type %d in %+v", typeID, balances)
	return Balance{}
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
	return NewService(failingDB(t, match), s.store, jakarta)
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

// failingAfterDB fails the statements whose built SQL contains match, so one query can be singled out.
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
		callbacks.Raw().After("gorm:raw").Register("test:fail-after", fail),
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

func date(month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func ptr[T any](value T) *T { return &value }

// neverPut is a well-formed key under the user's prefix that nothing was uploaded to.
func neverPut(userID int64) string {
	return fmt.Sprintf("leave/%d/00000000-0000-0000-0000-000000000000.pdf", userID)
}
