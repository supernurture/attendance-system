package example

import (
	"context"
	"errors"
	"testing"
	"time"

	examplecontract "github.com/supernurture/go-template/internal/api/server/oapicodegen/example"
	"github.com/supernurture/go-template/pkg/logger"
)

func nilHandler(t *testing.T) *Handler { return NewHandler(nilService(), testLogger(t)) }

func testLogger(t *testing.T) *logger.Logger {
	t.Helper()

	log, err := logger.New(logger.Config{ServiceName: "test", Path: t.TempDir()})
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	return log
}

func TestHandlerMapsValidationErrorsTo400(t *testing.T) {
	handler := nilHandler(t)

	t.Run("create", func(t *testing.T) {
		request := examplecontract.CreateExampleNoteRequestObject{Body: &examplecontract.CreateNoteRequest{Title: "  "}}

		response, err := handler.CreateExampleNote(context.Background(), request)
		if err != nil {
			t.Fatalf("CreateExampleNote: %v", err)
		}
		bad, ok := response.(examplecontract.CreateExampleNote400JSONResponse)
		if !ok {
			t.Fatalf("response = %T, want a 400", response)
		}
		if bad.Message != "title is required" {
			t.Errorf("message = %q, want the service's message", bad.Message)
		}
	})

	t.Run("list", func(t *testing.T) {
		limit := maxLimit + 1
		request := examplecontract.ListExampleNotesRequestObject{
			Params: examplecontract.ListExampleNotesParams{Limit: &limit},
		}

		response, err := handler.ListExampleNotes(context.Background(), request)
		if err != nil {
			t.Fatalf("ListExampleNotes: %v", err)
		}
		if _, ok := response.(examplecontract.ListExampleNotes400JSONResponse); !ok {
			t.Errorf("response = %T, want a 400", response)
		}
	})
}

func TestCreateWithoutBodyIs400(t *testing.T) {
	response, err := nilHandler(t).CreateExampleNote(
		context.Background(), examplecontract.CreateExampleNoteRequestObject{})
	if err != nil {
		t.Fatalf("CreateExampleNote: %v", err)
	}

	bad, ok := response.(examplecontract.CreateExampleNote400JSONResponse)
	if !ok {
		t.Fatalf("response = %T, want a 400", response)
	}
	if bad.Message != "a JSON body is required" {
		t.Errorf("message = %q", bad.Message)
	}
}

func TestHandlerPassesRealFailuresThrough(t *testing.T) {
	repo, mock := mockRepository(t)
	mock.ExpectQuery(`SELECT \* FROM "example_notes"`).WillReturnError(errors.New("connection reset"))

	handler := NewHandler(NewService(nil, repo), testLogger(t))
	response, err := handler.ListExampleNotes(context.Background(), examplecontract.ListExampleNotesRequestObject{})

	if err == nil {
		t.Fatalf("response = %v, want the failure to surface as an error", response)
	}
	var invalid ValidationError
	if errors.As(err, &invalid) {
		t.Error("a database failure was reported as a caller mistake")
	}
}

func TestGetExampleVisits(t *testing.T) {
	service, server := miniredisService(t)
	handler := NewHandler(service, testLogger(t))

	response, err := handler.GetExampleVisits(context.Background(), examplecontract.GetExampleVisitsRequestObject{})
	if err != nil {
		t.Fatalf("GetExampleVisits: %v", err)
	}
	ok, isOK := response.(examplecontract.GetExampleVisits200JSONResponse)
	if !isOK {
		t.Fatalf("response = %T, want a 200", response)
	}
	if ok.Visits != 1 {
		t.Errorf("visits = %d, want 1", ok.Visits)
	}

	server.Close()
	request := examplecontract.GetExampleVisitsRequestObject{}
	if _, err := handler.GetExampleVisits(context.Background(), request); err == nil {
		t.Error("expected the Redis failure to surface as an error")
	}
}

func TestCreateExampleNoteReturns201(t *testing.T) {
	repo, mock := mockRepository(t)
	expectInsert(mock, 3)

	handler := NewHandler(NewService(nil, repo), testLogger(t))
	request := examplecontract.CreateExampleNoteRequestObject{
		Body: &examplecontract.CreateNoteRequest{Title: "First note", Body: new("the body")},
	}

	response, err := handler.CreateExampleNote(context.Background(), request)
	if err != nil {
		t.Fatalf("CreateExampleNote: %v", err)
	}

	created, ok := response.(examplecontract.CreateExampleNote201JSONResponse)
	if !ok {
		t.Fatalf("response = %T, want a 201", response)
	}
	if created.Id != 3 || created.Title != "First note" || created.Body != "the body" {
		t.Errorf("note = %+v, want the stored values", created)
	}
}

func TestCreateExampleNotePassesRealFailuresThrough(t *testing.T) {
	repo, mock := mockRepository(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "example_notes"`).WillReturnError(errors.New("disk full"))
	mock.ExpectRollback()

	handler := NewHandler(NewService(nil, repo), testLogger(t))
	request := examplecontract.CreateExampleNoteRequestObject{
		Body: &examplecontract.CreateNoteRequest{Title: "First note"},
	}

	if _, err := handler.CreateExampleNote(context.Background(), request); err == nil {
		t.Fatal("expected the insert failure to surface as an error")
	}
}

func TestListExampleNotesReturnsEveryRow(t *testing.T) {
	repo, mock := mockRepository(t)
	created := time.Date(2026, 8, 17, 10, 30, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT \* FROM "example_notes"`).
		WillReturnRows(mock.NewRows([]string{"id", "title", "body", "created_at"}).
			AddRow(int64(2), "newer", "", created).
			AddRow(int64(1), "older", "", created))

	handler := NewHandler(NewService(nil, repo), testLogger(t))
	response, err := handler.ListExampleNotes(context.Background(), examplecontract.ListExampleNotesRequestObject{})
	if err != nil {
		t.Fatalf("ListExampleNotes: %v", err)
	}

	notes, ok := response.(examplecontract.ListExampleNotes200JSONResponse)
	if !ok {
		t.Fatalf("response = %T, want a 200", response)
	}
	if len(notes) != 2 || notes[0].Title != "newer" || notes[1].Id != 1 {
		t.Errorf("notes = %+v, want both rows in order", notes)
	}
}

func TestListExampleNotesWithNoRows(t *testing.T) {
	repo, mock := mockRepository(t)
	mock.ExpectQuery(`SELECT \* FROM "example_notes"`).
		WillReturnRows(mock.NewRows([]string{"id", "title", "body", "created_at"}))

	handler := NewHandler(NewService(nil, repo), testLogger(t))
	response, err := handler.ListExampleNotes(context.Background(), examplecontract.ListExampleNotesRequestObject{})
	if err != nil {
		t.Fatalf("ListExampleNotes: %v", err)
	}

	notes, ok := response.(examplecontract.ListExampleNotes200JSONResponse)
	if !ok || notes == nil {
		t.Fatalf("response = %#v, want an empty non-nil slice", response)
	}
	if len(notes) != 0 {
		t.Errorf("notes = %+v, want none", notes)
	}
}

func TestToContract(t *testing.T) {
	created := time.Date(2026, 8, 17, 10, 30, 0, 0, time.UTC)
	got := toContract(Note{ID: 7, Title: "the title", Body: "the body", CreatedAt: created})

	want := examplecontract.Note{Id: 7, Title: "the title", Body: "the body", CreatedAt: created}
	if got != want {
		t.Errorf("toContract = %+v, want %+v", got, want)
	}
}
