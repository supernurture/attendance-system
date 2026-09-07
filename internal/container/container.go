package container

import (
	"errors"
	"fmt"

	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/supernurture/go-template/internal/config"
	"github.com/supernurture/go-template/internal/httpclient"
	"github.com/supernurture/go-template/pkg/database"
	"github.com/supernurture/go-template/pkg/logger"
	"github.com/supernurture/go-template/pkg/redis"
)

// Container holds shared dependencies, keyed by their config name.
type Container struct {
	Logger     *logger.Logger
	HTTPClient *httpclient.HTTPClient
	Postgres   map[string]*gorm.DB
	SQLServer  map[string]*gorm.DB
	Redis      map[string]*goredis.Client

	shutdowns []func() error
}

// NewContainer opens every configured dependency. Call Close when done.
func NewContainer(cfg *config.Config) (*Container, error) {
	log, err := newLogger(cfg)
	if err != nil {
		return nil, err
	}

	deps := &Container{
		Logger:     log,
		HTTPClient: httpclient.NewHTTPClient(cfg, log),
		Postgres:   make(map[string]*gorm.DB, len(cfg.Databases.Postgres)),
		SQLServer:  make(map[string]*gorm.DB, len(cfg.Databases.SQLServer)),
		Redis:      make(map[string]*goredis.Client, len(cfg.Redis)),

		shutdowns: []func() error{log.Close},
	}

	if err := deps.open(cfg); err != nil {
		_ = deps.Close()
		return nil, err
	}

	return deps, nil
}

// Close unwinds every hook in reverse, continuing past failures.
func (c *Container) Close() error {
	var errs []error
	for x := len(c.shutdowns) - 1; x >= 0; x-- {
		errs = append(errs, c.shutdowns[x]())
	}

	return errors.Join(errs...)
}

var (
	newPostgres  = database.NewPostgres
	newSQLServer = database.NewSQLServer
	newRedis     = redis.New
)

func (c *Container) open(cfg *config.Config) error {
	for name, db := range cfg.Databases.Postgres {
		conn, err := newPostgres(db.Host, db.Port, db.User, db.Password, db.Database, db.Opts, database.PoolConfig{
			MaxOpenConns:    db.MaxOpenConns,
			MaxIdleConns:    db.MaxIdleConns,
			ConnMaxLifetime: db.ConnMaxLifetime,
		})
		if err != nil {
			return fmt.Errorf("open postgres %q: %w", name, err)
		}
		c.Postgres[name] = conn
		c.shutdowns = append(c.shutdowns, closeGorm(conn))
	}

	for name, db := range cfg.Databases.SQLServer {
		conn, err := newSQLServer(db.Host, db.Port, db.User, db.Password, db.Database, db.Opts, database.PoolConfig{
			MaxOpenConns:    db.MaxOpenConns,
			MaxIdleConns:    db.MaxIdleConns,
			ConnMaxLifetime: db.ConnMaxLifetime,
		})
		if err != nil {
			return fmt.Errorf("open sql server %q: %w", name, err)
		}
		c.SQLServer[name] = conn
		c.shutdowns = append(c.shutdowns, closeGorm(conn))
	}

	for name, cache := range cfg.Redis {
		client, err := newRedis(
			cache.Host, cache.Port, cache.User, cache.Password, cache.DB, cache.TLS,
			redis.PoolConfig{
				PoolSize:        cache.PoolSize,
				MinIdleConns:    cache.MinIdleConns,
				ConnMaxLifetime: cache.ConnMaxLifetime,
			})
		if err != nil {
			return fmt.Errorf("open redis %q: %w", name, err)
		}
		c.Redis[name] = client
		c.shutdowns = append(c.shutdowns, client.Close)
	}

	return nil
}

func newLogger(cfg *config.Config) (*logger.Logger, error) {
	log, err := logger.New(logger.Config{
		ServiceName: cfg.App.Name,
		Env:         cfg.App.Env,
		Path:        cfg.Logger.Path,
		Level:       cfg.Logger.Level,
		Console:     cfg.Logger.Console,
		Rotation: logger.RotationOptions{
			Daily:      cfg.Logger.RotationPattern == "daily",
			MaxSizeMB:  cfg.Logger.RotationSizeMB,
			MaxAgeDays: cfg.Logger.RetentionDays,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("build logger: %w", err)
	}

	return log, nil
}

func closeGorm(conn *gorm.DB) func() error {
	return func() error {
		sqlDB, err := conn.DB()
		if err != nil {
			return err
		}
		return sqlDB.Close()
	}
}
