package attendance

import (
	"context"
	"time"

	"gorm.io/gorm"

	"attendance-system/internal/api/server/modules/schedule"
	"attendance-system/internal/middleware"
	"attendance-system/internal/pkg/apperr"
)

// Proposal asks for one work date's times to change. A nil UserID files for the caller.
type Proposal struct {
	UserID     *int64
	WorkDate   time.Time
	CheckInAt  *time.Time
	CheckOutAt *time.Time
	Reason     string
}

// RequestCorrection files a correction for the caller, or for anyone as hr_admin, refusing one that could not be
// approved against the attendance as it stands.
func (s *Service) RequestCorrection(
	ctx context.Context, claims middleware.Claims, proposal Proposal,
) (Correction, error) {
	userID := claims.UserID
	if proposal.UserID != nil && *proposal.UserID != claims.UserID {
		if !claims.Role.AtLeast(middleware.RoleHRAdmin) {
			return Correction{}, apperr.ErrForbidden
		}
		userID = *proposal.UserID
	}

	proposal, err := checkProposal(proposal, now(), s.zone)
	if err != nil {
		return Correction{}, err
	}
	correction := Correction{
		UserID:             userID,
		WorkDate:           proposal.WorkDate,
		RequestedBy:        claims.UserID,
		ProposedCheckInAt:  proposal.CheckInAt,
		ProposedCheckOutAt: proposal.CheckOutAt,
		Reason:             proposal.Reason,
		Status:             correctionPending,
	}

	current, err := s.repo.Held(ctx, userID, correction.WorkDate)
	if err != nil {
		return Correction{}, err
	}
	if _, err := corrected(current, correction); err != nil {
		return Correction{}, err
	}
	if err := s.repo.CreateCorrection(ctx, &correction); err != nil {
		return Correction{}, err
	}
	return correction, nil
}

// MyCorrections lists the corrections to the caller's own attendance, newest first.
func (s *Service) MyCorrections(ctx context.Context, userID int64) ([]Correction, error) {
	return s.repo.CorrectionsOf(ctx, userID)
}

// PendingCorrections lists what the caller may decide, oldest first.
func (s *Service) PendingCorrections(ctx context.Context, claims middleware.Claims) ([]Correction, error) {
	if !claims.Role.AtLeast(middleware.RoleSupervisor) {
		return nil, apperr.ErrForbidden
	}
	return s.repo.PendingCorrections(ctx, scope(claims), claims.UserID)
}

// CancelCorrection withdraws a pending correction, for the employee it is about or whoever filed it.
func (s *Service) CancelCorrection(ctx context.Context, claims middleware.Claims, id int64) error {
	correction, err := s.repo.CorrectionByID(ctx, id)
	if err != nil {
		return err
	}
	if claims.UserID != correction.UserID && claims.UserID != correction.RequestedBy {
		return apperr.ErrForbidden
	}

	_, err = s.repo.Settle(ctx, id, map[string]any{"status": correctionCancelled})
	return err
}

// DecideCorrection approves or rejects a pending correction, as a supervisor above its employee or hr_admin, and
// never one's own. Approving recomputes lateness against the schedule the attendance kept, or rule A's for a day
// that had none.
func (s *Service) DecideCorrection(
	ctx context.Context, claims middleware.Claims, id int64, decision Decision, note string,
) (Correction, error) {
	if !claims.Role.AtLeast(middleware.RoleSupervisor) {
		return Correction{}, apperr.ErrForbidden
	}
	if decision != DecisionApprove && decision != DecisionReject {
		return Correction{}, apperr.Invalid("decision must be %s or %s", DecisionApprove, DecisionReject)
	}
	if err := checkNote(note); err != nil {
		return Correction{}, err
	}

	correction, err := s.repo.CorrectionByID(ctx, id)
	if err != nil {
		return Correction{}, err
	}
	if correction.UserID == claims.UserID {
		return Correction{}, apperr.ErrForbidden
	}
	if err := s.reach(ctx, claims, correction.UserID); err != nil {
		return Correction{}, err
	}

	decided := map[string]any{"status": correctionRejected, "reviewed_by": claims.UserID, "reviewed_at": now()}
	if note != "" {
		decided["review_note"] = note
	}
	if decision == DecisionReject {
		return s.repo.Settle(ctx, id, decided)
	}

	decided["status"] = correctionApproved
	return s.repo.Approve(ctx, id, decided, func(
		tx *gorm.DB, correction Correction, current *Attendance,
	) (Attendance, error) {
		next, err := corrected(current, correction)
		if err != nil {
			return Attendance{}, err
		}
		hours, err := s.hoursFor(ctx, tx, correction, current)
		if err != nil {
			return Attendance{}, err
		}

		measured := place(next.WorkDate, hours, s.zone)
		next.ScheduleID = measured.scheduleID()
		next.LateMinutes = measured.lateMinutes(next.CheckInAt)
		if next.CheckOutAt != nil {
			next.EarlyLeaveMinutes = measured.earlyLeaveMinutes(*next.CheckOutAt)
		}
		return next, nil
	})
}

// hoursFor is the schedule a corrected attendance is measured against: the one it kept, or rule A's for a day
// without one. It reads through the approval's own transaction: a second pooled connection could wait forever
// for the one that transaction holds.
func (s *Service) hoursFor(
	ctx context.Context, tx *gorm.DB, correction Correction, current *Attendance,
) (*schedule.WorkSchedule, error) {
	if current != nil {
		return scheduleOf(ctx, schedule.NewRepository(tx), current.ScheduleID)
	}

	day := schedule.Range{From: correction.WorkDate, To: correction.WorkDate}
	days, err := schedule.NewService(tx, s.zone).Days(ctx, correction.UserID, day)
	if err != nil {
		return nil, err
	}
	return days[0].Schedule, nil
}

// corrected is the attendance a correction leaves behind: its times laid over the one held, or a new one.
func corrected(current *Attendance, correction Correction) (Attendance, error) {
	next := Attendance{UserID: correction.UserID, WorkDate: correction.WorkDate}
	if current != nil {
		next = *current
	}
	if correction.ProposedCheckInAt != nil {
		next.CheckInAt = *correction.ProposedCheckInAt
	}
	if correction.ProposedCheckOutAt != nil {
		next.CheckOutAt = correction.ProposedCheckOutAt
	}

	switch {
	case next.CheckInAt.IsZero():
		return Attendance{}, apperr.Invalid("there is no check-in on %s, so proposed_check_in_at is required",
			correction.WorkDate.Format(time.DateOnly))
	case next.CheckOutAt != nil && !next.CheckOutAt.After(next.CheckInAt):
		return Attendance{}, apperr.Invalid("check-out must be after check-in")
	}
	return next, nil
}
