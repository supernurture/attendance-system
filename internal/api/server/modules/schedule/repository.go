package schedule

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"attendance-system/internal/pkg/apperr"
)

// WorkSchedule is one set of hours. Whether it crosses midnight is derived from the two times, never
// stored, so they cannot disagree.
type WorkSchedule struct {
	ID               int64
	Name             string
	StartTime        Clock
	EndTime          Clock
	GraceMinutes     int
	BreakMinutes     int
	Workdays         pqInt16Array `gorm:"type:smallint[]"`
	ObservesHolidays bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        gorm.DeletedAt
}

func (WorkSchedule) TableName() string { return "work_schedules" }

// CrossesMidnight is true for a night shift such as 22:00–06:00.
func (s WorkSchedule) CrossesMidnight() bool { return s.EndTime <= s.StartTime }

// ShiftAssignment is the roster: one row per person per date. A nil ScheduleID is a planned day off.
type ShiftAssignment struct {
	ID         int64
	UserID     int64
	WorkDate   time.Time
	ScheduleID *int64
	Note       string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (ShiftAssignment) TableName() string { return "shift_assignments" }

type Holiday struct {
	ID        int64
	Date      time.Time
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (Holiday) TableName() string { return "holidays" }

type OfficeLocation struct {
	ID        int64
	Name      string
	Lat       float64
	Lng       float64
	RadiusM   int
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt
}

func (OfficeLocation) TableName() string { return "office_locations" }

// AuditLog mirrors the audit_logs row the user module also writes.
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

// employee is the part of users this module reads: which schedule a person keeps by default.
type employee struct {
	ID                int64
	DefaultScheduleID *int64
}

func (employee) TableName() string { return "users" }

type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) ListSchedules(ctx context.Context) ([]WorkSchedule, error) {
	var schedules []WorkSchedule
	return schedules, r.db.WithContext(ctx).Order("name, id").Find(&schedules).Error
}

func (r *Repository) ScheduleByID(ctx context.Context, id int64) (WorkSchedule, error) {
	var schedule WorkSchedule
	err := r.db.WithContext(ctx).Take(&schedule, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return WorkSchedule{}, apperr.ErrNotFound
	}
	return schedule, err
}

func (r *Repository) CreateSchedule(ctx context.Context, schedule *WorkSchedule) error {
	return translate(r.db.WithContext(ctx).Create(schedule).Error)
}

func (r *Repository) ReplaceSchedule(ctx context.Context, schedule WorkSchedule) (WorkSchedule, error) {
	result := r.db.WithContext(ctx).Model(&WorkSchedule{}).Where("id = ?", schedule.ID).
		Updates(map[string]any{
			"name":              schedule.Name,
			"start_time":        schedule.StartTime,
			"end_time":          schedule.EndTime,
			"grace_minutes":     schedule.GraceMinutes,
			"break_minutes":     schedule.BreakMinutes,
			"workdays":          schedule.Workdays,
			"observes_holidays": schedule.ObservesHolidays,
			"updated_at":        time.Now(),
		})
	if result.Error != nil {
		return WorkSchedule{}, translate(result.Error)
	}
	if result.RowsAffected == 0 {
		return WorkSchedule{}, apperr.ErrNotFound
	}
	return r.ScheduleByID(ctx, schedule.ID)
}

// DeleteSchedule keeps the row, so attendance that pointed at it still reads back.
func (r *Repository) DeleteSchedule(ctx context.Context, id int64) error {
	return deleted(r.db.WithContext(ctx).Delete(&WorkSchedule{}, id))
}

// ScheduleInUse reports whether a live employee still keeps this schedule by default or is rostered
// onto it from today on, which is what stops it being removed. Past roster does not count: resolution
// loads schedules Unscoped, so history keeps reading back either way.
func (r *Repository) ScheduleInUse(ctx context.Context, id int64) (bool, error) {
	var users, assignments int64
	if err := r.db.WithContext(ctx).Model(&employee{}).
		Where("default_schedule_id = ? AND deleted_at IS NULL", id).Count(&users).Error; err != nil {
		return false, err
	}
	err := r.db.WithContext(ctx).Model(&ShiftAssignment{}).
		Where("schedule_id = ? AND work_date >= CURRENT_DATE", id).Count(&assignments).Error
	return users+assignments > 0, err
}

func (r *Repository) ListHolidays(ctx context.Context, span Range) ([]Holiday, error) {
	var holidays []Holiday
	err := r.db.WithContext(ctx).Where("date BETWEEN ? AND ?", span.From, span.To).
		Order("date").Find(&holidays).Error
	return holidays, err
}

func (r *Repository) HolidayByID(ctx context.Context, id int64) (Holiday, error) {
	var holiday Holiday
	err := r.db.WithContext(ctx).Take(&holiday, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Holiday{}, apperr.ErrNotFound
	}
	return holiday, err
}

// CreateHoliday writes the holiday and, when one is given, its audit row in the same transaction; the
// audit's entity id is filled in here because it exists only once the row does.
func (r *Repository) CreateHoliday(ctx context.Context, holiday *Holiday, audit *AuditLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := translate(tx.Create(holiday).Error); err != nil {
			return err
		}
		if audit != nil {
			audit.EntityID = holiday.ID
		}
		return writeAudit(tx, audit)
	})
}

