package attendance

import (
	"strings"
	"time"
	"unicode/utf8"

	"attendance-system/internal/pkg/apperr"
)

// checkMark refuses what no phone could report. The photo itself is verified against the store later.
func checkMark(mark Mark) error {
	switch {
	case mark.PhotoKey == "":
		return apperr.Invalid("photo_key is required")
	case mark.Lat < -90 || mark.Lat > 90:
		return apperr.Invalid("lat must be between -90 and 90")
	case mark.Lng < -180 || mark.Lng > 180:
		return apperr.Invalid("lng must be between -180 and 180")
	case mark.AccuracyM < 0:
		return apperr.Invalid("accuracy_m cannot be negative")
	}
	return nil
}

// checkReport trims a daily report and refuses an empty or oversized one.
func checkReport(workDate time.Time, content string) (string, error) {
	content = strings.TrimSpace(content)
	switch {
	case workDate.IsZero():
		return "", apperr.Invalid("work_date is required")
	case content == "":
		return "", apperr.Invalid("content is required")
	case utf8.RuneCountInString(content) > maxReportChars:
		return "", apperr.Invalid("content must be at most %d characters", maxReportChars)
	}
	return content, nil
}

// checkNote refuses a review note the column cannot hold.
func checkNote(note string) error {
	if utf8.RuneCountInString(note) > maxReasonChars {
		return apperr.Invalid("note must be at most %d characters", maxReasonChars)
	}
	return nil
}

// checkProposal validates a correction on its own: a work date, a reason, and at least one time, each already
// past and within correctionSlack of that date's midnights in zone, which leaves room for a night shift.
func checkProposal(proposal Proposal, now time.Time, zone *time.Location) (Proposal, error) {
	proposal.Reason = strings.TrimSpace(proposal.Reason)
	switch {
	case proposal.WorkDate.IsZero():
		return Proposal{}, apperr.Invalid("work_date is required")
	case proposal.CheckInAt == nil && proposal.CheckOutAt == nil:
		return Proposal{}, apperr.Invalid("proposed_check_in_at or proposed_check_out_at is required")
	case proposal.Reason == "":
		return Proposal{}, apperr.Invalid("reason is required")
	case utf8.RuneCountInString(proposal.Reason) > maxReasonChars:
		return Proposal{}, apperr.Invalid("reason must be at most %d characters", maxReasonChars)
	}

	date := dateOnly(proposal.WorkDate)
	if date.After(dateIn(now, zone)) {
		// Approving it would create that day now, and its real check-in would then be refused.
		return Proposal{}, apperr.Invalid("work_date cannot be after today")
	}
	earliest := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, zone).Add(-correctionSlack)
	latest := time.Date(date.Year(), date.Month(), date.Day()+1, 0, 0, 0, 0, zone).Add(correctionSlack)
	for _, at := range []*time.Time{proposal.CheckInAt, proposal.CheckOutAt} {
		switch {
		case at == nil:
		case at.After(now):
			return Proposal{}, apperr.Invalid("a corrected time cannot be in the future")
		case at.Before(earliest) || at.After(latest):
			return Proposal{}, apperr.Invalid("corrected times must fall within %d hours of %s",
				int(correctionSlack.Hours()), date.Format(time.DateOnly))
		}
	}

	proposal.WorkDate = date
	return proposal, nil
}
