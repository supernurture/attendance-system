package upload

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"attendance-system/internal/pkg/apperr"
	"attendance-system/internal/pkg/storage"
)

type Intent struct {
	Key string
	storage.PresignedURL
}

type Service struct {
	repo *Repository
}

func NewService(store *storage.Storage) *Service {
	return &Service{repo: NewRepository(store)}
}

// Intent signs a PUT for one object of exactly size bytes under the user's own prefix.
func (s *Service) Intent(
	ctx context.Context, userID int64, purpose Purpose, contentType string, size int64,
) (Intent, error) {
	r, err := ruleFor(purpose)
	if err != nil {
		return Intent{}, err
	}
	ext, err := checkType(r, purpose, contentType)
	if err != nil {
		return Intent{}, err
	}
	if err := checkSize(r, purpose, size); err != nil {
		return Intent{}, err
	}

	key := fmt.Sprintf("%s%s.%s", ownPrefix(r, userID), uuid.NewString(), ext)
	url, err := s.repo.PresignUpload(ctx, key, contentType, size)
	if err != nil {
		return Intent{}, err
	}
	return Intent{Key: key, PresignedURL: url}, nil
}

// Verify checks an uploaded key before it is committed and returns the sniffed content type.
// Refused content is deleted, so the bucket never keeps it.
func (s *Service) Verify(ctx context.Context, userID int64, purpose Purpose, key string) (string, error) {
	r, err := ruleFor(purpose)
	if err != nil {
		return "", err
	}
	if err := checkKey(r, userID, key); err != nil {
		return "", err
	}

	size, err := s.repo.Size(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return "", apperr.Invalid("nothing was uploaded to %q", key)
	}
	if err != nil {
		return "", err
	}
	if err := checkSize(r, purpose, size); err != nil {
		return "", s.reject(ctx, key, err)
	}

	contentType, err := s.repo.ContentType(ctx, key)
	if err != nil {
		return "", err
	}
	if _, err := checkType(r, purpose, contentType); err != nil {
		return "", s.reject(ctx, key, err)
	}
	return contentType, nil
}

func (s *Service) reject(ctx context.Context, key string, reason error) error {
	if err := s.repo.Delete(ctx, key); err != nil {
		return errors.Join(reason, err)
	}
	return reason
}
