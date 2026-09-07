package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/supernurture/go-template/internal/api/server"
	"github.com/supernurture/go-template/internal/config"
	"github.com/supernurture/go-template/internal/container"
)

const (
	readHeaderTimeout = 5 * time.Second
	writeSlack        = 5 * time.Second

	readTimeout = 15 * time.Second
	idleTimeout = 60 * time.Second
)

var (
	exit      = os.Exit
	listen    = net.Listen
	newRouter = server.NewRouter
	closeDeps = func(deps *container.Container) error { return deps.Close() }

	shutdownTimeout = 8 * time.Second
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		exit(1)
	}
}

func run(ctx context.Context) (err error) {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	deps, err := container.NewContainer(cfg)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := closeDeps(deps); closeErr != nil && err == nil {
			err = fmt.Errorf("close dependencies: %w", closeErr)
		}
	}()

	router, err := newRouter(cfg, deps)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           router,
		ReadHeaderTimeout: readHeaderTimeout,
		WriteTimeout:      cfg.Server.Timeout + writeSlack,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
	}

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	listener, err := listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	serveErrCh := make(chan error, 1)
	go func() {
		if serveErr := srv.Serve(listener); !errors.Is(serveErr, http.ErrServerClosed) {
			serveErrCh <- serveErr
		}
	}()
	deps.Logger.Info("server listening", map[string]any{"addr": listener.Addr().String()})

	select {
	case serveErr := <-serveErrCh:
		return fmt.Errorf("serve: %w", serveErr)
	case <-ctx.Done():
	}
	deps.Logger.Info("shutting down", map[string]any{"timeout": shutdownTimeout.String()})

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if shutdownErr := srv.Shutdown(shutdownCtx); shutdownErr != nil {
		return fmt.Errorf("shutdown server: %w", shutdownErr)
	}

	return nil
}
