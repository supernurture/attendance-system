package database

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/gorm"
)

func TestWithTransactionCommit(t *testing.T) {
	db, mock := mockDB(t)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := WithTransaction(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Exec("UPDATE users SET name = 'go'").Error
	})
	if err != nil {
		t.Fatalf("WithTransaction: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestWithTransactionRollback(t *testing.T) {
	db, mock := mockDB(t)

	mock.ExpectBegin()
	mock.ExpectRollback()

	wantErr := errors.New("error at WithTransaction")
	err := WithTransaction(context.Background(), db, func(tx *gorm.DB) error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("WithTransaction error = %v, want %v", err, wantErr)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestWithTransactionResult(t *testing.T) {
	db, mock := mockDB(t)

	mock.ExpectBegin()
	mock.ExpectCommit()
	got, err := WithTransactionResult(context.Background(), db, func(tx *gorm.DB) (int, error) { return 2, nil })
	if err != nil || got != 2 {
		t.Fatalf("WithTransactionResult = (%d, %v), want (2, nil)", got, err)
	}

	mock.ExpectBegin()
	mock.ExpectRollback()
	wantErr := errors.New("error at WithTransactionResult")
	got, err = WithTransactionResult(context.Background(), db, func(tx *gorm.DB) (int, error) { return 2, wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if got != 0 {
		t.Errorf("rollback returned %d, want zero value", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
