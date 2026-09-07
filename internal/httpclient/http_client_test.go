package httpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/supernurture/go-template/internal/config"
	"github.com/supernurture/go-template/pkg/logger"
)

func newTestLogger(t *testing.T) (*logger.Logger, func() string) {
	t.Helper()

	dir := t.TempDir()
	log, err := logger.New(logger.Config{ServiceName: "test", Path: dir, Level: "DEBUG"})
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	return log, func() string {
		if err := log.Close(); err != nil {
			t.Fatalf("close logger: %v", err)
		}
		files, err := filepath.Glob(filepath.Join(dir, "test", "*"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
		}
		if len(files) == 0 {
			return ""
		}
		contents, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		return string(contents)
	}
}

func testConfig(baseURL string) *config.Config {
	cfg := &config.Config{}
	cfg.Services = map[string]config.Service{
		"example": {
			BaseURL: baseURL,
			Timeout: 5 * time.Second,
			Auth:    config.ServiceAuth{User: "user", Password: "password"},
		},
	}
	return cfg
}

func TestNewHTTPClientBuildsEveryUpstream(t *testing.T) {
	log, _ := newTestLogger(t)

	clients := NewHTTPClient(testConfig("https://api.example.com"), log)

	if clients.Example == nil {
		t.Error("Example client was not built")
	}
}

func TestWarnsWhenBaseURLIsNotHTTPS(t *testing.T) {
	tests := map[string]struct {
		baseURL  string
		wantWarn bool
	}{
		"plaintext":    {"http://api.example.com", true},
		"no scheme":    {"api.example.com", true},
		"tls":          {"https://api.example.com", false},
		"tls any case": {"HTTPS://api.example.com", false},
		// Nothing is configured, so there are no credentials to send in cleartext.
		"unconfigured": {"", false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			log, logged := newTestLogger(t)

			NewHTTPClient(testConfig(tc.baseURL), log)

			warned := strings.Contains(logged(), "not HTTPS")
			if warned != tc.wantWarn {
				t.Errorf("warned = %v, want %v for %q", warned, tc.wantWarn, tc.baseURL)
			}
		})
	}
}

func TestNewHTTPClientWithoutServiceConfig(t *testing.T) {
	log, _ := newTestLogger(t)

	if clients := NewHTTPClient(&config.Config{}, log); clients.Example == nil {
		t.Error("Example client was not built from an empty config")
	}
}

// An empty Authorization header is worse than none: some upstreams reject the malformed
// value, and it says the request is authenticated when nothing was configured.
func TestAuthorizationIsSentOnlyWhenConfigured(t *testing.T) {
	tests := map[string]struct {
		auth     config.ServiceAuth
		wantAuth string
	}{
		"no credentials":  {config.ServiceAuth{}, ""},
		"password only":   {config.ServiceAuth{Password: "password"}, ""},
		"full credential": {config.ServiceAuth{User: "user", Password: "password"}, "Basic dXNlcjpwYXNzd29yZA=="},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var got []string
			server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got = r.Header["Authorization"]
			}))
			defer server.Close()

			log, _ := newTestLogger(t)
			cfg := testConfig(server.URL)
			service := cfg.Services["example"]
			service.Auth = tc.auth
			cfg.Services["example"] = service

			resp, err := NewHTTPClient(cfg, log).Example.Do(context.Background(), http.MethodGet, "/x", nil)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			_ = resp.Body.Close()

			if tc.wantAuth == "" && len(got) != 0 {
				t.Errorf("Authorization = %q, want the header absent", got)
			}
			if tc.wantAuth != "" && (len(got) != 1 || got[0] != tc.wantAuth) {
				t.Errorf("Authorization = %q, want %q", got, tc.wantAuth)
			}
		})
	}
}
