package leave

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"attendance-system/internal/api/server/modules/schedule"
	"attendance-system/internal/api/server/modules/upload"
	"attendance-system/internal/middleware"
	"attendance-system/internal/pkg/apperr"
	"attendance-system/internal/pkg/storage"
)

// now is a seam, so a test can decide at a chosen moment. Postgres keeps microseconds, so nothing finer.
var now = func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// Filing is what an employee asks for. A nil AttachmentKey sends no attachment.
type Filing struct {
	Type          string
	StartDate     time.Time
	EndDate       time.Time
	Reason        string
	AttachmentKey *string
}

type Service struct {
	repo    *Repository
	plan    *schedule.Service // rule A, which decides the working days
	uploads *upload.Service
	zone    *time.Location
}

// NewService reads "today", and so the current year, in zone.
func NewService(db *gorm.DB, store *storage.Storage, zone *time.Location) *Service {
	return &Service{
		repo:    NewRepository(db, store),
		plan:    schedule.NewService(db, zone),
		uploads: upload.NewService(store),
		zone:    zone,
	}
}

// Request files leave for the caller. Its working days are rule A's over the range, held to the type's limits and,
// for a type with a yearly quota, to what is left of it. A type that auto-approves is approved on filing.
func (s *Service) Request(ctx context.Context, userID int64, filing Filing) (Leave, error) {
	filing, err := checkFiling(filing)
	if err != nil {
		return Leave{}, err
	}
	kind, err := s.repo.TypeByCode(ctx, filing.Type)
	if errors.Is(err, apperr.ErrNotFound) {
		return Leave{}, apperr.Invalid("unknown leave type %q", filing.Type)
	}
	if err != nil {
		return Leave{}, err
	}

	days, err := s.plan.Days(ctx, userID, schedule.Range{From: filing.StartDate, To: filing.EndDate})
	if err != nil {
		return Leave{}, err
	}
	working := 0
	for _, day := range days {
		if day.Working {
			working++
		}
	}
	if err := checkRules(kind, filing, working, s.today().Year()); err != nil {
		return Leave{}, err
	}

	row := Leave{
		UserID:      userID,
		LeaveTypeID: kind.ID,
		StartDate:   filing.StartDate,
		EndDate:     filing.EndDate,
		WorkingDays: working,
		Reason:      filing.Reason,
		Status:      statusPending,
	}
	if kind.AutoApprove {
		at := now()
		row.Status, row.ReviewedAt = statusApproved, &at
	}
	if filing.AttachmentKey != nil {
		mime, err := s.attachment(ctx, userID, *filing.AttachmentKey)
		if err != nil {
			return Leave{}, err
		}
		row.AttachmentKey, row.AttachmentMime = filing.AttachmentKey, &mime
	}

	var limit func(Balance) error
	if kind.QuotaDaysPerYear != nil {
		limit = func(held Balance) error {
			if left := held.RemainingDays(); working > left {
				return apperr.Invalid("%d working days of %s leave requested, but only %d remain for %d", working,
					kind.Code, max(left, 0), held.Year)
			}
			return nil
		}
	}
	if err := s.repo.Create(ctx, &row, limit); err != nil {
		return Leave{}, err
	}
	return row, nil
}

// Mine lists the caller's own requests, newest first.
func (s *Service) Mine(ctx context.Context, userID int64) ([]Leave, error) {
	return s.repo.Of(ctx, userID)
}

// Balances is rule C for the caller in a year, the current one in the company's zone when year is nil.
func (s *Service) Balances(ctx context.Context, userID int64, year *int) ([]Balance, error) {
	wanted := s.today().Year()
	if year != nil {
		wanted = *year
	}
	if err := checkYear(wanted); err != nil {
		return nil, err
	}
	return s.repo.Balances(ctx, userID, wanted)
}

// Pending lists what the caller may decide, oldest first.
func (s *Service) Pending(ctx context.Context, claims middleware.Claims) ([]Leave, error) {
	if !claims.Role.AtLeast(middleware.RoleSupervisor) {
		return nil, apperr.ErrForbidden
	}
	return s.repo.Pending(ctx, scope(claims), claims.UserID)
}

