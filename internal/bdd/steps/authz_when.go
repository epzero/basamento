package steps

import (
	"context"
	"strings"

	"github.com/cucumber/godog"
	"github.com/epzero/basamento/auth/authz"
)

func registerWhenAuthz(sc *godog.ScenarioContext) {
	action := func(ctx context.Context, who, act string, res authz.Resource) error {
		w := authzW(ctx)
		if err := w.ensureStore(ctx); err != nil {
			return err
		}
		tctx := w.tenantCtx(w.tenant)
		key := res.Kind + ":" + res.ID
		if r, ok := w.resources[key]; ok && r.Session != "" {
			res.Session = r.Session
		}
		if act == "run.get" && w.costHeavy {
			res.Attrs = costHeavyAttrs()
		}
		return w.api.attemptCheck(tctx, who, act, res)
	}
	sc.Step(`^([a-z]+) cancels a run in project "([^"]*)"$`, func(ctx context.Context, who, project string) error {
		w := authzW(ctx)
		if err := w.ensureStore(ctx); err != nil {
			return err
		}
		return w.api.cancelRun(w.tenantCtx(w.tenant), who, project, "")
	})
	sc.Step(`^([a-z]+) cancels a run "([^"]*)" in project "([^"]*)"$`, func(ctx context.Context, who, rid, project string) error {
		w := authzW(ctx)
		if err := w.ensureStore(ctx); err != nil {
			return err
		}
		return w.api.cancelRun(w.tenantCtx(w.tenant), who, project, rid)
	})
	sc.Step(`^([a-z]+) starts a run in project "([^"]*)"$`, func(ctx context.Context, who, project string) error {
		w := authzW(ctx)
		if err := w.ensureStore(ctx); err != nil {
			return err
		}
		return w.api.startRun(w.tenantCtx(w.tenant), who, project)
	})
	sc.Step(`^([a-z]+) lists runs in project "([^"]*)"$`, func(ctx context.Context, who, project string) error {
		w := authzW(ctx)
		if err := w.ensureStore(ctx); err != nil {
			return err
		}
		return w.api.listRuns(w.tenantCtx(w.tenant), who, project)
	})
	sc.Step(`^([a-z]+) attempts undeclared action "([^"]*)" on a run in project "([^"]*)"$`,
		func(ctx context.Context, who, act, project string) error {
			res := authz.Resource{Project: project, Kind: "run", ID: "r1"}
			return action(ctx, who, act, res)
		})
	sc.Step(`^([a-z]+) attempts action "([^"]*)" on a run in project "([^"]*)"$`,
		func(ctx context.Context, who, act, project string) error {
			res := authz.Resource{Project: project, Kind: "run", ID: "r1"}
			return action(ctx, who, act, res)
		})
	sc.Step(`^([a-z]+) attempts action "([^"]*)" on resource "([^"]*)" in project "([^"]*)"$`,
		func(ctx context.Context, who, act, raw, project string) error {
			parts := strings.SplitN(raw, ":", 2)
			res := authz.Resource{Project: project, Kind: parts[0], ID: parts[1]}
			return action(ctx, who, act, res)
		})
	sc.Step(`^([a-z]+) attempts action "([^"]*)" on a resource with no project$`,
		func(ctx context.Context, who, act string) error {
			res := authz.Resource{Kind: "tenant", ID: "acme"}
			return action(ctx, who, act, res)
		})
	sc.Step(`^a check for tenant "([^"]*)" asks ([a-z]+) to cancel a run in project "([^"]*)"$`, crossTenantCheck)
	sc.Step(`^a request reaches that RPC$`, func(ctx context.Context) error {
		w := authzW(ctx)
		proc := w.rpcProc
		if proc == "" {
			proc = "bad.rpc"
		}
		err := w.api.invoke(w.tenantCtx(w.tenant), "caller", proc, authzProjectReq{Project: "p1"})
		w.recordErr(err)
		return nil
	})
	grant := func(ctx context.Context, grantor, role, target, project, tenant, cond string) error {
		w := authzW(ctx)
		if err := w.ensureStore(ctx); err != nil {
			return err
		}
		if tenant == "" {
			tenant = w.tenant
		}
		snap, err := w.snapshotBindings(tenant)
		if err != nil {
			return err
		}
		w.bindingsSnap = snap
		tctx := w.tenantCtx(tenant)
		g := w.principal(grantor, tenant)
		b := authz.Binding{Principal: target, Role: authz.Role(role), Project: project, Condition: cond}
		err = w.auth.Grant(tctx, g, b)
		w.recordErr(err)
		return nil
	}
	sc.Step(`^([a-z]+) grants role "([^"]*)" to "([^"]*)" in project "([^"]*)"$`,
		func(ctx context.Context, g, role, target, project string) error {
			return grant(ctx, g, role, target, project, "", "")
		})
	sc.Step(`^([a-z]+) grants role "([^"]*)" to "([^"]*)" in project "([^"]*)" of tenant "([^"]*)"$`,
		func(ctx context.Context, g, role, target, project, tenant string) error {
			return grant(ctx, g, role, target, project, tenant, "")
		})
	sc.Step(`^([a-z]+) grants role "([^"]*)" to "([^"]*)" in project "([^"]*)" with condition "([^"]*)"$`,
		func(ctx context.Context, g, role, target, project, cond string) error {
			return grant(ctx, g, role, target, project, "", cond)
		})
	sc.Step(`^([a-z]+) lists the bindings of "([^"]*)" in project "([^"]*)"$`, func(ctx context.Context, who, target, project string) error {
		w := authzW(ctx)
		if err := w.ensureStore(ctx); err != nil {
			return err
		}
		tctx := w.tenantCtx(w.tenant)
		list, err := w.auth.List(tctx, w.principal(who, ""), target, project)
		if err != nil {
			w.recordErr(err)
			return nil
		}
		w.lastList = make([]string, len(list))
		for i, b := range list {
			w.lastList[i] = string(b.Role)
		}
		w.recordErr(nil)
		return nil
	})
	sc.Step(`^([a-z]+) revokes role "([^"]*)" from "([^"]*)" in project "([^"]*)"$`,
		func(ctx context.Context, who, role, target, project string) error {
			w := authzW(ctx)
			if err := w.ensureStore(ctx); err != nil {
				return err
			}
			err := w.auth.Revoke(w.tenantCtx(w.tenant), w.principal(who, ""), target, authz.Role(role), project)
			w.recordErr(err)
			return nil
		})
	sc.Step(`^([a-z]+) lists credential names in project "([^"]*)"$`, func(ctx context.Context, who, project string) error {
		w := authzW(ctx)
		if err := w.ensureStore(ctx); err != nil {
			return err
		}
		return w.api.listCredNames(w.tenantCtx(w.tenant), who, project)
	})
	sc.Step(`^([a-z]+) resolves credential "([^"]*)" in project "([^"]*)"$`, func(ctx context.Context, who, raw, project string) error {
		w := authzW(ctx)
		if err := w.ensureStore(ctx); err != nil {
			return err
		}
		return w.api.resolveCred(w.tenantCtx(w.tenant), who, project, raw)
	})
	sc.Step(`^([a-z]+) resolves a gate of the run in project "([^"]*)"$`, func(ctx context.Context, who, project string) error {
		w := authzW(ctx)
		if err := w.ensureStore(ctx); err != nil {
			return err
		}
		return w.api.resolveGate(w.tenantCtx(w.tenant), who, project)
	})
	sc.Step(`^([a-z]+) sends a message to the conversation in project "([^"]*)"$`, func(ctx context.Context, who, project string) error {
		w := authzW(ctx)
		if err := w.ensureStore(ctx); err != nil {
			return err
		}
		return w.api.sendMessage(w.tenantCtx(w.tenant), who, project)
	})
	sc.Step(`^([a-z]+) cancels the run in project "([^"]*)"$`, func(ctx context.Context, who, project string) error {
		w := authzW(ctx)
		if err := w.ensureStore(ctx); err != nil {
			return err
		}
		return w.api.cancelRun(w.tenantCtx(w.tenant), who, project, "r1")
	})
}
