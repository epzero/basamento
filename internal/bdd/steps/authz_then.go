package steps

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/cucumber/godog"
	"github.com/epzero/basamento/auth/authz"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

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
		for _, action := range authz.Actions() {
			if strings.Contains(msg, action) {
				return fmt.Errorf("message names permission %q", action)
			}
		}
		return nil
	})
	sc.Step(`^no binding for "([^"]*)" is stored$`, func(ctx context.Context, target string) error {
		w := authzW(ctx)
		n, err := w.principalBindingCount(w.tenant, target)
		if err != nil {
			return err
		}
		if n != 0 {
			return fmt.Errorf("principal %q has %d bindings", target, n)
		}
		return nil
	})
	sc.Step(`^no binding for tenant "([^"]*)" is stored$`, func(ctx context.Context, tenant string) error {
		w := authzW(ctx)
		after, err := w.snapshotBindings(tenant)
		if err != nil {
			return err
		}
		if !bindingsEqual(w.bindingsSnap, after) {
			return fmt.Errorf("bindings changed from %+v to %+v", w.bindingsSnap, after)
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
		w := authzW(ctx)
		events, err := w.readAudits(w.tenant)
		if err != nil {
			return err
		}
		var denials []platform.AuditEvent
		for _, e := range events {
			if e.Outcome == "denied" {
				denials = append(denials, e)
			}
		}
		if len(denials) != 1 {
			return fmt.Errorf("want 1 denial, got %d in %+v", len(denials), events)
		}
		if denials[0].Resource != want {
			return fmt.Errorf("resource %q want %q", denials[0].Resource, want)
		}
		return nil
	})
	sc.Step(`^the audit event holds no condition text and no token$`, func(ctx context.Context) error {
		w := authzW(ctx)
		events, err := w.readAudits(w.tenant)
		if err != nil {
			return err
		}
		cond := w.seededCondition
		for _, e := range events {
			for _, field := range auditStringFields(e) {
				if cond != "" && strings.Contains(field, cond) {
					return fmt.Errorf("leaks condition text in %q", field)
				}
				if strings.Contains(field, "resource.attrs") || strings.Contains(field, "principal.id") {
					return fmt.Errorf("leaks attrs in %q", field)
				}
				if strings.Contains(strings.ToLower(field), "token") {
					return fmt.Errorf("leaks token in %q", field)
				}
			}
			if e.Reason != "" && !safeAuditReasons[e.Reason] {
				return fmt.Errorf("unsafe reason %q", e.Reason)
			}
			if e.Principal != "vic" {
				return fmt.Errorf("principal %q want vic", e.Principal)
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
		w := authzW(ctx)
		if w.api.startedRunID == "" {
			return fmt.Errorf("run not started")
		}
		if st := w.api.runs["p1"][w.api.startedRunID]; st != "active" {
			return fmt.Errorf("started run %q state %q", w.api.startedRunID, st)
		}
		return nil
	})
	sc.Step(`^the gate is resolved$`, func(ctx context.Context) error {
		if authzW(ctx).api.gateProject != "p1" {
			return fmt.Errorf("gate project %q", authzW(ctx).api.gateProject)
		}
		return nil
	})
	sc.Step(`^the message is accepted$`, func(ctx context.Context) error {
		if authzW(ctx).api.messageProject != "p1" {
			return fmt.Errorf("message project %q", authzW(ctx).api.messageProject)
		}
		return nil
	})
	sc.Step(`^the list contains the runs of project "([^"]*)"$`, func(ctx context.Context, project string) error {
		w := authzW(ctx)
		want := sortedRunIDs(w.api.runs[project])
		got := authzSortStrings(w.lastList)
		if len(got) != len(want) {
			return fmt.Errorf("list %v want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				return fmt.Errorf("list %v want %v", got, want)
			}
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
		w := authzW(ctx)
		if len(w.credSink) != 0 {
			return fmt.Errorf("value in sink")
		}
		if bytes.Contains(w.lastNamesJSON, w.credSecret) {
			return fmt.Errorf("names response contains secret")
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
		w := authzW(ctx)
		if len(w.credSink) == 0 || !bytes.Equal(w.credSink, w.credSecret) {
			return fmt.Errorf("credential not used in sink")
		}
		if bytes.Contains(w.lastResolveBody, w.credSecret) {
			return fmt.Errorf("resolve response contains secret")
		}
		return nil
	})
	sc.Step(`^the RPC is refused before any permission check$`, func(ctx context.Context) error {
		w := authzW(ctx)
		if w.guardWiringErr == nil {
			return fmt.Errorf("want wiring error")
		}
		if string(core.CodeOf(w.guardWiringErr)) != string(core.CodeInvalidArgument) {
			return fmt.Errorf("wiring %w", w.guardWiringErr)
		}
		if !strings.Contains(w.guardWiringErr.Error(), w.rpcProc) {
			return fmt.Errorf("wiring error does not name %q: %w", w.rpcProc, w.guardWiringErr)
		}
		if w.lastErr == nil {
			return fmt.Errorf("want unsupported on request")
		}
		if string(core.CodeOf(w.lastErr)) != string(core.CodeUnsupported) {
			return fmt.Errorf("request %w", w.lastErr)
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
		if e.Action != "authz.deny" {
			continue
		}
		if e.Principal != who {
			continue
		}
		if reason != "" && e.Reason != reason {
			continue
		}
		return nil
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
