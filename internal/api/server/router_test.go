package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/supernurture/go-template/internal/config"
	"github.com/supernurture/go-template/internal/container"
	"github.com/supernurture/go-template/pkg/logger"
	"github.com/supernurture/go-template/pkg/redis"
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

	return &container.Container{Logger: log}
}

func withRedis(t *testing.T, deps *container.Container) *container.Container {
	t.Helper()
	server := miniredis.RunT(t)
	port, err := strconv.Atoi(server.Port())
	if err != nil {
		t.Fatalf("miniredis port %q: %v", server.Port(), err)
	}
	client, err := redis.New(server.Host(), port, "", "", 0, false, redis.PoolConfig{})
	if err != nil {
		t.Fatalf("redis.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	deps.Redis = map[string]*goredis.Client{"example": client}
	return deps
}

func withPostgres(t *testing.T, deps *container.Container) (*container.Container, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(
		postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}),
		&gorm.Config{DisableAutomaticPing: true},
	)
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}

	deps.Postgres = map[string]*gorm.DB{"example": db}
	return deps, mock
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

func TestNewRouterServesExample(t *testing.T) {
	deps, mock := withPostgres(t, withRedis(t, newTestDeps(t)))
	router := newTestRouter(t, testConfig(), deps)

	t.Run("visits counter comes from redis", func(t *testing.T) {
		for _, want := range []string{`{"visits":1}`, `{"visits":2}`} {
			rec := get(t, router, "/example/visits")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if got := rec.Body.String(); got != want+"\n" && got != want {
				t.Errorf("body = %q, want %q", got, want)
			}
		}
	})

	t.Run("notes come from postgres", func(t *testing.T) {
		created := time.Date(2026, 8, 17, 10, 30, 0, 0, time.UTC)
		mock.ExpectQuery(`SELECT \* FROM "example_notes"`).
			WillReturnRows(sqlmock.NewRows([]string{"id", "title", "body", "created_at"}).
				AddRow(int64(1), "First note", "the body", created))

		rec := get(t, router, "/example/notes")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := rec.Body.String(); !strings.Contains(got, `"title":"First note"`) {
			t.Errorf("body = %q, want the stored note", got)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
}

func TestNewRouterSkipsExampleWithoutEveryDependency(t *testing.T) {
	paths := []string{"/example/visits", "/example/notes"}

	setups := map[string]func(t *testing.T) *container.Container{
		"nothing configured": func(t *testing.T) *container.Container { return newTestDeps(t) },
		"only redis":         func(t *testing.T) *container.Container { return withRedis(t, newTestDeps(t)) },
		"only postgres": func(t *testing.T) *container.Container {
			deps, _ := withPostgres(t, newTestDeps(t))
			return deps
		},
	}

	for name, setup := range setups {
		t.Run(name, func(t *testing.T) {
			router := newTestRouter(t, testConfig(), setup(t))
			for _, path := range paths {
				if rec := get(t, router, path); rec.Code != http.StatusNotFound {
					t.Errorf("%s: status = %d, want %d", path, rec.Code, http.StatusNotFound)
				}
			}
		})
	}
}

func TestNewRouterRejectsBadTrustedProxy(t *testing.T) {
	cfg := testConfig()
	cfg.Server.TrustedProxies = []string{"not-an-ip"}

	if _, err := NewRouter(cfg, newTestDeps(t)); err == nil {
		t.Fatal("NewRouter accepted an invalid trusted proxy")
	}
}
