package database

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func mockDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()

	sqlDB, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(postgres.New(postgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
	}), &gorm.Config{Logger: gormLogger, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	return db, mock
}

func brokenDB() *gorm.DB {
	return &gorm.DB{Config: &gorm.Config{}}
}

func TestConfigurePool(t *testing.T) {
	db, _ := mockDB(t)

	pool := PoolConfig{MaxOpenConns: 5, MaxIdleConns: 5, ConnMaxLifetime: time.Minute}
	if err := configurePool(db, pool); err != nil {
		t.Fatalf("configurePool: %v", err)
	}
	sqlDB, _ := db.DB()
	if got := sqlDB.Stats().MaxOpenConnections; got != 5 {
		t.Errorf("MaxOpenConnections = %d, want 5", got)
	}

	if err := configurePool(db, PoolConfig{}); err != nil {
		t.Fatalf("configurePool zero: %v", err)
	}
	if got := sqlDB.Stats().MaxOpenConnections; got != 5 {
		t.Errorf("zero PoolConfig changed MaxOpenConnections to %d", got)
	}

	if err := configurePool(brokenDB(), PoolConfig{}); err == nil {
		t.Error("expected error from a DB with no connection pool")
	}
}

func TestPing(t *testing.T) {
	db, mock := mockDB(t)

	mock.ExpectPing()
	if err := ping(db); err != nil {
		t.Fatalf("ping: %v", err)
	}

	wantErr := errors.New("boom")
	mock.ExpectPing().WillReturnError(wantErr)
	if err := ping(db); !errors.Is(err, wantErr) {
		t.Errorf("ping error = %v, want %v", err, wantErr)
	}

	if err := ping(brokenDB()); err == nil {
		t.Error("expected error from a DB with no connection pool")
	}
}

func TestTLSWarning(t *testing.T) {
	tests := map[string]string{
		"sslmode=verify-full":                   "",
		"connect_timeout=2 sslmode=verify-full": "",

		"sslmode=verify-ca": "hostname",
		"sslmode=require":   "does not verify",
		"sslmode=disable":   "not encrypted",
		"sslmode=allow":     "not encrypted",
		"sslmode=prefer":    "not encrypted",

		"":                  "defaults to prefer",
		"connect_timeout=2": "defaults to prefer",
	}

	for opts, want := range tests {
		got := TLSWarning(opts)
		switch {
		case want == "" && got != "":
			t.Errorf("TLSWarning(%q) = %q, want no warning", opts, got)
		case want != "" && !strings.Contains(got, want):
			t.Errorf("TLSWarning(%q) = %q, want it to mention %q", opts, got, want)
		}
	}
}

func TestSSLMode(t *testing.T) {
	for opts, want := range map[string]string{
		"sslmode=require":                    "require",
		"connect_timeout=2 sslmode=disable":  "disable",
		"sslmode=verify-full connect_time=1": "verify-full",
		"connect_timeout=2":                  "",
		"":                                   "",
		"password=xsslmode=require":          "",
	} {
		if got := sslMode(opts); got != want {
			t.Errorf("sslMode(%q) = %q, want %q", opts, got, want)
		}
	}
}

func TestSSLModeLastOneWins(t *testing.T) {
	for opts, want := range map[string]string{
		"sslmode=require sslmode=disable":     "disable",
		"sslmode=disable sslmode=require":     "require",
		"sslmode=verify-full sslmode=disable": "disable",
	} {
		if got := sslMode(opts); got != want {
			t.Errorf("sslMode(%q) = %q, want %q", opts, got, want)
		}
	}

	if TLSWarning("sslmode=verify-full sslmode=disable") == "" {
		t.Error("a trailing sslmode=disable was reported as an authenticated connection")
	}
}
