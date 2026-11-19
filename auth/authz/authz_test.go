package authz

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/epzero/basamento/auth/authn"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform"
)

type testBindings map[string][]Role

func bindingKey(tenant core.TenantID, project, principal string) string {
	return string(tenant) + "|" + project + "|" + principal
}

func (b testBindings) RolesFor(ctx context.Context, principalID, project string) ([]Role, error) {
	tenant, ok := core.TenantFrom(ctx)
	if !ok {
		return nil, nil
	}
	return b[bindingKey(tenant, project, principalID)], nil
}

type failingBindings struct{ err error }

func (b failingBindings) RolesFor(context.Context, string, string) ([]Role, error) {
	return nil, b.err
}

type recordingAudit struct {
	events []platform.AuditEvent
	err    error
}

func (a *recordingAudit) Record(ctx context.Context, e platform.AuditEvent) error {
	if a.err != nil {
		return a.err
	}
	tenant, ok := core.TenantFrom(ctx)
	if !ok {
		return errors.New("test audit: no tenant in context")
	}
	if e.Tenant != "" && e.Tenant != tenant {
		return errors.New("test audit: event tenant differs from context")
	}
	a.events = append(a.events, e)
	return nil
}

func newFixture(t *testing.T) (*Authorizer, *recordingAudit, *testBindings, *coretest.FakeClock) {
	t.Helper()
	bindings := testBindings{}
	audit := &recordingAudit{}
	clock := coretest.NewFakeClock(time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC))
	a, err := NewAuthorizer(bindings, audit, clock)
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}
	return a, audit, &bindings, clock
}

func tenantCtx(tenant core.TenantID) context.Context {
	return core.WithTenant(context.Background(), tenant)
}

func TestPermissionsDenyByDefault(t *testing.T) {
	tests := []struct {
		name   string
		role   Role
		action Action
		want   bool
	}{
		{"viewer reads a run", RoleViewer, ActionRunRead, true},
		{"viewer reads a project", RoleViewer, ActionProjectRead, true},
		{"viewer cannot cancel a run", RoleViewer, ActionRunCancel, false},
		{"editor cancels a run", RoleEditor, ActionRunCancel, true},
		{"editor creates a run", RoleEditor, ActionRunCreate, true},
		{"editor cannot manage a project", RoleEditor, ActionProjectManage, false},
		{"admin manages a project", RoleAdmin, ActionProjectManage, true},
		{"unknown role grants nothing", Role("owner"), ActionRunRead, false},
		{"unknown action is never granted", RoleAdmin, Action("run.delete"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Can(tt.role, tt.action); got != tt.want {
				t.Fatalf("Can(%q, %q) = %v, want %v", tt.role, tt.action, got, tt.want)
			}
		})
	}
}

func TestPermissionsAreCopied(t *testing.T) {
	first, ok := Permissions(RoleViewer)
	if !ok || len(first) == 0 {
		t.Fatalf("Permissions(viewer) = %v, %v", first, ok)
	}
	first[0] = Action("tampered")

	second, _ := Permissions(RoleViewer)
	if slices.Contains(second, Action("tampered")) {
		t.Fatal("Permissions returns shared mutable state")
	}
	if _, ok := Permissions(Role("owner")); ok {
		t.Fatal("Permissions reported an unknown role as known")
	}
}

