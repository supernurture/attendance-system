package user

import "errors"

// Actions recorded in audit_logs. Only the sensitive few are written; deletions carry their own
// deleted_at, so they are not here.
const (
	actionRoleChanged = "user.role_changed"
	actionDeactivated = "user.deactivated"

	entityUser = "user"
)

const minPasswordLen = 8 // the maximum, 72 bytes, is bcrypt's own and it reports it

var (
	// ErrForbidden means the caller's role or hierarchy does not reach the target.
	ErrForbidden = errors.New("forbidden")
	// ErrNotFound means no live record has that id.
	ErrNotFound = errors.New("not found")
	// ErrConflict means the email or department name is taken.
	ErrConflict = errors.New("already exists")
)
