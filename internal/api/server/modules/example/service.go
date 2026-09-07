package example

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	goredis "github.com/redis/go-redis/v9"
)

type ValidationError struct {
	Message string
}

func (e ValidationError) Error() string { return e.Message }

type Service struct {
	redis *goredis.Client
	notes *Repository
}

func NewService(client *goredis.Client, notes *Repository) *Service {
	return &Service{redis: client, notes: notes}
}

func (s *Service) CountVisit(ctx context.Context) (int64, error) {
	visits, err := s.redis.Incr(ctx, visitsKey).Result()
	if err != nil {
		return 0, fmt.Errorf("increment %q: %w", visitsKey, err)
	}
	return visits, nil
}

func (s *Service) CreateNote(ctx context.Context, title, body string) (Note, error) {
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)

	switch {
	case title == "":
		return Note{}, invalid("title is required")
	case utf8.RuneCountInString(title) > maxTitleLen:
		return Note{}, invalid("title must be at most %d characters", maxTitleLen)
	case utf8.RuneCountInString(body) > maxBodyLen:
		return Note{}, invalid("body must be at most %d characters", maxBodyLen)
	}

	note := Note{Title: title, Body: body}
	if err := s.notes.Create(ctx, &note); err != nil {
		return Note{}, fmt.Errorf("create note: %w", err)
	}
	return note, nil
}

func (s *Service) ListNotes(ctx context.Context, limit *int) ([]Note, error) {
	size := defaultLimit
	if limit != nil {
		size = *limit
	}
	if size < 1 || size > maxLimit {
		return nil, invalid("limit must be between 1 and %d", maxLimit)
	}

	notes, err := s.notes.List(ctx, size)
	if err != nil {
		return nil, fmt.Errorf("list notes: %w", err)
	}
	return notes, nil
}

func invalid(format string, args ...any) error {
	return ValidationError{Message: fmt.Sprintf(format, args...)}
}