// ReplaceHoliday writes the holiday and its audit row together when one is given.
func (r *Repository) ReplaceHoliday(ctx context.Context, holiday Holiday, audit *AuditLog) (Holiday, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&Holiday{}).Where("id = ?", holiday.ID).
			Updates(map[string]any{"date": holiday.Date, "name": holiday.Name, "updated_at": time.Now()})
		if result.Error != nil {
			return translate(result.Error)
		}
		if result.RowsAffected == 0 {
			return apperr.ErrNotFound
		}
		return writeAudit(tx, audit)
	})
	if err != nil {
		return Holiday{}, err
	}
	return r.HolidayByID(ctx, holiday.ID)
}

func (r *Repository) DeleteHoliday(ctx context.Context, id int64, audit *AuditLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := deleted(tx.Delete(&Holiday{}, id)); err != nil {
			return err
		}
		return writeAudit(tx, audit)
	})
}

func (r *Repository) ListLocations(ctx context.Context) ([]OfficeLocation, error) {
	var locations []OfficeLocation
	return locations, r.db.WithContext(ctx).Order("name, id").Find(&locations).Error
}

func (r *Repository) LocationByID(ctx context.Context, id int64) (OfficeLocation, error) {
	var location OfficeLocation
	err := r.db.WithContext(ctx).Take(&location, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return OfficeLocation{}, apperr.ErrNotFound
	}
	return location, err
}

func (r *Repository) CreateLocation(ctx context.Context, location *OfficeLocation) error {
	return translate(r.db.WithContext(ctx).Create(location).Error)
}

func (r *Repository) ReplaceLocation(ctx context.Context, location OfficeLocation) (OfficeLocation, error) {
	result := r.db.WithContext(ctx).Model(&OfficeLocation{}).Where("id = ?", location.ID).
		Updates(map[string]any{
			"name": location.Name, "lat": location.Lat, "lng": location.Lng,
			"radius_m": location.RadiusM, "updated_at": time.Now(),
		})
	if result.Error != nil {
		return OfficeLocation{}, translate(result.Error)
	}
	if result.RowsAffected == 0 {
		return OfficeLocation{}, apperr.ErrNotFound
	}
	return r.LocationByID(ctx, location.ID)
}

func (r *Repository) DeleteLocation(ctx context.Context, id int64) error {
	return deleted(r.db.WithContext(ctx).Delete(&OfficeLocation{}, id))
}

