package steps

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/auth/authn"
	"github.com/epzero/basamento/auth/authz"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

// authzScenarioTenant is the tenant the component scenarios act in. The
// permission scenarios name a project, not a tenant; the component driver puts
// them in a fixed tenant so a check always has the tenant a request carries.
const authzScenarioTenant core.TenantID = "acme"

// authzBindings is the component-level binding store: roles per tenant, project
// and principal. It scopes every lookup by the context tenant like the SQL
// store will.
type authzBindings map[string][]authz.Role

func authzBindingKey(tenant core.TenantID, project, principal string) string {
	return string(tenant) + "|" + project + "|" + principal
}

func (b authzBindings) RolesFor(ctx context.Context, principalID, project string) ([]authz.Role, error) {
	tenant, ok := core.TenantFrom(ctx)
	if !ok {
		return nil, nil
	}
	return b[authzBindingKey(tenant, project, principalID)], nil
}

// authzAudit records denials in memory for the scenario to inspect. It applies
// the platform.Audit tenant rule so a check cannot pass an event for a tenant
// the request does not carry.
type authzAudit struct {
	events []platform.AuditEvent
}

func (a *authzAudit) Record(ctx context.Context, e platform.AuditEvent) error {
	tenant, ok := core.TenantFrom(ctx)
	if !ok {
		return fmt.Errorf("authz audit: no tenant in context")
	}
	if e.Tenant != tenant {
		return fmt.Errorf("authz audit: event tenant %q, context tenant %q", e.Tenant, tenant)
	}
	a.events = append(a.events, e)
	return nil
}

type authzWorld struct {
	bindings    authzBindings
	audit       *authzAudit
	authorizer  *authz.Authorizer
	principalID string
	lastErr     error
}

func newAuthzWorld() *authzWorld {
	return &authzWorld{bindings: authzBindings{}, audit: &authzAudit{}}
}

func (w *authzWorld) check(ctx context.Context, principal string, action authz.Action, resource authz.Resource) error {
	if w.authorizer == nil {
		a, err := authz.NewAuthorizer(w.bindings, w.audit, world(ctx).Core.clock)
		if err != nil {
			return err
		}
		w.authorizer = a
	}
	w.lastErr = w.authorizer.Check(
		core.WithTenant(ctx, authzScenarioTenant),
		authn.Principal{ID: principal, Tenant: authzScenarioTenant},
		action,
		resource,
	)
	return nil
}

func registerAuthz(sc *godog.ScenarioContext) {
	sc.Step(`^principal "([^"]*)" has role "([^"]*)" in project "([^"]*)"$`, func(ctx context.Context, principal, role, project string) error {
		r := authz.Role(role)
		if _, ok := authz.Permissions(r); !ok {
			return fmt.Errorf("unknown role %q", role)
		}
		w := world(ctx).Authz
		key := authzBindingKey(authzScenarioTenant, project, principal)
		w.bindings[key] = append(w.bindings[key], r)
		w.principalID = principal
		return nil
	})
	sc.Step(`^([^"]*) cancels a run in project "([^"]*)"$`, func(ctx context.Context, principal, project string) error {
		w := world(ctx).Authz
		if w.principalID != principal {
			return fmt.Errorf("principal under test is %q, not %q", w.principalID, principal)
		}
		return w.check(ctx, principal, authz.ActionRunCancel, authz.Resource{
			Tenant:  authzScenarioTenant,
			Project: project,
		})
	})
	sc.Step(`^the action fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		w := world(ctx)
		var err error
		if w.Conversation != nil && w.Conversation.lastErr != nil {
			err = w.Conversation.lastErr
		} else {
			err = w.Authz.lastErr
		}
		if err == nil {
			return fmt.Errorf("action succeeded, want code %s", code)
		}
		if got := core.CodeOf(err); string(got) != code {
			return fmt.Errorf("got code %s, want %s", got, code)
		}
		return nil
	})
	sc.Step(`^an audit event records the denied action by ([^"]+)$`, func(ctx context.Context, principal string) error {
		w := world(ctx).Authz
		var found []platform.AuditEvent
		for _, e := range w.audit.events {
			if e.Principal == principal && e.Outcome == "denied" {
				found = append(found, e)
			}
		}
		if len(found) != 1 {
			return fmt.Errorf("found %d denied audit events by %q, want 1", len(found), principal)
		}
		e := found[0]
		if e.Tenant != authzScenarioTenant {
			return fmt.Errorf("audit event tenant %q, want %q", e.Tenant, authzScenarioTenant)
		}
		if e.Action != string(authz.ActionRunCancel) {
			return fmt.Errorf("audit event action %q, want %q", e.Action, authz.ActionRunCancel)
		}
		return nil
	})
}
