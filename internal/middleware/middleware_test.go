package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/supernurture/go-template/internal/config"
	"github.com/supernurture/go-template/pkg/logger"
)

func newRouter(t *testing.T, cfg *config.Config) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	log, err := logger.New(logger.Config{ServiceName: "test", Path: t.TempDir()})
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	router := gin.New()
	router.Use(Default(cfg, log)...)
	router.POST("/echo", func(c *gin.Context) {
		body, err := c.GetRawData()
		if err != nil {
			c.String(http.StatusRequestEntityTooLarge, "too large")
			return
		}
		c.String(http.StatusOK, "%s|%s", RequestIDFrom(c.Request.Context()), body)
	})
	router.GET("/boom", func(*gin.Context) { panic("boom") })
	router.GET("/half", func(c *gin.Context) {
		c.String(http.StatusOK, "half")
		c.Writer.Flush()
		panic("boom")
	})
	router.GET("/abort", func(*gin.Context) { panic(http.ErrAbortHandler) })
	router.GET("/slow", func(c *gin.Context) { <-c.Request.Context().Done() })

	return router
}

func readLog(t *testing.T, dir string) string {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(dir, "app-*.log"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no log file under %s (glob err: %v)", dir, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	return string(data)
}

func do(router *gin.Engine, req *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func TestDefaultChain(t *testing.T) {
	cfg := &config.Config{}
	cfg.App.Env = "development"
	cfg.Server.CORSOrigins = []string{"https://app.example.com"}
	cfg.Server.MaxBodyBytes = 16
	cfg.Server.Timeout = 50 * time.Millisecond
	router := newRouter(t, cfg)

	t.Run("generates and echoes a request reqID", func(t *testing.T) {
		recorder := do(router, httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader("hey")))

		reqID := recorder.Header().Get(requestIDHeader)
		if reqID == "" {
			t.Fatal("want a generated request reqID header")
		}
		if got, want := recorder.Body.String(), reqID+"|hey"; got != want {
			t.Errorf("body = %q, want %q (reqID must reach the handler context)", got, want)
		}
	})

	t.Run("rejects a hostile inbound request reqID", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader("hey"))
		req.Header.Set(requestIDHeader, "ok-2")
		if got := do(router, req).Header().Get(requestIDHeader); got != "ok-2" {
			t.Errorf("sane reqID = %q, want it reused", got)
		}

		req = httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader("hey"))
		req.Header.Set(requestIDHeader, strings.Repeat("x", maxRequestIDLen+2))
		if got := do(router, req).Header().Get(requestIDHeader); len(got) != requestIDLength {
			t.Errorf("oversized reqID = %q, want a fresh generated one", got)
		}
	})

	t.Run("request reqID reaches the middleware chain, not just the handler", func(t *testing.T) {
		router := gin.New()
		router.Use(RequestID())
		var got string
		router.GET("/reqID", func(c *gin.Context) { got = RequestIDFrom(c.Request.Context()) })
		recorder := do(router, httptest.NewRequest(http.MethodGet, "/reqID", nil))

		if want := recorder.Header().Get(requestIDHeader); got == "" || got != want {
			t.Errorf("reqID seen by the chain = %q, want the echoed %q", got, want)
		}
	})

	t.Run("recovers panics as 500", func(t *testing.T) {
		recorder := do(router, httptest.NewRequest(http.MethodGet, "/boom", nil))
		if recorder.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", recorder.Code)
		}
	})

	t.Run("lets ErrAbortHandler through instead of logging it as a 500", func(t *testing.T) {
		defer func() {
			got := recover()
			if panicErr, ok := got.(error); !ok || !errors.Is(panicErr, http.ErrAbortHandler) {
				t.Errorf("recovered %v, want ErrAbortHandler to propagate", got)
			}
		}()
		do(router, httptest.NewRequest(http.MethodGet, "/abort", nil))
	})

	t.Run("records handler errors in the log line, not the response", func(t *testing.T) {
		dir := t.TempDir()
		log, err := logger.New(logger.Config{ServiceName: "test", Path: dir})
		if err != nil {
			t.Fatalf("logger.New: %v", err)
		}

		router := gin.New()
		router.Use(AccessLog(log))
		router.GET("/ops", func(c *gin.Context) {
			_ = c.Error(errors.New("secret cause"))
			c.String(http.StatusBadRequest, "invalid request")
		})
		recorder := do(router, httptest.NewRequest(http.MethodGet, "/ops", nil))
		if err := log.Close(); err != nil { // flush before reading the file
			t.Fatalf("log.Close: %v", err)
		}

		if got := recorder.Body.String(); strings.Contains(got, "secret cause") {
			t.Errorf("body = %q, want the cause withheld from the client", got)
		}
		if got := readLog(t, filepath.Join(dir, "test")); !strings.Contains(got, "secret cause") {
			t.Errorf("log = %s, want the cause recorded", got)
		}
	})

	t.Run("cancels an overrunning request with 504", func(t *testing.T) {
		recorder := do(router, httptest.NewRequest(http.MethodGet, "/slow", nil))
		if recorder.Code != http.StatusGatewayTimeout {
			t.Errorf("status = %d, want 504", recorder.Code)
		}
	})

	t.Run("sets security headers without hsts outside production", func(t *testing.T) {
		recorder := do(router, httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader("hey")))
		if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
		}
		if got := recorder.Header().Get("Strict-Transport-Security"); got != "" {
			t.Errorf("HSTS = %q, want none in development", got)
		}
	})

	t.Run("varies on origin whether or not one was sent", func(t *testing.T) {
		for _, origin := range []string{"", "https://app.example.com", "https://evil.example.com"} {
			req := httptest.NewRequest(http.MethodGet, "/echo", nil)
			if origin != "" {
				req.Header.Set("Origin", origin)
			}
			if got := do(router, req).Header().Get("Vary"); got != "Origin" {
				t.Errorf("Vary = %q for origin %q, want %q", got, origin, "Origin")
			}
		}
	})

	t.Run("answers preflight only for allowed origins", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "/echo", nil)
		req.Header.Set("Origin", "https://app.example.com")
		recorder := do(router, req)
		if recorder.Code != http.StatusNoContent {
			t.Errorf("status = %d, want 204", recorder.Code)
		}
		if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
			t.Errorf("allow-origin = %q, want the request origin", got)
		}

		req = httptest.NewRequest(http.MethodOptions, "/echo", nil)
		req.Header.Set("Origin", "https://evil.example.com")
		if got := do(router, req).Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("allow-origin = %q, want empty for a disallowed origin", got)
		}
	})

	t.Run("preflight echoes the requested headers and exposes the request reqID", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "/echo", nil)
		req.Header.Set("Origin", "https://app.example.com")
		req.Header.Set("Access-Control-Request-Headers", "X-Api-Key")
		recorder := do(router, req)

		if got := recorder.Header().Get("Access-Control-Allow-Headers"); got != "X-Api-Key" {
			t.Errorf("allow-headers = %q, want the requested header", got)
		}
		if got := recorder.Header().Get("Access-Control-Expose-Headers"); got != requestIDHeader {
			t.Errorf("expose-headers = %q, want %s", got, requestIDHeader)
		}
	})

	t.Run("allows a simple cross-origin request through to the handler", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader("hey"))
		req.Header.Set("Origin", "https://app.example.com")
		recorder := do(router, req)

		if recorder.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", recorder.Code)
		}
		if got := recorder.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
			t.Errorf("allow-credentials = %q, want true for an explicit origin", got)
		}
	})

	t.Run("replaces an inbound request reqID that is not printable", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader("hey"))
		req.Header.Set(requestIDHeader, "bad\x01id")
		recorder := do(router, req)

		if got := recorder.Header().Get(requestIDHeader); got == "bad\x01id" || got == "" {
			t.Errorf("request reqID = %q, want a generated replacement", got)
		}
	})

	t.Run("truncates instead of corrupting a response that already started", func(t *testing.T) {
		recorder := do(router, httptest.NewRequest(http.MethodGet, "/half", nil))
		if got := recorder.Body.String(); got != "half" {
			t.Errorf("body = %q, want %q with no error JSON appended", got, "half")
		}
	})

	t.Run("caps the request body", func(t *testing.T) {
		body := strings.NewReader(strings.Repeat("x", int(cfg.Server.MaxBodyBytes)+1))
		recorder := do(router, httptest.NewRequest(http.MethodPost, "/echo", body))
		if recorder.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want 413", recorder.Code)
		}
	})
}

func TestDefaultChainUnset(t *testing.T) {
	cfg := &config.Config{}
	cfg.App.Env = "production"
	router := newRouter(t, cfg)

	t.Run("sets hsts in production", func(t *testing.T) {
		recorder := do(router, httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader("hey")))
		if got := recorder.Header().Get("Strict-Transport-Security"); got == "" {
			t.Error("HSTS header missing in production")
		}
	})

	t.Run("serves a request with the timeout disabled", func(t *testing.T) {
		recorder := do(router, httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader("hey")))
		if recorder.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", recorder.Code)
		}
	})

	t.Run("falls back to the default body cap", func(t *testing.T) {
		body := strings.NewReader(strings.Repeat("x", defaultMaxBodyBytes+1))
		recorder := do(router, httptest.NewRequest(http.MethodPost, "/echo", body))
		if recorder.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want 413 past the %d byte default", recorder.Code, defaultMaxBodyBytes)
		}
	})
}
