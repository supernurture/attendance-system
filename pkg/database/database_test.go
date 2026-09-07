package database

import (
	"errors"
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

func TestHasTLS(t *testing.T) {
	if !hasTLS("sslmode=require pool=2", "sslmode=require", "sslmode=verify") {
		t.Error("expected require to count as TLS")
	}
	if !hasTLS("sslmode=verify-full", "sslmode=require", "sslmode=verify") {
		t.Error("expected verify-full to count as TLS")
	}
	if hasTLS("sslmode=disable", "sslmode=require", "sslmode=verify") {
		t.Error("expected disable not to count as TLS")
	}
	if hasTLS("random") {
		t.Error("expected no secure values to mean no TLS")
	}
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
