package upload

import (
	"context"
	"net/http"

	"attendance-system/internal/pkg/storage"
)

// presignPut is a seam: presigning a key this package built never fails, so only a test can make it.
var presignPut = (*storage.Storage).PresignPut

// Repository is upload's access to the object store, where its data lives.
type Repository struct {
	store *storage.Storage
}

func NewRepository(store *storage.Storage) *Repository {
	return &Repository{store: store}
}

func (r *Repository) PresignUpload(
	ctx context.Context, key, contentType string, size int64,
) (storage.PresignedURL, error) {
	return presignPut(r.store, ctx, key, contentType, size)
}

// Size fails with storage.ErrNotFound when nothing was uploaded to key.
func (r *Repository) Size(ctx context.Context, key string) (int64, error) {
	return r.store.Size(ctx, key)
}

// ContentType sniffs the object's first bytes: what it is, not what the client said it is.
func (r *Repository) ContentType(ctx context.Context, key string) (string, error) {
	head, err := r.store.ReadPrefix(ctx, key, sniffBytes)
	if err != nil {
		return "", err
	}
	return http.DetectContentType(head), nil
}

func (r *Repository) Delete(ctx context.Context, key string) error {
	return r.store.Delete(ctx, key)
}
