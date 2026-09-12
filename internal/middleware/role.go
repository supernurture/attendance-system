package middleware

// Role is a tier: each one holds every right of the roles below it.
type Role string

const (
	RoleEmployee   Role = "employee"
	RoleSupervisor Role = "supervisor"
	RoleHRAdmin    Role = "hr_admin"
	RoleSuperAdmin Role = "super_admin"
)

// roleRank is the ladder. The database only checks membership; the order lives here.
var roleRank = map[Role]int{RoleEmployee: 1, RoleSupervisor: 2, RoleHRAdmin: 3, RoleSuperAdmin: 4}

// Valid reports whether the database would accept role.
func (r Role) Valid() bool {
	_, ok := roleRank[r]
	return ok
}

// AtLeast reports whether r holds every right of min.
func (r Role) AtLeast(min Role) bool {
	return roleRank[r] >= roleRank[min]
}
