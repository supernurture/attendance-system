package user

import (
	"attendance-system/internal/pkg/apperr"
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// User is an employee. DeletedAt makes every query here skip the removed ones, and it is why
// auth refuses a deleted account: the same column filters its lookups.
type User struct {
	ID                int64
	Email             string
	PasswordHash      string
	FullName          string
	Role              string
	IsActive          bool
	JoinDate          time.Time
	DepartmentID      *int64
	ManagerID         *int64
	DefaultScheduleID *int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
	DeletedAt         gorm.DeletedAt
}

func (User) TableName() string { return "users" }

type Department struct {
	ID        int64
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt
}

func (Department) TableName() string { return "departments" }

// workSchedule is the part of work_schedules this module reads: whether one is still live.
type workSchedule struct {
	ID        int64
	DeletedAt gorm.DeletedAt
}

func (workSchedule) TableName() string { return "work_schedules" }

// AuditLog is one sensitive action, with the actor the database itself could not know.
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

type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Exists reports whether a live user has that id; the foreign key alone would accept a removed one.
func (r *Repository) Exists(ctx context.Context, id int64) (bool, error) {
	var found int64
	err := r.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).Count(&found).Error
	return found > 0, err
}

// ByID fails with apperr.ErrNotFound when no live user has that id.
func (r *Repository) ByID(ctx context.Context, id int64) (User, error) {
	var user User
	err := r.db.WithContext(ctx).Take(&user, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return User{}, apperr.ErrNotFound
	}
	return user, err
}

func (r *Repository) List(ctx context.Context, page Page) ([]User, error) {
	var users []User
	err := r.db.WithContext(ctx).Order(listOrder).
		Limit(page.Limit).Offset(page.Offset).Find(&users).Error
	return users, err
}

// listOrder breaks ties by id: ordering by name alone lets paging repeat one namesake and skip another,
// because Postgres may return equal names in any order.
const listOrder = "full_name, id"

// SubtreeQuery walks manager_id down from managerID. The CYCLE clause is what keeps circular manager
// data from looping forever; without it the query never returns.
const SubtreeQuery = `WITH RECURSIVE subordinates AS (
	SELECT id FROM users WHERE manager_id = ? AND deleted_at IS NULL
	UNION
	SELECT u.id FROM users u JOIN subordinates s ON u.manager_id = s.id WHERE u.deleted_at IS NULL
) CYCLE id SET is_cycle USING path
SELECT id FROM subordinates`

// ListSubtree returns everyone below managerID in the hierarchy, however deep.
func (r *Repository) ListSubtree(ctx context.Context, managerID int64, page Page) ([]User, error) {
	var users []User
	err := r.db.WithContext(ctx).
		Where("id IN (?)", r.db.Raw(SubtreeQuery, managerID)).
		Order(listOrder).Limit(page.Limit).Offset(page.Offset).Find(&users).Error
	return users, err
}

// InSubtree reports whether userID sits below managerID.
func (r *Repository) InSubtree(ctx context.Context, managerID, userID int64) (bool, error) {
	var found int64
	err := r.db.WithContext(ctx).Model(&User{}).
		Where("id = ? AND id IN (?)", userID, r.db.Raw(SubtreeQuery, managerID)).
		Count(&found).Error
	return found > 0, err
}

// Create stores a new user, reporting apperr.ErrConflict for a taken email.
func (r *Repository) Create(ctx context.Context, user *User) error {
	return translate(r.db.WithContext(ctx).Create(user).Error)
}

// Replace writes the user's editable columns, and audit alongside it in one transaction when given.
func (r *Repository) Replace(ctx context.Context, user User, audit *AuditLog) (User, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&User{}).Where("id = ?", user.ID).Updates(map[string]any{
			"full_name":           user.FullName,
			"is_active":           user.IsActive,
			"join_date":           user.JoinDate,
			"department_id":       user.DepartmentID,
			"manager_id":          user.ManagerID,
			"default_schedule_id": user.DefaultScheduleID,
			"updated_at":          time.Now(),
		})
		if result.Error != nil {
			return translate(result.Error)
		}
		if result.RowsAffected == 0 {
			return apperr.ErrNotFound
		}
		return writeAudit(tx, audit)
	})
	if err != nil {
		return User{}, err
	}
	return r.ByID(ctx, user.ID)
}

