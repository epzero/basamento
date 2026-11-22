package authz

import "slices"

// PermissionSet is a set of declared action strings.
type PermissionSet map[string]struct{}

// Union returns the set union of a and b.
func (a PermissionSet) Union(b PermissionSet) PermissionSet {
	if len(a) == 0 {
		return b.clone()
	}
	if len(b) == 0 {
		return a.clone()
	}
	out := make(PermissionSet, len(a)+len(b))
	for k := range a {
		out[k] = struct{}{}
	}
	for k := range b {
		out[k] = struct{}{}
	}
	return out
}

// Has reports whether action is in the set.
func (a PermissionSet) Has(action string) bool {
	_, ok := a[action]
	return ok
}

func (a PermissionSet) clone() PermissionSet {
	if len(a) == 0 {
		return nil
	}
	out := make(PermissionSet, len(a))
	for k := range a {
		out[k] = struct{}{}
	}
	return out
}

// slice returns actions in sorted order for stable tests and docs.
func (a PermissionSet) slice() []string {
	if len(a) == 0 {
		return nil
	}
	out := make([]string, 0, len(a))
	for k := range a {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
