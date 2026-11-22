package authz

// narrowScopes applies principal scope narrowing to an effective permission set
// (REQ-AUTH-018). With an empty scope list it returns perms unchanged.
//
// TODO(EP0-208): scope grammar and matching are undefined in specs/auth/design.md;
// non-empty scopes deny everything until the design lands.
func narrowScopes(perms PermissionSet, scopes []string) PermissionSet {
	if len(scopes) == 0 {
		return perms.clone()
	}
	return nil
}
