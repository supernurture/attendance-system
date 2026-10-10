package health

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	healthcontract "attendance-system/internal/api/server/oapicodegen/health"
	"attendance-system/pkg/logger"
)

type checks = map[string]func(context.Context) error

// newHandler returns a handler over checks and a func that flushes its log and returns what it wrote.
func newHandler(t *testing.T, checks checks) (*Handler, func() string) {
	t.Helper()

	dir := t.TempDir()
	log, err := logger.New(logger.Config{ServiceName: "test", Path: dir})
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() }) // Windows cannot remove the temp dir while the file is open
	return NewHandler(checks, log), func() string {
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

func up(context.Context) error { return nil }

func TestGetHealth(t *testing.T) {
	handler, _ := newHandler(t, nil)
	response, err := handler.GetHealth(context.Background(), healthcontract.GetHealthRequestObject{})
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}

	got, ok := response.(healthcontract.GetHealth200JSONResponse)
	if !ok {
		t.Fatalf("response = %T, want a 200", response)
	}
	if got.Condition != "Healthy" {
		t.Errorf("condition = %q, want %q", got.Condition, "Healthy")
	}
}

func TestGetReadyWhenEveryDependencyAnswers(t *testing.T) {
	handler, read := newHandler(t, checks{"postgres/primary": up, "redis/cache": up})

	response, err := handler.GetReady(context.Background(), healthcontract.GetReadyRequestObject{})
	if err != nil {
		t.Fatalf("GetReady: %v", err)
	}
	if got, ok := response.(healthcontract.GetReady200JSONResponse); !ok || got.Condition != "Ready" {
		t.Errorf("response = %#v, want a 200 Ready", response)
	}
	if out := read(); out != "" {
		t.Errorf("log = %s, want nothing logged when ready", out)
	}
}

func TestGetReadyNamesEveryDependencyThatIsDownInTheLogOnly(t *testing.T) {
	handler, read := newHandler(t, checks{
		"postgres/primary": func(context.Context) error { return errors.New("connection refused") },
		"redis/cache":      up,
		"storage":          func(context.Context) error { panic("nil client") },
	})

	response, err := handler.GetReady(context.Background(), healthcontract.GetReadyRequestObject{})
	if err != nil {
		t.Fatalf("GetReady: %v", err)
	}
	if got, ok := response.(healthcontract.GetReady503JSONResponse); !ok || got.Condition != "NotReady" {
		t.Errorf("response = %#v, want a 503 that names no dependency", response)
	}

	out := read()
	for _, want := range []string{"postgres/primary", "connection refused", `"dependency":"storage"`, "nil client"} {
		if !strings.Contains(out, want) {
			t.Errorf("log = %s, want it to contain %s", out, want)
		}
	}
	if strings.Contains(out, "redis/cache") {
		t.Errorf("log = %s, want the healthy dependency left out", out)
	}
}

func TestGetReadyBoundsAHangingCheck(t *testing.T) {
	orig := checkTimeout
	checkTimeout = 20 * time.Millisecond
	t.Cleanup(func() { checkTimeout = orig })

	handler, _ := newHandler(t, checks{
		"redis/cache": func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
	})

	response, err := handler.GetReady(context.Background(), healthcontract.GetReadyRequestObject{})
	if err != nil {
		t.Fatalf("GetReady: %v", err)
	}
	if _, ok := response.(healthcontract.GetReady503JSONResponse); !ok {
		t.Errorf("response = %T, want a 503 once the check's deadline passes", response)
	}
}
