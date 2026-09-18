package report

import (
	"slices"

	"attendance-system/internal/api/server/modules/attendance"
	"attendance-system/internal/pkg/apperr"
)

// checkStatus refuses a status filter rule B never produces; nil does not filter.
func checkStatus(status *attendance.Status) error {
	if status != nil && !slices.Contains(statuses, *status) {
		return apperr.Invalid("status %q is not one of %v", *status, statuses)
	}
	return nil
}
