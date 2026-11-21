package providers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/providers"
	"github.com/epzero/basamento/model/providers/sim"
)

const pollManifest = `name: sim-image
locality: cloud
protocol: poll
http:
  base_url: %s
  start:
    method: POST
    path: /start
    body: '{client_ref: .client_ref, prompt: (.params.prompt // "")}'
    response: {id: .id}
  status:
    path: /status/{id}
    response:
      state: .state
      state_map: {running: running, succeeded: succeeded, failed: failed, cancelled: cancelled}
      progress: .progress
      outputs: .outputs
      usage: .usage
      cost: .cost_mpts
  cancel:
    method: POST
    path: /cancel/{id}
  lookup:
    path: /lookup
    query: {client_ref: .client_ref}
    response: {id: .id}
models:
  - id: sim-image
    caps: [image.generate]
    params_schema: {type: object}
`

// testCtx carries the tenant the invocation registry requires on every call.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	return core.WithTenant(t.Context(), "acme")
}

func readManifest(t *testing.T, src string) *providers.Manifest {
	t.Helper()
	m, err := providers.ReadManifest(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func httpProvider(t *testing.T, srv *httptest.Server, opts providers.HTTPOptions) *providers.HTTP {
	t.Helper()
	if opts.PollInterval == 0 {
		opts.PollInterval = -1
	}
	p, err := providers.NewHTTP(readManifest(t, fmt.Sprintf(pollManifest, srv.URL)), opts)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHTTPContract(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(sim.NewHTTP(sim.HTTPConfig{}).Handler())
	defer srv.Close()
	p := httpProvider(t, srv, providers.HTTPOptions{Clock: core.SystemClock{}})

	descs, err := p.Describe(testCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(descs) != 1 || descs[0].ID != "sim-image" || descs[0].Locality != model.LocalityCloud || descs[0].Protocol != model.ProtocolPoll {
		t.Fatalf("descriptors = %+v", descs)
	}

	req := model.Request{Model: "sim-image", Op: model.CapImageGenerate, ClientRef: "ref-1", Params: json.RawMessage(`{"prompt":"a cat"}`)}
	h, err := p.Start(testCtx(t), req)
	if err != nil {
		t.Fatal(err)
	}
	if h.ClientRef != "ref-1" || h.ID != "job-1" || h.Provider != "sim-image" {
		t.Fatalf("handle = %+v", h)
	}
	h2, err := p.Start(testCtx(t), req)
	if err != nil {
		t.Fatal(err)
	}
	if h2 != h {
		t.Fatalf("second start = %+v, want %+v", h2, h)
	}

	events := drain(t, mustEvents(t, p, h, 0))
	if len(events) == 0 {
		t.Fatal("no progress events")
	}
	for i, e := range events {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d", i, e.Seq)
		}
		if e.Kind != model.EventProgress {
			t.Fatalf("event %d kind = %q, want progress", i, e.Kind)
		}
	}
	res, err := p.Result(testCtx(t), h)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != model.StateSucceeded {
		t.Fatalf("state = %q, want succeeded", res.State)
	}
	if len(res.Outputs) != 2 || res.Outputs[0].URL != "https://sim.test/image-1.png" {
		t.Fatalf("outputs = %+v", res.Outputs)
	}
	if res.Usage == nil || res.Usage.InputTokens != 1000 || res.Usage.OutputTokens != 500 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	if res.CostMpts != 480 {
		t.Fatalf("cost = %d, want 480", res.CostMpts)
	}

	if got, err := p.Lookup(testCtx(t), "ref-1"); err != nil || got == nil || *got != h {
		t.Fatalf("lookup = %+v, %v", got, err)
	}
	if got, err := p.Lookup(testCtx(t), "missing"); err != nil || got != nil {
		t.Fatalf("lookup missing = %+v, %v; want nil, nil", got, err)
	}
}

func TestHTTPStartRetryAfterALostResponseFindsTheInvocation(t *testing.T) {
	t.Parallel()
	simulator := sim.NewHTTP(sim.HTTPConfig{})
	srv := httptest.NewServer(simulator.Handler())
	defer srv.Close()
	p := httpProvider(t, srv, providers.HTTPOptions{Clock: core.SystemClock{}})
	// A start that reached the vendor but whose response was lost is resolved
	// by lookup instead of starting again (REQ-MODEL-005).
	req := model.Request{Model: "sim-image", Op: model.CapImageGenerate, ClientRef: "ref-lost"}
	if _, err := p.Start(testCtx(t), req); err != nil {
		t.Fatal(err)
	}
	// A second provider instance has no local memory of the invocation.
	fresh := httpProvider(t, srv, providers.HTTPOptions{Clock: core.SystemClock{}})
	got, err := fresh.Lookup(testCtx(t), "ref-lost")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != "job-1" {
		t.Fatalf("lookup = %+v, want the vendor invocation", got)
	}
}

func TestHTTPCancelEndsTheInvocation(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(sim.NewHTTP(sim.HTTPConfig{}).Handler())
	defer srv.Close()
	// Back-to-back polling would race the cancel with the first status call;
	// a long interval leaves the invocation running until cancel.
	p := httpProvider(t, srv, providers.HTTPOptions{Clock: core.SystemClock{}, PollInterval: time.Hour})
	h, err := p.Start(testCtx(t), model.Request{Model: "sim-image", Op: model.CapImageGenerate, ClientRef: "ref-cancel"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Cancel(testCtx(t), h); err != nil {
		t.Fatal(err)
	}
	res, err := p.Result(testCtx(t), h)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != model.StateCancelled {
		t.Fatalf("state = %q, want cancelled", res.State)
	}
}

func TestHTTPAuthUsesTheCredentialReference(t *testing.T) {
	t.Parallel()
	simulator := sim.NewHTTP(sim.HTTPConfig{Token: "s3cret"})
	srv := httptest.NewServer(simulator.Handler())
	defer srv.Close()
	m := readManifest(t, strings.Replace(fmt.Sprintf(pollManifest, srv.URL), "  start:", "  credential: cred://sim\n  auth: {header: Authorization, scheme: Bearer}\n  start:", 1))
	creds := credsFunc(func(_ context.Context, ref string) ([]byte, error) {
		if ref != "cred://sim" {
			t.Fatalf("resolved %q", ref)
		}
		return []byte("s3cret"), nil
	})
	p, err := providers.NewHTTP(m, providers.HTTPOptions{Clock: core.SystemClock{}, Credentials: creds, PollInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Start(testCtx(t), model.Request{Model: "sim-image", Op: model.CapImageGenerate, ClientRef: "ref-auth"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Result(testCtx(t), model.Handle{Provider: "sim-image", ID: "job-1", ClientRef: "ref-auth"}); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPStatusErrorMapsToTheTaxonomy(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"job-1"}`)
			return
		}
		w.Header().Set("Retry-After", "7")
		http.Error(w, `{"error":"slow down"}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := httpProvider(t, srv, providers.HTTPOptions{Clock: core.SystemClock{}})
	h, err := p.Start(testCtx(t), model.Request{Model: "sim-image", Op: model.CapImageGenerate, ClientRef: "ref-rate"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Result(testCtx(t), h)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != model.StateFailed {
		t.Fatalf("state = %q, want failed", res.State)
	}
	var failure model.Failure
	if err := json.Unmarshal(res.Error, &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Code != core.CodeRateLimited || !failure.Retryable {
		t.Fatalf("failure = %+v, want rate_limited/retryable", failure)
	}
}

func TestHTTPWithoutLookupReportsUnsupported(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(sim.NewHTTP(sim.HTTPConfig{}).Handler())
	defer srv.Close()
	m := readManifest(t, strings.Replace(fmt.Sprintf(pollManifest, srv.URL), "  lookup:\n    path: /lookup\n    query: {client_ref: .client_ref}\n    response: {id: .id}\n", "", 1))
	p, err := providers.NewHTTP(m, providers.HTTPOptions{Clock: core.SystemClock{}, PollInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Lookup(testCtx(t), "nope"); core.CodeOf(err) != core.CodeUnsupported {
		t.Fatalf("lookup code = %q, want unsupported", core.CodeOf(err))
	}
}

func TestHTTPWebhookResolve(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"job-9"}`)
	}))
	defer srv.Close()
	m := readManifest(t, `name: hook
locality: cloud
protocol: webhook
http:
  base_url: `+srv.URL+`
  start:
    path: /start
    response: {id: .id}
  status:
    response:
      state: .state
      state_map: {done: succeeded}
      outputs: '.files'
models:
  - id: hook-image
    caps: [image.generate]
`)
	p, err := providers.NewHTTP(m, providers.HTTPOptions{Clock: core.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := p.Start(testCtx(t), model.Request{Model: "hook-image", Op: model.CapImageGenerate, ClientRef: "ref-hook"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Resolve(testCtx(t), "ref-hook", []byte(`{"state":"done","files":[{"url":"https://sim.test/a.png","media":"image"}]}`)); err != nil {
		t.Fatal(err)
	}
	res, err := p.Result(testCtx(t), h)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != model.StateSucceeded || len(res.Outputs) != 1 {
		t.Fatalf("result = %+v", res)
	}
}

func TestCLIContract(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m := readManifest(t, `name: local-render
locality: machine_local
protocol: command
cli:
  command: [render]
  dir: `+dir+`
  args: '["--prompt", (.params.prompt // ""), "--out", (.workdir + "/out")]'
  outputs: ["out/*.png"]
  progress: 'frame: ([0-9]+)'
models:
  - id: local-render
    caps: [image.generate]
`)
	p, err := providers.NewCLI(m, providers.CLIOptions{Clock: core.SystemClock{}, Runner: &sim.CLI{
		Files: map[string][]byte{"out/a.png": []byte("image a"), "out/b.png": []byte("image b")},
		Lines: []string{"frame: 50", "frame: 100"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := p.Start(testCtx(t), model.Request{
		Model: "local-render", Op: model.CapImageGenerate, ClientRef: "ref-cli",
		Params: json.RawMessage(`{"prompt":"a cat"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drain(t, mustEvents(t, p, h, 0))
	if len(events) != 2 || events[0].Progress != 0.5 || events[1].Progress != 1 {
		t.Fatalf("events = %+v", events)
	}
	res, err := p.Result(testCtx(t), h)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != model.StateSucceeded || len(res.Outputs) != 2 {
		t.Fatalf("result = %+v", res)
	}
	// The command runs in its own subdirectory of the provider's directory.
	if !strings.Contains(res.Outputs[0].Path, "/runs/ref-cli-") ||
		!strings.HasSuffix(res.Outputs[0].Path, "/out/a.png") || res.Outputs[0].Mime != "image/png" {
		t.Fatalf("output = %+v", res.Outputs[0])
	}
}

func TestCLIFailedCommandFailsTheInvocation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m := readManifest(t, `name: local-render
locality: machine_local
protocol: command
cli:
  command: [render]
  dir: `+dir+`
  outputs: ["out/*.png"]
models:
  - id: local-render
    caps: [image.generate]
`)
	p, err := providers.NewCLI(m, providers.CLIOptions{Clock: core.SystemClock{}, Runner: &sim.CLI{Err: errors.New("exit status 1")}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := p.Start(testCtx(t), model.Request{Model: "local-render", Op: model.CapImageGenerate, ClientRef: "ref-cli-fail"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Result(testCtx(t), h)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != model.StateFailed {
		t.Fatalf("state = %q, want failed", res.State)
	}
}

func TestHTTPResolvesInputAssetsForTheVendor(t *testing.T) {
	t.Parallel()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"job-7","state":"succeeded","outputs":[]}`)
	}))
	defer srv.Close()
	m := readManifest(t, `name: assets
locality: cloud
protocol: single
http:
  base_url: `+srv.URL+`
  start:
    path: /start
    body: '{image: .assets.first_frame[0].url}'
    response: {id: .id, state: .state, outputs: .outputs}
models:
  - id: asset-model
    caps: [video.generate]
`)
	p, err := providers.NewHTTP(m, providers.HTTPOptions{Clock: core.SystemClock{}, Assets: assetURLs{"https://blob.test/signed"}})
	if err != nil {
		t.Fatal(err)
	}
	ref := model.AssetRef{BlobKey: "blob-key", SHA256: "abc", Media: "image"}
	if _, err := p.Start(testCtx(t), model.Request{
		Model: "asset-model", Op: model.CapVideoGenerate, ClientRef: "ref-asset",
		Assets: map[string][]model.AssetRef{"first_frame": {ref}},
	}); err != nil {
		t.Fatal(err)
	}
	if body["image"] != "https://blob.test/signed" {
		t.Fatalf("vendor body = %+v, want the resolved asset URL", body)
	}
}

func TestShippedManifestsLoad(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"manifests/openai-images.yaml", "manifests/runway-video.yaml"} {
		m, err := providers.LoadManifest(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if m.Name == "" || len(m.Models) != 1 {
			t.Fatalf("%s: manifest = %+v", path, m)
		}
		descs, err := m.Descriptors()
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if descs[0].ID == "" || descs[0].Locality != model.LocalityCloud {
			t.Fatalf("%s: descriptor = %+v", path, descs[0])
		}
		// Construction compiles every mapping; a bad jq expression in a shipped
		// manifest must fail here, not at the first call.
		if _, err := providers.NewHTTP(m, providers.HTTPOptions{Clock: core.SystemClock{}, Credentials: credsFunc(func(context.Context, string) ([]byte, error) {
			return []byte("test"), nil
		})}); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
}

type assetURLs struct{ url string }

func (a assetURLs) URL(context.Context, model.AssetRef) (string, error) { return a.url, nil }

type credsFunc func(context.Context, string) ([]byte, error)

func (f credsFunc) Resolve(ctx context.Context, ref string) ([]byte, error) { return f(ctx, ref) }

type stubInvoker struct {
	desc model.Descriptor
}

func (s stubInvoker) Describe(context.Context) ([]model.Descriptor, error) {
	return []model.Descriptor{s.desc}, nil
}
func (stubInvoker) Start(context.Context, model.Request) (model.Handle, error) {
	return model.Handle{}, nil
}
func (stubInvoker) Events(context.Context, model.Handle, int64) (<-chan model.Event, error) {
	return nil, nil
}
func (stubInvoker) Result(context.Context, model.Handle) (model.Result, error) {
	return model.Result{}, nil
}
func (stubInvoker) Cancel(context.Context, model.Handle) error { return nil }
func (stubInvoker) Lookup(context.Context, string) (*model.Handle, error) {
	return nil, nil
}

func TestRouterNeverPicksMachineLocalInTheCloudProfile(t *testing.T) {
	t.Parallel()
	local := stubInvoker{desc: model.Descriptor{ID: "local-image", Caps: []model.Capability{model.CapImageGenerate}, Locality: model.LocalityMachineLocal}}
	cloud := stubInvoker{desc: model.Descriptor{ID: "cloud-image", Caps: []model.Capability{model.CapImageGenerate}, Locality: model.LocalityCloud}}
	r, err := providers.NewRouter(
		providers.Route{Invoker: local, Descriptor: local.desc},
		providers.Route{Invoker: cloud, Descriptor: cloud.desc},
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Pick(providers.ProfileCloud, model.CapImageGenerate)
	if err != nil {
		t.Fatal(err)
	}
	if got.Descriptor.ID != "cloud-image" {
		t.Fatalf("picked %q, want cloud-image", got.Descriptor.ID)
	}

	onlyLocal, err := providers.NewRouter(providers.Route{Invoker: local, Descriptor: local.desc})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := onlyLocal.Pick(providers.ProfileCloud, model.CapImageGenerate); core.CodeOf(err) != core.CodeUnsupported {
		t.Fatalf("code = %q, want unsupported", core.CodeOf(err))
	}
	if got, err := onlyLocal.Pick(providers.ProfileEmbedded, model.CapImageGenerate); err != nil || got.Descriptor.ID != "local-image" {
		t.Fatalf("embedded pick = %+v, %v", got, err)
	}
}

func TestRouterRejectsDuplicateModelsAndMissingCapability(t *testing.T) {
	t.Parallel()
	one := stubInvoker{desc: model.Descriptor{ID: "m", Caps: []model.Capability{model.CapChat}, Locality: model.LocalityCloud}}
	if _, err := providers.NewRouter(
		providers.Route{Invoker: one, Descriptor: one.desc},
		providers.Route{Invoker: one, Descriptor: one.desc},
	); core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("code = %q, want conflict", core.CodeOf(err))
	}
	r, err := providers.NewRouter(providers.Route{Invoker: one, Descriptor: one.desc})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Pick(providers.ProfileCloud, model.CapImageGenerate); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("code = %q, want not_found", core.CodeOf(err))
	}
}

func TestManifestValidation(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"two drivers": `name: x
locality: cloud
protocol: single
http: {base_url: https://x.test, start: {path: /a}}
cli: {command: [x], outputs: ["*"]}
models: [{id: m, caps: [chat]}]
`,
		"cli is machine local": `name: x
locality: cloud
protocol: command
cli: {command: [x], outputs: ["*"]}
models: [{id: m, caps: [chat]}]
`,
		"poll needs state": `name: x
locality: cloud
protocol: poll
http:
  base_url: https://x.test
  start: {path: /a}
  status: {path: /s/{id}}
models: [{id: m, caps: [chat]}]
`,
		"unknown field": `name: x
locality: cloud
protocol: single
http: {base_url: https://x.test, start: {path: /a}}
models: [{id: m, caps: [chat]}]
extra: true
`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := providers.ReadManifest(strings.NewReader(src)); err == nil {
				t.Fatal("manifest was accepted")
			}
		})
	}
}

func mustEvents(t *testing.T, p model.Invoker, h model.Handle, after int64) <-chan model.Event {
	t.Helper()
	ch, err := p.Events(testCtx(t), h, after)
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

func drain(t *testing.T, ch <-chan model.Event) []model.Event {
	t.Helper()
	var out []model.Event
	for e := range ch {
		out = append(out, e)
	}
	return out
}

// countingStarts wraps a handler and counts POST /start calls, so a test can
// prove a repeated start never reaches the vendor.
func countingStarts(h http.Handler) (http.Handler, func() int) {
	var mu sync.Mutex
	n := 0
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/start" {
				mu.Lock()
				n++
				mu.Unlock()
			}
			h.ServeHTTP(w, r)
		}), func() int {
			mu.Lock()
			defer mu.Unlock()
			return n
		}
}

// F1: a second Start with the same client ref must answer from the registry,
// not send another paid /start. The registry's local lookup needs the tenant.
func TestHTTPSecondStartWithTheSameRefReachesTheVendorOnce(t *testing.T) {
	t.Parallel()
	handler, starts := countingStarts(sim.NewHTTP(sim.HTTPConfig{}).Handler())
	srv := httptest.NewServer(handler)
	defer srv.Close()
	p := httpProvider(t, srv, providers.HTTPOptions{Clock: core.SystemClock{}})

	req := model.Request{Model: "sim-image", Op: model.CapImageGenerate, ClientRef: "ref-1"}
	h1, err := p.Start(testCtx(t), req)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := p.Start(testCtx(t), req)
	if err != nil {
		t.Fatal(err)
	}
	if h2 != h1 {
		t.Fatalf("second start = %+v, want %+v", h2, h1)
	}
	if got := starts(); got != 1 {
		t.Fatalf("vendor saw %d /start calls, want 1", got)
	}
	found, err := p.Lookup(testCtx(t), "ref-1")
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.ID != h1.ID {
		t.Fatalf("lookup = %+v, want the vendor id %q", found, h1.ID)
	}
}

// F2: two tenants that use the same client ref must keep their own vendor job;
// the vendor id lives on the run, so tenant A's cancel cannot address B's job.
func TestHTTPTenantsSharingAClientRefKeepTheirOwnVendorJob(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	next := 0
	var cancels []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/start":
			next++
			_, _ = fmt.Fprintf(w, `{"id":"job-%d"}`, next)
		case strings.HasPrefix(r.URL.Path, "/cancel/"):
			cancels = append(cancels, strings.TrimPrefix(r.URL.Path, "/cancel/"))
			_, _ = fmt.Fprint(w, `{"ok":true}`)
		case strings.HasPrefix(r.URL.Path, "/status/"):
			_, _ = fmt.Fprint(w, `{"state":"running"}`)
		default:
			http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p := httpProvider(t, srv, providers.HTTPOptions{Clock: core.SystemClock{}})

	acme := core.WithTenant(t.Context(), "acme")
	beta := core.WithTenant(t.Context(), "beta")
	req := model.Request{Model: "sim-image", Op: model.CapImageGenerate, ClientRef: "shared"}
	a, err := p.Start(acme, req)
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.Start(beta, req)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "job-1" || b.ID != "job-2" {
		t.Fatalf("handles = %q and %q, want job-1 and job-2", a.ID, b.ID)
	}
	if err := p.Cancel(acme, a); err != nil {
		t.Fatal(err)
	}
	found, err := p.Lookup(beta, "shared")
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.ID != "job-2" {
		t.Fatalf("beta lookup = %+v, want job-2", found)
	}
	if err := p.Cancel(beta, *found); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := append([]string(nil), cancels...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "job-1" || got[1] != "job-2" {
		t.Fatalf("cancels = %v, want [job-1 job-2]", got)
	}
}

// F3: a start that the registry cannot record must not reach the vendor.
func TestHTTPStartWithoutATenantNeverReachesTheVendor(t *testing.T) {
	t.Parallel()
	handler, starts := countingStarts(sim.NewHTTP(sim.HTTPConfig{}).Handler())
	srv := httptest.NewServer(handler)
	defer srv.Close()
	p := httpProvider(t, srv, providers.HTTPOptions{Clock: core.SystemClock{}})

	req := model.Request{Model: "sim-image", Op: model.CapImageGenerate, ClientRef: "ref-1"}
	if _, err := p.Start(context.Background(), req); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code = %q, want invalid_argument", core.CodeOf(err))
	}
	if got := starts(); got != 0 {
		t.Fatalf("vendor saw %d /start calls, want 0", got)
	}
}

// F4: a transient status error must not fail a paid run while the vendor job
// continues.
func TestHTTPFollowRetriesATransientStatusError(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	statuses := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/start":
			_, _ = fmt.Fprint(w, `{"id":"job-1"}`)
		case strings.HasPrefix(r.URL.Path, "/status/"):
			statuses++
			if statuses <= 2 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = fmt.Fprint(w, `{"state":"succeeded","outputs":[]}`)
		default:
			http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p := httpProvider(t, srv, providers.HTTPOptions{Clock: core.SystemClock{}})

	ctx, cancel := context.WithTimeout(testCtx(t), 10*time.Second)
	defer cancel()
	h, err := p.Start(ctx, model.Request{Model: "sim-image", Op: model.CapImageGenerate, ClientRef: "ref-1"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Result(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != model.StateSucceeded {
		t.Fatalf("state = %q, want succeeded", res.State)
	}
	mu.Lock()
	got := statuses
	mu.Unlock()
	if got != 3 {
		t.Fatalf("status calls = %d, want 3", got)
	}
}

// F7: a vendor state the manifest does not map fails the run instead of
// leaving it running forever.
func TestHTTPUnmappedVendorStateFailsTheRun(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	statuses := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/start":
			_, _ = fmt.Fprint(w, `{"id":"job-1"}`)
		case strings.HasPrefix(r.URL.Path, "/status/"):
			statuses++
			_, _ = fmt.Fprint(w, `{"state":"mystery"}`)
		default:
			http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p := httpProvider(t, srv, providers.HTTPOptions{Clock: core.SystemClock{}})

	ctx, cancel := context.WithTimeout(testCtx(t), 10*time.Second)
	defer cancel()
	h, err := p.Start(ctx, model.Request{Model: "sim-image", Op: model.CapImageGenerate, ClientRef: "ref-1"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Result(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != model.StateFailed {
		t.Fatalf("state = %q, want failed", res.State)
	}
	var failure model.Failure
	if err := json.Unmarshal(res.Error, &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Code != core.CodeVendor {
		t.Fatalf("failure code = %q, want vendor_error", failure.Code)
	}
	mu.Lock()
	got := statuses
	mu.Unlock()
	if got != 1 {
		t.Fatalf("status calls = %d, want 1", got)
	}
}

// fileRunner writes one output file per invocation, named after the call, and
// reports success.
type fileRunner struct {
	mu sync.Mutex
	n  int
}

func (r *fileRunner) Start(_ context.Context, cmd providers.Command) (providers.Process, error) {
	r.mu.Lock()
	r.n++
	n := r.n
	r.mu.Unlock()
	path := filepath.Join(cmd.Dir, "out", fmt.Sprintf("file-%d.png", n))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte("png"), 0o600); err != nil {
		return nil, err
	}
	return &quietProcess{}, nil
}

type quietProcess struct{}

func (*quietProcess) Stdout() io.Reader { return strings.NewReader("") }
func (*quietProcess) Wait() error       { return nil }

// F6: each invocation collects only the files it wrote, even when two runs
// share the provider's directory.
func TestCLIInvocationsKeepTheirOwnOutputs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m := readManifest(t, `name: local-render
locality: machine_local
protocol: command
cli:
  command: [render]
  dir: `+dir+`
  outputs: ["out/*.png"]
models:
  - id: local-render
    caps: [image.generate]
`)
	p, err := providers.NewCLI(m, providers.CLIOptions{Clock: core.SystemClock{}, Runner: &fileRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"file-1.png", "file-2.png"} {
		ref := fmt.Sprintf("ref-%d", i+1)
		h, err := p.Start(testCtx(t), model.Request{Model: "local-render", Op: model.CapImageGenerate, ClientRef: ref})
		if err != nil {
			t.Fatal(err)
		}
		res, err := p.Result(testCtx(t), h)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Outputs) != 1 || filepath.Base(res.Outputs[0].Path) != want {
			t.Fatalf("%s outputs = %+v, want only %s", ref, res.Outputs, want)
		}
	}
}

// pipeProcess mimics a subprocess whose Wait closes the stdout pipe.
type pipeProcess struct {
	eof             atomic.Bool
	waitedBeforeEOF atomic.Bool
}

func (p *pipeProcess) Stdout() io.Reader {
	return &pipeReader{p: p, lines: []string{"frame: 50\n", "frame: 100\n"}}
}

func (p *pipeProcess) Wait() error {
	if !p.eof.Load() {
		p.waitedBeforeEOF.Store(true)
	}
	return nil
}

type pipeReader struct {
	p     *pipeProcess
	lines []string
}

func (r *pipeReader) Read(b []byte) (int, error) {
	if len(r.lines) == 0 {
		r.p.eof.Store(true)
		return 0, io.EOF
	}
	// Yield so a Wait racing the reader gets its chance to observe the stream
	// mid-way, which is how a subprocess pipe loses its buffered output.
	runtime.Gosched()
	n := copy(b, r.lines[0])
	r.lines = r.lines[1:]
	return n, nil
}

type runnerFunc func(context.Context, providers.Command) (providers.Process, error)

func (f runnerFunc) Start(ctx context.Context, cmd providers.Command) (providers.Process, error) {
	return f(ctx, cmd)
}

// N1: stdout is drained before Wait, so no progress line is lost.
func TestCLIDrainsStdoutBeforeWaiting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m := readManifest(t, `name: local-render
locality: machine_local
protocol: command
cli:
  command: [render]
  dir: `+dir+`
  outputs: ["out/*.png"]
  progress: 'frame: ([0-9]+)'
models:
  - id: local-render
    caps: [image.generate]
`)
	proc := &pipeProcess{}
	p, err := providers.NewCLI(m, providers.CLIOptions{Clock: core.SystemClock{}, Runner: runnerFunc(
		func(context.Context, providers.Command) (providers.Process, error) { return proc, nil },
	)})
	if err != nil {
		t.Fatal(err)
	}
	h, err := p.Start(testCtx(t), model.Request{Model: "local-render", Op: model.CapImageGenerate, ClientRef: "ref-drain"})
	if err != nil {
		t.Fatal(err)
	}
	events := drain(t, mustEvents(t, p, h, 0))
	if len(events) != 2 || events[0].Progress != 0.5 || events[1].Progress != 1 {
		t.Fatalf("events = %+v, want both progress lines", events)
	}
	if _, err := p.Result(testCtx(t), h); err != nil {
		t.Fatal(err)
	}
	if proc.waitedBeforeEOF.Load() {
		t.Fatal("Wait ran before stdout was drained")
	}
}
