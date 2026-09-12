package upload

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	uploadcontract "attendance-system/internal/api/server/oapicodegen/upload"
	"attendance-system/internal/middleware"
	"attendance-system/internal/pkg/storage"
)

const faultKey = "attendance/7/00000000-0000-0000-0000-000000000000.jpg"

// Minimal bodies with the magic bytes http.DetectContentType looks for.
var (
	jpeg = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte("j"), 1000)...)
	pdf  = []byte("%PDF-1.4\n" + strings.Repeat("p", 1000))
)

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func newTestService(t *testing.T) (*Service, *storage.Storage) {
	t.Helper()

	store, err := storage.New(storage.Config{
		Endpoint:        envOr("STORAGE_TEST_ENDPOINT", "http://localhost:9000"),
		Region:          envOr("STORAGE_TEST_REGION", "auto"),
		Bucket:          envOr("STORAGE_TEST_BUCKET", "attendance"),
		AccessKeyID:     envOr("STORAGE_TEST_ACCESS_KEY_ID", "minioadmin"),
		SecretAccessKey: envOr("STORAGE_TEST_SECRET_ACCESS_KEY", "minioadmin"),
		ForcePathStyle:  true,
		PresignTTL:      time.Minute,
	})
	if err != nil {
		if os.Getenv("STORAGE_TEST_REQUIRED") != "" {
			t.Fatalf("STORAGE_TEST_REQUIRED is set but no object storage is reachable: %v", err)
		}
		t.Skipf("no object storage reachable (run `docker compose up -d`): %v", err)
	}
	return NewService(store), store
}

// put uploads body as a client would: to intent.URL, with exactly intent.Headers.
func put(t *testing.T, intent Intent, body []byte) int {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, intent.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build PUT: %v", err)
	}
	for name, value := range intent.Headers {
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// upload runs intent + PUT, as a client would, and returns the key.
func upload(t *testing.T, svc *Service, userID int64, purpose Purpose, contentType string, body []byte) string {
	t.Helper()

	intent, err := svc.Intent(t.Context(), userID, purpose, contentType, int64(len(body)))
	if err != nil {
		t.Fatalf("Intent: %v", err)
	}
	if status := put(t, intent, body); status != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", status)
	}
	return intent.Key
}

func isValidationError(err error) bool {
	_, ok := errors.AsType[*ValidationError](err)
	return ok
}

// postIntent asks for an attendance photo upload as user 5, through middleware.Auth like the real route.
func postIntent(t *testing.T, svc *Service, size int64) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)

	secret := []byte("a-test-secret-that-is-long-enough-000")
	router := gin.New()
	router.ContextWithFallback = true
	uploadcontract.RegisterHandlers(router.Group("", middleware.Auth(secret)),
		uploadcontract.NewStrictHandler(NewHandler(svc), nil))

	body, _ := json.Marshal(uploadcontract.UploadIntentRequest{
		Purpose: uploadcontract.AttendancePhoto, ContentType: "image/jpeg", SizeBytes: size,
	})
	req := httptest.NewRequest(http.MethodPost, "/uploads/intent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization",
		"Bearer "+middleware.SignAccessToken(secret, middleware.Claims{UserID: 5, Role: "employee"}, time.Minute))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// replies maps an HTTP method to how the fake object store answers it.
type replies map[string]func(http.ResponseWriter)

// fakeS3 answers the bucket check, then each object request with the reply for its method, or 403.
// It stands in for failures a healthy MinIO cannot produce; the happy paths run against MinIO.
type fakeS3 struct {
	replies replies

	mu   sync.Mutex
	seen []string
}

func newFaultyService(t *testing.T, answers replies) (*Service, *fakeS3) {
	t.Helper()

	fake := &fakeS3{replies: answers}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/attendance" {
			return // HeadBucket in storage.New
		}
		fake.mu.Lock()
		fake.seen = append(fake.seen, r.Method)
		fake.mu.Unlock()
		if reply, ok := fake.replies[r.Method]; ok {
			reply(w)
			return
		}
		w.WriteHeader(http.StatusForbidden) // not retried by the SDK, and not a 404
	}))
	t.Cleanup(srv.Close)

	store, err := storage.New(storage.Config{
		Endpoint: srv.URL, Region: "auto", Bucket: "attendance",
		AccessKeyID: "x", SecretAccessKey: "x", ForcePathStyle: true, PresignTTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	return NewService(store), fake
}

func (f *fakeS3) deleted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, method := range f.seen {
		if method == http.MethodDelete {
			return true
		}
	}
	return false
}

func sized(size int) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) { w.Header().Set("Content-Length", strconv.Itoa(size)) }
}

func body(b []byte) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) { _, _ = w.Write(b) }
}
