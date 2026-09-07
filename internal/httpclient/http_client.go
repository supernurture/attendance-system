package httpclient

import (
	"strings"

	"github.com/supernurture/go-template/internal/config"
	"github.com/supernurture/go-template/internal/pkg/util"
	"github.com/supernurture/go-template/pkg/httpclient"
	"github.com/supernurture/go-template/pkg/logger"
)

// HTTPClient bundles the app's configured upstream HTTP clients.
type HTTPClient struct {
	Example *httpclient.Client
}

// NewHTTPClient builds every upstream HTTP client from config.
func NewHTTPClient(cfg *config.Config, log *logger.Logger) *HTTPClient {
	return &HTTPClient{
		Example: newExampleClient(cfg, log),
	}
}

// An unconfigured service has no base URL and no credentials to leak, so it is not warned about.
func warnIfNotHTTPS(log *logger.Logger, service, baseURL string) {
	if baseURL != "" && !strings.HasPrefix(strings.ToLower(baseURL), "https://") {
		log.Warn(service+" base URL is not HTTPS; basic-auth credentials will be sent in cleartext",
			map[string]any{"base_url": baseURL})
	}
}

func newExampleClient(cfg *config.Config, log *logger.Logger) *httpclient.Client {
	example := cfg.Services["example"]

	warnIfNotHTTPS(log, "Example", example.BaseURL)

	opts := []httpclient.Option{
		httpclient.WithTimeout(example.Timeout),
		httpclient.WithBaseURL(example.BaseURL),
	}
	// Setting it unconditionally would send an empty Authorization header on every request.
	if auth := util.GenerateBasicAuth(example.Auth.User, example.Auth.Password); auth != "" {
		opts = append(opts, httpclient.WithHeader("Authorization", auth))
	}

	return httpclient.New(opts...)
}
