package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{
		Endpoint:        envOr("STORAGE_TEST_ENDPOINT", "http://localhost:9000"),
		Region:          envOr("STORAGE_TEST_REGION", "auto"),
		Bucket:          envOr("STORAGE_TEST_BUCKET", "attendance"),
		AccessKeyID:     envOr("STORAGE_TEST_ACCESS_KEY_ID", "minioadmin"),
		SecretAccessKey: envOr("STORAGE_TEST_SECRET_ACCESS_KEY", "minioadmin"),
		ForcePathStyle:  true,
		PresignTTL:      time.Minute,
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func newTestStorage(t *testing.T) *Storage {
	t.Helper()

	store, err := New(testConfig())
	if err != nil {
		if os.Getenv("STORAGE_TEST_REQUIRED") != "" {
			t.Fatalf("STORAGE_TEST_REQUIRED is set but no object storage is reachable: %v", err)
		}
		t.Skipf("no object storage reachable (run `docker compose up -d`): %v", err)
	}
	return store
}

func put(t *testing.T, url, contentType string, body []byte) int {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build PUT: %v", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("If-None-Match", "*")
	req.ContentLength = int64(len(body))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	return resp.StatusCode
}

func TestPresignRoundTrip(t *testing.T) {
	store := newTestStorage(t)
	ctx := t.Context()

	const contentType = "image/jpeg"
	key := "test/roundtrip-" + t.Name() + ".bin"
	body := []byte("selfie bytes, pretend this is a JPEG")
	t.Cleanup(func() { _ = store.Delete(context.Background(), key) }) // write-once: a rerun needs it gone

	upload, err := store.PresignPut(ctx, key, contentType, int64(len(body)))
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	if upload.ExpiresAt.Before(time.Now()) {
		t.Errorf("ExpiresAt = %v, want a future time", upload.ExpiresAt)
	}

	if status := put(t, upload.URL, contentType, body); status != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", status)
	}
	if status := put(t, upload.URL, contentType, body); status != http.StatusPreconditionFailed {
		t.Errorf("second PUT to the same URL = %d, want 412: an uploaded object must not be replaceable", status)
	}

	download, err := store.PresignGet(ctx, key)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, download.URL, nil)
	if err != nil {
		t.Fatalf("build GET: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body = %q", resp.StatusCode, got)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("downloaded %q, want %q", got, body)
	}
}

func TestPresignPutRejectsADifferentSize(t *testing.T) {
	store := newTestStorage(t)

	const contentType = "image/jpeg"
	key := "test/wrong-size.bin"
	agreed := []byte("ten bytes!")

	upload, err := store.PresignPut(t.Context(), key, contentType, int64(len(agreed)))
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}

	oversized := bytes.Repeat([]byte("x"), len(agreed)*100)
	if status := put(t, upload.URL, contentType, oversized); status < 400 {
		t.Errorf("PUT of %d bytes against a URL signed for %d returned %d; "+
			"the object store is not enforcing the signed Content-Length, so size must be "+
			"checked with HeadObject after upload", len(oversized), len(agreed), status)
	}
}

func TestPresignPutRejectsADifferentContentType(t *testing.T) {
	store := newTestStorage(t)

	body := []byte("%PDF-1.4 pretending to be a photo")
	upload, err := store.PresignPut(t.Context(), "test/wrong-type.bin", "image/jpeg", int64(len(body)))
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}

	if status := put(t, upload.URL, "application/pdf", body); status < 400 {
		t.Errorf("PUT with Content-Type application/pdf against a URL signed for image/jpeg "+
			"returned %d; the declared type is not enforced and must be sniffed after upload", status)
	}
}

func TestPresignRejectsAnEmptyKey(t *testing.T) {
	store := newTestStorage(t)
	ctx := t.Context()

	if _, err := store.PresignPut(ctx, "", "image/jpeg", 10); err == nil {
		t.Error("PresignPut accepted an empty key")
	} else if !strings.Contains(err.Error(), "presign put") {
		t.Errorf("error = %v, want it to name the operation", err)
	}

	if _, err := store.PresignGet(ctx, ""); err == nil {
		t.Error("PresignGet accepted an empty key")
	} else if !strings.Contains(err.Error(), "presign get") {
		t.Errorf("error = %v, want it to name the operation", err)
	}
}

func TestSizeReadPrefixAndDelete(t *testing.T) {
	store := newTestStorage(t)
	ctx := t.Context()

	key := "test/object-" + t.Name() + ".bin"
	body := []byte("0123456789")
	t.Cleanup(func() { _ = store.Delete(context.Background(), key) })
	upload, err := store.PresignPut(ctx, key, "image/jpeg", int64(len(body)))
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	if status := put(t, upload.URL, "image/jpeg", body); status != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", status)
	}

	if size, err := store.Size(ctx, key); err != nil || size != int64(len(body)) {
		t.Errorf("Size = %d, %v; want %d", size, err, len(body))
	}
	if head, err := store.ReadPrefix(ctx, key, 4); err != nil || string(head) != "0123" {
		t.Errorf("ReadPrefix(4) = %q, %v; want the first four bytes only", head, err)
	}
	if all, err := store.ReadPrefix(ctx, key, 512); err != nil || !bytes.Equal(all, body) {
		t.Errorf("ReadPrefix past the end = %q, %v; want the whole object", all, err)
	}

	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Size(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Errorf("Size after Delete: err = %v, want ErrNotFound", err)
	}
	if _, err := store.ReadPrefix(ctx, key, 4); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReadPrefix after Delete: err = %v, want ErrNotFound", err)
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Errorf("deleting a missing key: err = %v, want none", err)
	}
}

func TestFailuresOtherThanAMissingObjectAreNotErrNotFound(t *testing.T) {
	store := newTestStorage(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, sizeErr := store.Size(ctx, "test/any")
	_, readErr := store.ReadPrefix(ctx, "test/any", 4)
	deleteErr := store.Delete(ctx, "test/any")
	for name, err := range map[string]error{"Size": sizeErr, "ReadPrefix": readErr, "Delete": deleteErr} {
		if err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("%s on a cancelled context: err = %v, want an error that is not ErrNotFound", name, err)
		}
	}
}

func fakeBucket(t *testing.T, object http.HandlerFunc) *Storage {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/attendance" {
			object(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	cfg := testConfig()
	cfg.Endpoint = srv.URL
	store, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store
}

func TestReadPrefixReportsACutConnection(t *testing.T) {
	store := fakeBucket(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "512")
		_, _ = w.Write([]byte("short"))
	})

	if _, err := store.ReadPrefix(t.Context(), "test/any", 512); err == nil || !strings.Contains(err.Error(), "read") {
		t.Errorf("err = %v, want the truncated body reported", err)
	}
}

func TestNewReportsAnUnreachableBucket(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)

	cfg := testConfig()
	cfg.Endpoint = srv.URL
	if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "reach bucket") {
		t.Errorf("err = %v, want the missing bucket to stop startup", err)
	}
}
