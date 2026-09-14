package leave

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"attendance-system/internal/api/server/modules/user"
	"attendance-system/internal/pkg/apperr"
	"attendance-system/internal/pkg/storage"
)

// LeaveType is one kind of leave and the rules it carries. A nil rule does not apply.
type LeaveType struct {
	ID                         int64
	Code                       string
	Name                       string
	QuotaDaysPerYear           *int
	MaxWorkingDaysPerRequest   *int
	AttachmentRequiredFromDays *int
	IsPaid                     bool
	AutoApprove                bool
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
	DeletedAt                  gorm.DeletedAt
}

func (LeaveType) TableName() string { return "leave_types" }

// Quota is one person's allowance of a type for a year. The days used are never stored next to it.
type Quota struct {
	UserID          int64 `gorm:"primaryKey"`
	LeaveTypeID     int64 `gorm:"primaryKey"`
	Year            int   `gorm:"primaryKey"`
	QuotaDays       int
	CarriedOverDays int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (Quota) TableName() string { return "leave_balances" }

// Leave is one request for leave and, once decided, who decided it.
type Leave struct {
	ID             int64
	UserID         int64
	LeaveTypeID    int64
	StartDate      time.Time
	EndDate        time.Time
	WorkingDays    int
	Reason         string
	AttachmentKey  *string
	AttachmentMime *string
	Status         string
	ReviewedBy     *int64
	ReviewedAt     *time.Time
	ReviewNote     *string
	CancelledBy    *int64
	CancelledAt    *time.Time
	CreatedAt      time.Time
}

func (Leave) TableName() string { return "leave_requests" }

// Balance is rule C for one type and year: the quota, less every pending and approved request starting that year.
type Balance struct {
	LeaveTypeID     int64
	Code            string
	Name            string
	Year            int
	QuotaDays       int
	CarriedOverDays int
	UsedDays        int
	PendingDays     int
}

// RemainingDays counts pending requests as spent, so nobody can book past the quota while waiting.
func (b Balance) RemainingDays() int {
	return b.QuotaDays + b.CarriedOverDays - b.UsedDays - b.PendingDays
}

// AuditLog mirrors the audit_logs row the user and schedule modules also write.
type AuditLog struct {
	ID         int64
	ActorID    int64
	Action     string
	EntityType string
	EntityID   int64
	Before     []byte `gorm:"type:jsonb"`
	After      []byte `gorm:"type:jsonb"`
	CreatedAt  time.Time
}

func (AuditLog) TableName() string { return "audit_logs" }

// person is the part of users this module reads: who reports to whom.
type person struct {
	ID int64
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

// Types lists the live leave types by code.
func (r *Repository) Types(ctx context.Context) ([]LeaveType, error) {
	var types []LeaveType
	return types, r.db.WithContext(ctx).Order("code").Find(&types).Error
}

// TypeByID fails with apperr.ErrNotFound when no live type has that id.
func (r *Repository) TypeByID(ctx context.Context, id int64) (LeaveType, error) {
	var kind LeaveType
	return kind, found(r.db.WithContext(ctx).Take(&kind, id).Error)
}

// TypeByCode fails with apperr.ErrNotFound when no live type has that code.
func (r *Repository) TypeByCode(ctx context.Context, code string) (LeaveType, error) {
	var kind LeaveType
	return kind, found(r.db.WithContext(ctx).Where("code = ?", code).Take(&kind).Error)
}

// CreateType stores a new leave type; a live one with the same code is a conflict.
func (r *Repository) CreateType(ctx context.Context, kind *LeaveType) error {
	return translate(r.db.WithContext(ctx).Create(kind).Error)
}

// ReplaceType rewrites a leave type. audit sees the row as it stood, locked inside the same transaction, so its
// entry cannot describe a state something else changed in between; it returns nil when nothing is worth recording.
func (r *Repository) ReplaceType(
	ctx context.Context, kind LeaveType, audit func(before LeaveType) *AuditLog,
) (LeaveType, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before LeaveType
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&before, kind.ID).Error; err != nil {
			return found(err)
		}

		err := tx.Model(&LeaveType{}).Where("id = ?", kind.ID).Updates(map[string]any{
			"code":                          kind.Code,
			"name":                          kind.Name,
			"quota_days_per_year":           kind.QuotaDaysPerYear,
			"max_working_days_per_request":  kind.MaxWorkingDaysPerRequest,
			"attachment_required_from_days": kind.AttachmentRequiredFromDays,
			"is_paid":                       kind.IsPaid,
			"auto_approve":                  kind.AutoApprove,
			"updated_at":                    time.Now(),
		}).Error
		if err != nil {
			return translate(err)
		}
		return writeAudit(tx, audit(before))
	})
	if err != nil {
		return LeaveType{}, err
	}
	return r.TypeByID(ctx, kind.ID)
}

