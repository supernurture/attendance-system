package database

import (
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"
)

func TestPgQuote(t *testing.T) {
	cases := map[string]string{
		"plain":      `'plain'`,
		`it's`:       `'it\'s'`,
		`back\slash`: `'back\\slash'`,
		`both\ '`:    `'both\\ \''`,
		"":           `''`,
	}
	for in, want := range cases {
		if got := pgQuote(in); got != want {
			t.Errorf("pgQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewPostgres(t *testing.T) {
	db, mock := mockDB(t)
	mock.ExpectPing()

	orig := gormOpen
	gormOpen = func(gorm.Dialector, ...gorm.Option) (*gorm.DB, error) { return db, nil }
	t.Cleanup(func() { gormOpen = orig })

	got, err := NewPostgres(
		"localhost", 2222, "user", "password", "database", "sslmode=require", PoolConfig{MaxOpenConns: 2})
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}
	if got != db {
		t.Error("expected the opened DB back")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}

	mock.ExpectPing().WillReturnError(errors.New("boom"))
	if _, err := NewPostgres(
		"localhost", 2222, "user", "password", "database", "sslmode=require", PoolConfig{}); err == nil {
		t.Error("expected ping failure to abort")
	}

	gormOpen = func(gorm.Dialector, ...gorm.Option) (*gorm.DB, error) { return brokenDB(), nil }
	if _, err := NewPostgres(
		"localhost", 2222, "user", "password", "database", "sslmode=require", PoolConfig{}); err == nil {
		t.Error("expected configurePool failure")
	}
}

func TestNewPostgresUnreachable(t *testing.T) {
	if _, err := NewPostgres(
		"127.0.0.2", 2, "user", "password", "database", "sslmode=disable connect_timeout=2", PoolConfig{}); err == nil {
		t.Fatal("expected connection error")
	}
}

func TestPostgresDSNQuotesEveryValue(t *testing.T) {
	dsn := PostgresDSN("db.internal", 5432, "app", "x sslmode=disable", "attendance", "sslmode=require")

	if strings.Contains(dsn, "password='x' sslmode=disable") {
		t.Fatalf("password broke out of its field: %s", dsn)
	}
	if want := `password='x sslmode=disable'`; !strings.Contains(dsn, want) {
		t.Errorf("dsn = %s, want it to contain %s", dsn, want)
	}
	if !strings.Contains(dsn, "sslmode=require") {
		t.Errorf("dsn = %s, want opts appended verbatim", dsn)
	}
}

// The zone must be last, or an opts that sets its own would win and date columns would shift a day.
func TestPostgresDSNPinsUTCLast(t *testing.T) {
	dsn := PostgresDSN("db.internal", 5432, "app", "secret", "attendance", "TimeZone=America/New_York")

	if !strings.HasSuffix(dsn, "TimeZone=UTC") {
		t.Errorf("dsn = %s, want it to end with TimeZone=UTC", dsn)
	}
}
