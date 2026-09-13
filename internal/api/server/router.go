package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"attendance-system/internal/api/server/modules/auth"
	"attendance-system/internal/api/server/modules/health"
	"attendance-system/internal/api/server/modules/schedule"
	"attendance-system/internal/api/server/modules/upload"
	"attendance-system/internal/api/server/modules/user"
	authcontract "attendance-system/internal/api/server/oapicodegen/auth"
	healthcontract "attendance-system/internal/api/server/oapicodegen/health"
	schedulecontract "attendance-system/internal/api/server/oapicodegen/schedule"
	uploadcontract "attendance-system/internal/api/server/oapicodegen/upload"
	usercontract "attendance-system/internal/api/server/oapicodegen/user"
	"attendance-system/internal/config"
	"attendance-system/internal/container"
	"attendance-system/internal/middleware"
)

const (
	postgresName = "primary"
	redisName    = "cache"

	seedTimeout = 10 * time.Second
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

	if err := register(router, cfg, deps); err != nil {
		return nil, err
	}
	return router, nil
}

func register(router gin.IRouter, cfg *config.Config, deps *container.Container) error {
	db, ok := deps.Postgres[postgresName]
	if !ok {
		return fmt.Errorf("databases.postgres.%s is required in the config", postgresName)
	}
	cache, ok := deps.Redis[redisName]
	if !ok {
		return fmt.Errorf("redis.%s is required in the config", redisName)
	}

	zone, err := time.LoadLocation(cfg.Attendance.Timezone)
	if err != nil {
		return fmt.Errorf("attendance.timezone: %w", err)
	}

	secret := []byte(cfg.Auth.JWTSecret)
	authSvc := auth.NewService(db, cache, secret)
	if err := seedAdmin(cfg.Auth, authSvc); err != nil {
		return err
	}

	healthcontract.RegisterHandlers(router,
		healthcontract.NewStrictHandlerWithOptions(health.NewHandler(), nil, healthOptions))
	authcontract.RegisterHandlers(router,
		authcontract.NewStrictHandlerWithOptions(auth.NewHandler(authSvc), nil, authOptions))

	protected := router.Group("", middleware.Auth(secret))
	usercontract.RegisterHandlers(protected,
		usercontract.NewStrictHandlerWithOptions(user.NewHandler(user.NewService(db, zone)), nil, userOptions))
	schedulecontract.RegisterHandlers(protected,
		schedulecontract.NewStrictHandlerWithOptions(
			schedule.NewHandler(schedule.NewService(db, zone)), nil, scheduleOptions))
	uploadHandler := upload.NewHandler(upload.NewService(deps.Storage))
	uploadcontract.RegisterHandlers(protected,
		uploadcontract.NewStrictHandlerWithOptions(uploadHandler, nil, uploadOptions))
	return nil
}

func seedAdmin(cfg config.Auth, svc *auth.Service) error {
	if cfg.SeedAdminEmail == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), seedTimeout)
	defer cancel()
	return svc.SeedAdmin(ctx, cfg.SeedAdminEmail, cfg.SeedAdminPassword)
}

var (
	healthOptions = healthcontract.StrictGinServerOptions{
		RequestErrorHandlerFunc: badRequest, HandlerErrorFunc: internalError, ResponseErrorHandlerFunc: internalError,
	}
	authOptions = authcontract.StrictGinServerOptions{
		RequestErrorHandlerFunc: badRequest, HandlerErrorFunc: internalError, ResponseErrorHandlerFunc: internalError,
	}
	uploadOptions = uploadcontract.StrictGinServerOptions{
		RequestErrorHandlerFunc: badRequest, HandlerErrorFunc: internalError, ResponseErrorHandlerFunc: internalError,
	}
	userOptions = usercontract.StrictGinServerOptions{
		RequestErrorHandlerFunc: badRequest, HandlerErrorFunc: internalError, ResponseErrorHandlerFunc: internalError,
	}
	scheduleOptions = schedulecontract.StrictGinServerOptions{
		RequestErrorHandlerFunc: badRequest, HandlerErrorFunc: internalError, ResponseErrorHandlerFunc: internalError,
	}
)

func badRequest(c *gin.Context, err error) {
	c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
}

func internalError(c *gin.Context, err error) {
	_ = c.Error(err)
	c.JSON(http.StatusInternalServerError, gin.H{"message": "internal server error"})
}
