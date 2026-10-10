package container

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"attendance-system/internal/middleware"
	"attendance-system/pkg/logger"
)

// logTo returns a gormLog and a func that flushes it and returns what it wrote.
func logTo(t *testing.T) (gormLog, func() string) {
	t.Helper()

	dir := t.TempDir()
	log, err := logger.New(logger.Config{ServiceName: "test", Path: dir})
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	return gormLog{log}, func() string {
		if err := log.Close(); err != nil {
			t.Fatalf("log.Close: %v", err)
		}
		matches, _ := filepath.Glob(filepath.Join(dir, "test", "app-*.log"))
		if len(matches) == 0 {
			return ""
		}
		data, err := os.ReadFile(matches[0])
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		return string(data)
	}
}

func TestGormLogTagsFailuresWithTheRequestAndDropsValues(t *testing.T) {
	queryLog, read := logTo(t)
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	mock.ExpectExec("UPDATE users").WillReturnError(errors.New("boom"))
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}),
		&gorm.Config{Logger: queryLog, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	router.GET("/", func(c *gin.Context) {
		db.WithContext(c.Request.Context()).Exec("UPDATE users SET password_hash = ? WHERE id = 1", "s3cret-hash")
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "trace-me")
	router.ServeHTTP(httptest.NewRecorder(), req)

	out := read()
	wants := []string{`"message":"query failed"`, `"request_id":"trace-me"`, "UPDATE users", `"error":"boom"`}
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("log = %s, want it to contain %s", out, want)
		}
	}
	if strings.Contains(out, "s3cret-hash") {
		t.Errorf("log = %s, want bind values left out", out)
	}
}

func TestGormLogTrace(t *testing.T) {
	queryLog, read := logTo(t)
	sql := func() (string, int64) { return "SELECT 1", 1 }

	queryLog.Trace(context.Background(), time.Now(), sql, nil)
	queryLog.Trace(context.Background(), time.Now(), sql, gorm.ErrRecordNotFound)
	queryLog.Trace(context.Background(), time.Now().Add(-time.Second), sql, nil)

	lines := strings.Split(strings.TrimSpace(read()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], `"message":"slow query"`) {
		t.Errorf("log = %v, want only the slow query, not the fast one or a missing row", lines)
	}
}

func TestGormLogMessages(t *testing.T) {
	queryLog, read := logTo(t)
	if got := queryLog.LogMode(gormlogger.Silent); got != queryLog {
		t.Errorf("LogMode = %v, want the same logger", got)
	}

	queryLog.Info(context.Background(), "info %d", 1)
	queryLog.Warn(context.Background(), "warn %d", 2)
	queryLog.Error(context.Background(), "error %d", 3)

	out := read()
	for _, want := range []string{"info 1", "warn 2", "error 3"} {
		if !strings.Contains(out, want) {
			t.Errorf("log = %s, want it to contain %s", out, want)
		}
	}
}
