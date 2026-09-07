package redis

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func TestOptions(t *testing.T) {
	pool := PoolConfig{PoolSize: 8, MinIdleConns: 2, ConnMaxLifetime: time.Minute}
	opts := opts("cache.example.com", 6480, "user", "password", 4, true, pool)

	if got, want := opts.Addr, "cache.example.com:6480"; got != want {
		t.Errorf("Addr = %q, want %q", got, want)
	}
	if opts.DB != 4 {
		t.Errorf("DB = %d, want 4", opts.DB)
	}
	if opts.Username != "user" || opts.Password != "password" {
		t.Errorf("credentials = %q/%q, want user/password", opts.Username, opts.Password)
	}
	if opts.PoolSize != pool.PoolSize || opts.MinIdleConns != pool.MinIdleConns ||
		opts.ConnMaxLifetime != pool.ConnMaxLifetime {
		t.Errorf("pool settings not carried over: %+v", opts)
	}
	if opts.TLSConfig == nil {
		t.Fatal("TLSConfig is nil, the connection would be cleartext")
	}
	if got := opts.TLSConfig.ServerName; got != "cache.example.com" {
		t.Errorf("TLSConfig.ServerName = %q, want the host", got)
	}
}

func TestOptionsWithoutTLS(t *testing.T) {
	if opts := opts("localhost", 6480, "", "", 0, false, PoolConfig{}); opts.TLSConfig != nil {
		t.Error("TLSConfig set even though TLS was not requested")
	}
}

func TestNew(t *testing.T) {
	server := miniredis.RunT(t)
	port, err := strconv.Atoi(server.Port())
	if err != nil {
		t.Fatalf("miniredis port %q: %v", server.Port(), err)
	}

	client, err := New(server.Host(), port, "", "", 0, false, PoolConfig{PoolSize: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if err := client.Set(context.Background(), "key", "value", 0).Err(); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, _ := server.Get("key"); got != "value" {
		t.Errorf("stored value = %q, want %q", got, "value")
	}
}

func TestNewUnreachable(t *testing.T) {
	if _, err := New("127.0.0.1", 2, "", "", 0, false, PoolConfig{}); err == nil {
		t.Fatal("expected connection error")
	}
}
