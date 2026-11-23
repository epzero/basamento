package steps

import (
	"context"
	"fmt"
	"strings"

	"github.com/cucumber/godog"
	"github.com/epzero/basamento/auth/authz"
)

func registerAuthz(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, scn *godog.Scenario) (context.Context, error) {
		if !scenarioUsesAuthz(scn) {
			return ctx, nil
		}
		w := newAuthzWorld()
		if scenarioPinsSQLite(scn) {
			w.forceSQLite = true
		}
		world(ctx).Authz = w
		return ctx, nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		if w := world(ctx).Authz; w != nil {
			w.teardown()
		}
		return ctx, nil
	})

	registerGivenAuthz(sc)
	registerWhenAuthz(sc)
	registerThenAuthz(sc)
}

func registerGivenAuthz(sc *godog.ScenarioContext) {
	hasRole := func(ctx context.Context, pid, tenant, role, project string) error {
		w := authzW(ctx)
		if tenant == "" {
			tenant = w.tenant
		}
		return w.seedRole(ctx, tenant, pid, role, project)
	}
	sc.Step(`^principal "([^"]*)" has role "([^"]*)" in project "([^"]*)"$`, func(ctx context.Context, pid, role, project string) error {
		return hasRole(ctx, pid, "", role, project)
	})
	sc.Step(`^principal "([^"]*)" of tenant "([^"]*)" has role "([^"]*)" in project "([^"]*)"$`,
		func(ctx context.Context, pid, tenant, role, project string) error {
			return hasRole(ctx, pid, tenant, role, project)
		})
	sc.Step(`^principal "([^"]*)" of tenant "([^"]*)" has tenant-wide role "([^"]*)"$`,
		func(ctx context.Context, pid, tenant, role string) error {
			return hasRole(ctx, pid, tenant, role, "")
		})
	sc.Step(`^principal "([^"]*)" of tenant "([^"]*)" has no role in project "([^"]*)"$`,
		func(ctx context.Context, pid, tenant, project string) error {
			w := authzW(ctx)
			if err := w.ensureStore(ctx); err != nil {
				return err
			}
			w.setPrincipal(pid, tenant, nil)
			return nil
		})
	sc.Step(`^principal "([^"]*)" of tenant "([^"]*)" has a binding in project "([^"]*)" whose condition exceeds the cost limit$`,
		func(ctx context.Context, pid, tenant, project string) error {
			w := authzW(ctx)
			if err := w.ensureStore(ctx); err != nil {
				return err
			}
			w.costHeavy = true
			cond := `resource.attrs["a"].all(x, resource.attrs["b"].all(y, y > 0))`
			return w.seedCondition(ctx, tenant, pid, project, cond)
		})
	sc.Step(`^principal "([^"]*)" of tenant "([^"]*)" has a binding in project "([^"]*)" whose condition errors at check time$`,
		func(ctx context.Context, pid, tenant, project string) error {
			return authzW(ctx).seedCondition(ctx, tenant, pid, project, `resource.attrs.owner == principal.id`)
		})
	sc.Step(`^principal "([^"]*)" of tenant "([^"]*)" has a binding in project "([^"]*)" with condition "([^"]*)"$`,
		func(ctx context.Context, pid, tenant, project, cond string) error {
			return authzW(ctx).seedCondition(ctx, tenant, pid, project, cond)
		})
	sc.Step(`^the principal "([^"]*)" carries scopes "([^"]*)"$`, func(ctx context.Context, pid, scopes string) error {
		w := authzW(ctx)
		parts := strings.Fields(scopes)
		w.setPrincipal(pid, "", parts)
		return nil
	})
	sc.Step(`^resource "([^"]*)" has session "([^"]*)"$`, func(ctx context.Context, kindID, sess string) error {
		w := authzW(ctx)
		parts := strings.SplitN(kindID, ":", 2)
		kind, id := parts[0], parts[1]
		res := authz.Resource{Kind: kind, ID: id, Session: sess}
		w.resources[kindID] = res
		return nil
	})
	sc.Step(`^the role "([^"]*)" holds every permission of the role "([^"]*)"$`, func(ctx context.Context, holder, contained string) error {
		return roleHoldsEveryPermissionOf(holder, contained)
	})
	sc.Step(`^the runs table holds no row for the target resource$`, func(ctx context.Context) error {
		w := authzW(ctx)
		if err := w.ensureStore(ctx); err != nil {
			return err
		}
		rows, err := w.db.Execer().Query(w.tenantCtx(w.tenant), `SELECT id FROM runs WHERE id = ?`, "r9")
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		if rows.Next() {
			return fmt.Errorf("runs table has r9")
		}
		return rows.Err()
	})
	sc.Step(`^the binding store uses a tenant-scoped SQL table on SQLite$`, func(ctx context.Context) error {
		w := authzW(ctx)
		if err := w.ensureStore(ctx); err != nil {
			return err
		}
		if w.db.Dialect() != "sqlite" {
			return fmt.Errorf("dialect %q", w.db.Dialect())
		}
		rows, err := w.db.Execer().Query(context.Background(), `PRAGMA table_info(authz_bindings)`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		hasTenant := false
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dflt any
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				return err
			}
			if name == "tenant" {
				hasTenant = true
			}
		}
		if !hasTenant {
			return fmt.Errorf("no tenant column")
		}
		return rows.Err()
	})
	sc.Step(`^the binding store cannot answer checks$`, func(ctx context.Context) error {
		w := authzW(ctx)
		if err := w.ensureStore(ctx); err != nil {
			return err
		}
		// AUTH-020-01 has no audit assertions; closing the shared SQL store
		// also disables the audit port for this scenario.
		_ = w.db.Close()
		w.storeBroken = true
		w.db = nil
		return nil
	})
	sc.Step(`^the API declares an RPC with no action$`, func(ctx context.Context) error {
		w := authzW(ctx)
		if err := w.ensureStore(ctx); err != nil {
			return err
		}
		w.rpcMode = "no-action"
		w.rpcProc = "bad.rpc"
		return w.rebuildGuard()
	})
	sc.Step(`^the API declares an RPC with an action but no resource resolver$`, func(ctx context.Context) error {
		w := authzW(ctx)
		if err := w.ensureStore(ctx); err != nil {
			return err
		}
		w.rpcMode = "no-resolver"
		w.rpcProc = "bad.rpc"
		return w.rebuildGuard()
	})
}
