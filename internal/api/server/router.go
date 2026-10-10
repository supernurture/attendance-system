package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"attendance-system/internal/api/server/modules/attendance"
	"attendance-system/internal/api/server/modules/auth"
	"attendance-system/internal/api/server/modules/health"
	"attendance-system/internal/api/server/modules/leave"
	"attendance-system/internal/api/server/modules/report"
	"attendance-system/internal/api/server/modules/schedule"
	"attendance-system/internal/api/server/modules/upload"
	"attendance-system/internal/api/server/modules/user"
	attendancecontract "attendance-system/internal/api/server/oapicodegen/attendance"
	authcontract "attendance-system/internal/api/server/oapicodegen/auth"
	healthcontract "attendance-system/internal/api/server/oapicodegen/health"
	leavecontract "attendance-system/internal/api/server/oapicodegen/leave"
	reportcontract "attendance-system/internal/api/server/oapicodegen/report"
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

	healthcontract.RegisterHandlersWithOptions(router,
		healthcontract.NewStrictHandlerWithOptions(health.NewHandler(deps.Pings(), deps.Logger), nil, healthOptions),
		healthcontract.GinServerOptions{ErrorHandler: invalidParam})
	authcontract.RegisterHandlersWithOptions(router,
		authcontract.NewStrictHandlerWithOptions(auth.NewHandler(authSvc), nil, authOptions),
		authcontract.GinServerOptions{ErrorHandler: invalidParam})

	protected := router.Group("", middleware.Auth(secret))
	usercontract.RegisterHandlersWithOptions(protected,
		usercontract.NewStrictHandlerWithOptions(user.NewHandler(user.NewService(db, zone)), nil, userOptions),
		usercontract.GinServerOptions{ErrorHandler: invalidParam})
	schedulecontract.RegisterHandlersWithOptions(protected,
		schedulecontract.NewStrictHandlerWithOptions(
			schedule.NewHandler(schedule.NewService(db, zone)), nil, scheduleOptions),
		schedulecontract.GinServerOptions{ErrorHandler: invalidParam})
	uploadHandler := upload.NewHandler(upload.NewService(deps.Storage))
	uploadcontract.RegisterHandlersWithOptions(protected,
		uploadcontract.NewStrictHandlerWithOptions(uploadHandler, nil, uploadOptions),
		uploadcontract.GinServerOptions{ErrorHandler: invalidParam})
	attendanceSvc := attendance.NewService(db, deps.Storage, zone, cfg.Attendance.GeofenceEnforce)
	attendanceHandler := attendance.NewHandler(attendanceSvc)
	attendancecontract.RegisterHandlersWithOptions(protected,
		attendancecontract.NewStrictHandlerWithOptions(attendanceHandler, nil, attendanceOptions),
		attendancecontract.GinServerOptions{ErrorHandler: invalidParam})
	leaveHandler := leave.NewHandler(leave.NewService(db, deps.Storage, zone))
	leavecontract.RegisterHandlersWithOptions(protected,
		leavecontract.NewStrictHandlerWithOptions(leaveHandler, nil, leaveOptions),
		leavecontract.GinServerOptions{ErrorHandler: invalidParam})
	reportHandler := report.NewHandler(report.NewService(attendanceSvc))
	reportcontract.RegisterHandlersWithOptions(protected,
		reportcontract.NewStrictHandlerWithOptions(reportHandler, nil, reportOptions),
		reportcontract.GinServerOptions{ErrorHandler: invalidParam})
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
	attendanceOptions = attendancecontract.StrictGinServerOptions{
		RequestErrorHandlerFunc: badRequest, HandlerErrorFunc: internalError, ResponseErrorHandlerFunc: internalError,
	}
	leaveOptions = leavecontract.StrictGinServerOptions{
		RequestErrorHandlerFunc: badRequest, HandlerErrorFunc: internalError, ResponseErrorHandlerFunc: internalError,
	}
	reportOptions = reportcontract.StrictGinServerOptions{
		RequestErrorHandlerFunc: badRequest, HandlerErrorFunc: internalError, ResponseErrorHandlerFunc: internalError,
	}
)

// invalidParam answers a path or query parameter that does not parse, in the same shape as every other error.
func invalidParam(c *gin.Context, err error, status int) {
	c.JSON(status, gin.H{"message": err.Error()})
}

// badRequest answers a body the server could not decode: 413 past the size cap, else 400.
func badRequest(c *gin.Context, err error) {
	switch tooLarge := (*http.MaxBytesError)(nil); {
	case errors.As(err, &tooLarge):
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"message": "request body too large"})
	case errors.Is(err, io.EOF):
		c.JSON(http.StatusBadRequest, gin.H{"message": "a JSON body is required"})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
	}
}

// internalError keeps the cause for the access log; past the deadline it writes no body, so Timeout answers 504.
func internalError(c *gin.Context, err error) {
	_ = c.Error(err)
	if c.Writer.Written() || c.Request.Context().Err() != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"message": "internal server error"})
}
