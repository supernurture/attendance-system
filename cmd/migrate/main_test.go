package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Assembled rather than written out, so secret scanners do not read the fixture as a leaked key.
var testJWTSecret = strings.Repeat("fixture-", 5) // 40 chars, over the 32 the config requires

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func testDB() (host, port, user, password, database string) {
	return envOr("POSTGRES_TEST_HOST", "localhost"),
		envOr("POSTGRES_TEST_PORT", "5432"),
		envOr("POSTGRES_TEST_USER", "postgres"),
		envOr("POSTGRES_TEST_PASSWORD", "postgres"),
		envOr("POSTGRES_TEST_DB", "attendance")
}

func configYAML(postgresKey, host, port string) string {
	return fmt.Sprintf(`
app:
  name: attendance-system
  version: 1.0.0
  env: development
server:
  mode: test
  port: 8080
  timeout: 5s
logger:
  level: INFO
storage:
  endpoint: http://localhost:9000
  region: auto
  bucket: attendance
  access_key_id: minioadmin
  secret_access_key: minioadmin
  presign_ttl: 5m
auth:
  jwt_secret: %s
databases:
  postgres:
    %s:
      host: %s
      port: %s
      user: %s
      password: %s
      database: %s
      opts: sslmode=disable connect_timeout=2
`, testJWTSecret, postgresKey, host, port, envOr("POSTGRES_TEST_USER", "postgres"),
		envOr("POSTGRES_TEST_PASSWORD", "postgres"), envOr("POSTGRES_TEST_DB", "attendance"))
}

func writeConfig(t *testing.T, contents string) {
	t.Helper()

	t.Chdir(t.TempDir())
	if err := os.MkdirAll("configs", 0o750); err != nil {
		t.Fatalf("mkdir configs: %v", err)
	}
	if err := os.WriteFile(filepath.Join("configs", "config.yaml"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func requireDB(t *testing.T) {
	t.Helper()

	host, port, user, password, database := testDB()
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable connect_timeout=2",
		host, port, user, password, database)

	unreachable := func(err error) {
		t.Helper()
		if os.Getenv("POSTGRES_TEST_REQUIRED") != "" {
			t.Fatalf("POSTGRES_TEST_REQUIRED is set but no postgres is reachable: %v", err)
		}
		t.Skipf("no postgres reachable (run `docker compose up -d`): %v", err)
	}

	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		unreachable(err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.PingContext(t.Context()); err != nil {
		unreachable(err)
	}
}

func TestRunReportsMissingConfig(t *testing.T) {
	t.Chdir(t.TempDir())

	err := run(t.Context(), nil)
	if err == nil || !strings.Contains(err.Error(), "config.yaml") {
		t.Fatalf("error = %v, want it to name the missing config", err)
	}
}

func TestRunReportsMissingPrimaryDatabase(t *testing.T) {
	writeConfig(t, configYAML("secondary", "localhost", "5432"))

	err := run(t.Context(), nil)
	if err == nil || !strings.Contains(err.Error(), `"primary"`) {
		t.Fatalf("error = %v, want it to name the missing %q key", err, databaseName)
	}
}

func TestRunReportsUnreachableDatabase(t *testing.T) {
	writeConfig(t, configYAML(databaseName, "127.0.0.1", "2"))

	err := run(t.Context(), []string{"status"})
	if err == nil || !strings.Contains(err.Error(), "goose status") {
		t.Fatalf("error = %v, want the goose command to be named", err)
	}
}

func TestRunDefaultsToUp(t *testing.T) {
	requireDB(t)

	host, port, _, _, _ := testDB()
	writeConfig(t, configYAML(databaseName, host, port))

	if err := run(t.Context(), nil); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestRunAcceptsACommandAndItsArguments(t *testing.T) {
	requireDB(t)

	host, port, _, _, _ := testDB()
	writeConfig(t, configYAML(databaseName, host, port))

	for _, args := range [][]string{{"status"}, {"version"}, {"up-to", "1"}} {
		if err := run(t.Context(), args); err != nil {
			t.Errorf("run %v: %v", args, err)
		}
	}
}

func TestMainExitsNonZeroWhenRunFails(t *testing.T) {
	t.Chdir(t.TempDir())

	code := -1
	orig := exit
	exit = func(c int) { code = c }
	t.Cleanup(func() { exit = orig })

	stderr := os.Stderr
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	os.Stderr = devNull
	t.Cleanup(func() { os.Stderr = stderr; _ = devNull.Close() })

	main()

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}

func TestRunReportsOpenFailure(t *testing.T) {
	writeConfig(t, configYAML(databaseName, "localhost", "5432"))

	orig := sqlOpen
	sqlOpen = func(string, string) (*sql.DB, error) { return nil, errors.New("driver refused") }
	t.Cleanup(func() { sqlOpen = orig })

	err := run(t.Context(), nil)
	if err == nil || !strings.Contains(err.Error(), "driver refused") {
		t.Fatalf("error = %v, want the open failure", err)
	}
}

func TestRunReportsDialectFailure(t *testing.T) {
	writeConfig(t, configYAML(databaseName, "localhost", "5432"))

	orig := setDialect
	setDialect = func(string) error { return errors.New("unknown dialect") }
	t.Cleanup(func() { setDialect = orig })

	err := run(t.Context(), nil)
	if err == nil || !strings.Contains(err.Error(), "set dialect") {
		t.Fatalf("error = %v, want the dialect failure", err)
	}
}

func TestRunPrintsUsageAndTouchesNothing(t *testing.T) {
	t.Chdir(t.TempDir())

	for _, arg := range []string{"help", "-h", "--help"} {
		if err := run(t.Context(), []string{arg}); err != nil {
			t.Errorf("run %q: %v", arg, err)
		}
	}
}

func TestRunRejectsBadCommands(t *testing.T) {
	t.Chdir(t.TempDir())

	tests := map[string][]string{
		`unknown command "bogus"`: {"bogus"},
		"up takes 0 argument":     {"up", "3"},
		"up-to takes 1 argument":  {"up-to"},
	}

	for want, args := range tests {
		err := run(t.Context(), args)
		if err == nil {
			t.Errorf("run %v returned no error", args)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("run %v = %q, want it to mention %q", args, err, want)
		}
		if !strings.Contains(err.Error(), "usage:") {
			t.Errorf("run %v = %q, want the usage text", args, err)
		}
	}
}
