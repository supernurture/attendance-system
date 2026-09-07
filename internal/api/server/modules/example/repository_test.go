package example

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func mockRepository(t *testing.T) (*Repository, sqlmock.Sqlmock) {
	t.Helper()

	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(
		postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}),
		&gorm.Config{DisableAutomaticPing: true},
	)
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	return NewRepository(db), mock
}

func expectInsert(mock sqlmock.Sqlmock, id int64) {
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "example_notes" \("title","body","created_at"\) VALUES \([^)]*\) RETURNING "id"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(id))
	mock.ExpectQuery(`INSERT INTO "example_note_events"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(id))
	mock.ExpectCommit()
}

func TestCreateLetsPostgresGenerateTheID(t *testing.T) {
	repo, mock := mockRepository(t)
	expectInsert(mock, 42)

	note := Note{Title: "a title", Body: "a body"}
	if err := repo.Create(context.Background(), &note); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if note.ID != 42 {
		t.Errorf("ID = %d, want the 42 the database generated", note.ID)
	}
	if note.CreatedAt.IsZero() {
		t.Error("CreatedAt was not set")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestCreateRollsBackTheNoteWhenTheEventFails(t *testing.T) {
	repo, mock := mockRepository(t)

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "example_notes"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))
	mock.ExpectQuery(`INSERT INTO "example_note_events"`).WillReturnError(errors.New("audit table is gone"))
	mock.ExpectRollback()

	note := Note{Title: "a title"}
	if err := repo.Create(context.Background(), &note); err == nil {
		t.Fatal("Create returned nil, want the event failure to surface")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestListReadsNewestFirst(t *testing.T) {
	repo, mock := mockRepository(t)

	created := time.Date(2026, 8, 17, 10, 30, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT \* FROM "example_notes" ORDER BY id DESC LIMIT`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "title", "body", "created_at"}).
			AddRow(int64(2), "newer", "", created).
			AddRow(int64(1), "older", "", created))

	notes, err := repo.List(context.Background(), 20)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(notes) != 2 || notes[0].Title != "newer" {
		t.Errorf("notes = %+v, want the newer one first", notes)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
