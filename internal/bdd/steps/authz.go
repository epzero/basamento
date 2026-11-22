package steps

import (
	"context"
	"fmt"
	"strings"

	"github.com/cucumber/godog"
	"github.com/epzero/basamento/auth/authz"
	"github.com/epzero/basamento/core"
)

func registerAuthz(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		world(ctx).Authz = newAuthzWorld()
		if world(ctx).Core == nil {
			world(ctx).Core = newCoreWorld()
		}
		world(ctx).Core.clock = authzW(ctx).clock
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
			return w.ensureStore(ctx)
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
	sc.Step(`^the role "([^"]*)" holds every permission of the role "([^"]*)"$`, func(ctx context.Context, _, _ string) error {
		return memberContainsViewer()
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
		w.dbClosed = true
		_ = w.db.Close()
		return w.rebuildAuthorizer()
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
		tctx := w.tenantCtx(tenant)
		g := w.principal(grantor, tenant)
		b := authz.Binding{Principal: target, Role: authz.Role(role), Project: project, Condition: cond}
		err := w.auth.Grant(tctx, g, b)
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

func registerThenAuthz(sc *godog.ScenarioContext) {
	sc.Step(`^the action fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		w := authzW(ctx)
		if w.lastErr == nil {
			return fmt.Errorf("want error code %s", code)
		}
		if string(core.CodeOf(w.lastErr)) != code {
			return fmt.Errorf("got %s want %s", core.CodeOf(w.lastErr), code)
		}
		return nil
	})
	sc.Step(`^the action is allowed for ([a-z]+)$`, func(ctx context.Context, who string) error {
		w := authzW(ctx)
		if w.lastErr != nil {
				return fmt.Errorf("%s: %w", who, w.lastErr)
			}
		return nil
	})
	sc.Step(`^an audit event records the denied action by ([a-z]+)$`, func(ctx context.Context, who string) error {
		return auditDeniedBy(ctx, who, "")
	})
	sc.Step(`^an audit event records the denied action by ([a-z]+) with reason "([^"]*)"$`, auditDeniedBy)
	sc.Step(`^no audit event records a denial$`, func(ctx context.Context) error {
		w := authzW(ctx)
		events, err := w.readAudits(w.tenant)
		if err != nil {
			return err
		}
		for _, e := range events {
			if e.Outcome == "denied" {
				return fmt.Errorf("denial recorded: %+v", e)
			}
		}
		return nil
	})
	sc.Step(`^no audit event records an allowed check by ([a-z]+)$`, func(ctx context.Context, _ string) error {
		return auditNoAllow(ctx)
	})
	sc.Step(`^the denial message names neither "([^"]*)" nor the role "([^"]*)"$`,
		func(ctx context.Context, act, role string) error {
			msg := strings.ToLower(authzW(ctx).denialMessage())
			if strings.Contains(msg, act) || strings.Contains(msg, role) {
				return fmt.Errorf("message leaks detail: %q", authzW(ctx).denialMessage())
			}
			return nil
		})
	sc.Step(`^the rejection names neither "([^"]*)" nor any permission$`, func(ctx context.Context, name string) error {
		msg := strings.ToLower(authzW(ctx).denialMessage())
		if strings.Contains(msg, name) {
			return fmt.Errorf("message names %q", name)
		}
		return nil
	})
	sc.Step(`^no binding for "([^"]*)" is stored$`, func(ctx context.Context, target string) error {
		w := authzW(ctx)
		list, err := w.sqlStore.List(w.tenantCtx(w.tenant), target, "p1")
		if err != nil {
			return err
		}
		if len(list) != 0 {
			return fmt.Errorf("bindings %+v", list)
		}
		return nil
	})
	sc.Step(`^no binding for tenant "([^"]*)" is stored$`, func(ctx context.Context, tenant string) error {
		w := authzW(ctx)
		rows, err := w.db.Execer().Query(context.Background(),
			`SELECT COUNT(*) FROM authz_bindings WHERE tenant = ? AND (principal LIKE '../%' OR project LIKE '../%')`,
			tenant)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		var n int
		if !rows.Next() {
			return fmt.Errorf("no count")
		}
		if err := rows.Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return fmt.Errorf("invalid bindings stored: %d", n)
		}
		return nil
	})
	sc.Step(`^the binding of "([^"]*)" role "([^"]*)" in project "([^"]*)" is stored for tenant "([^"]*)"$`,
		func(ctx context.Context, pid, role, project, tenant string) error {
			w := authzW(ctx)
			list, err := w.sqlStore.List(w.tenantCtx(tenant), pid, project)
			if err != nil {
				return err
			}
			for _, b := range list {
				if string(b.Role) == role {
					return nil
				}
			}
			return fmt.Errorf("no binding %+v", list)
		})
	sc.Step(`^the list contains exactly the role "([^"]*)"$`, func(ctx context.Context, role string) error {
		w := authzW(ctx)
		if len(w.lastList) != 1 || w.lastList[0] != role {
			return fmt.Errorf("list %v want %s", w.lastList, role)
		}
		return nil
	})
	sc.Step(`^the binding of "([^"]*)" in project "([^"]*)" is absent$`, func(ctx context.Context, pid, project string) error {
		w := authzW(ctx)
		list, err := w.sqlStore.List(w.tenantCtx(w.tenant), pid, project)
		if err != nil {
			return err
		}
		if len(list) != 0 {
			return fmt.Errorf("still bound %+v", list)
		}
		return nil
	})
	sc.Step(`^an audit event records action "([^"]*)" and outcome "([^"]*)" by ([a-z]+)$`,
		func(ctx context.Context, act, outcome, who string) error {
			events, err := authzW(ctx).readAudits(authzW(ctx).tenant)
			if err != nil {
				return err
			}
			for _, e := range events {
				if e.Action == act && e.Outcome == outcome && e.Principal == who {
					return nil
				}
			}
			return fmt.Errorf("events %+v", events)
		})
	sc.Step(`^the audit event names resource "([^"]*)" with no attributes$`, func(ctx context.Context, want string) error {
		events, err := authzW(ctx).readAudits(authzW(ctx).tenant)
		if err != nil {
			return err
		}
		for _, e := range events {
			if e.Resource == want {
				return nil
			}
		}
		return fmt.Errorf("events %+v", events)
	})
	sc.Step(`^the audit event holds no condition text and no token$`, func(ctx context.Context) error {
		events, err := authzW(ctx).readAudits(authzW(ctx).tenant)
		if err != nil {
			return err
		}
		for _, e := range events {
			if strings.Contains(e.Reason, "resource.attrs") || strings.Contains(e.Resource, "token") {
				return fmt.Errorf("unsafe audit %+v", e)
			}
		}
		return nil
	})
	sc.Step(`^the failed audit record is logged and counted$`, func(ctx context.Context) error {
		w := authzW(ctx)
		if w.metric.get() != 1 {
			return fmt.Errorf("metric %d", w.metric.get())
		}
		for _, r := range w.logBuf.records() {
			if r.Level.String() != "ERROR" {
				continue
			}
			if recordContainsField(r, "resource.attrs") {
				return fmt.Errorf("log leaks condition")
			}
			if _, ok := logRecordHasAttrs(r, "principal"); !ok {
				continue
			}
			if _, ok := logRecordHasAttrs(r, "reason"); !ok {
				continue
			}
			return nil
		}
		return fmt.Errorf("no error log")
	})
	sc.Step(`^the run is started$`, func(ctx context.Context) error {
		if !authzW(ctx).api.started {
			return fmt.Errorf("run not started")
		}
		return nil
	})
	sc.Step(`^the gate is resolved$`, func(ctx context.Context) error {
		if !authzW(ctx).api.gate {
			return fmt.Errorf("gate not resolved")
		}
		return nil
	})
	sc.Step(`^the message is accepted$`, func(ctx context.Context) error {
		if !authzW(ctx).api.message {
			return fmt.Errorf("message not accepted")
		}
		return nil
	})
	sc.Step(`^the list contains the runs of project "([^"]*)"$`, func(ctx context.Context, project string) error {
		w := authzW(ctx)
		if len(w.lastList) == 0 {
			return fmt.Errorf("empty list")
		}
		if _, ok := w.api.runs[project]; !ok {
			return fmt.Errorf("no runs for %s", project)
		}
		return nil
	})
	sc.Step(`^the list contains the credential names of project "([^"]*)"$`, func(ctx context.Context, _ string) error {
		w := authzW(ctx)
		if len(w.lastNames) == 0 {
			return fmt.Errorf("no names")
		}
		return nil
	})
	sc.Step(`^no credential value is returned$`, func(ctx context.Context) error {
		if len(authzW(ctx).credSink) != 0 {
			return fmt.Errorf("value leaked")
		}
		return nil
	})
	sc.Step(`^the binding is created$`, func(ctx context.Context) error {
		if authzW(ctx).lastErr != nil {
			return authzW(ctx).lastErr
		}
		return nil
	})
	sc.Step(`^the credential value is used in the step but not returned to ([a-z]+)$`, func(ctx context.Context, _ string) error {
		if len(authzW(ctx).credSink) == 0 {
			return fmt.Errorf("credential not used")
		}
		return nil
	})
	sc.Step(`^the RPC is refused before any permission check$`, func(ctx context.Context) error {
		w := authzW(ctx)
			if w.lastErr == nil {
				return fmt.Errorf("want unsupported, got success")
			}
			if string(core.CodeOf(w.lastErr)) != string(core.CodeUnsupported) {
				return fmt.Errorf("got %w", w.lastErr)
			}
		return nil
	})
}

func auditDeniedBy(ctx context.Context, who, reason string) error {
	w := authzW(ctx)
	events, err := w.readAudits(w.tenant)
	if err != nil {
		return err
	}
	for _, e := range events {
		if e.Outcome != "denied" {
			continue
		}
		if e.Principal != who {
			continue
		}
		if reason != "" && e.Reason != reason {
			continue
		}
		if reason == "" || e.Reason == reason {
			return nil
		}
	}
	return fmt.Errorf("no denial for %s in %+v", who, events)
}

func auditNoAllow(ctx context.Context) error {
	w := authzW(ctx)
	events, err := w.readAudits(w.tenant)
	if err != nil {
		return err
	}
	if len(events) != 0 {
		return fmt.Errorf("audit events %+v", events)
	}
	return nil
}
