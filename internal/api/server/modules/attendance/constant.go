package attendance

import (
	"fmt"
	"time"

	"attendance-system/internal/pkg/apperr"
)

// Status is where one person stands on one date, as whos-in reports it.
type Status string

const (
	StatusPresent Status = "present"
	StatusLate    Status = "late"
	StatusHoliday Status = "holiday"
	StatusOff     Status = "off"     // not a working day by rule A
	StatusNotYet  Status = "not_yet" // a working day whose shift, grace included, has not started
	StatusAbsent  Status = "absent"
)

// Side picks the check-in or the check-out photo.
type Side string

const (
	SideIn  Side = "in"
	SideOut Side = "out"
)

// Decision is what a reviewer does with a pending correction.
type Decision string

const (
	DecisionApprove Decision = "approve"
	DecisionReject  Decision = "reject"
)

// Correction states; only a pending one can still be decided or cancelled.
const (
	correctionPending   = "pending"
	correctionApproved  = "approved"
	correctionRejected  = "rejected"
	correctionCancelled = "cancelled"
)

const (
	checkInOpensBefore  = 4 * time.Hour  // how long before a shift's start its check-in is accepted
	maxOpenCheckIn      = 24 * time.Hour // a check-in older than this is closed only by a correction
	checkOutClosesAfter = 8 * time.Hour  // how long past its shift's end a check-in can still be checked out
	correctionSlack     = 12 * time.Hour // how far past its work date's midnights a corrected time may fall

	maxReportChars = 5000
	maxReasonChars = 500 // the reason and review_note columns
)

var (
	errNoOpenCheckIn  = apperr.Invalid("there is no open check-in recent enough to close; request a correction")
	errPhotoKeyUsed   = apperr.Invalid("photo_key has already been used")
	errCheckedIn      = fmt.Errorf("%w: already checked in for that work date", apperr.ErrConflict)
	errAlreadyPending = fmt.Errorf("%w: a correction for that work date is already pending", apperr.ErrConflict)
	errNotPending     = fmt.Errorf("%w: the correction is no longer pending", apperr.ErrConflict)
)
