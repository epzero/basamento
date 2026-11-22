package authz

import (
	"regexp"

	"github.com/epzero/basamento/core"
)

var (
	projectRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	principalRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@:/-]{0,127}$`)
)

func validProject(id string) bool {
	if id == "" {
		return true
	}
	return projectRE.MatchString(id)
}

func validPrincipal(id string) bool {
	return principalRE.MatchString(id)
}

func validBuiltinRole(r Role) bool {
	switch r {
	case RoleViewer, RoleMember, RoleAdmin:
		return true
	default:
		return false
	}
}

func errInvalidProject() error {
	return core.NewError(core.CodeInvalidArgument, "project id is not valid", nil)
}

func errInvalidPrincipal() error {
	return core.NewError(core.CodeInvalidArgument, "principal id is not valid", nil)
}

func errInvalidRole() error {
	return core.NewError(core.CodeInvalidArgument, "role is not valid", nil)
}

func errPermissionDenied() error {
	return core.NewError(core.CodePermissionDenied, "permission denied", nil)
}

func errUnavailable() error {
	return core.NewError(core.CodeUnavailable, "authorization unavailable", nil)
}

func errInvalidCondition() error {
	return core.NewError(core.CodeInvalidArgument, "condition is not valid", nil)
}
