package database

import (
	"errors"
	"net/url"
	"testing"

	"gorm.io/gorm"
)

func TestSQLServerDSN(t *testing.T) {
	cases := map[string]string{
		"": "sqlserver://user:s4cr4t@localhost:2222?database=go",
		"encrypt=true&connection+timeout=40": "sqlserver://user:s4cr4t@localhost:2222?database=go" +
			"&encrypt=true&connection+timeout=40",
	}
	for opts, want := range cases {
		if got := sqlServerDSN("localhost", 2222, "user", "s4cr4t", "go", opts); got != want {
			t.Errorf("sqlServerDSN(opts=%q) = %q, want %q", opts, got, want)
		}
	}
}

func TestSQLServerDSNRoundTripsCredentials(t *testing.T) {
	const user, password, database = `admin@corp.com`, "p@ss w/rd?&:", "my db"

	dsn := sqlServerDSN("localhost", 2222, user, password, database, "")
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", dsn, err)
	}

	gotPassword, _ := parsed.User.Password()
	if parsed.User.Username() != user || gotPassword != password {
		t.Errorf("credentials = %q / %q, want %q / %q", parsed.User.Username(), gotPassword, user, password)
	}
	if got := parsed.Query().Get("database"); got != database {
		t.Errorf("database = %q, want %q", got, database)
	}
}

func TestNewSQLServer(t *testing.T) {
	db, mock := mockDB(t)
	mock.ExpectPing()

	orig := gormOpen
	gormOpen = func(gorm.Dialector, ...gorm.Option) (*gorm.DB, error) { return db, nil }
	t.Cleanup(func() { gormOpen = orig })

	got, err := NewSQLServer(
		"localhost", 2222, "user", "password", "database", "encrypt=true", PoolConfig{MaxIdleConns: 2})
	if err != nil {
		t.Fatalf("NewSQLServer: %v", err)
	}
	if got != db {
		t.Error("expected the opened DB back")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}

	mock.ExpectPing().WillReturnError(errors.New("boom"))
	if _, err := NewSQLServer(
		"localhost", 2222, "user", "password", "database", "encrypt=strict", PoolConfig{}); err == nil {
		t.Error("expected ping failure to abort")
	}

	gormOpen = func(gorm.Dialector, ...gorm.Option) (*gorm.DB, error) { return brokenDB(), nil }
	if _, err := NewSQLServer(
		"localhost", 2222, "user", "password", "database", "encrypt=true", PoolConfig{}); err == nil {
		t.Error("expected configurePool failure")
	}

	gormOpen = func(gorm.Dialector, ...gorm.Option) (*gorm.DB, error) { return nil, errors.New("boom") }
	if _, err := NewSQLServer(
		"localhost", 2222, "user", "password", "database", "encrypt=true", PoolConfig{}); err == nil {
		t.Error("expected open failure")
	}
}

func TestNewSQLServerUnreachable(t *testing.T) {
	if _, err := NewSQLServer(
		"127.0.0.2", 2, "user", "password", "database", "dial+timeout=2", PoolConfig{}); err == nil {
		t.Fatal("expected connection error")
	}
}
