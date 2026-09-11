package server

import (
	"fmt"

	"github.com/gin-gonic/gin"

	"attendance-system/internal/api/server/modules/health"
	healthcontract "attendance-system/internal/api/server/oapicodegen/health"
	"attendance-system/internal/config"
	"attendance-system/internal/container"
	"attendance-system/internal/middleware"
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

func register(router gin.IRouter, _ *container.Container) {
	healthcontract.RegisterHandlers(
		router, healthcontract.NewStrictHandler(health.NewHandler(), nil))
}
