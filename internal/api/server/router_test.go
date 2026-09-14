package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"attendance-system/internal/config"
	"attendance-system/internal/container"
	"attendance-system/pkg/logger"
)

func testConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Server.Mode = gin.TestMode
	cfg.Server.Timeout = time.Second
	return cfg
}

func newTestDeps(t *testing.T) *container.Container {
	t.Helper()
	log, err := logger.New(logger.Config{ServiceName: "test", Path: t.TempDir()})
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	db, err := gorm.Open(postgres.Open("host=127.0.0.1 port=1"), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	return &container.Container{
		Logger:   log,
		Postgres: map[string]*gorm.DB{"primary": db},
		Redis:    map[string]*goredis.Client{"cache": goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})},
	}
}

func post(router *gin.Engine, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	return rec
}

func newTestRouter(t *testing.T, cfg *config.Config, deps *container.Container) *gin.Engine {
	t.Helper()
	router, err := NewRouter(cfg, deps)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return router
}

func get(t *testing.T, router *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestNewRouterServesHealth(t *testing.T) {
	rec := get(t, newTestRouter(t, testConfig(), newTestDeps(t)), "/health")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got, want := rec.Body.String(), `{"condition":"Healthy"}`; got != want+"\n" && got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("X-Request-ID header missing, middleware chain did not run")
	}
}

func TestNewRouterRejectsBadTrustedProxy(t *testing.T) {
	cfg := testConfig()
	cfg.Server.TrustedProxies = []string{"not-an-ip"}

	if _, err := NewRouter(cfg, newTestDeps(t)); err == nil {
		t.Fatal("NewRouter accepted an invalid trusted proxy")
	}
}

func TestNewRouterRejectsAnUnknownTimezone(t *testing.T) {
	cfg := testConfig()
	cfg.Attendance.Timezone = "Mars/Olympus"

	if _, err := NewRouter(cfg, newTestDeps(t)); err == nil || !strings.Contains(err.Error(), "attendance.timezone") {
		t.Fatalf("err = %v, want the timezone named", err)
	}
}

func TestNewRouterRequiresPostgresAndRedis(t *testing.T) {
	for _, missing := range []string{"postgres", "redis"} {
		deps := newTestDeps(t)
		if missing == "postgres" {
			deps.Postgres = nil
		} else {
			deps.Redis = nil
		}

		if _, err := NewRouter(testConfig(), deps); err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("without %s: err = %v, want it named", missing, err)
		}
	}
}

func TestNewRouterRejectsABadSeedAdmin(t *testing.T) {
	cfg := testConfig()
	cfg.Auth.SeedAdminEmail = "admin@example.com"
	cfg.Auth.SeedAdminPassword = "short"

	if _, err := NewRouter(cfg, newTestDeps(t)); err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("err = %v, want the seed admin password rejected", err)
	}
}

func TestProtectedRouteWithoutTokenIs401(t *testing.T) {
	router := newTestRouter(t, testConfig(), newTestDeps(t))

	paths := []string{"/me", "/users", "/departments", "/attendance/whos-in?date=2030-03-04", "/leaves/me"}
	for _, path := range paths {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s: status = %d, want 401", path, rec.Code)
		}
	}

	rec := post(router, "/uploads/intent",
		`{"purpose":"attendance_photo","content_type":"image/jpeg","size_bytes":10}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("POST /uploads/intent: status = %d, want 401; body = %s", rec.Code, rec.Body)
	}
}

func TestMalformedBodyIs400(t *testing.T) {
	rec := post(newTestRouter(t, testConfig(), newTestDeps(t)), "/auth/login", `{"email":`)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"message"`) {
		t.Fatalf("got %d %s, want 400 with a message", rec.Code, rec.Body)
	}
}

func TestInternalErrorDoesNotLeakDetails(t *testing.T) {
	rec := post(newTestRouter(t, testConfig(), newTestDeps(t)), "/auth/logout", `{"refresh_token":"x"}`)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if got, want := rec.Body.String(), `{"message":"internal server error"}`; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}
