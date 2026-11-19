package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/api"
	"github.com/epzero/basamento/auth/authn"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/runtime"
	"github.com/epzero/basamento/runtime/local"
)

// apiWorld drives the HTTP surface at its boundary: a real api.Server over a
// real local runtime, reached with net/http as a client would. Tenants and
// principals are data, so no vendor and no wall clock are involved.
type apiWorld struct {
	runs        *local.Runtime
	http        *httptest.Server
	runIDs      map[core.TenantID]string
	tokenTenant core.TenantID
	listed      []apiRun
	fetchStatus int
	fetchCode   core.Code
}

// apiRun is the part of a list response the scenario asserts on.
type apiRun struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

type apiList struct {
	Runs []apiRun `json:"runs"`
}

func newAPIWorld() *apiWorld { return &apiWorld{runIDs: map[core.TenantID]string{}} }

func (w *apiWorld) teardown() {
	if w.http != nil {
		w.http.Close()
		w.http = nil
	}
}

// startRuns builds a runtime from the scenario clock and starts one run per
// tenant. The flow host returns an empty result at once; the scenario is about
// who may see the run, not what it produces.
func (w *apiWorld) startRuns(ctx context.Context, tenants []core.TenantID) error {
	w.runs = local.New(local.Options{Clock: world(ctx).Core.clock, IDs: &coretest.SeqIds{}})
	host := func(runtime.DurableContext, runtime.FlowRef, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}
	if err := w.runs.RegisterFlowHost(host); err != nil {
		return err
	}
	for _, tenant := range tenants {
		id := string(tenant) + "-run"
		ref, err := w.runs.Start(core.WithTenant(ctx, tenant), runtime.StartRequest{
			Flow:  runtime.FlowRef{Name: "assistant", Hash: "sha256:test"},
			Input: json.RawMessage(`{}`),
			RunID: id,
		})
		if err != nil {
			return fmt.Errorf("start run for %q: %w", tenant, err)
		}
		w.runIDs[tenant] = ref.RunID
	}
	return nil
}

// startServer mounts a server whose authenticator maps one bearer token to a
// principal of tenant. Tenant identity comes from that principal, never from a
// request value (REQ-AUTH-002).
func (w *apiWorld) startServer(tenant core.TenantID) error {
	authenticator := api.AuthenticatorFunc(func(_ context.Context, r *http.Request) (authn.Principal, error) {
		if r.Header.Get("Authorization") != "Bearer "+string(tenant)+"-token" {
			return authn.Principal{}, core.NewError(core.CodeUnauthenticated, "no token", nil)
		}
		return authn.Principal{ID: "ana", Tenant: tenant}, nil
	})
	server, err := api.New(api.Options{Runs: w.runs, Authenticator: authenticator})
	if err != nil {
		return err
	}
	w.http = httptest.NewServer(server.Handler())
	return nil
}

// get performs one authenticated GET. It decodes a 200 body into out and
// returns the platform code of a failed response.
func (w *apiWorld) get(ctx context.Context, path string, out any) (int, core.Code, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.http.URL+path, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+string(w.tokenTenant)+"-token")
	resp, err := w.http.Client().Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		if out != nil {
			if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
				return resp.StatusCode, "", err
			}
		}
		return resp.StatusCode, "", nil
	}
	var body struct {
		Error struct {
			Code core.Code `json:"code"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body.Error.Code, nil
}

func registerAPI(sc *godog.ScenarioContext) {
	sc.Step(`^tenants "([^"]*)" and "([^"]*)" each have a run$`, func(ctx context.Context, first, second string) error {
		return world(ctx).API.startRuns(ctx, []core.TenantID{core.TenantID(first), core.TenantID(second)})
	})
	sc.Step(`^a principal of "([^"]*)" lists runs and fetches the "([^"]*)" run by id$`, func(ctx context.Context, tenant, other string) error {
		w := world(ctx).API
		w.tokenTenant = core.TenantID(tenant)
		if err := w.startServer(core.TenantID(tenant)); err != nil {
			return err
		}
		var list apiList
		status, code, err := w.get(ctx, "/v1/runs", &list)
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("list status = %d, code %q", status, code)
		}
		w.listed = list.Runs
		otherRun, ok := w.runIDs[core.TenantID(other)]
		if !ok {
			return fmt.Errorf("no run for tenant %q", other)
		}
		w.fetchStatus, w.fetchCode, err = w.get(ctx, "/v1/runs/"+otherRun, nil)
		return err
	})
	sc.Step(`^the list contains only "([^"]*)" runs$`, func(ctx context.Context, tenant string) error {
		w := world(ctx).API
		want, ok := w.runIDs[core.TenantID(tenant)]
		if !ok {
			return fmt.Errorf("no run for tenant %q", tenant)
		}
		if len(w.listed) != 1 {
			return fmt.Errorf("listed %d runs, want only %q", len(w.listed), want)
		}
		if w.listed[0].ID != want {
			return fmt.Errorf("listed run %q, want %q", w.listed[0].ID, want)
		}
		return nil
	})
	sc.Step(`^the fetch fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		w := world(ctx).API
		if w.fetchStatus != http.StatusNotFound {
			return fmt.Errorf("fetch status = %d, want 404", w.fetchStatus)
		}
		if string(w.fetchCode) != code {
			return fmt.Errorf("fetch code = %q, want %q", w.fetchCode, code)
		}
		return nil
	})
}
