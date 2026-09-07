package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client is a small JSON-oriented HTTP client with a base URL and default headers.
type Client struct {
	http    *http.Client
	baseURL string
	headers map[string]string
}

// Option configures a Client during New.
type Option func(*Client)

// WithTimeout sets the underlying http.Client timeout; zero or less keeps New's default.
func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) {
		if timeout > 0 {
			c.http.Timeout = timeout
		}
	}
}

// WithBaseURL sets the base URL prepended to every request path.
func WithBaseURL(baseURL string) Option {
	return func(c *Client) { c.baseURL = baseURL }
}

// WithHeader adds a default header sent on every request.
func WithHeader(key, value string) Option {
	return func(c *Client) { c.headers[key] = value }
}

// WithHTTPClient replaces the underlying *http.Client.
func WithHTTPClient(http *http.Client) Option {
	return func(c *Client) { c.http = http }
}

// WithTransport sets the RoundTripper on the underlying http.Client.
func WithTransport(transport http.RoundTripper) Option {
	return func(c *Client) { c.http.Transport = transport }
}

// New initializes a new Client with the provided options.
func New(opts ...Option) *Client {
	client := &Client{
		http:    &http.Client{Timeout: 30 * time.Second},
		headers: map[string]string{},
	}
	for _, opt := range opts {
		opt(client)
	}
	return client
}

// Do sends a raw request. body may be nil. The caller owns closing the response body.
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, joinURL(c.baseURL, path), body)
	if err != nil {
		return nil, err
	}
	for key, value := range c.headers {
		req.Header.Set(key, value)
	}
	return c.http.Do(req)
}

// GetJSON sends GET path and decodes the JSON response into out.
func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	resp, err := c.Do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	return decode(resp, out)
}

// PostJSON marshals in as JSON, POSTs it, and decodes the response into out; out=nil ignores the body.
func (c *Client) PostJSON(ctx context.Context, path string, in, out any) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL(c.baseURL, path), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	for key, value := range c.headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	return decode(resp, out)
}

func decode(resp *http.Response, out any) error {
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		msg, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if err != nil {
			return fmt.Errorf("httpclient: %s: reading error body: %w", resp.Status, err)
		}
		return fmt.Errorf("httpclient: %s: %s", resp.Status, msg)
	}
	if out == nil {
		_, err := io.Copy(io.Discard, resp.Body)
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func joinURL(baseURL, path string) string {
	joined, err := url.JoinPath(baseURL, path)
	if err != nil {
		return baseURL + path
	}
	return joined
}