// DeleteType keeps the row, so the requests that point at it still read back.
func (r *Repository) DeleteType(ctx context.Context, id int64) error {
	result := r.db.WithContext(ctx).Delete(&LeaveType{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return apperr.ErrNotFound
	}
	return nil
}

// SetQuota replaces one person's quota for a type and year, keeping the carried-over days already held when
// keepCarriedOver is set. audit sees the row it replaces, nil when there was none, locked inside the same transaction,
// and the row as written.
func (r *Repository) SetQuota(
	ctx context.Context, quota Quota, keepCarriedOver bool, audit func(before *Quota, after Quota) AuditLog,
) (Quota, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var held []Quota
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND leave_type_id = ? AND year = ?", quota.UserID, quota.LeaveTypeID, quota.Year).
			Find(&held).Error
		if err != nil {
			return err
		}
		var before *Quota
		if len(held) > 0 {
			before = &held[0]
			if keepCarriedOver {
				quota.CarriedOverDays = before.CarriedOverDays
			}
		}

		err = tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "leave_type_id"}, {Name: "year"}},
			DoUpdates: clause.AssignmentColumns([]string{"quota_days", "carried_over_days", "updated_at"}),
		}).Create(&quota).Error
		if err != nil {
			return translate(err)
		}
		entry := audit(before, quota)
		return writeAudit(tx, &entry)
	})
	return quota, err
}

// Balances is rule C for every live type with a yearly quota, by code.
func (r *Repository) Balances(ctx context.Context, userID int64, year int) ([]Balance, error) {
	var rows []Balance
	err := balances(r.db.WithContext(ctx), userID, year,
		"t.deleted_at IS NULL AND t.quota_days_per_year IS NOT NULL").Scan(&rows).Error
	for x := range rows {
		rows[x].Year = year
	}
	return rows, err
}

// Create files a request. When limit is given, the person's balance for the request's type and year is read and
// passed to it inside the transaction, with their other filings held off until it commits, so two requests cannot
// each spend the same remaining days.
func (r *Repository) Create(ctx context.Context, row *Leave, limit func(Balance) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if limit != nil {
			var locked int64
			err := tx.Raw("SELECT id FROM users WHERE id = ? FOR NO KEY UPDATE", row.UserID).Scan(&locked).Error
			if err != nil {
				return err
			}

			held := Balance{Year: row.StartDate.Year()}
			err = balances(tx, row.UserID, row.StartDate.Year(), "t.id = ?", row.LeaveTypeID).Scan(&held).Error
			if err != nil {
				return err
			}
			if err := limit(held); err != nil {
				return err
			}
		}
		return translate(tx.Create(row).Error)
	})
}

// AttachmentUsed reports whether a key already backs a request.
func (r *Repository) AttachmentUsed(ctx context.Context, key string) (bool, error) {
	var used int64
	err := r.db.WithContext(ctx).Model(&Leave{}).Where("attachment_key = ?", key).Count(&used).Error
	return used > 0, err
}

// ByID fails with apperr.ErrNotFound when no request has that id.
func (r *Repository) ByID(ctx context.Context, id int64) (Leave, error) {
	var row Leave
	return row, found(r.db.WithContext(ctx).Take(&row, id).Error)
}

