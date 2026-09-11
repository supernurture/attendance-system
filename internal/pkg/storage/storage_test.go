package storage

import (
	"bytes"
	"io"
	"net/http"
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
