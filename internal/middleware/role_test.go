package middleware

import "testing"

func TestRoleValid(t *testing.T) {
	for _, role := range []Role{RoleEmployee, RoleSupervisor, RoleHRAdmin, RoleSuperAdmin} {
		if !role.Valid() {
			t.Errorf("%q is not valid, want the database to accept it", role)
		}
	}
	for _, role := range []Role{"", "admin", "Employee", "owner"} {
		if role.Valid() {
			t.Errorf("%q is valid, want it refused", role)
		}
	}
}

func TestRoleAtLeast(t *testing.T) {
	tests := map[string]struct {
		role, min Role
		want      bool
	}{
		"the same role":           {RoleSupervisor, RoleSupervisor, true},
		"one rung up":             {RoleHRAdmin, RoleSupervisor, true},
		"the top of the ladder":   {RoleSuperAdmin, RoleEmployee, true},
		"one rung down":           {RoleSupervisor, RoleHRAdmin, false},
		"an employee reaching up": {RoleEmployee, RoleSuperAdmin, false},
		"an unknown role":         {"owner", RoleEmployee, false},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := test.role.AtLeast(test.min); got != test.want {
				t.Errorf("%q.AtLeast(%q) = %v, want %v", test.role, test.min, got, test.want)
			}
		})
	}
}
