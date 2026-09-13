package attendance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"attendance-system/internal/api/server/modules/schedule"
	"attendance-system/internal/api/server/modules/user"
	"attendance-system/internal/pkg/apperr"
	"attendance-system/internal/pkg/storage"
)

// Evidence is what one side of an attendance proved: where the phone was and which selfie it sent. It is all nil
// on an attendance an approved correction created; a correction that moves a time keeps the evidence, and its
// old_* times say when that evidence was taken.
type Evidence struct {
	Lat            *float64
	Lng            *float64
	AccuracyM      *float64
	PhotoKey       *string
	LocationID     *int64
	WithinGeofence *bool
	MockLocation   *bool
}

// Attendance is one person's work date: when they came and left, the evidence for both, and the daily report.
type Attendance struct {
	ID                   int64
	UserID               int64
	WorkDate             time.Time
	ScheduleID           *int64
	CheckInAt            time.Time
	CheckOutAt           *time.Time
	CheckIn              Evidence `gorm:"embedded;embeddedPrefix:check_in_"`
	CheckOut             Evidence `gorm:"embedded;embeddedPrefix:check_out_"`
	LateMinutes          int
	EarlyLeaveMinutes    int
	DailyReport          *string
	DailyReportUpdatedAt *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

func (Attendance) TableName() string { return "attendances" }

// Correction is one request to change a work date's times and, once approved, the times it replaced.
type Correction struct {
	ID                 int64
	UserID             int64
	WorkDate           time.Time
	RequestedBy        int64
	ProposedCheckInAt  *time.Time
	ProposedCheckOutAt *time.Time
	OldCheckInAt       *time.Time
	OldCheckOutAt      *time.Time
	Reason             string
	Status             string
	ReviewedBy         *int64
	ReviewedAt         *time.Time
	ReviewNote         *string
	CreatedAt          time.Time
}

func (Correction) TableName() string { return "attendance_corrections" }

// person is the part of users whos-in lists.
type person struct {
	ID                int64
	FullName          string
	DepartmentID      *int64
	DefaultScheduleID *int64
}

func (person) TableName() string { return "users" }

// presignGet is a seam: presigning with static credentials never fails, so only a test can make it.
var presignGet = (*storage.Storage).PresignGet

type Repository struct {
	db    *gorm.DB
	store *storage.Storage
}

func NewRepository(db *gorm.DB, store *storage.Storage) *Repository {
	return &Repository{db: db, store: store}
}

// Create stores a new attendance; a second one on the same work date is a conflict.
func (r *Repository) Create(ctx context.Context, row *Attendance) error {
	return translate(r.db.WithContext(ctx).Create(row).Error)
}

// PhotoKeyUsed reports whether a key already backs a check-in or a check-out. Each column's UNIQUE cannot see
// the other column.
func (r *Repository) PhotoKeyUsed(ctx context.Context, key string) (bool, error) {
	var found int64
	err := r.db.WithContext(ctx).Model(&Attendance{}).
		Where("check_in_photo_key = ? OR check_out_photo_key = ?", key, key).Count(&found).Error
	return found > 0, err
}

// OpenCheckIn is the person's latest check-in since that moment that has no check-out yet.
func (r *Repository) OpenCheckIn(ctx context.Context, userID int64, since time.Time) (Attendance, error) {
	var row Attendance
	err := r.db.WithContext(ctx).Where("user_id = ? AND check_out_at IS NULL AND check_in_at >= ?", userID, since).
		Order("check_in_at DESC").Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Attendance{}, errNoOpenCheckIn
	}
	return row, err
}

// CheckOut writes the check-out side, unless another check-out got there first.
func (r *Repository) CheckOut(ctx context.Context, row Attendance) error {
	result := r.db.WithContext(ctx).Model(&Attendance{}).Where("id = ? AND check_out_at IS NULL", row.ID).
		Updates(map[string]any{
			"check_out_at":              row.CheckOutAt,
			"check_out_lat":             row.CheckOut.Lat,
			"check_out_lng":             row.CheckOut.Lng,
			"check_out_accuracy_m":      row.CheckOut.AccuracyM,
			"check_out_photo_key":       row.CheckOut.PhotoKey,
			"check_out_location_id":     row.CheckOut.LocationID,
			"check_out_within_geofence": row.CheckOut.WithinGeofence,
			"check_out_mock_location":   row.CheckOut.MockLocation,
			"early_leave_minutes":       row.EarlyLeaveMinutes,
			"updated_at":                time.Now(),
		})
	if result.Error != nil {
		return translate(result.Error)
	}
	if result.RowsAffected == 0 {
		return errNoOpenCheckIn
	}
	return nil
}

