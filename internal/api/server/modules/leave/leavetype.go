package leave

import (
	"context"
	"errors"
	"fmt"

	"attendance-system/internal/middleware"
	"attendance-system/internal/pkg/apperr"
)

// Types lists the live leave types for anyone signed in, so an employee can pick one.
func (s *Service) Types(ctx context.Context) ([]LeaveType, error) {
	return s.repo.Types(ctx)
}

// CreateType adds a leave type, as hr_admin.
func (s *Service) CreateType(ctx context.Context, claims middleware.Claims, kind LeaveType) (LeaveType, error) {
	if !claims.Role.AtLeast(middleware.RoleHRAdmin) {
		return LeaveType{}, apperr.ErrForbidden
	}
	kind, err := checkType(kind)
	if err != nil {
		return LeaveType{}, err
	}
	if err := s.repo.CreateType(ctx, &kind); err != nil {
		return LeaveType{}, err
	}
	return kind, nil
}

// ReplaceType rewrites a leave type, as hr_admin. A changed yearly quota is audited: it is the quota of everyone who
// has none of their own.
func (s *Service) ReplaceType(
	ctx context.Context, claims middleware.Claims, id int64, kind LeaveType,
) (LeaveType, error) {
	if !claims.Role.AtLeast(middleware.RoleHRAdmin) {
		return LeaveType{}, apperr.ErrForbidden
	}
	kind, err := checkType(kind)
	if err != nil {
		return LeaveType{}, err
	}
	kind.ID = id

	return s.repo.ReplaceType(ctx, kind, func(before LeaveType) *AuditLog {
		if sameDays(before.QuotaDaysPerYear, kind.QuotaDaysPerYear) {
			return nil
		}
		return &AuditLog{
			ActorID: claims.UserID, Action: actionTypeQuotaChanged, EntityType: entityLeaveType, EntityID: id,
			Before: []byte(typeQuotaJSON(before.QuotaDaysPerYear)), After: []byte(typeQuotaJSON(kind.QuotaDaysPerYear)),
		}
	})
}

// DeleteType retires a leave type, as hr_admin; requests already filed under it keep reading back.
func (s *Service) DeleteType(ctx context.Context, claims middleware.Claims, id int64) error {
	if !claims.Role.AtLeast(middleware.RoleHRAdmin) {
		return apperr.ErrForbidden
	}
	return s.repo.DeleteType(ctx, id)
}

// SetQuota replaces someone else's quota of a type for a year, as hr_admin, and audits what it replaced. A nil
// carriedOver keeps the carried-over days the person already has, 0 when they had none.
func (s *Service) SetQuota(
	ctx context.Context, claims middleware.Claims, quota Quota, carriedOver *int,
) (Quota, error) {
	if !claims.Role.AtLeast(middleware.RoleHRAdmin) {
		return Quota{}, apperr.ErrForbidden
	}
	if quota.UserID == claims.UserID {
		return Quota{}, fmt.Errorf("%w: nobody sets their own leave quota", apperr.ErrForbidden)
	}
	quota.CarriedOverDays = 0
	if carriedOver != nil {
		quota.CarriedOverDays = *carriedOver
	}
	if err := checkQuota(quota); err != nil {
		return Quota{}, err
	}

	kind, err := s.repo.TypeByID(ctx, quota.LeaveTypeID)
	if errors.Is(err, apperr.ErrNotFound) {
		return Quota{}, apperr.Invalid("leave_type_id does not exist")
	}
	if err != nil {
		return Quota{}, err
	}
	if kind.QuotaDaysPerYear == nil {
		return Quota{}, apperr.Invalid("%s leave has no yearly quota to set", kind.Code)
	}

	return s.repo.SetQuota(ctx, quota, carriedOver == nil, func(before *Quota, after Quota) AuditLog {
		was := nullJSON
		if before != nil {
			was = quotaJSON(*before)
		}
		return AuditLog{
			ActorID: claims.UserID, Action: actionQuotaSet, EntityType: entityUser, EntityID: quota.UserID,
			Before: []byte(was), After: []byte(quotaJSON(after)),
		}
	})
}

// sameDays reports whether two optional day counts agree, both unset included.
func sameDays(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func typeQuotaJSON(days *int) string {
	if days == nil {
		return `{"quota_days_per_year":null}`
	}
	return fmt.Sprintf(`{"quota_days_per_year":%d}`, *days)
}

func quotaJSON(quota Quota) string {
	return fmt.Sprintf(`{"leave_type_id":%d,"year":%d,"quota_days":%d,"carried_over_days":%d}`,
		quota.LeaveTypeID, quota.Year, quota.QuotaDays, quota.CarriedOverDays)
}
