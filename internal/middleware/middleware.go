package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/supernurture/go-template/internal/config"
	"github.com/supernurture/go-template/internal/pkg/util"
	"github.com/supernurture/go-template/pkg/logger"
)

const (
	requestIDHeader = "X-Request-ID"
	requestIDLength = 16
	maxRequestIDLen = 64

	defaultMaxBodyBytes = 1 << 20
)

type reqIDContextKey struct{}

// Default returns the standard chain in execution order; mount with router.Use(Default(cfg, log)...).
func Default(cfg *config.Config, log *logger.Logger) []gin.HandlerFunc {
	var maxBody int64 = defaultMaxBodyBytes
	if cfg.Server.MaxBodyBytes > 0 {
		maxBody = cfg.Server.MaxBodyBytes
	}

	return []gin.HandlerFunc{
		RequestID(),
		AccessLog(log), // outside Recovery so it logs the 500 Recovery writes
		Recovery(log),
		Timeout(cfg.Server.Timeout),
		SecurityHeaders(cfg.App.Env == "production"),
		CORS(cfg.Server.CORSOrigins),
		MaxBodyBytes(maxBody),
	}
}

// RequestID reuses a sane inbound X-Request-ID or generates one, then stores it in the request context and echoes it.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		reqID := c.GetHeader(requestIDHeader)
		if !validRequestID(reqID) {
			uniqueID, err := util.GenerateUniqueID(requestIDLength)
			if err != nil {
				uniqueID = fmt.Sprintf("%x", time.Now().UnixNano())
			}
			reqID = uniqueID
		}

		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), reqIDContextKey{}, reqID))
		c.Header(requestIDHeader, reqID)
		c.Next()
	}
}

// RequestIDFrom returns the request ID, or "" outside a request. Pass c.Request.Context(), not the *gin.Context.
func RequestIDFrom(ctx context.Context) string {
	reqID, _ := ctx.Value(reqIDContextKey{}).(string)
	return reqID
}

// AccessLog logs one line per request: 5xx as error, 4xx as warn, rest as info.
func AccessLog(log *logger.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		status := c.Writer.Status()
		fields := map[string]any{
			"request_id": RequestIDFrom(c.Request.Context()),
			"method":     c.Request.Method,
			"path":       c.Request.URL.Path,
			"status":     status,
			"latency_ms": time.Since(start).Milliseconds(),
			"client_ip":  c.ClientIP(),
		}

		if len(c.Errors) > 0 {
			fields["errors"] = c.Errors.Errors()
		}

		switch {
		case status >= http.StatusInternalServerError:
			log.Error("request", fields)
		case status >= http.StatusBadRequest:
			log.Warn("request", fields)
		default:
			log.Info("request", fields)
		}
	}
}

// Recovery turns a panic into a logged 500 instead of a dropped connection.
func Recovery(log *logger.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				if panicErr, ok := err.(error); ok && errors.Is(panicErr, http.ErrAbortHandler) {
					panic(err)
				}

				log.Error("panic recovered", map[string]any{
					"request_id": RequestIDFrom(c.Request.Context()),
					"panic":      fmt.Sprint(err),
					"stack":      string(debug.Stack()),
				})
				if c.Writer.Written() {
					c.Abort()
					return
				}
				c.AbortWithStatusJSON(
					http.StatusInternalServerError, gin.H{"message": "internal server error"})
			}
		}()
		c.Next()
	}
}

// Timeout gives the handler and every call it makes a deadline, answering 504 on overrun; zero or less disables it.
func Timeout(timeout time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		if timeout <= 0 {
			c.Next()
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)

		c.Next()

		if errors.Is(ctx.Err(), context.DeadlineExceeded) && !c.Writer.Written() {
			c.AbortWithStatusJSON(http.StatusGatewayTimeout, gin.H{"message": "request timeout"})
		}
	}
}

// SecurityHeaders sets the baseline response headers; set hsts only when served over HTTPS.
func SecurityHeaders(hsts bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		if hsts {
			c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Next()
	}
}

// CORS echoes an allowed Origin and answers preflight; an empty list allows none, "*" allows any without credentials.
func CORS(allowed []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Add("Vary", "Origin")
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next()
			return
		}

		if !originAllowed(allowed, origin) {
			c.Next()
			return
		}

		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Access-Control-Expose-Headers", requestIDHeader)
		if !slices.Contains(allowed, "*") {
			c.Header("Access-Control-Allow-Credentials", "true")
		}
		if c.Request.Method == http.MethodOptions {
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")

			headers := c.GetHeader("Access-Control-Request-Headers")
			if headers == "" {
				headers = "Authorization, Content-Type, " + requestIDHeader
			}

			c.Header("Access-Control-Allow-Headers", headers)
			c.Header("Access-Control-Max-Age", "600")
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// MaxBodyBytes caps the request body so a huge upload cannot exhaust memory.
func MaxBodyBytes(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if limit > 0 && c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		}
		c.Next()
	}
}

func validRequestID(reqID string) bool {
	if reqID == "" || len(reqID) > maxRequestIDLen {
		return false
	}
	for _, value := range reqID {
		if value < '!' || value > '~' {
			return false
		}
	}
	return true
}

func originAllowed(allowed []string, origin string) bool {
	for _, value := range allowed {
		if value == "*" || strings.EqualFold(value, origin) {
			return true
		}
	}
	return false
}
