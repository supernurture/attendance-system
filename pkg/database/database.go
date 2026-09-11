package database

import (
	"context"
	"log"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var gormLogger = logger.New(
	log.New(log.Writer(), "\r\n", log.LstdFlags),
	logger.Config{
		SlowThreshold:             200 * time.Millisecond,
		LogLevel:                  logger.Warn,
		ParameterizedQueries:      true,
		IgnoreRecordNotFoundError: true,
	},
)

// PoolConfig holds connection-pool settings; a zero value keeps the driver default.
type PoolConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// TLSWarning says how opts falls short of an authenticated TLS connection, or "" if it does not.
func TLSWarning(opts string) string {
	switch sslMode(opts) {
	case "verify-full":
		return ""
	case "verify-ca":
		return "verifies the CA but not the hostname, so a valid certificate issued to a " +
			"different host is accepted; use sslmode=verify-full"
	case "require":
		return "is encrypted but does not verify the server certificate, so a redirected " +
			"connection can be read by whoever redirected it; use sslmode=verify-full"
	case "":
		return "does not set sslmode, so libpq defaults to prefer and falls back to cleartext " +
			"without saying so; use sslmode=verify-full"
	default:
		return "is not encrypted; credentials and query data cross the network in cleartext"
	}
}

// sslMode returns the sslmode in opts, or "" when absent. Last one wins, as libpq does.
func sslMode(opts string) string {
	mode := ""
	for field := range strings.FieldsSeq(opts) {
		if value, found := strings.CutPrefix(field, "sslmode="); found {
			mode = value
		}
	}
	return mode
}

func configurePool(db *gorm.DB, pool PoolConfig) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}

	if pool.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(pool.MaxOpenConns)
	}
	if pool.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(pool.MaxIdleConns)
	}
	if pool.ConnMaxLifetime > 0 {
		sqlDB.SetConnMaxLifetime(pool.ConnMaxLifetime)
	}
	return nil
}

func ping(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return sqlDB.PingContext(ctx)
}