// ChangeRole writes the new role and its audit row together: an audit log that can go missing is
// worse than none, because it looks complete.
func (r *Repository) ChangeRole(ctx context.Context, userID int64, role string, audit AuditLog) (User, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&User{}).Where("id = ?", userID).
			Updates(map[string]any{"role": role, "updated_at": time.Now()})
		if result.Error != nil {
			return translate(result.Error)
		}
		if result.RowsAffected == 0 {
			return apperr.ErrNotFound
		}
		return writeAudit(tx, &audit)
	})
	if err != nil {
		return User{}, err
	}
	return r.ByID(ctx, userID)
}

// SoftDelete sets deleted_at, keeping the row so reports can still join what the user did, and moves
// anyone who reported to them up to their manager. Without that the subtree query stops at the
// removed row and the whole branch below it drops out of the hierarchy.
func (r *Repository) SoftDelete(ctx context.Context, id int64, audit *AuditLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Locked, so two deletes at once cannot both reparent and both write an audit row.
		var leaver User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&leaver, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.ErrNotFound
			}
			return err
		}

		reparent := tx.Model(&User{}).Where("manager_id = ?", id).
			Updates(map[string]any{"manager_id": leaver.ManagerID, "updated_at": time.Now()})
		if reparent.Error != nil {
			return reparent.Error
		}
		if err := tx.Delete(&User{}, id).Error; err != nil {
			return err
		}
		return writeAudit(tx, audit)
	})
}

// DepartmentExists reports whether a live department has that id; the foreign key alone would accept
// a removed one, since it cannot see deleted_at.
func (r *Repository) DepartmentExists(ctx context.Context, id int64) (bool, error) {
	var found int64
	err := r.db.WithContext(ctx).Model(&Department{}).Where("id = ?", id).Count(&found).Error
	return found > 0, err
}

// ScheduleExists reports whether a live work schedule has that id. Assigning a retired one would leave
// the employee with hours nobody maintains any more.
func (r *Repository) ScheduleExists(ctx context.Context, id int64) (bool, error) {
	var found int64
	err := r.db.WithContext(ctx).Model(&workSchedule{}).Where("id = ?", id).Count(&found).Error
	return found > 0, err
}

func (r *Repository) ListDepartments(ctx context.Context) ([]Department, error) {
	var departments []Department
	return departments, r.db.WithContext(ctx).Order("name").Find(&departments).Error
}

func (r *Repository) CreateDepartment(ctx context.Context, department *Department) error {
	return translate(r.db.WithContext(ctx).Create(department).Error)
}

func (r *Repository) RenameDepartment(ctx context.Context, id int64, name string) (Department, error) {
	result := r.db.WithContext(ctx).Model(&Department{}).Where("id = ?", id).
		Updates(map[string]any{"name": name, "updated_at": time.Now()})
	if result.Error != nil {
		return Department{}, translate(result.Error)
	}
	if result.RowsAffected == 0 {
		return Department{}, apperr.ErrNotFound
	}

	var department Department
	return department, r.db.WithContext(ctx).Take(&department, id).Error
}

func (r *Repository) DeleteDepartment(ctx context.Context, id int64) error {
	result := r.db.WithContext(ctx).Delete(&Department{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return apperr.ErrNotFound
	}
	return nil
}

func writeAudit(tx *gorm.DB, audit *AuditLog) error {
	if audit == nil {
		return nil
	}
	return tx.Create(audit).Error
}

// translate turns the constraint violations this module can provoke into its own errors, so the
// handler answers 409 or 400 instead of 500.
func translate(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case "23505": // unique_violation
		return apperr.ErrConflict
	case "23503": // foreign_key_violation
		return apperr.Invalid("department_id or manager_id does not exist")
	}
	return err
}
