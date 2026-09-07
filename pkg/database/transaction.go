package database

import (
	"context"

	"gorm.io/gorm"
)

// WithTransaction runs fn in a transaction: commit on nil, rollback on error.
func WithTransaction(ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) error) error {
	return db.WithContext(ctx).Transaction(fn)
}

// WithTransactionResult is WithTransaction for an fn returning a value; zero value on rollback.
func WithTransactionResult[T any](ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) (T, error)) (T, error) {
	var result T

	err := WithTransaction(ctx, db, func(tx *gorm.DB) error {
		value, err := fn(tx)
		if err != nil {
			return err
		}
		result = value
		return nil
	})
	if err != nil {
		var zero T
		return zero, err
	}

	return result, nil
}
