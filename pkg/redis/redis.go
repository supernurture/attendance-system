package redis

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const pingTimeout = 5 * time.Second

// New opens a pooled Redis client and pings it.
func New(
	host string, port int, user string, password string, db int, useTLS bool, pool PoolConfig,
) (*goredis.Client, error) {
	if !useTLS {
		log.Printf("warning: Redis connection to %s:%d is not encrypted; "+
			"credentials and cached data cross the network in cleartext\n", host, port)
	}
	client := goredis.NewClient(opts(host, port, user, password, db, useTLS, pool))

	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis %s:%d: %w", host, port, err)
	}

	log.Printf("connected to Redis at %s:%d (db %d)\n", host, port, db)
	return client, nil
}

// PoolConfig holds connection-pool settings; a zero value keeps the client default.
type PoolConfig struct {
	PoolSize        int
	MinIdleConns    int
	ConnMaxLifetime time.Duration
}

func opts(host string, port int, user string, password string, db int, useTLS bool, pool PoolConfig) *goredis.Options {
	opts := &goredis.Options{
		Addr:            fmt.Sprintf("%s:%d", host, port),
		Username:        user,
		Password:        password,
		DB:              db,
		PoolSize:        pool.PoolSize,
		MinIdleConns:    pool.MinIdleConns,
		ConnMaxLifetime: pool.ConnMaxLifetime,
	}
	if useTLS {
		opts.TLSConfig = &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	}
	return opts
}