// ListByUser returns one person's attendance over a span, by work date.
func (r *Repository) ListByUser(ctx context.Context, userID int64, span schedule.Range) ([]Attendance, error) {
	var rows []Attendance
	err := r.db.WithContext(ctx).Where("user_id = ? AND work_date BETWEEN ? AND ?", userID, span.From, span.To).
		Order("work_date").Find(&rows).Error
	return rows, err
}

// OnDate returns the attendance on one work date: everyone's, or only below managerID when given. The scope is a
// subquery, not a list of ids, so a large company cannot run past the 65535 parameters Postgres takes.
func (r *Repository) OnDate(ctx context.Context, managerID *int64, date time.Time) ([]Attendance, error) {
	query := r.db.WithContext(ctx).Where("work_date = ?", date)
	if managerID != nil {
		query = query.Where("user_id IN (?)", r.db.Raw(user.SubtreeQuery, *managerID))
	}

	var rows []Attendance
	return rows, query.Find(&rows).Error
}

// ByID fails with apperr.ErrNotFound when no attendance has that id.
func (r *Repository) ByID(ctx context.Context, id int64) (Attendance, error) {
	var row Attendance
	return row, found(r.db.WithContext(ctx).Take(&row, id).Error)
}

// Held is the person's attendance on a work date, nil when they have none.
func (r *Repository) Held(ctx context.Context, userID int64, date time.Time) (*Attendance, error) {
	return held(r.db.WithContext(ctx), userID, date)
}

// SaveReport replaces the daily report on an attendance that already exists.
func (r *Repository) SaveReport(ctx context.Context, userID int64, date time.Time, content string) (Attendance, error) {
	var row Attendance
	now := time.Now()
	result := r.db.WithContext(ctx).Model(&row).Clauses(clause.Returning{}).
		Where("user_id = ? AND work_date = ?", userID, date).
		Updates(map[string]any{"daily_report": content, "daily_report_updated_at": now, "updated_at": now})
	if result.Error != nil {
		return Attendance{}, result.Error
	}
	if result.RowsAffected == 0 {
		return Attendance{}, fmt.Errorf("%w: no attendance on %s", apperr.ErrNotFound, date.Format(time.DateOnly))
	}
	return row, nil
}

// PhotoURL signs a short-lived download of one object.
func (r *Repository) PhotoURL(ctx context.Context, key string) (string, error) {
	signed, err := presignGet(r.store, ctx, key)
	return signed.URL, err
}

// InSubtree reports whether userID sits anywhere below managerID.
func (r *Repository) InSubtree(ctx context.Context, managerID, userID int64) (bool, error) {
	var found int64
	err := r.db.WithContext(ctx).Model(&person{}).
		Where("id = ? AND id IN (?)", userID, r.db.Raw(user.SubtreeQuery, managerID)).Count(&found).Error
	return found > 0, err
}

// People lists who is employed and active on a date, by name: everyone, or those below managerID when given.
func (r *Repository) People(ctx context.Context, managerID *int64, date time.Time) ([]person, error) {
	query := r.db.WithContext(ctx).Where("deleted_at IS NULL AND is_active AND join_date <= ?", date)
	if managerID != nil {
		query = query.Where("id IN (?)", r.db.Raw(user.SubtreeQuery, *managerID))
	}

	var people []person
	return people, query.Order("full_name, id").Find(&people).Error
}

// CreateCorrection files a correction; a second pending one for the same work date is a conflict.
func (r *Repository) CreateCorrection(ctx context.Context, correction *Correction) error {
	return translate(r.db.WithContext(ctx).Create(correction).Error)
}

