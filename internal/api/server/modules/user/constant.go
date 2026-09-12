package user

// Actions recorded in audit_logs: the few that take someone's access away, or hand it out.
const (
	actionRoleChanged = "user.role_changed"
	actionDeactivated = "user.deactivated"
	actionDeleted     = "user.deleted"

	entityUser = "user"
)

const minPasswordLen = 8 // the maximum, 72 bytes, is bcrypt's own and it reports it

const (
	defaultPageLimit = 100
	maxPageLimit     = 500
	maxPageOffset    = 100_000 // past this, paging is the wrong tool: filter the listing
)
