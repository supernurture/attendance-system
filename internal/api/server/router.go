package server

import (
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/supernurture/go-template/internal/api/server/modules/example"
	"github.com/supernurture/go-template/internal/api/server/modules/health"
	examplecontract "github.com/supernurture/go-template/internal/api/server/oapicodegen/example"
	healthcontract "github.com/supernurture/go-template/internal/api/server/oapicodegen/health"
	"github.com/supernurture/go-template/internal/config"
	"github.com/supernurture/go-template/internal/container"
	"github.com/supernurture/go-template/internal/middleware"
)

// NewRouter builds the gin engine: mode, trusted proxies, the middleware chain, and every module's generated routes.
func NewRouter(cfg *config.Config, deps *container.Container) (*gin.Engine, error) {
	gin.SetMode(cfg.Server.Mode)

	router := gin.New()
	if err := router.SetTrustedProxies(cfg.Server.TrustedProxies); err != nil {
		return nil, fmt.Errorf("set trusted proxies: %w", err)
	}

	// Without this, a handler's *gin.Context carries no deadline and the timeout never reaches downstream calls.
	router.ContextWithFallback = true
	router.Use(middleware.Default(cfg, deps.Logger)...)

	register(router, deps)
	return router, nil
}

func register(router gin.IRouter, deps *container.Container) {
	healthcontract.RegisterHandlers(
		router, healthcontract.NewStrictHandler(health.NewHandler(), nil))

	if client, db := deps.Redis["example"], deps.Postgres["example"]; client != nil && db != nil {
		service := example.NewService(client, example.NewRepository(db))
		handler := example.NewHandler(service, deps.Logger)
		examplecontract.RegisterHandlers(
			router, examplecontract.NewStrictHandler(handler, nil))
	}
}
