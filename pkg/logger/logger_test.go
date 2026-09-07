package logger

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap/zapcore"
)

func readLogFile(t *testing.T, dir string) string {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(dir, "app-*.log"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no log file under %s (glob err: %v)", dir, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	return string(data)
}

func TestNewWritesJSONWithServiceFields(t *testing.T) {
	base := t.TempDir()

	log, err := New(Config{ServiceName: "go", Env: "test", Path: base, Level: "DEBUG"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	log.Info("hello", map[string]any{"count": 4})
	if err := log.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var entry map[string]any
	line := strings.TrimSpace(readLogFile(t, filepath.Join(base, "go")))
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		t.Fatalf("log line is not JSON (%q): %v", line, err)
	}

	if entry["message"] != "hello" {
		t.Errorf("message = %v, want hello", entry["message"])
	}
	if entry["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", entry["level"])
	}
	if entry["application_name"] != "go" {
		t.Errorf("application_name = %v, want go", entry["application_name"])
	}
	if entry["env"] != "test" {
		t.Errorf("env = %v, want test", entry["env"])
	}
	wantHost, err := os.Hostname()
	if err != nil {
		wantHost = ""
	}
	if entry["host"] != wantHost {
		t.Errorf("host = %v, want %v", entry["host"], wantHost)
	}
	if entry["count"] != float64(4) {
		t.Errorf("count = %v, want 4", entry["count"])
	}
	if _, ok := entry["timestamp"]; !ok {
		t.Error("entry has no timestamp")
	}
	if _, ok := entry["caller"]; ok {
		t.Error("caller present although ReportCaller is false")
	}
}

func TestHostFallsBackToEmptyString(t *testing.T) {
	base := t.TempDir()

	orig := hostname
	hostname = func() (string, error) { return "ignored", errors.New("no hostname") }
	t.Cleanup(func() { hostname = orig })

	log, err := New(Config{ServiceName: "go", Path: base})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	log.Info("hello", nil)
	_ = log.Close()

	if !strings.Contains(readLogFile(t, filepath.Join(base, "go")), `"host":""`) {
		t.Error("a hostname lookup failure must log an empty host, not fail New")
	}
}

func TestAllLevelsAreWritten(t *testing.T) {
	base := t.TempDir()

	log, err := New(Config{ServiceName: "go", Path: base, Level: "debug"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	log.Debug("debug", map[string]any{"key": "value"})
	log.Info("info", nil)
	log.Warn("warn", map[string]any{"key": "value"})
	log.Error("error", nil)
	_ = log.Close()

	out := readLogFile(t, filepath.Join(base, "go"))
	for _, want := range []string{`"DEBUG"`, `"INFO"`, `"WARN"`, `"ERROR"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing a %s entry; got %q", want, out)
		}
	}
}

func TestLevelFiltering(t *testing.T) {
	base := t.TempDir()

	log, err := New(Config{ServiceName: "go", Path: base, Level: "WARN"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	log.Debug("dropped-debug", nil)
	log.Info("dropped-info", nil)
	log.Warn("kept-warn", nil)
	_ = log.Close()

	out := readLogFile(t, filepath.Join(base, "go"))
	if strings.Contains(out, "dropped-debug") || strings.Contains(out, "dropped-info") {
		t.Errorf("entries below WARN were written; got %q", out)
	}
	if !strings.Contains(out, "kept-warn") {
		t.Errorf("WARN entry missing; got %q", out)
	}
}

func TestReportCallerAddsCaller(t *testing.T) {
	base := t.TempDir()

	log, err := New(Config{ServiceName: "go", Path: base, ReportCaller: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	log.Info("hello", nil)
	_ = log.Close()

	out := readLogFile(t, filepath.Join(base, "go"))
	if !strings.Contains(out, "logger_test.go") {
		t.Errorf("caller does not point at the call site; got %q", out)
	}
}

func TestConsoleTeesToStdout(t *testing.T) {
	base := t.TempDir()

	log, err := New(Config{ServiceName: "go", Path: base, Console: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	log.Info("go-to-console", nil)
	_ = log.Close()

	if !strings.Contains(readLogFile(t, filepath.Join(base, "go")), "go-to-console") {
		t.Error("Console must tee, not replace the file sink")
	}
}

func TestNewDefaultsToLocalLogsDir(t *testing.T) {
	t.Chdir(t.TempDir())

	log, err := New(Config{ServiceName: "go"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	log.Info("hello", nil)
	_ = log.Close()

	if _, err := os.Stat(filepath.Join("logs", "go")); err != nil {
		t.Errorf("empty Path did not default to ./logs: %v", err)
	}
}

func TestNewRotationOptions(t *testing.T) {
	tests := []struct {
		name     string
		rotation RotationOptions
	}{
		{"daily", RotationOptions{Daily: true, MaxAgeDays: 10}},
		{"by size", RotationOptions{MaxSizeMB: 10}},
		{"size ignored when daily", RotationOptions{Daily: true, MaxSizeMB: 10}},
		{"no rotation", RotationOptions{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()

			log, err := New(Config{ServiceName: "go", Path: base, Rotation: test.rotation})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			log.Info("hello", nil)
			if err := log.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if !strings.Contains(readLogFile(t, filepath.Join(base, "go")), "hello") {
				t.Error("entry was not written")
			}
		})
	}
}

func TestNewMkdirError(t *testing.T) {
	base := t.TempDir()

	blocker := filepath.Join(base, "go")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	if _, err := New(Config{ServiceName: "go", Path: base}); err == nil {
		t.Error("New = nil error, want MkdirAll failure")
	}
}

func TestNewInvalidRotationPattern(t *testing.T) {
	base := t.TempDir()

	if _, err := New(Config{ServiceName: "bad%q", Path: base}); err == nil {
		t.Error("New = nil error, want rotatelogs pattern failure")
	}
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input string
		want  zapcore.Level
	}{
		{"DEBUG", zapcore.DebugLevel},
		{"debug", zapcore.DebugLevel},
		{" INFO ", zapcore.InfoLevel},
		{"WARN", zapcore.WarnLevel},
		{"ERROR", zapcore.ErrorLevel},
		{"", zapcore.InfoLevel},
		{"unknown", zapcore.InfoLevel},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			if got := parseLevel(test.input); got != test.want {
				t.Errorf("parseLevel(%q) = %v, want %v", test.input, got, test.want)
			}
		})
	}
}

func TestZapFields(t *testing.T) {
	if got := zapFields(nil); len(got) != 0 {
		t.Errorf("zapFields(nil) = %v, want empty", got)
	}

	got := zapFields(map[string]any{"a": "one", "b": "two"})
	if len(got) != 2 {
		t.Fatalf("zapFields returned %d fields, want 2", len(got))
	}

	keys := map[string]bool{got[0].Key: true, got[1].Key: true}
	if !keys["a"] || !keys["b"] {
		t.Errorf("zapFields keys = %v, want a and b", keys)
	}
}

func TestCloseWithoutCloser(t *testing.T) {
	log, err := New(Config{ServiceName: "go", Path: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	log.closer = nil

	if err := log.Close(); err != nil {
		t.Errorf("Close with a nil closer = %v, want nil", err)
	}
}
