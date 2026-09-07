package example

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/supernurture/go-template/pkg/redis"
)

func miniredisService(t *testing.T) (*Service, *miniredis.Miniredis) {
	t.Helper()

	server := miniredis.RunT(t)
	port, err := strconv.Atoi(server.Port())
	if err != nil {
		t.Fatalf("miniredis port %q: %v", server.Port(), err)
	}
	client, err := redis.New(server.Host(), port, "", "", 0, false, redis.PoolConfig{})
	if err != nil {
		t.Fatalf("redis.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return NewService(client, nil), server
}

func TestValidationErrorMessage(t *testing.T) {
	if got := (ValidationError{Message: "title is required"}).Error(); got != "title is required" {
		t.Errorf("Error() = %q, want the message verbatim", got)
	}
}

func TestCountVisitIncrements(t *testing.T) {
	service, _ := miniredisService(t)

	for want := int64(1); want <= 3; want++ {
		got, err := service.CountVisit(context.Background())
		if err != nil {
			t.Fatalf("CountVisit: %v", err)
		}
		if got != want {
			t.Errorf("visits = %d, want %d", got, want)
		}
	}
}

func TestCountVisitReportsRedisFailure(t *testing.T) {
	service, server := miniredisService(t)
	server.Close()

	if _, err := service.CountVisit(context.Background()); err == nil {
		t.Fatal("expected the Redis failure to surface")
	}
}
func TestCreateNoteStoresTheNote(t *testing.T) {
	repo, mock := mockRepository(t)
	expectInsert(mock, 7)

	note, err := NewService(nil, repo).CreateNote(context.Background(), "  a title  ", "  a body  ")
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}

	if note.ID != 7 || note.Title != "a title" || note.Body != "a body" {
		t.Errorf("note = %+v, want the trimmed values and id 7", note)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestCreateNoteReportsRepositoryFailure(t *testing.T) {
	repo, mock := mockRepository(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "example_notes"`).WillReturnError(errors.New("disk full"))
	mock.ExpectRollback()

	_, err := NewService(nil, repo).CreateNote(context.Background(), "a title", "")

	var invalid ValidationError
	if err == nil || errors.As(err, &invalid) {
		t.Fatalf("error = %v, want a real failure rather than a caller mistake", err)
	}
}

func TestListNotesReportsRepositoryFailure(t *testing.T) {
	repo, mock := mockRepository(t)
	mock.ExpectQuery(`SELECT \* FROM "example_notes"`).WillReturnError(errors.New("connection reset"))

	if _, err := NewService(nil, repo).ListNotes(context.Background(), nil); err == nil {
		t.Fatal("expected the query failure to surface")
	}
}

func nilService() *Service { return NewService(nil, nil) }

func TestCreateNoteRejectsBadInput(t *testing.T) {
	tests := map[string]struct {
		title, body string
		want        string
	}{
		"empty title":    {"", "", "title is required"},
		"blank title":    {"   ", "", "title is required"},
		"title too long": {strings.Repeat("a", maxTitleLen+1), "", "title must be at most"},
		"body too long":  {"ok", strings.Repeat("a", maxBodyLen+1), "body must be at most"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := nilService().CreateNote(context.Background(), tc.title, tc.body)

			var invalid ValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("error = %v, want a ValidationError", err)
			}
			if !strings.Contains(invalid.Message, tc.want) {
				t.Errorf("message = %q, want it to contain %q", invalid.Message, tc.want)
			}
		})
	}

	t.Run("multi-byte title at the limit is accepted", func(t *testing.T) {
		repo, mock := mockRepository(t)
		expectInsert(mock, 1)

		title := strings.Repeat("é", maxTitleLen)
		if _, err := NewService(nil, repo).CreateNote(context.Background(), title, ""); err != nil {
			t.Errorf("rejected a %d-character title: %v", maxTitleLen, err)
		}
	})
}

func TestListNotesRejectsLimitOutOfRange(t *testing.T) {
	for _, limit := range []int{0, -1, maxLimit + 1} {
		_, err := nilService().ListNotes(context.Background(), &limit)

		var invalid ValidationError
		if !errors.As(err, &invalid) {
			t.Errorf("limit %d: error = %v, want a ValidationError", limit, err)
		}
	}
}

func TestListNotesDefaultsTheLimit(t *testing.T) {
	repo, mock := mockRepository(t)
	mock.ExpectQuery(`SELECT \* FROM "example_notes" ORDER BY id DESC LIMIT`).
		WithArgs(defaultLimit).
		WillReturnRows(mock.NewRows([]string{"id", "title", "body", "created_at"}))

	if _, err := NewService(nil, repo).ListNotes(context.Background(), nil); err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
