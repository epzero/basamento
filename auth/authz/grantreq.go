package authz

import "slices"

// grantRequirements lists the grant (or revoke) action plus every permission of target.
func grantRequirements(action string, target Role, rolePerms func(Role) PermissionSet) []string {
	seen := make(map[string]struct{}, 32)
	add := func(a string) {
		seen[a] = struct{}{}
	}
	add(action)
	for a := range rolePerms(target) {
		add(a)
	}
	out := make([]string, 0, len(seen))
	for a := range seen {
		out = append(out, a)
	}
	slices.Sort(out)
	return out
}
