package leave

import (
	"fmt"

	"attendance-system/internal/pkg/apperr"
)

// Decision is what a reviewer does with a pending leave request.
type Decision string

const (
	DecisionApprove Decision = "approve"
	DecisionReject  Decision = "reject"
)

// Request states; only a pending one can still be decided or cancelled, and only these two hold days.
const (
	statusPending   = "pending"
	statusApproved  = "approved"
	statusRejected  = "rejected"
	statusCancelled = "cancelled"
)

// Actions recorded in audit_logs: a quota decides how much leave someone may take.
const (
	actionQuotaSet         = "leave.quota_set"
	actionTypeQuotaChanged = "leave_type.quota_changed"

	entityUser      = "user" // a quota row has no id of its own; its user, type and year are in the payload
	entityLeaveType = "leave_type"

	nullJSON = "null"
)

const (
	maxReasonChars = 500 // the reason and review_note columns
	maxCodeChars   = 50
	maxNameChars   = 100
	maxDays        = 366
	minYear        = 2000
	maxYear        = 9999
)

var (
	errAttachmentUsed = apperr.Invalid("attachment_key has already been used")
	errOverlap        = fmt.Errorf("%w: the dates overlap another pending or approved leave", apperr.ErrConflict)
	errCodeTaken      = fmt.Errorf("%w: a leave type with that code already exists", apperr.ErrConflict)
)
