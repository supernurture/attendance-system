package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"attendance-system/internal/config"
	"attendance-system/internal/db"
	"attendance-system/pkg/database"
)

const databaseName = "primary"

var commands = map[string]int{
	"up": 0, "up-by-one": 0, "up-to": 1,
	"down": 0, "down-to": 1, "redo": 0,
	"reset": 0, "status": 0, "version": 0,
}

var (
	exit       = os.Exit
	sqlOpen    = sql.Open
	setDialect = goose.SetDialect
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	command, rest := "up", []string(nil)
	if len(args) > 0 {
		command, rest = args[0], args[1:]
	}

	if slices.Contains([]string{"help", "-h", "--help"}, command) {
		fmt.Println(usage())
		return nil
	}

	wantArgs, known := commands[command]
	if !known {
		return fmt.Errorf("unknown command %q\n\n%s", command, usage())
	}
	if len(rest) != wantArgs {
		return fmt.Errorf("%s takes %d argument(s), got %d\n\n%s", command, wantArgs, len(rest), usage())
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	pg, ok := cfg.Databases.Postgres[databaseName]
	if !ok {
		return fmt.Errorf("no postgres database named %q in configs/config.yaml", databaseName)
	}

	if warning := database.TLSWarning(pg.Opts); warning != "" {
		log.Printf("warning: connection for migrating %q at %s:%d %s (opts=%q)\n",
			pg.Database, pg.Host, pg.Port, warning, pg.Opts)
	}

	conn, err := sqlOpen("pgx", database.PostgresDSN(pg.Host, pg.Port, pg.User, pg.Password, pg.Database, pg.Opts))
	if err != nil {
		return fmt.Errorf("open %s: %w", pg.Database, err)
	}
	defer func() { _ = conn.Close() }()

	goose.SetBaseFS(db.Migrations)
	if err := setDialect("postgres"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}

	if err := goose.RunContext(ctx, command, conn, "migrations", rest...); err != nil {
		return fmt.Errorf("goose %s: %w", command, err)
	}
	return nil
}

func usage() string {
	names := make([]string, 0, len(commands))
	for name, wantArgs := range commands {
		if wantArgs > 0 {
			name += " <version>"
		}
		names = append(names, "  "+name)
	}
	slices.Sort(names)
	return "usage: migrate [command]   (default: up)\n\n" + strings.Join(names, "\n")
}
