package upload

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"attendance-system/internal/pkg/storage"
)

func TestRepositoryRoundTrip(t *testing.T) {
	_, store := newTestService(t)
	repo := NewRepository(store)
	const key = "attendance/7/0f8fad5b-d9cb-469f-a165-70867728950e.jpg"
	t.Cleanup(func() { _ = repo.Delete(context.Background(), key) })

	intent, err := repo.PresignUpload(t.Context(), key, "image/jpeg", int64(len(jpeg)))
	if err != nil {
		t.Fatalf("PresignUpload: %v", err)
	}
	if status := put(t, Intent{Key: key, PresignedURL: intent}, jpeg); status != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", status)
	}

	if size, err := repo.Size(t.Context(), key); err != nil || size != int64(len(jpeg)) {
		t.Errorf("Size = %d, %v; want %d", size, err, len(jpeg))
	}
	if contentType, err := repo.ContentType(t.Context(), key); err != nil || contentType != "image/jpeg" {
		t.Errorf("ContentType = %q, %v; want image/jpeg", contentType, err)
	}
	if err := repo.Delete(t.Context(), key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.Size(t.Context(), key); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Size after Delete: err = %v, want ErrNotFound", err)
	}
}

func TestRepositoryContentTypeSniffsTheRealType(t *testing.T) {
	svc, store := newTestService(t)
	repo := NewRepository(store)

	// Uploaded as image/jpeg, but the bytes are a PDF.
	key := upload(t, svc, 7, LeaveAttachment, "image/jpeg", pdf)
	t.Cleanup(func() { _ = repo.Delete(context.Background(), key) })

	if contentType, err := repo.ContentType(t.Context(), key); err != nil || contentType != "application/pdf" {
		t.Errorf("ContentType = %q, %v; want application/pdf, not the declared type", contentType, err)
	}
}
