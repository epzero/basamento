package authz

import "testing"

func TestGrantRequirementsUnion(t *testing.T) {
	cases := []struct {
		action string
		role   Role
	}{
		{action: "binding.grant", role: RoleViewer},
		{action: "binding.revoke", role: RoleMember},
		{action: "binding.grant", role: RoleAdmin},
		{action: "binding.grant", role: Role("custom-unknown")},
	}
	for _, tc := range cases {
		t.Run(string(tc.role), func(t *testing.T) {
			got := grantRequirements(tc.action, tc.role, rolePerms)
			seen := make(map[string]struct{}, len(got))
			for _, a := range got {
				seen[a] = struct{}{}
			}
			if _, ok := seen[tc.action]; !ok {
				t.Fatalf("missing grant action %q in %v", tc.action, got)
			}
			for a := range rolePerms(tc.role) {
				if _, ok := seen[a]; !ok {
					t.Fatalf("missing role perm %q in %v", a, got)
				}
			}
			if len(got) != len(seen) {
				t.Fatalf("duplicate entries in %v", got)
			}
		})
	}
}
