package leave

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"attendance-system/internal/api/server/modules/schedule"
	"attendance-system/internal/pkg/apperr"
)

var codeShape = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// checkFiling validates a request on its own: a type, a reason, and a range of at most maxDays calendar days.
func checkFiling(filing Filing) (Filing, error) {
	filing.Type = strings.TrimSpace(filing.Type)
	filing.Reason = strings.TrimSpace(filing.Reason)
	switch {
	case filing.Type == "":
		return Filing{}, apperr.Invalid("type is required")
	case filing.Reason == "":
		return Filing{}, apperr.Invalid("reason is required")
	case utf8.RuneCountInString(filing.Reason) > maxReasonChars:
		return Filing{}, apperr.Invalid("reason must be at most %d characters", maxReasonChars)
	case filing.AttachmentKey != nil && *filing.AttachmentKey == "":
		return Filing{}, apperr.Invalid("attachment_key cannot be empty; leave it out instead")
	case filing.StartDate.IsZero() || filing.EndDate.IsZero():
		return Filing{}, apperr.Invalid("start_date and end_date are both required")
	case filing.EndDate.Before(filing.StartDate):
		return Filing{}, apperr.Invalid("end_date cannot be before start_date")
	}

	span, err := schedule.CheckRange(schedule.Range{From: filing.StartDate, To: filing.EndDate})
	if err != nil {
		return Filing{}, err
	}
	filing.StartDate, filing.EndDate = span.From, span.To
	return filing, nil
}

// checkRules holds a request of so many working days to its type's own limits in thisYear; the balance is checked
// later.
func checkRules(kind LeaveType, filing Filing, working, thisYear int) error {
	switch {
	case working == 0:
		return apperr.Invalid("there are no working days between %s and %s", day(filing.StartDate),
			day(filing.EndDate))
	case kind.QuotaDaysPerYear != nil && filing.StartDate.Year() != filing.EndDate.Year():
		// Each year has its own quota, so a request spanning two could not be charged to either.
		return apperr.Invalid("%s leave cannot span two years; request each year separately", kind.Code)
	case kind.QuotaDaysPerYear != nil && filing.StartDate.Year() < thisYear:
		// A past year's quota closed with it; spending it now would carry it over by the back door.
		return apperr.Invalid("%s leave can no longer be requested for %d", kind.Code, filing.StartDate.Year())
	case kind.MaxWorkingDaysPerRequest != nil && working > *kind.MaxWorkingDaysPerRequest:
		return apperr.Invalid("%s leave allows at most %d working days per request, not %d", kind.Code,
			*kind.MaxWorkingDaysPerRequest, working)
	case kind.AttachmentRequiredFromDays != nil && working >= *kind.AttachmentRequiredFromDays &&
		filing.AttachmentKey == nil:
		return apperr.Invalid("%s leave of %d working days or more needs an attachment", kind.Code,
			*kind.AttachmentRequiredFromDays)
	}
	return nil
}

// checkNote refuses a review note the column cannot hold.
func checkNote(note string) error {
	if utf8.RuneCountInString(note) > maxReasonChars {
		return apperr.Invalid("note must be at most %d characters", maxReasonChars)
	}
	return nil
}

// checkType trims a leave type's code and name and refuses a rule the columns cannot hold.
func checkType(kind LeaveType) (LeaveType, error) {
	kind.Code = strings.TrimSpace(kind.Code)
	kind.Name = strings.TrimSpace(kind.Name)
	switch {
	case len(kind.Code) > maxCodeChars || !codeShape.MatchString(kind.Code):
		return LeaveType{}, apperr.Invalid(
			"code must be lowercase letters, digits and underscores, starting with a letter, at most %d long",
			maxCodeChars)
	case kind.Name == "":
		return LeaveType{}, apperr.Invalid("name is required")
	case utf8.RuneCountInString(kind.Name) > maxNameChars:
		return LeaveType{}, apperr.Invalid("name must be at most %d characters", maxNameChars)
	case outside(kind.QuotaDaysPerYear, 0):
		return LeaveType{}, apperr.Invalid("quota_days_per_year must be between 0 and %d", maxDays)
	case outside(kind.MaxWorkingDaysPerRequest, 1):
		return LeaveType{}, apperr.Invalid("max_working_days_per_request must be between 1 and %d", maxDays)
	case outside(kind.AttachmentRequiredFromDays, 1):
		return LeaveType{}, apperr.Invalid("attachment_required_from_days must be between 1 and %d", maxDays)
	}
	return kind, nil
}

// checkQuota refuses a quota the leave_balances columns cannot hold.
func checkQuota(quota Quota) error {
	if err := checkYear(quota.Year); err != nil {
		return err
	}
	switch {
	case quota.QuotaDays < 0 || quota.QuotaDays > maxDays:
		return apperr.Invalid("quota_days must be between 0 and %d", maxDays)
	case quota.CarriedOverDays < 0 || quota.CarriedOverDays > maxDays:
		return apperr.Invalid("carried_over_days must be between 0 and %d", maxDays)
	}
	return nil
}

func checkYear(year int) error {
	if year < minYear || year > maxYear {
		return apperr.Invalid("year must be between %d and %d", minYear, maxYear)
	}
	return nil
}

// outside reports whether an optional count is set and out of lowest to maxDays.
func outside(days *int, lowest int) bool {
	return days != nil && (*days < lowest || *days > maxDays)
}
