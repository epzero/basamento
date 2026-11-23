package authz

import (
	"context"
	"log/slog"
	"time"

	"github.com/epzero/basamento/auth/authn"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

const (
	defaultCostLimit   = uint64(10_000)
	maxConditionBytes  = 2048
	auditRecordTimeout = 5 * time.Second
)

// bindingStore loads bindings for checks and mutates them on grant paths.
type bindingStore interface {
	BindingsForCheck(ctx context.Context, principalID, resourceProject string) ([]BindingRow, error)
	Grant(ctx context.Context, b Binding) error
	Revoke(ctx context.Context, principalID string, role Role, project string) error
	List(ctx context.Context, principalID, project string) ([]Binding, error)
}

// Options configures an Authorizer.
type Options struct {
	Store              *SQLStore
	Audit              platform.Audit
	Clock              core.Clock
	Logger             *slog.Logger
	AuditFailureMetric AuditFailureCounter
	CostLimit          uint64
	ConditionCacheSize int
}

// Authorizer evaluates RBAC with optional CEL conditions (specs/auth/design.md §5).
type Authorizer struct {
	store     bindingStore
	audit     platform.Audit
	clock     core.Clock
	log       *slog.Logger
	metric    AuditFailureCounter
	compile   *conditionCompiler
	cache     *conditionCache
	costLimit uint64
	roles     map[Role]PermissionSet
}

// New validates options and returns an Authorizer.
func New(opts Options) (*Authorizer, error) {
	if opts.Store == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "store is required", nil)
	}
	if opts.Audit == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "audit is required", nil)
	}
	if opts.Clock == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "clock is required", nil)
	}
	limit := opts.CostLimit
	if limit == 0 {
		limit = defaultCostLimit
	}
	cc, err := newConditionCompiler(limit)
	if err != nil {
		return nil, err
	}
	cacheSize := opts.ConditionCacheSize
	if cacheSize <= 0 {
		cacheSize = defaultConditionCacheSize
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Authorizer{
		store:     opts.Store,
		audit:     opts.Audit,
		clock:     opts.Clock,
		log:       log,
		metric:    opts.AuditFailureMetric,
		compile:   cc,
		cache:     newConditionCache(cacheSize),
		costLimit: limit,
		roles:     cloneRoleTable(rolePermissions),
	}, nil
}

func cloneRoleTable(in map[Role]PermissionSet) map[Role]PermissionSet {
	out := make(map[Role]PermissionSet, len(in))
	for k, v := range in {
		out[k] = v.clone()
	}
	return out
}

func (a *Authorizer) rolePermsFor(r Role) PermissionSet {
	if ps, ok := a.roles[r]; ok {
		return ps.clone()
	}
	return nil
}

func (a *Authorizer) grantRequirements(action string, target Role) []string {
	return grantRequirements(action, target, a.rolePermsFor)
}

// Check runs the decision procedure of design §5 on principal, action and resource.
func (a *Authorizer) Check(ctx context.Context, principal authn.Principal, action string, resource Resource) error {
	if !catalogHas(action) {
		return a.deny(ctx, principal, resource, ReasonUndeclaredAction)
	}
	return a.authorize(ctx, principal, resource, action)
}

// authorize performs one store read and evaluates every required action.
// An empty required list is denied as undeclared-action before any store read.
func (a *Authorizer) authorize(ctx context.Context, principal authn.Principal, resource Resource, required ...string) error {
	if len(required) == 0 {
		return a.deny(ctx, principal, resource, ReasonUndeclaredAction)
	}
	ctxTenant, ctxOK := core.TenantFrom(ctx)
	if !ctxOK || ctxTenant != principal.Tenant {
		return a.deny(ctx, principal, resource, ReasonTenantMismatch)
	}
	rows, err := a.store.BindingsForCheck(ctx, principal.ID, resource.Project)
	if err != nil {
		return errUnavailable()
	}
	perms, condReason := a.unionBindings(principal, resource, rows)
	beforeNarrow := perms.clone()
	nr := narrowScopes(perms, principal.Scopes, resource.Session)

	for _, action := range required {
		if nr.perms.Has(action) {
			continue
		}
		reason := denyReasonForScopes(principal.Scopes, beforeNarrow, action, condReason, nr)
		return a.deny(ctx, principal, resource, reason)
	}
	return nil
}