// UpsertAssignments writes a whole roster slice, replacing whatever each person already had on those
// dates. When audits is given it is called inside the transaction with the rows about to be replaced,
// so an audit entry cannot describe a state that something else changed in between.
func (r *Repository) UpsertAssignments(
	ctx context.Context, assignments []ShiftAssignment, audits func(before []ShiftAssignment) []AuditLog,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var written []AuditLog
		if audits != nil {
			before, err := lockAssignments(tx, assignments)
			if err != nil {
				return err
			}
			written = audits(before)
		}

		err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "work_date"}},
			DoUpdates: clause.AssignmentColumns([]string{"schedule_id", "note", "updated_at"}),
		}).Create(&assignments).Error
		if err != nil {
			return translate(err)
		}
		for _, audit := range written {
			if err := writeAudit(tx, &audit); err != nil {
				return err
			}
		}
		return nil
	})
}

// lockAssignments reads the rows these writes are about to replace and holds them for the transaction.
func lockAssignments(tx *gorm.DB, assignments []ShiftAssignment) ([]ShiftAssignment, error) {
	pairs := make([]string, 0, len(assignments))
	args := make([]any, 0, len(assignments)*2)
	for _, assignment := range assignments {
		pairs = append(pairs, "(?,?)")
		args = append(args, assignment.UserID, assignment.WorkDate)
	}

	var before []ShiftAssignment
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("(user_id, work_date) IN ("+strings.Join(pairs, ",")+")", args...).
		Find(&before).Error
	return before, err
}

func (r *Repository) AssignmentByID(ctx context.Context, id int64) (ShiftAssignment, error) {
	var assignment ShiftAssignment
	err := r.db.WithContext(ctx).Take(&assignment, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ShiftAssignment{}, apperr.ErrNotFound
	}
	return assignment, err
}

func (r *Repository) DeleteAssignment(ctx context.Context, id int64, audit *AuditLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := deleted(tx.Delete(&ShiftAssignment{}, id)); err != nil {
			return err
		}
		return writeAudit(tx, audit)
	})
}

// ListAssignments returns the roster for a span, for everyone or for one person.
func (r *Repository) ListAssignments(ctx context.Context, span Range, userID *int64) ([]ShiftAssignment, error) {
	query := r.db.WithContext(ctx).Where("work_date BETWEEN ? AND ?", span.From, span.To)
	if userID != nil {
		query = query.Where("user_id = ?", *userID)
	}

	var assignments []ShiftAssignment
	return assignments, query.Order("work_date, user_id").Find(&assignments).Error
}

// DefaultScheduleID is the schedule a person keeps when the roster says nothing.
func (r *Repository) DefaultScheduleID(ctx context.Context, userID int64) (*int64, error) {
	var person employee
	err := r.db.WithContext(ctx).Where("id = ? AND deleted_at IS NULL", userID).Take(&person).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperr.ErrNotFound
	}
	return person.DefaultScheduleID, err
}

// SchedulesByID loads the schedules rule A will need, deleted ones included: a roster row or a
// default may still point at a schedule that was retired.
func (r *Repository) SchedulesByID(ctx context.Context, ids []int64) (map[int64]WorkSchedule, error) {
	byID := make(map[int64]WorkSchedule, len(ids))
	if len(ids) == 0 {
		return byID, nil
	}

	var schedules []WorkSchedule
	if err := r.db.WithContext(ctx).Unscoped().Where("id IN ?", ids).Find(&schedules).Error; err != nil {
		return nil, err
	}
	for _, schedule := range schedules {
		byID[schedule.ID] = schedule
	}
	return byID, nil
}

func writeAudit(tx *gorm.DB, audit *AuditLog) error {
	if audit == nil {
		return nil
	}
	return tx.Create(audit).Error
}

// deleted turns "nothing matched" into ErrNotFound, so the handler answers 404 instead of 204.
func deleted(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return apperr.ErrNotFound
	}
	return nil
}

// translate turns the constraint violations this module can provoke into errors the handler maps.
func translate(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case "23505": // unique_violation
		return apperr.ErrConflict
	case "23503": // foreign_key_violation
		return apperr.Invalid("user_id or schedule_id does not exist")
	case "23514": // check_violation
		return apperr.Invalid("a value is outside the range the column allows: %s", pgErr.ConstraintName)
	}
	return err
}
