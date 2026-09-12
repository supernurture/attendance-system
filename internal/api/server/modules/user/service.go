package user

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"attendance-system/internal/middleware"
)

type Service struct {
	repo *Repository
}

func NewService(db *gorm.DB) *Service {
	return &Service{repo: NewRepository(db)}
}

// NewUser is what hr_admin supplies to add an employee; the role is granted separately.
type NewUser struct {
	Email        string
	Password     string
	FullName     string
	JoinDate     time.Time
	DepartmentID *int64
	ManagerID    *int64
}

// Details are the fields an update replaces wholesale.
type Details struct {
	FullName     string
	IsActive     bool
	JoinDate     time.Time
	DepartmentID *int64
	ManagerID    *int64
}

// Me returns the caller's own record.
func (s *Service) Me(ctx context.Context, claims middleware.Claims) (User, error) {
	return s.repo.ByID(ctx, claims.UserID)
}

// List returns everyone the caller may see: a supervisor's subtree, or all of them for hr_admin up.
func (s *Service) List(ctx context.Context, claims middleware.Claims) ([]User, error) {
	switch {
	case claims.Role.AtLeast(middleware.RoleHRAdmin):
		return s.repo.List(ctx)
	case claims.Role.AtLeast(middleware.RoleSupervisor):
		return s.repo.ListSubtree(ctx, claims.UserID)
	}
	return nil, ErrForbidden
}

// Get returns one employee: themselves, someone in their subtree, or anyone for hr_admin up.
func (s *Service) Get(ctx context.Context, claims middleware.Claims, id int64) (User, error) {
	if err := s.reach(ctx, claims, id); err != nil {
		return User{}, err
	}
	return s.repo.ByID(ctx, id)
}