func TestCheckAllowsRolePermission(t *testing.T) {
	a, audit, bindings, _ := newFixture(t)
	(*bindings)[bindingKey("acme", "p1", "vic")] = []Role{RoleEditor}

	err := a.Check(tenantCtx("acme"), authn.Principal{ID: "vic", Tenant: "acme"}, ActionRunCancel, Resource{Tenant: "acme", Project: "p1"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(audit.events) != 0 {
		t.Fatalf("audited %d events on an allowed action, want 0", len(audit.events))
	}
}

func TestCheckAllowsScope(t *testing.T) {
	a, audit, bindings, _ := newFixture(t)
	(*bindings)[bindingKey("acme", "p1", "token-1")] = []Role{RoleViewer}

	p := authn.Principal{ID: "token-1", Tenant: "acme", Scopes: []string{string(ActionRunCancel)}}
	if err := a.Check(tenantCtx("acme"), p, ActionRunCancel, Resource{Tenant: "acme", Project: "p1"}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(audit.events) != 0 {
		t.Fatalf("audited %d events on an allowed action, want 0", len(audit.events))
	}
}

func TestCheckDeniesUndeclaredActionEvenWithMatchingScope(t *testing.T) {
	a, audit, bindings, _ := newFixture(t)
	(*bindings)[bindingKey("acme", "p1", "token-1")] = []Role{RoleAdmin}

	p := authn.Principal{ID: "token-1", Tenant: "acme", Scopes: []string{"run.delete"}}
	err := a.Check(tenantCtx("acme"), p, Action("run.delete"), Resource{Tenant: "acme", Project: "p1"})
	if code := core.CodeOf(err); code != core.CodePermissionDenied {
		t.Fatalf("Check code = %s, want %s", code, core.CodePermissionDenied)
	}
	if len(audit.events) != 1 {
		t.Fatalf("audited %d events, want 1", len(audit.events))
	}
}

func TestCheckDeniesViewerCancelAndAudits(t *testing.T) {
	a, audit, bindings, clock := newFixture(t)
	(*bindings)[bindingKey("acme", "p1", "vic")] = []Role{RoleViewer}
	p := authn.Principal{ID: "vic", Tenant: "acme"}

	err := a.Check(tenantCtx("acme"), p, ActionRunCancel, Resource{Tenant: "acme", Project: "p1"})
	if code := core.CodeOf(err); code != core.CodePermissionDenied {
		t.Fatalf("Check code = %s, want %s", code, core.CodePermissionDenied)
	}
	if len(audit.events) != 1 {
		t.Fatalf("audited %d events, want 1", len(audit.events))
	}
	e := audit.events[0]
	if e.Tenant != "acme" || e.Principal != "vic" || e.Outcome != "denied" {
		t.Fatalf("event tenant/principal/outcome = %q/%q/%q", e.Tenant, e.Principal, e.Outcome)
	}
	if e.Action != string(ActionRunCancel) {
		t.Fatalf("event action = %q, want %q", e.Action, ActionRunCancel)
	}
	if e.Resource != "project/p1" {
		t.Fatalf("event resource = %q, want project/p1", e.Resource)
	}
	if e.Reason != string(core.CodePermissionDenied) {
		t.Fatalf("event reason = %q, want %q", e.Reason, core.CodePermissionDenied)
	}
	if !e.Time.Equal(clock.Now()) {
		t.Fatalf("event time = %s, want %s", e.Time, clock.Now())
	}
}

func TestCheckDeniesWithoutBinding(t *testing.T) {
	a, audit, _, _ := newFixture(t)

	err := a.Check(tenantCtx("acme"), authn.Principal{ID: "vic", Tenant: "acme"}, ActionRunRead, Resource{Tenant: "acme", Project: "p1"})
	if code := core.CodeOf(err); code != core.CodePermissionDenied {
		t.Fatalf("Check code = %s, want %s", code, core.CodePermissionDenied)
	}
	if len(audit.events) != 1 {
		t.Fatalf("audited %d events, want 1", len(audit.events))
	}
}

func TestCheckKeepsTenantsApart(t *testing.T) {
	a, _, bindings, _ := newFixture(t)
	(*bindings)[bindingKey("acme", "p1", "vic")] = []Role{RoleAdmin}
	p := authn.Principal{ID: "vic", Tenant: "globex"}

	err := a.Check(tenantCtx("globex"), p, ActionRunCancel, Resource{Tenant: "globex", Project: "p1"})
	if code := core.CodeOf(err); code != core.CodePermissionDenied {
		t.Fatalf("Check code = %s, want %s", code, core.CodePermissionDenied)
	}
}

func TestCheckMapsTenantMismatchToNotFound(t *testing.T) {
	tests := []struct {
		name      string
		ctx       core.TenantID
		principal authn.Principal
		resource  Resource
	}{
		{"principal from another tenant", "acme", authn.Principal{ID: "vic", Tenant: "globex"}, Resource{Tenant: "acme", Project: "p1"}},
		{"resource from another tenant", "acme", authn.Principal{ID: "vic", Tenant: "acme"}, Resource{Tenant: "globex", Project: "p1"}},
		{"resource without a tenant", "acme", authn.Principal{ID: "vic", Tenant: "acme"}, Resource{Project: "p1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, audit, _, _ := newFixture(t)
			err := a.Check(tenantCtx(tt.ctx), tt.principal, ActionRunCancel, tt.resource)
			if code := core.CodeOf(err); code != core.CodeNotFound {
				t.Fatalf("Check code = %s, want %s", code, core.CodeNotFound)
			}
			if len(audit.events) != 1 {
				t.Fatalf("audited %d events, want 1", len(audit.events))
			}
			if audit.events[0].Tenant != tt.ctx {
				t.Fatalf("event tenant = %q, want %q", audit.events[0].Tenant, tt.ctx)
			}
		})
	}
}

func TestCheckMapsMissingProjectToNotFound(t *testing.T) {
	a, audit, bindings, _ := newFixture(t)
	(*bindings)[bindingKey("acme", "p1", "vic")] = []Role{RoleAdmin}

	err := a.Check(tenantCtx("acme"), authn.Principal{ID: "vic", Tenant: "acme"}, ActionRunCancel, Resource{Tenant: "acme"})
	if code := core.CodeOf(err); code != core.CodeNotFound {
		t.Fatalf("Check code = %s, want %s", code, core.CodeNotFound)
	}
	if len(audit.events) != 1 {
		t.Fatalf("audited %d events, want 1", len(audit.events))
	}
}

func TestCheckMapsUnauthenticated(t *testing.T) {
	tests := []struct {
		name      string
		ctx       context.Context
		principal authn.Principal
	}{
		{"no tenant in context", context.Background(), authn.Principal{ID: "vic", Tenant: "acme"}},
		{"invalid tenant in context", core.WithTenant(context.Background(), "bad/tenant"), authn.Principal{ID: "vic", Tenant: "acme"}},
		{"no principal", tenantCtx("acme"), authn.Principal{Tenant: "acme"}},
		{"principal without a tenant", tenantCtx("acme"), authn.Principal{ID: "vic"}},
		{"principal with an invalid tenant", tenantCtx("acme"), authn.Principal{ID: "vic", Tenant: "bad\\tenant"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, audit, _, _ := newFixture(t)
			err := a.Check(tt.ctx, tt.principal, ActionRunCancel, Resource{Tenant: "acme", Project: "p1"})
			if code := core.CodeOf(err); code != core.CodeUnauthenticated {
				t.Fatalf("Check code = %s, want %s", code, core.CodeUnauthenticated)
			}
			if len(audit.events) != 0 {
				t.Fatalf("audited %d events without an authenticated principal, want 0", len(audit.events))
			}
		})
	}
}

func TestCheckFailsClosedOnBindingsError(t *testing.T) {
	audit := &recordingAudit{}
	a, err := NewAuthorizer(failingBindings{err: errors.New("store down")}, audit, coretest.NewFakeClock(time.Now()))
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}

	err = a.Check(tenantCtx("acme"), authn.Principal{ID: "vic", Tenant: "acme"}, ActionRunCancel, Resource{Tenant: "acme", Project: "p1"})
	if err == nil {
		t.Fatal("Check allowed an action when the bindings lookup failed")
	}
	if len(audit.events) != 0 {
		t.Fatalf("audited %d events on a lookup failure, want 0", len(audit.events))
	}
}

func TestCheckFailsClosedWhenDenialCannotBeAudited(t *testing.T) {
	bindings := testBindings{bindingKey("acme", "p1", "vic"): []Role{RoleViewer}}
	audit := &recordingAudit{err: errors.New("audit down")}
	a, err := NewAuthorizer(bindings, audit, coretest.NewFakeClock(time.Now()))
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}

	err = a.Check(tenantCtx("acme"), authn.Principal{ID: "vic", Tenant: "acme"}, ActionRunCancel, Resource{Tenant: "acme", Project: "p1"})
	if err == nil {
		t.Fatal("Check allowed an action when its denial could not be audited")
	}
	if code := core.CodeOf(err); code != core.CodeUnavailable {
		t.Fatalf("Check code = %s, want %s", code, core.CodeUnavailable)
	}
}

func TestCheckDoesNotLeakTenantOrResource(t *testing.T) {
	a, _, bindings, _ := newFixture(t)
	(*bindings)[bindingKey("acme", "p1", "vic")] = []Role{RoleViewer}

	err := a.Check(tenantCtx("acme"), authn.Principal{ID: "vic", Tenant: "acme"}, ActionRunCancel, Resource{Tenant: "acme", Project: "p1"})
	var pe *core.Error
	if !errors.As(err, &pe) {
		t.Fatalf("Check error is %T, want *core.Error", err)
	}
	for _, leak := range []string{"acme", "p1"} {
		if strings.Contains(pe.Message, leak) {
			t.Fatalf("error message leaks %q: %q", leak, pe.Message)
		}
	}
	if pe.Details != nil || pe.Params != nil {
		t.Fatalf("error carries details/params: %v/%v", pe.Details, pe.Params)
	}
}

func TestNewAuthorizerRequiresDependencies(t *testing.T) {
	audit := &recordingAudit{}
	clock := coretest.NewFakeClock(time.Now())
	tests := []struct {
		name     string
		bindings Bindings
		audit    platform.Audit
		clock    core.Clock
	}{
		{"no bindings", nil, audit, clock},
		{"no audit", testBindings{}, nil, clock},
		{"no clock", testBindings{}, audit, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewAuthorizer(tt.bindings, tt.audit, tt.clock); core.CodeOf(err) != core.CodeInvalidArgument {
				t.Fatalf("NewAuthorizer error = %v, want %s", err, core.CodeInvalidArgument)
			}
		})
	}
}