// Cancel withdraws a pending or approved request: its owner while it has not started, hr_admin for someone else at
// any time. Its days are free again at once, since nothing counted them but the request itself.
func (s *Service) Cancel(ctx context.Context, claims middleware.Claims, id int64) error {
	row, err := s.repo.ByID(ctx, id)
	if err != nil {
		return err
	}
	// hr_admin overrides only for others: withdrawing one's own leave once taken would hand the days back to oneself.
	override := claims.Role.AtLeast(middleware.RoleHRAdmin) && row.UserID != claims.UserID
	switch {
	case row.UserID != claims.UserID && !override:
		return apperr.ErrForbidden
	case row.Status != statusPending && row.Status != statusApproved:
		return fmt.Errorf("%w: a %s leave request cannot be cancelled", apperr.ErrConflict, row.Status)
	case row.Status == statusApproved && !override && !row.StartDate.After(s.today()):
		return fmt.Errorf("%w: approved leave that has started can only be cancelled by hr_admin", apperr.ErrForbidden)
	}

	_, err = s.repo.Settle(ctx, id, row.Status, map[string]any{
		"status": statusCancelled, "cancelled_by": claims.UserID, "cancelled_at": now(),
	})
	return err
}

// Decide approves or rejects a pending request, as a supervisor above its employee or hr_admin, and never one's own.
func (s *Service) Decide(
	ctx context.Context, claims middleware.Claims, id int64, decision Decision, note string,
) (Leave, error) {
	if !claims.Role.AtLeast(middleware.RoleSupervisor) {
		return Leave{}, apperr.ErrForbidden
	}
	if decision != DecisionApprove && decision != DecisionReject {
		return Leave{}, apperr.Invalid("decision must be %s or %s", DecisionApprove, DecisionReject)
	}
	if err := checkNote(note); err != nil {
		return Leave{}, err
	}

	row, err := s.repo.ByID(ctx, id)
	if err != nil {
		return Leave{}, err
	}
	if row.UserID == claims.UserID {
		return Leave{}, apperr.ErrForbidden
	}
	if err := s.reach(ctx, claims, row.UserID); err != nil {
		return Leave{}, err
	}

	decided := map[string]any{"status": statusRejected, "reviewed_by": claims.UserID, "reviewed_at": now()}
	if decision == DecisionApprove {
		decided["status"] = statusApproved
	}
	if note != "" {
		decided["review_note"] = note
	}
	return s.repo.Settle(ctx, id, statusPending, decided)
}

// AttachmentURL signs a download of a request's attachment for its owner, a supervisor above them, or hr_admin.
func (s *Service) AttachmentURL(ctx context.Context, claims middleware.Claims, id int64) (string, error) {
	row, err := s.repo.ByID(ctx, id)
	if err != nil {
		return "", err
	}
	if err := s.reach(ctx, claims, row.UserID); err != nil {
		return "", err
	}
	if row.AttachmentKey == nil {
		return "", fmt.Errorf("%w: leave request %d has no attachment", apperr.ErrNotFound, id)
	}
	return s.repo.AttachmentURL(ctx, *row.AttachmentKey)
}

// attachment verifies an uploaded key no request has used yet and returns its sniffed content type.
func (s *Service) attachment(ctx context.Context, userID int64, key string) (string, error) {
	used, err := s.repo.AttachmentUsed(ctx, key)
	if err != nil {
		return "", err
	}
	if used {
		return "", errAttachmentUsed
	}
	return s.uploads.Verify(ctx, userID, upload.LeaveAttachment, key)
}

// reach is rule D for one person: the caller themselves, anyone below a supervisor, anyone at all for hr_admin.
func (s *Service) reach(ctx context.Context, claims middleware.Claims, userID int64) error {
	if claims.UserID == userID || claims.Role.AtLeast(middleware.RoleHRAdmin) {
		return nil
	}
	if !claims.Role.AtLeast(middleware.RoleSupervisor) {
		return apperr.ErrForbidden
	}

	below, err := s.repo.InSubtree(ctx, claims.UserID, userID)
	if err != nil {
		return err
	}
	if !below {
		return apperr.ErrForbidden
	}
	return nil
}

// today is the calendar date it is now in the company's zone.
func (s *Service) today() time.Time {
	at := now().In(s.zone)
	return time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
}

// scope is whose requests a supervisor-and-up caller lists: nil for everyone, else their own subtree.
func scope(claims middleware.Claims) *int64 {
	if claims.Role.AtLeast(middleware.RoleHRAdmin) {
		return nil
	}
	return &claims.UserID
}

// day formats a calendar date the way the API writes one.
func day(date time.Time) string {
	return date.Format(time.DateOnly)
}
