package user

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// User is an employee. DeletedAt makes every query here skip the removed ones, and it is why
// auth refuses a deleted account: the same column filters its lookups.
type User struct {
	ID           int64
	Email        string
	PasswordHash string
	FullName     string
	Role         string
	IsActive     bool
	JoinDate     time.Time
	DepartmentID *int64
	ManagerID    *int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DeletedAt    gorm.DeletedAt
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

// ByID fails with ErrNotFound when no live user has that id.
func (r *Repository) ByID(ctx context.Context, id int64) (User, error) {
	var user User
	err := r.db.WithContext(ctx).Take(&user, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return User{}, ErrNotFound
	}
	return user, err
}

func (r *Repository) List(ctx context.Context) ([]User, error) {
	var users []User
	return users, r.db.WithContext(ctx).Order("full_name").Find(&users).Error
}

// subtree walks manager_id down from managerID. The CYCLE clause is what keeps circular manager
// data from looping forever; without it the query never returns.
const subtree = `WITH RECURSIVE subordinates AS (
	SELECT id FROM users WHERE manager_id = ? AND deleted_at IS NULL
	UNION
	SELECT u.id FROM users u JOIN subordinates s ON u.manager_id = s.id WHERE u.deleted_at IS NULL
) CYCLE id SET is_cycle USING path
SELECT id FROM subordinates`

// ListSubtree returns everyone below managerID in the hierarchy, however deep.
func (r *Repository) ListSubtree(ctx context.Context, managerID int64) ([]User, error) {
	var users []User
	err := r.db.WithContext(ctx).
		Where("id IN (?)", r.db.Raw(subtree, managerID)).
		Order("full_name").Find(&users).Error
	return users, err
}

// InSubtree reports whether userID sits below managerID.
func (r *Repository) InSubtree(ctx context.Context, managerID, userID int64) (bool, error) {
	var found int64
	err := r.db.WithContext(ctx).Model(&User{}).
		Where("id = ? AND id IN (?)", userID, r.db.Raw(subtree, managerID)).
		Count(&found).Error
	return found > 0, err
}

// Create stores a new user, reporting ErrConflict for a taken email.
func (r *Repository) Create(ctx context.Context, user *User) error {
	return translate(r.db.WithContext(ctx).Create(user).Error)
}

// Replace writes the user's editable columns, and audit alongside it in one transaction when given.
func (r *Repository) Replace(ctx context.Context, user User, audit *AuditLog) (User, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&User{}).Where("id = ?", user.ID).Updates(map[string]any{
			"full_name":     user.FullName,
			"is_active":     user.IsActive,
			"join_date":     user.JoinDate,
			"department_id": user.DepartmentID,
			"manager_id":    user.ManagerID,
			"updated_at":    time.Now(),
		})
		if result.Error != nil {
			return translate(result.Error)
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
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
			return ErrNotFound
		}
		return writeAudit(tx, &audit)
	})
	if err != nil {
		return User{}, err
	}
	return r.ByID(ctx, userID)
}

// SoftDelete sets deleted_at, keeping the row so reports can still join what the user did.
func (r *Repository) SoftDelete(ctx context.Context, id int64) error {
	result := r.db.WithContext(ctx).Delete(&User{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
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
		return Department{}, ErrNotFound
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
		return ErrNotFound
	}
	return nil
}

func (r *Repository) auditLogs(ctx context.Context, entityType string, entityID int64) ([]AuditLog, error) {
	var logs []AuditLog
	err := r.db.WithContext(ctx).
		Where("entity_type = ? AND entity_id = ?", entityType, entityID).
		Order("id").Find(&logs).Error
	return logs, err
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
		return ErrConflict
	case "23503": // foreign_key_violation
		return invalid("department_id or manager_id does not exist")
	}
	return err
}
