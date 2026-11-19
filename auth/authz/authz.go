// Package authz implements in-app authorization: permissions declared in Go,
// roles bound per project, and one tenant-scoped check in front of every
// side effect (ADR-0008, REQ-AUTH-004).
//
// A principal authenticated by auth/authn carries its tenant and optional
// scopes. Check decides whether a role binding or a scope grants an action on a
// project; an absent binding, an unknown role and an unknown action all deny,
// so access must be granted explicitly. A tenant mismatch is reported as
// not_found rather than permission_denied, so a caller cannot learn that
// another tenant's project exists (REQ-AUTH-002).
//
// Every denial is written through platform.Audit under the effective tenant.
// A denial whose event cannot be stored still fails the action: the check never
// turns a failure into an allow.
package authz

import (
	"context"

	"github.com/epzero/basamento/auth/authn"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

// Role is a named set of permissions bound to a principal in one project.
// Roles are declared here and bound per project in storage; a role this package
// does not know grants nothing.
type Role string

const (
	// RoleViewer reads a project and its runs.
	RoleViewer Role = "viewer"
	// RoleEditor additionally creates and cancels runs.
	RoleEditor Role = "editor"
	// RoleAdmin manages the project and its role bindings.
	RoleAdmin Role = "admin"
)

// Action names one operation a principal can attempt. The names are the
// permission identifiers stored in role definitions and token scopes.
type Action string

const (
	// ActionProjectRead reads a project and its runs.
	ActionProjectRead Action = "project.read"
	// ActionProjectManage changes a project, its settings and its role bindings.
	ActionProjectManage Action = "project.manage"
	// ActionRunRead reads a run, its history and its events.
	ActionRunRead Action = "run.read"
	// ActionRunCreate starts a run.
	ActionRunCreate Action = "run.create"
	// ActionRunCancel cancels a running run.
	ActionRunCancel Action = "run.cancel"
)

// Permissions returns the actions role grants and whether the role is declared.
// The slice is a fresh copy: changing it does not change the role. An unknown
// role reports false and no actions, so deny by default holds for roles added
// by a later version or written by mistake.
func Permissions(role Role) ([]Action, bool) {
	switch role {
	case RoleViewer:
		return []Action{ActionProjectRead, ActionRunRead}, true
	case RoleEditor:
		return []Action{ActionProjectRead, ActionRunRead, ActionRunCreate, ActionRunCancel}, true
	case RoleAdmin:
		return []Action{ActionProjectRead, ActionProjectManage, ActionRunRead, ActionRunCreate, ActionRunCancel}, true
	}
	return nil, false
}

// Can reports whether role grants action. It is the single implementation of
// the role-to-permission mapping Check consumes.
func Can(role Role, action Action) bool {
	actions, ok := Permissions(role)
	if !ok {
		return false
	}
	for _, granted := range actions {
		if granted == action {
			return true
		}
	}
	return false
}

// Declared reports whether action is one this package defines. Check refuses an
// undeclared action before consulting roles or scopes, so a scope naming an
// action the codebase does not know cannot become a permission by accident.
func Declared(action Action) bool {
	switch action {
	case ActionProjectRead, ActionProjectManage, ActionRunRead, ActionRunCreate, ActionRunCancel:
		return true
	}
	return false
}

// Resource is the object an action is attempted on. It carries its tenant so a
// check never has to infer the tenant from the context, and an object of
// another tenant is never visible.
type Resource struct {
	Tenant  core.TenantID
	Project string
}

// Authorizer checks principals against role bindings for the tenant of the
// request and records denials.
type Authorizer struct {
	bindings Bindings
	audit    platform.Audit
	clock    core.Clock
}

// NewAuthorizer builds an Authorizer. Bindings, audit and clock are required:
// without any of them the check could not decide or could not record a denial,
// so it refuses to exist rather than fail open.
func NewAuthorizer(bindings Bindings, audit platform.Audit, clock core.Clock) (*Authorizer, error) {
	switch {
	case bindings == nil:
		return nil, core.NewError(core.CodeInvalidArgument, "authorization bindings are required", nil)
	case audit == nil:
		return nil, core.NewError(core.CodeInvalidArgument, "audit sink is required", nil)
	case clock == nil:
		return nil, core.NewError(core.CodeInvalidArgument, "clock is required", nil)
	}
	return &Authorizer{bindings: bindings, audit: audit, clock: clock}, nil
}

// Check returns nil when principal may perform action on resource, and a
// *core.Error otherwise:
//
//   - unauthenticated when the context has no valid tenant, or principal has no
//     id or no valid tenant. No audit event is written for an unauthenticated
//     caller, which is the authentication edge's concern.
//   - not_found when principal or resource belongs to another tenant, or the
//     resource names no project. The answer is the same as for an object that
//     does not exist, so tenancy never leaks (REQ-AUTH-002).
//   - permission_denied when the principal is authenticated in the tenant but
//     the action is not declared, or no role binding and no scope grants it
//     (REQ-AUTH-004).
//
// A binding lookup error is returned as is and denies the action. A denial is
// recorded through platform.Audit before the error is returned; if the event
// cannot be stored the check returns unavailable. In every failure path the
// action is denied.
//
// Scopes grant their exact action name; there are no wildcards, so a scope
// written for one action never grants another.
func (a *Authorizer) Check(ctx context.Context, principal authn.Principal, action Action, resource Resource) error {
	tenant, ok := core.TenantFrom(ctx)
	if !ok {
		return core.NewError(core.CodeUnauthenticated, "the request has no tenant", nil)
	}
	if principal.ID == "" || !validPrincipalTenant(principal.Tenant) {
		return core.NewError(core.CodeUnauthenticated, "the principal is not authenticated", nil)
	}
	if principal.Tenant != tenant || resource.Tenant != tenant || resource.Project == "" {
		return a.deny(ctx, tenant, principal, action, resource, core.CodeNotFound)
	}
	if !Declared(action) {
		return a.deny(ctx, tenant, principal, action, resource, core.CodePermissionDenied)
	}

	roles, err := a.bindings.RolesFor(ctx, principal.ID, resource.Project)
	if err != nil {
		return err
	}
	if granted(roles, principal.Scopes, action) {
		return nil
	}
	return a.deny(ctx, tenant, principal, action, resource, core.CodePermissionDenied)
}

func granted(roles []Role, scopes []string, action Action) bool {
	for _, granted := range roles {
		if Can(granted, action) {
			return true
		}
	}
	for _, scope := range scopes {
		if scope == string(action) {
			return true
		}
	}
	return false
}

// deny records the refusal and returns the error for code. The event's action
// is the permission that was refused and its reason the returned code, so an
// operator can see what was attempted without another cross-tenant read. The
// event is stored under the request tenant, which is the only tenant the caller
// may write; no field echoes the resource id beyond its project name.
func (a *Authorizer) deny(ctx context.Context, tenant core.TenantID, principal authn.Principal, action Action, resource Resource, code core.Code) error {
	event := platform.AuditEvent{
		Time:      a.clock.Now(),
		Tenant:    tenant,
		Principal: principal.ID,
		Action:    string(action),
		Resource:  "project/" + resource.Project,
		Outcome:   "denied",
		Reason:    string(code),
	}
	if err := a.audit.Record(ctx, event); err != nil {
		return core.NewError(core.CodeUnavailable, "the authorization denial could not be recorded", nil)
	}
	return core.NewError(code, "the principal may not perform this action on the resource", nil)
}

// validPrincipalTenant applies the tenant grammar of REQ-CORE-005 to a
// principal's claim. It reuses core rather than growing a second parser.
func validPrincipalTenant(id core.TenantID) bool {
	_, ok := core.TenantFrom(core.WithTenant(context.Background(), id))
	return ok
}