func (a *Authorizer) unionBindings(principal authn.Principal, resource Resource, rows []BindingRow) (PermissionSet, string) {
	var perms PermissionSet
	worst := ""
	for _, row := range rows {
		hold, reason := a.evalBinding(principal, resource, row)
		if reason != "" {
			worst = worstConditionReason(worst, reason)
		}
		if hold {
			perms = perms.Union(a.rolePermsFor(row.Role))
		}
	}
	return perms, worst
}

func (a *Authorizer) evalBinding(principal authn.Principal, resource Resource, row BindingRow) (bool, string) {
	if !row.Condition.Valid || row.Condition.String == "" {
		return true, ""
	}
	text := row.Condition.String
	p, err := a.cache.get(text, a.compile.compileNew)
	if err != nil {
		return false, ReasonConditionError
	}
	switch evalCondition(p, a.compile, principal, resource) {
	case condTrue:
		return true, ""
	case condFalse:
		return false, ""
	case condCost:
		return false, ReasonConditionCostExceed
	default:
		return false, ReasonConditionError
	}
}

func (a *Authorizer) deny(ctx context.Context, principal authn.Principal, resource Resource, reason string) error {
	sanitizedPrincipal := sanitizeAuditPart(principal.ID)
	ev := platform.AuditEvent{
		Time:      a.clock.Now().UTC(),
		Principal: sanitizedPrincipal,
		Action:    "authz.deny",
		Resource:  sanitizeAuditResource(resource.Kind, resource.ID),
		Outcome:   "denied",
		Reason:    reason,
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditRecordTimeout)
	defer cancel()
	if err := a.audit.Record(auditCtx, ev); err != nil {
		a.log.Error("authz: audit record failed",
			"principal", sanitizedPrincipal,
			"reason", reason,
			"code", core.CodeOf(err),
		)
		if a.metric != nil {
			a.metric.Add(auditCtx, 1)
		}
	}
	return errPermissionDenied()
}

// Grant validates identifiers, grantor authority, the condition, then stores the binding.
// Validation order: (1) static grammars, (2) grantor authority including superset rule,
// (3) condition compile and type-check, (4) store write.
func (a *Authorizer) Grant(ctx context.Context, grantor authn.Principal, b Binding) error {
	if !validProject(b.Project) {
		return errInvalidProject()
	}
	if !validPrincipal(b.Principal) {
		return errInvalidPrincipal()
	}
	if !validBuiltinRole(b.Role) {
		return errInvalidRole()
	}
	res := Resource{Project: b.Project, Kind: "project", ID: b.Project}
	if err := a.authorize(ctx, grantor, res, a.grantRequirements("binding.grant", b.Role)...); err != nil {
		return err
	}
	if b.Condition != "" {
		if len(b.Condition) > maxConditionBytes {
			return errInvalidCondition()
		}
		if _, err := a.compile.compileNew(b.Condition); err != nil {
			return err
		}
	}
	return a.store.Grant(ctx, b)
}

// Revoke removes a binding after validation and grantor authority checks.
func (a *Authorizer) Revoke(ctx context.Context, grantor authn.Principal, principalID string, role Role, project string) error {
	if !validProject(project) {
		return errInvalidProject()
	}
	if !validPrincipal(principalID) {
		return errInvalidPrincipal()
	}
	if !validBuiltinRole(role) {
		return errInvalidRole()
	}
	res := Resource{Project: project, Kind: "project", ID: project}
	if err := a.authorize(ctx, grantor, res, a.grantRequirements("binding.revoke", role)...); err != nil {
		return err
	}
	return a.store.Revoke(ctx, principalID, role, project)
}

// List returns bindings visible to requester for principalID in project scope.
func (a *Authorizer) List(ctx context.Context, requester authn.Principal, principalID, project string) ([]Binding, error) {
	if !validProject(project) {
		return nil, errInvalidProject()
	}
	if !validPrincipal(principalID) {
		return nil, errInvalidPrincipal()
	}
	res := Resource{Project: project, Kind: "project", ID: project}
	if err := a.authorize(ctx, requester, res, "binding.list"); err != nil {
		return nil, err
	}
	return a.store.List(ctx, principalID, project)
}
