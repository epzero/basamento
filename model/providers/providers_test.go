package providers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	p := httpProvider(t, srv, providers.HTTPOptions{})

	descs, err := p.Describe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(descs) != 1 || descs[0].ID != "sim-image" || descs[0].Locality != model.LocalityCloud || descs[0].Protocol != model.ProtocolPoll {
		t.Fatalf("descriptors = %+v", descs)
	}

	req := model.Request{Model: "sim-image", Op: model.CapImageGenerate, ClientRef: "ref-1", Params: json.RawMessage(`{"prompt":"a cat"}`)}
	h, err := p.Start(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if h.ClientRef != "ref-1" || h.ID != "job-1" || h.Provider != "sim-image" {
		t.Fatalf("handle = %+v", h)
	}
	h2, err := p.Start(t.Context(), req)
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
	res, err := p.Result(t.Context(), h)
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

	if got, err := p.Lookup(t.Context(), "ref-1"); err != nil || got == nil || *got != h {
		t.Fatalf("lookup = %+v, %v", got, err)
	}
	if got, err := p.Lookup(t.Context(), "missing"); err != nil || got != nil {
		t.Fatalf("lookup missing = %+v, %v; want nil, nil", got, err)
	}
}

func TestHTTPStartRetryAfterALostResponseFindsTheInvocation(t *testing.T) {
	t.Parallel()
	simulator := sim.NewHTTP(sim.HTTPConfig{})
	srv := httptest.NewServer(simulator.Handler())
	defer srv.Close()
	p := httpProvider(t, srv, providers.HTTPOptions{})
	// A start that reached the vendor but whose response was lost is resolved
	// by lookup instead of starting again (REQ-MODEL-005).
	req := model.Request{Model: "sim-image", Op: model.CapImageGenerate, ClientRef: "ref-lost"}
	if _, err := p.Start(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	// A second provider instance has no local memory of the invocation.
	fresh := httpProvider(t, srv, providers.HTTPOptions{})
	got, err := fresh.Lookup(t.Context(), "ref-lost")
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
	p := httpProvider(t, srv, providers.HTTPOptions{PollInterval: time.Hour})
	h, err := p.Start(t.Context(), model.Request{Model: "sim-image", Op: model.CapImageGenerate, ClientRef: "ref-cancel"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Cancel(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	res, err := p.Result(t.Context(), h)
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
	p, err := providers.NewHTTP(m, providers.HTTPOptions{Credentials: creds, PollInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Start(t.Context(), model.Request{Model: "sim-image", Op: model.CapImageGenerate, ClientRef: "ref-auth"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Result(t.Context(), model.Handle{Provider: "sim-image", ID: "job-1", ClientRef: "ref-auth"}); err != nil {
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
	p := httpProvider(t, srv, providers.HTTPOptions{})
	h, err := p.Start(t.Context(), model.Request{Model: "sim-image", Op: model.CapImageGenerate, ClientRef: "ref-rate"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Result(t.Context(), h)
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
	p, err := providers.NewHTTP(m, providers.HTTPOptions{PollInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Lookup(t.Context(), "nope"); core.CodeOf(err) != core.CodeUnsupported {
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
	p, err := providers.NewHTTP(m, providers.HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := p.Start(t.Context(), model.Request{Model: "hook-image", Op: model.CapImageGenerate, ClientRef: "ref-hook"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Resolve(t.Context(), "ref-hook", []byte(`{"state":"done","files":[{"url":"https://sim.test/a.png","media":"image"}]}`)); err != nil {
		t.Fatal(err)
	}
	res, err := p.Result(t.Context(), h)
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
	p, err := providers.NewCLI(m, providers.CLIOptions{Runner: &sim.CLI{
		Files: map[string][]byte{"out/a.png": []byte("image a"), "out/b.png": []byte("image b")},
		Lines: []string{"frame: 50", "frame: 100"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := p.Start(t.Context(), model.Request{
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
	res, err := p.Result(t.Context(), h)
	if err != nil {
		t.Fatal(err)
	}
	if res.State != model.StateSucceeded || len(res.Outputs) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if res.Outputs[0].Path != dir+"/out/a.png" || res.Outputs[0].Mime != "image/png" {
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
	p, err := providers.NewCLI(m, providers.CLIOptions{Runner: &sim.CLI{Err: errors.New("exit status 1")}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := p.Start(t.Context(), model.Request{Model: "local-render", Op: model.CapImageGenerate, ClientRef: "ref-cli-fail"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Result(t.Context(), h)
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
	p, err := providers.NewHTTP(m, providers.HTTPOptions{Assets: assetURLs{"https://blob.test/signed"}})
	if err != nil {
		t.Fatal(err)
	}
	ref := model.AssetRef{BlobKey: "blob-key", SHA256: "abc", Media: "image"}
	if _, err := p.Start(t.Context(), model.Request{
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
		if _, err := providers.NewHTTP(m, providers.HTTPOptions{Credentials: credsFunc(func(context.Context, string) ([]byte, error) {
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
	ch, err := p.Events(t.Context(), h, after)
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
