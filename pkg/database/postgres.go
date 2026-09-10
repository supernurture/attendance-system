package database

import (
	"fmt"
	"log"
	"strings"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var gormOpen = gorm.Open

// PostgresDSN builds a postgres connection string, quoting every value so a password cannot smuggle in parameters.
func PostgresDSN(host string, port int, user, password, database, opts string) string {
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s %s",
		pgQuote(host), port, pgQuote(user), pgQuote(password), pgQuote(database), opts)
}

// NewPostgres opens a pooled GORM connection to a PostgreSQL database and pings it.
func NewPostgres(
	host string, port int, user string, password string, database string, opts string, pool PoolConfig,
) (*gorm.DB, error) {
	if warning := TLSWarning(opts); warning != "" {
		log.Printf("warning: PostgreSQL connection to %s:%d %s (opts=%q)\n", host, port, warning, opts)
	}

	db, err := gormOpen(postgres.Open(
		PostgresDSN(host, port, user, password, database, opts),
	), &gorm.Config{Logger: gormLogger})
	if err != nil {
		return nil, err
	}

	if err := configurePool(db, pool); err != nil {
		return nil, err
	}

	if err := ping(db); err != nil {
		return nil, err
	}

	log.Printf("connected to PostgreSQL database %q at %s:%d\n", database, host, port)
	return db, nil
}

func pgQuote(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `'`, `\'`)
	return "'" + v + "'"
}