// Of lists one person's requests, newest first.
func (r *Repository) Of(ctx context.Context, userID int64) ([]Leave, error) {
	var rows []Leave
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at DESC, id DESC").Find(&rows).Error
	return rows, err
}

// Pending lists what waits for a decision, oldest first: for everyone, or for those below managerID when given,
// and never the decider's own.
func (r *Repository) Pending(ctx context.Context, managerID *int64, deciderID int64) ([]Leave, error) {
	query := r.db.WithContext(ctx).Where("status = ? AND user_id <> ?", statusPending, deciderID)
	if managerID != nil {
		query = query.Where("user_id IN (?)", r.db.Raw(user.SubtreeQuery, *managerID))
	}

	var rows []Leave
	return rows, query.Order("created_at, id").Find(&rows).Error
}

// Settle moves a request on from the status it was read in, and fails with a conflict when it has left that status
// since.
func (r *Repository) Settle(ctx context.Context, id int64, from string, decided map[string]any) (Leave, error) {
	var settled Leave
	result := r.db.WithContext(ctx).Model(&settled).Clauses(clause.Returning{}).
		Where("id = ? AND status = ?", id, from).Updates(decided)
	if result.Error != nil {
		return Leave{}, result.Error
	}
	if result.RowsAffected == 0 {
		return Leave{}, fmt.Errorf("%w: the leave request is no longer %s", apperr.ErrConflict, from)
	}
	return settled, nil
}

// InSubtree reports whether userID sits anywhere below managerID.
func (r *Repository) InSubtree(ctx context.Context, managerID, userID int64) (bool, error) {
	var below int64
	err := r.db.WithContext(ctx).Model(&person{}).
		Where("id = ? AND id IN (?)", userID, r.db.Raw(user.SubtreeQuery, managerID)).Count(&below).Error
	return below > 0, err
}

// AttachmentURL signs a short-lived download of one object.
func (r *Repository) AttachmentURL(ctx context.Context, key string) (string, error) {
	signed, err := presignGet(r.store, ctx, key)
	return signed.URL, err
}

// balances builds rule C for the types where matches. A person without a quota row has the type's own quota, and
// a type whose quota was since removed counts as none left.
func balances(db *gorm.DB, userID int64, year int, where string, args ...any) *gorm.DB {
	from := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(year, time.December, 31, 0, 0, 0, 0, time.UTC)
	query := `SELECT t.id AS leave_type_id, t.code, t.name,
			COALESCE(b.quota_days, t.quota_days_per_year, 0) AS quota_days,
			COALESCE(b.carried_over_days, 0) AS carried_over_days,
			COALESCE(SUM(r.working_days) FILTER (WHERE r.status = 'approved'), 0) AS used_days,
			COALESCE(SUM(r.working_days) FILTER (WHERE r.status = 'pending'), 0) AS pending_days
		FROM leave_types t
		LEFT JOIN leave_balances b ON b.leave_type_id = t.id AND b.user_id = ? AND b.year = ?
		LEFT JOIN leave_requests r ON r.leave_type_id = t.id AND r.user_id = ?
			AND r.status IN ('pending', 'approved') AND r.start_date BETWEEN ? AND ?
		WHERE ` + where + `
		GROUP BY t.id, b.quota_days, b.carried_over_days
		ORDER BY t.code`
	return db.Raw(query, append([]any{userID, year, userID, from, to}, args...)...)
}

func writeAudit(tx *gorm.DB, audit *AuditLog) error {
	if audit == nil {
		return nil
	}
	return tx.Create(audit).Error
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
	case pgErr.Code == "23P01": // exclusion_violation: the only EXCLUDE is the overlap one
		return errOverlap
	case pgErr.Code == "23505" && strings.Contains(pgErr.ConstraintName, "attachment_key"): // unique_violation
		return errAttachmentUsed
	case pgErr.Code == "23505":
		return errCodeTaken
	case pgErr.Code == "23503": // foreign_key_violation
		return apperr.Invalid("user_id or leave_type_id does not exist")
	}
	return err
}