// Create adds an employee as hr_admin; they start as an employee until a role is granted.
func (s *Service) Create(ctx context.Context, claims middleware.Claims, next NewUser) (User, error) {
	if !claims.Role.AtLeast(middleware.RoleHRAdmin) {
		return User{}, ErrForbidden
	}

	email, err := checkNewUser(next.Email, next.Password)
	if err != nil {
		return User{}, err
	}
	fullName, err := checkName("full_name", next.FullName)
	if err != nil {
		return User{}, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(next.Password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, invalid("password: %v", err) // bcrypt reports its own 72-byte maximum
	}
	if next.JoinDate.IsZero() {
		next.JoinDate = time.Now()
	}

	user := User{
		Email:        email,
		PasswordHash: string(hash),
		FullName:     fullName,
		Role:         string(middleware.RoleEmployee),
		IsActive:     true,
		JoinDate:     next.JoinDate,
		DepartmentID: next.DepartmentID,
		ManagerID:    next.ManagerID,
	}
	if err := s.repo.Create(ctx, &user); err != nil {
		return User{}, err
	}
	return user, nil
}

// Replace writes an employee's details as hr_admin, recording an audit row when it deactivates them:
// a disabled account is how someone loses access, so it needs an actor.
func (s *Service) Replace(
	ctx context.Context, claims middleware.Claims, id int64, details Details,
) (User, error) {
	if !claims.Role.AtLeast(middleware.RoleHRAdmin) {
		return User{}, ErrForbidden
	}

	fullName, err := checkName("full_name", details.FullName)
	if err != nil {
		return User{}, err
	}
	if err := checkManager(id, details.ManagerID); err != nil {
		return User{}, err
	}

	before, err := s.repo.ByID(ctx, id)
	if err != nil {
		return User{}, err
	}
	if details.JoinDate.IsZero() {
		details.JoinDate = before.JoinDate
	}

	var audit *AuditLog
	if before.IsActive && !details.IsActive {
		audit = auditRow(claims.UserID, actionDeactivated, id, `{"is_active": true}`, `{"is_active": false}`)
	}

	return s.repo.Replace(ctx, User{
		ID:           id,
		FullName:     fullName,
		IsActive:     details.IsActive,
		JoinDate:     details.JoinDate,
		DepartmentID: details.DepartmentID,
		ManagerID:    details.ManagerID,
	}, audit)
}

// Delete removes an employee as hr_admin. The row stays with deleted_at set, and because auth reads
// the same column the account stops working at once.
func (s *Service) Delete(ctx context.Context, claims middleware.Claims, id int64) error {
	if !claims.Role.AtLeast(middleware.RoleHRAdmin) {
		return ErrForbidden
	}
	if id == claims.UserID {
		return invalid("an admin cannot delete their own account")
	}
	return s.repo.SoftDelete(ctx, id)
}

// ChangeRole grants a role as super_admin and records it. hr_admin cannot: whoever approves
// attendance corrections must not be able to promote themselves.
func (s *Service) ChangeRole(
	ctx context.Context, claims middleware.Claims, id int64, role middleware.Role,
) (User, error) {
	if !claims.Role.AtLeast(middleware.RoleSuperAdmin) {
		return User{}, ErrForbidden
	}
	if err := checkRole(role); err != nil {
		return User{}, err
	}
	if id == claims.UserID {
		return User{}, invalid("a super_admin cannot change their own role")
	}

	before, err := s.repo.ByID(ctx, id)
	if err != nil {
		return User{}, err
	}
	if before.Role == string(role) {
		return before, nil
	}

	audit := auditRow(claims.UserID, actionRoleChanged, id,
		fmt.Sprintf(`{"role": %q}`, before.Role), fmt.Sprintf(`{"role": %q}`, role))
	return s.repo.ChangeRole(ctx, id, string(role), *audit)
}

// ListDepartments is open to supervisor and above, who need the names to read reports.
func (s *Service) ListDepartments(ctx context.Context, claims middleware.Claims) ([]Department, error) {
	if !claims.Role.AtLeast(middleware.RoleSupervisor) {
		return nil, ErrForbidden
	}
	return s.repo.ListDepartments(ctx)
}

func (s *Service) CreateDepartment(
	ctx context.Context, claims middleware.Claims, name string,
) (Department, error) {
	if !claims.Role.AtLeast(middleware.RoleHRAdmin) {
		return Department{}, ErrForbidden
	}
	name, err := checkName("name", name)
	if err != nil {
		return Department{}, err
	}

	department := Department{Name: name}
	if err := s.repo.CreateDepartment(ctx, &department); err != nil {
		return Department{}, err
	}
	return department, nil
}

func (s *Service) RenameDepartment(
	ctx context.Context, claims middleware.Claims, id int64, name string,
) (Department, error) {
	if !claims.Role.AtLeast(middleware.RoleHRAdmin) {
		return Department{}, ErrForbidden
	}
	name, err := checkName("name", name)
	if err != nil {
		return Department{}, err
	}
	return s.repo.RenameDepartment(ctx, id, name)
}

func (s *Service) DeleteDepartment(ctx context.Context, claims middleware.Claims, id int64) error {
	if !claims.Role.AtLeast(middleware.RoleHRAdmin) {
		return ErrForbidden
	}
	return s.repo.DeleteDepartment(ctx, id)
}

// reach answers whether claims may look at user id at all.
func (s *Service) reach(ctx context.Context, claims middleware.Claims, id int64) error {
	if claims.UserID == id || claims.Role.AtLeast(middleware.RoleHRAdmin) {
		return nil
	}
	if !claims.Role.AtLeast(middleware.RoleSupervisor) {
		return ErrForbidden
	}

	inSubtree, err := s.repo.InSubtree(ctx, claims.UserID, id)
	if err != nil {
		return err
	}
	if !inSubtree {
		return ErrForbidden
	}
	return nil
}

// auditRow takes the payloads as JSON text: both are fixed shapes, so nothing here can fail.
func auditRow(actorID int64, action string, entityID int64, before, after string) *AuditLog {
	return &AuditLog{
		ActorID: actorID, Action: action, EntityType: entityUser, EntityID: entityID,
		Before: []byte(before), After: []byte(after),
	}
}
