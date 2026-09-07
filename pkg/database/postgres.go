package database

import (
	"fmt"
	"log"
	"strings"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var gormOpen = gorm.Open

// NewPostgres opens a pooled GORM connection to a PostgreSQL database and pings it.
func NewPostgres(
	host string, port int, user string, password string, database string, opts string, pool PoolConfig,
) (*gorm.DB, error) {
	if !hasTLS(opts, "sslmode=require", "sslmode=verify") {
		log.Printf("warning: PostgreSQL connection to %s:%d is not encrypted; "+
			"credentials and query data cross the network in cleartext (opts=%q)\n", host, port, opts)
	}

	db, err := gormOpen(postgres.Open(
		fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s %s",
			pgQuote(host), port, pgQuote(user), pgQuote(password), pgQuote(database), opts),
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
