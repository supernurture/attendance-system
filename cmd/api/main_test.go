package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/supernurture/go-template/internal/config"
	"github.com/supernurture/go-template/internal/container"
)

func validConfig(port int) string {
	return fmt.Sprintf(`
app:
  name: template
  version: 1.0.0
  env: development
server:
  mode: test
  port: %d
  timeout: 5s
  trusted_proxies: []
logger:
  level: INFO
`, port)
}

func writeConfig(t *testing.T, contents string) {
	t.Helper()

	dir := t.TempDir()
	t.Chdir(dir)

	if err := os.MkdirAll("configs", 0o750); err != nil {
		t.Fatalf("mkdir configs: %v", err)
	}
	if err := os.WriteFile(filepath.Join("configs", "config.yaml"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func freePort(t *testing.T) (int, net.Listener) {
	t.Helper()

	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address is %T, want *net.TCPAddr", listener.Addr())
	}
	return addr.Port, listener
}

func swap[T any](t *testing.T, seam *T, replacement T) {
	t.Helper()

	orig := *seam
	*seam = replacement
	t.Cleanup(func() { *seam = orig })
}

func captureStderr(t *testing.T) func() string {
	t.Helper()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = writer

	return func() string {
		os.Stderr = orig
		_ = writer.Close()
		out, _ := io.ReadAll(reader)
		_ = reader.Close()
		return string(out)
	}
}

func getHealth(t *testing.T, port int) *http.Response {
	t.Helper()

	for range 100 {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
		if err == nil {
			return resp
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("server never became reachable")
	return nil
}

func TestRunReportsMissingConfig(t *testing.T) {
	t.Chdir(t.TempDir())

	err := run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "config.yaml") {
		t.Fatalf("error = %v, want it to name the missing config", err)
	}
}

func TestRunReportsUnbuildableLogger(t *testing.T) {
	port, listener := freePort(t)
	_ = listener.Close()
	writeConfig(t, validConfig(port))

	if err := os.WriteFile("logs", []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write logs: %v", err)
	}

	if err := run(context.Background()); err == nil || !strings.Contains(err.Error(), "logger") {
		t.Fatalf("error = %v, want a logger failure", err)
	}
}

func TestRunReportsRouterFailure(t *testing.T) {
	port, listener := freePort(t)
	_ = listener.Close()
	writeConfig(t, validConfig(port))

	swap(t, &newRouter, func(*config.Config, *container.Container) (*gin.Engine, error) {
		return nil, errors.New("router refused to build")
	})

	err := run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "router refused to build") {
		t.Fatalf("error = %v, want the router failure", err)
	}
}

func TestRunReportsUnavailablePort(t *testing.T) {
	port, listener := freePort(t)
	defer func() { _ = listener.Close() }()
	writeConfig(t, validConfig(port))

	err := run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("error = %v, want the bind failure", err)
	}
}

func TestRunReportsServeFailure(t *testing.T) {
	port, listener := freePort(t)
	_ = listener.Close()
	writeConfig(t, validConfig(port))

	swap(t, &listen, func(network, addr string) (net.Listener, error) {
		dead, err := net.Listen(network, addr)
		if err != nil {
			return nil, err
		}
		_ = dead.Close()
		return dead, nil
	})

	err := run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "serve") {
		t.Fatalf("error = %v, want the serve failure", err)
	}
}

func TestRunReportsDependencyCloseFailure(t *testing.T) {
	port, listener := freePort(t)
	_ = listener.Close()
	writeConfig(t, validConfig(port))

	swap(t, &closeDeps, func(deps *container.Container) error {
		_ = deps.Close()
		return errors.New("pool still busy")
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := run(ctx)
	if err == nil || !strings.Contains(err.Error(), "close dependencies: pool still busy") {
		t.Fatalf("error = %v, want the dependency close failure", err)
	}
}

func TestRunReportsShutdownTimeout(t *testing.T) {
	port, listener := freePort(t)
	_ = listener.Close()
	writeConfig(t, validConfig(port))

	swap(t, &shutdownTimeout, 300*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()

	_ = getHealth(t, port).Body.Close()

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	time.Sleep(100 * time.Millisecond)

	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "shutdown server") {
			t.Fatalf("error = %v, want the shutdown timeout", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not return after the context was cancelled")
	}
}

func TestRunServesUntilContextIsCancelled(t *testing.T) {
	port, listener := freePort(t)
	_ = listener.Close()
	writeConfig(t, validConfig(port))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()

	resp := getHealth(t, port)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Healthy") {
		t.Errorf("GET /health = %d %q", resp.StatusCode, body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run returned %v, want a clean shutdown", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not return after the context was cancelled")
	}
}

func TestMainReportsFailureAndExitsNonZero(t *testing.T) {
	t.Chdir(t.TempDir())

	var code int
	swap(t, &exit, func(c int) { code = c })
	stderr := captureStderr(t)

	main()

	out := stderr()
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(out, "config.yaml") {
		t.Errorf("stderr = %q, want it to name the missing config", out)
	}
}