// CorrectionByID fails with apperr.ErrNotFound when no correction has that id.
func (r *Repository) CorrectionByID(ctx context.Context, id int64) (Correction, error) {
	var correction Correction
	return correction, found(r.db.WithContext(ctx).Take(&correction, id).Error)
}

// CorrectionsOf lists the corrections to one person's attendance, newest first.
func (r *Repository) CorrectionsOf(ctx context.Context, userID int64) ([]Correction, error) {
	var corrections []Correction
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at DESC, id DESC").
		Find(&corrections).Error
	return corrections, err
}

// PendingCorrections lists what waits for a decision, oldest first: for everyone, or for those below managerID
// when given, and never the decider's own.
func (r *Repository) PendingCorrections(ctx context.Context, managerID *int64, deciderID int64) ([]Correction, error) {
	query := r.db.WithContext(ctx).Where("status = ? AND user_id <> ?", correctionPending, deciderID)
	if managerID != nil {
		query = query.Where("user_id IN (?)", r.db.Raw(user.SubtreeQuery, *managerID))
	}

	var corrections []Correction
	return corrections, query.Order("created_at, id").Find(&corrections).Error
}

// Settle writes a decision onto a correction that is still pending.
func (r *Repository) Settle(ctx context.Context, id int64, decided map[string]any) (Correction, error) {
	return settle(r.db.WithContext(ctx), id, decided)
}

// Approve writes a correction's times and settles it in one transaction, recording the times they replace. apply
// builds the new attendance from the one held, nil when there is none, reading through tx; both rows stay locked
// meanwhile, so the snapshot cannot describe a state that changed in between.
func (r *Repository) Approve(
	ctx context.Context, id int64, decided map[string]any,
	apply func(tx *gorm.DB, correction Correction, current *Attendance) (Attendance, error),
) (Correction, error) {
	var settled Correction
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var correction Correction
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&correction, id).Error; err != nil {
			return found(err)
		}
		if correction.Status != correctionPending {
			return errNotPending
		}

		current, err := held(tx.Clauses(clause.Locking{Strength: "UPDATE"}), correction.UserID, correction.WorkDate)
		if err != nil {
			return err
		}
		next, err := apply(tx, correction, current)
		if err != nil {
			return err
		}

		if current == nil {
			err = translate(tx.Create(&next).Error)
		} else {
			decided["old_check_in_at"], decided["old_check_out_at"] = current.CheckInAt, current.CheckOutAt
			err = tx.Model(&Attendance{}).Where("id = ?", next.ID).Updates(map[string]any{
				"check_in_at":         next.CheckInAt,
				"check_out_at":        next.CheckOutAt,
				"late_minutes":        next.LateMinutes,
				"early_leave_minutes": next.EarlyLeaveMinutes,
				"updated_at":          time.Now(),
			}).Error
		}
		if err != nil {
			return err
		}

		settled, err = settle(tx, id, decided)
		return err
	})
	return settled, err
}

func held(db *gorm.DB, userID int64, date time.Time) (*Attendance, error) {
	var row Attendance
	err := db.Where("user_id = ? AND work_date = ?", userID, date).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func settle(db *gorm.DB, id int64, decided map[string]any) (Correction, error) {
	var settled Correction
	result := db.Model(&settled).Clauses(clause.Returning{}).
		Where("id = ? AND status = ?", id, correctionPending).Updates(decided)
	if result.Error != nil {
		return Correction{}, result.Error
	}
	if result.RowsAffected == 0 {
		return Correction{}, errNotPending
	}
	return settled, nil
}

// found turns "no row" into apperr.ErrNotFound, so the handler answers 404.
func found(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.ErrNotFound
	}
	return err
}

// translate turns the constraint violations this module can provoke into errors the handler maps.
func translate(err error) error {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	switch {
	case !ok:
		return err
	case pgErr.Code == "23505" && strings.Contains(pgErr.ConstraintName, "photo_key"): // unique_violation
		return errPhotoKeyUsed
	case pgErr.Code == "23505" && pgErr.TableName == "attendance_corrections":
		return errAlreadyPending
	case pgErr.Code == "23505":
		return errCheckedIn
	case pgErr.Code == "23503": // foreign_key_violation
		return apperr.Invalid("user_id does not exist")
	}
	return err
}
