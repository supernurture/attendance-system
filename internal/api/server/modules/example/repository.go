package example

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/supernurture/go-template/pkg/database"
)

type Note struct {
	ID        int64 `gorm:"primaryKey"`
	Title     string
	Body      string
	CreatedAt time.Time
}

func (Note) TableName() string { return "example_notes" }

type NoteEvent struct {
	ID        int64 `gorm:"primaryKey"`
	NoteID    int64
	Action    string
	CreatedAt time.Time
}

func (NoteEvent) TableName() string { return "example_note_events" }

type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Create(ctx context.Context, note *Note) error {
	return database.WithTransaction(ctx, r.db, func(tx *gorm.DB) error {
		if err := tx.Create(note).Error; err != nil {
			return err
		}
		return tx.Create(&NoteEvent{NoteID: note.ID, Action: "created"}).Error
	})
}

func (r *Repository) List(ctx context.Context, limit int) ([]Note, error) {
	var notes []Note
	err := r.db.WithContext(ctx).Order("id DESC").Limit(limit).Find(&notes).Error
	return notes, err
}
