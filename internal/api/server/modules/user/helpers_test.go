package user

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	usercontract "attendance-system/internal/api/server/oapicodegen/user"
	"attendance-system/internal/middleware"
)

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

	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable connect_timeout=2",
		envOr("POSTGRES_TEST_HOST", "localhost"), envOr("POSTGRES_TEST_PORT", "5432"),
		envOr("POSTGRES_TEST_USER", "postgres"), envOr("POSTGRES_TEST_PASSWORD", "postgres"),
		envOr("POSTGRES_TEST_DB", "attendance"))
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
	if !db.Migrator().HasTable("departments") {
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

	userIDs       []int64
	departmentIDs []int64
}

func newServer(t *testing.T) *server {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := testDB(t)
	s := &server{db: db, svc: NewService(db), repo: NewRepository(db)}

	router := gin.New()
	router.ContextWithFallback = true
	usercontract.RegisterHandlers(router.Group("", middleware.Auth(testSecret)),
		usercontract.NewStrictHandler(NewHandler(s.svc), nil))
	s.router = router

	// Rows reference each other through manager_id and audit_logs.actor_id, so they go together.
	t.Cleanup(func() {
		s.db.Exec("DELETE FROM audit_logs WHERE actor_id IN ? OR entity_id IN ?", s.userIDs, s.userIDs)
		s.db.Exec("UPDATE users SET manager_id = NULL WHERE id IN ?", s.userIDs)
		s.db.Exec("DELETE FROM refresh_tokens WHERE user_id IN ?", s.userIDs)
		s.db.Exec("DELETE FROM users WHERE id IN ?", s.userIDs)
		s.db.Exec("DELETE FROM departments WHERE id IN ?", s.departmentIDs)
	})
	return s
}

// addUser inserts a user with the given role and manager, and removes it when the test ends.
func (s *server) addUser(t *testing.T, role middleware.Role, managerID *int64) User {
	t.Helper()

	user := User{
		Email:        unique("user") + "@test.local",
		PasswordHash: "not used: tests sign their own tokens",
		FullName:     fmt.Sprintf("User %d", len(s.userIDs)+1),
		Role:         string(role),
		IsActive:     true,
		JoinDate:     time.Now(),
		ManagerID:    managerID,
	}
	if err := s.db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	s.userIDs = append(s.userIDs, user.ID)
	return user
}

func (s *server) addDepartment(t *testing.T, name string) Department {
	t.Helper()

	department := Department{Name: name}
	if err := s.db.Create(&department).Error; err != nil {
		t.Fatalf("create department: %v", err)
	}
	s.departmentIDs = append(s.departmentIDs, department.ID)
	return department
}

// track registers a row the service created, so the cleanup above reaches it too.
func (s *server) track(user User) User {
	s.userIDs = append(s.userIDs, user.ID)
	return user
}

func claimsOf(user User) middleware.Claims {
	return middleware.Claims{UserID: user.ID, Role: middleware.Role(user.Role)}
}

// do calls the API as user would: through the router, with a signed token.
func (s *server) do(t *testing.T, method, path string, as User, body any) *httptest.ResponseRecorder {
	t.Helper()

	var payload *strings.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		payload = strings.NewReader(string(encoded))
	} else {
		payload = strings.NewReader("")
	}

	req := httptest.NewRequest(method, path, payload)
	req.Header.Set("Content-Type", "application/json")
	token := middleware.SignAccessToken(testSecret, claimsOf(as), time.Minute)
	req.Header.Set("Authorization", "Bearer "+token)

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

// names lists the full names a response carries, which is what the scope assertions compare.
func names(users []usercontract.User) []string {
	listed := make([]string, 0, len(users))
	for _, user := range users {
		listed = append(listed, user.FullName)
	}
	return listed
}

func ids(users []User) []int64 {
	listed := make([]int64, 0, len(users))
	for _, user := range users {
		listed = append(listed, user.ID)
	}
	return listed
}

var errInjected = errors.New("injected failure")

// failingDB is a fresh handle on the test database where every statement touching match fails
// before it runs, the way a dropped connection would.
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

// failingRouter serves the module against a database that fails every statement.
func (s *server) failingRouter(t *testing.T) *gin.Engine {
	t.Helper()

	router := gin.New()
	router.ContextWithFallback = true
	svc := &Service{repo: NewRepository(failingDB(t, ""))}
	usercontract.RegisterHandlers(router.Group("", middleware.Auth(testSecret)),
		usercontract.NewStrictHandler(NewHandler(svc), nil))
	return router
}

// doOn is do against another router, for the fault cases.
func (s *server) doOn(
	t *testing.T, router *gin.Engine, method, path string, as User, body any,
) *httptest.ResponseRecorder {
	t.Helper()

	original := s.router
	s.router = router
	defer func() { s.router = original }()
	return s.do(t, method, path, as, body)
}

// auditLogs reads what the module wrote; nothing in production reads audit_logs yet.
func (s *server) auditLogs(t *testing.T, entityID int64) []AuditLog {
	t.Helper()

	var logs []AuditLog
	err := s.db.Where("entity_type = ? AND entity_id = ?", entityUser, entityID).Order("id").Find(&logs).Error
	if err != nil {
		t.Fatalf("read audit_logs: %v", err)
	}
	return logs
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
		callbacks.Update().After("gorm:update").Register("test:fail-after", fail),
		callbacks.Delete().After("gorm:delete").Register("test:fail-after", fail),
	} {
		if err != nil {
			t.Fatalf("register failure callback: %v", err)
		}
	}
	return db
}

// unique builds a value no other test or package will repeat. time.Now().UnixNano() is not enough:
// the Windows clock is coarse, so two packages running at once produced the same email and collided
// on the unique index.
func unique(prefix string) string {
	return prefix + "-" + strings.ToLower(rand.Text()) // rand.Text: 26 random base32 characters
}
