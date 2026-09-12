package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	authcontract "attendance-system/internal/api/server/oapicodegen/auth"
	"attendance-system/internal/middleware"
)

// Assembled rather than written out, so secret scanners do not read the fixture as a leaked key.
var testSecret = []byte(strings.Repeat("fixture-", 5))

const password = "correct horse battery"

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
	if !db.Migrator().HasTable("refresh_tokens") {
		t.Fatal("postgres is up but not migrated: run `make migrate-up`")
	}

	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// server is the module wired the way router.go wires it, plus a guarded route to spend a token on.
type server struct {
	router *gin.Engine
	db     *gorm.DB
	redis  *miniredis.Miniredis
	svc    *Service
}

func newServer(t *testing.T) *server {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := testDB(t)
	redis := miniredis.RunT(t)
	svc := NewService(db, goredis.NewClient(&goredis.Options{Addr: redis.Addr()}), testSecret)

	router := gin.New()
	router.ContextWithFallback = true
	authcontract.RegisterHandlers(router, authcontract.NewStrictHandler(NewHandler(svc), nil))
	router.GET("/me", middleware.Auth(testSecret), func(c *gin.Context) {
		claims, _ := middleware.ClaimsFrom(c)
		c.String(http.StatusOK, "%d|%s", claims.UserID, claims.Role)
	})
	return &server{router: router, db: db, redis: redis, svc: svc}
}

// createUser inserts a user with a unique email and removes it when the test ends.
func (s *server) createUser(t *testing.T, active bool) User {
	t.Helper()

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	user := User{
		Email:        fmt.Sprintf("user-%d@test.local", time.Now().UnixNano()),
		PasswordHash: string(hash),
		FullName:     "Test User",
		Role:         "employee",
		IsActive:     active,
	}
	if err := s.db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() { s.deleteUser(user.Email) })
	return user
}

func (s *server) deleteUser(email string) {
	s.db.Exec("DELETE FROM refresh_tokens WHERE user_id IN (SELECT id FROM users WHERE email = ?)", email)
	s.db.Exec("DELETE FROM users WHERE email = ?", email)
}

// signIn logs the user in through the service, as the handler would.
func (s *server) signIn(t *testing.T, email, clientIP string) TokenPair {
	t.Helper()

	pair, err := s.svc.Login(t.Context(), email, password, clientIP)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	return pair
}

func (s *server) post(t *testing.T, path, clientIP string, body any) *httptest.ResponseRecorder {
	t.Helper()

	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = clientIP + ":1234"

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	return rec
}

func (s *server) login(t *testing.T, email, pass string) *httptest.ResponseRecorder {
	t.Helper()
	return s.post(t, "/auth/login", "192.0.2.1", authcontract.LoginRequest{Email: email, Password: pass})
}

func (s *server) refresh(t *testing.T, token string) *httptest.ResponseRecorder {
	t.Helper()
	return s.post(t, "/auth/refresh", "192.0.2.1", authcontract.RefreshRequest{RefreshToken: token})
}

func (s *server) get(t *testing.T, path, accessToken string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	return rec
}

func tokens(t *testing.T, rec *httptest.ResponseRecorder) authcontract.TokenPair {
	t.Helper()

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body)
	}
	var pair authcontract.TokenPair
	if err := json.Unmarshal(rec.Body.Bytes(), &pair); err != nil {
		t.Fatalf("decode token pair: %v", err)
	}
	return pair
}

var errInjected = errors.New("injected failure")

// failingDB is a fresh handle on the test database on which every statement whose table is match, or
// whose SQL contains it, fails before it runs. An empty match fails everything: the database is down.
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
	} {
		if err != nil {
			t.Fatalf("register failure callback: %v", err)
		}
	}
	return db
}

// failingService runs against a database that fails every statement matching match.
func (s *server) failingService(t *testing.T, match string) *Service {
	t.Helper()
	return NewService(failingDB(t, match), goredis.NewClient(&goredis.Options{Addr: s.redis.Addr()}), testSecret)
}

// failDel fails DEL and lets every other command through.
type failDel struct{}

func (failDel) DialHook(next goredis.DialHook) goredis.DialHook { return next }

func (failDel) ProcessHook(next goredis.ProcessHook) goredis.ProcessHook {
	return func(ctx context.Context, cmd goredis.Cmder) error {
		if cmd.Name() == "del" {
			return errInjected
		}
		return next(ctx, cmd)
	}
}

func (failDel) ProcessPipelineHook(next goredis.ProcessPipelineHook) goredis.ProcessPipelineHook {
	return next
}
