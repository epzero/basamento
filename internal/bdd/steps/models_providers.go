package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/host"
	"github.com/epzero/basamento/flow/ir"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/catalog"
	"github.com/epzero/basamento/model/node"
	"github.com/epzero/basamento/model/providers"
	"github.com/epzero/basamento/model/providers/sim"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/platform/blob"
	"github.com/epzero/basamento/runtime"
	"github.com/epzero/basamento/runtime/local"
)

// providerWorld drives the manifest-driven providers and the router. The
// image scenario runs the model node over the generic HTTP provider against
// the in-process simulator, so no live vendor is reached.
type providerWorld struct {
	srv     *httptest.Server
	sim     *sim.HTTP
	invoker *providers.HTTP
	cat     *catalog.Catalog
	blobs   *memBlob
	handler *node.Handler

	rt      *local.Runtime
	ref     runtime.RunRef
	running bool
	doneCh  chan struct{}

	mu     sync.Mutex
	out    node.Output
	runErr error

	routerModels []routerModel
	router       *providers.Router
	picked       providers.Route
	pickErr      error
}

type routerModel struct {
	id      string
	invoker *routerInvoker
}

func (w *providerWorld) teardown() {
	if w.running && w.rt != nil {
		select {
		case <-w.doneCh:
		default:
			go func() { _ = w.rt.Cancel(core.WithTenant(context.Background(), "acme"), w.ref, "") }()
			select {
			case <-w.doneCh:
			case <-time.After(localWaitLimit):
			}
		}
		w.running = false
	}
	if w.srv != nil {
		w.srv.Close()
		w.srv = nil
	}
}

// imageProvider builds the HTTP provider over the simulator and starts a run
// that invokes image.generate with the scenario's seed.
func (w *providerWorld) imageProvider(ctx context.Context) error {
	if w.invoker != nil {
		return nil
	}
	w.sim = sim.NewHTTP(sim.HTTPConfig{})
	w.srv = httptest.NewServer(w.sim.Handler())
	m, err := providers.ReadManifest(strings.NewReader(fmt.Sprintf(imageManifest, w.srv.URL)))
	if err != nil {
		return err
	}
	w.invoker, err = providers.NewHTTP(m, providers.HTTPOptions{PollInterval: -1})
	if err != nil {
		return err
	}
	descs, err := w.invoker.Describe(ctx)
	if err != nil {
		return err
	}
	w.cat, err = catalog.New(scenarioClock{w: world(ctx)})
	if err != nil {
		return err
	}
	if err := w.cat.AddDescriptor(descs[0]); err != nil {
		return err
	}
	w.blobs = newMemBlob()
	w.rt = local.New(local.Options{Clock: world(ctx).Core.clock, IDs: &coretest.SeqIds{}})
	w.handler, err = node.New(node.Deps{
		Invoker: w.invoker,
		Catalog: w.cat,
		Steps:   w.rt,
		Blobs:   w.blobs,
		Fetch: func(_ context.Context, out model.Output) ([]byte, string, error) {
			if data, mime, ok := w.sim.Bytes(out.URL); ok {
				return data, mime, nil
			}
			return nil, "", fmt.Errorf("no simulated bytes for %q", out.URL)
		},
	})
	return err
}

const imageManifest = `name: sim-image
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
models:
  - id: sim-image
    caps: [image.generate]
    params_schema: {type: object}
`

// invoke starts one run of a one-node flow whose node is the model handler.
func (w *providerWorld) invoke(ctx context.Context, op string, seed int64) error {
	if w.running {
		return fmt.Errorf("a run is already in flight")
	}
	def := &flow.IR{
		Name:    "invoke",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes: map[string]flow.Node{
			"generate": {
				Kind: "model",
				Spec: json.RawMessage(fmt.Sprintf(`{"model":"sim-image","op":%q}`, op)),
			},
		},
		Edges: []flow.Edge{{From: ir.Start, To: "generate"}},
	}
	flowHost := host.New(singleCatalog{def: def})
	flowHost.RegisterHandler("model", func(dc runtime.DurableContext, n flow.Node, input json.RawMessage) (json.RawMessage, error) {
		cfg, err := node.ConfigFromSpec(n.Name, n.Spec)
		if err != nil {
			return nil, err
		}
		out, err := w.handler.Handle(dc, cfg, input)
		w.mu.Lock()
		w.out = out
		w.runErr = err
		w.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	})
	w.doneCh = make(chan struct{})
	if err := w.rt.RegisterFlowHost(func(dc runtime.DurableContext, ref runtime.FlowRef, in json.RawMessage) (json.RawMessage, error) {
		out, err := flowHost.Run(dc, ref, in)
		close(w.doneCh)
		return out, err
	}); err != nil {
		return err
	}
	input := fmt.Sprintf(`{"seed":%d,"params":{"prompt":"a cat"}}`, seed)
	ref, err := w.rt.Start(core.WithTenant(context.Background(), "acme"), runtime.StartRequest{
		Flow:  runtime.FlowRef{Name: def.Name},
		Input: json.RawMessage(input),
	})
	if err != nil {
		return err
	}
	w.ref = ref
	w.running = true
	return nil
}

func (w *providerWorld) settled() error {
	select {
	case <-w.doneCh:
		w.running = false
	case <-time.After(localWaitLimit):
		return fmt.Errorf("timed out waiting for the run to settle")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.runErr
}

func (w *providerWorld) assets() ([]node.Asset, error) {
	if err := w.settled(); err != nil {
		return nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.out.Assets) != 2 {
		return nil, fmt.Errorf("stored %d assets, want 2", len(w.out.Assets))
	}
	return w.out.Assets, nil
}

// memBlob is a content store held in memory; it implements platform.Blob.
type memBlob struct {
	mu   sync.Mutex
	data map[string][]byte
	typ  map[string]string
}

func newMemBlob() *memBlob {
	return &memBlob{data: map[string][]byte{}, typ: map[string]string{}}
}

func (m *memBlob) Put(_ context.Context, key string, r io.Reader, contentType string) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = body
	m.typ[key] = contentType
	return nil
}

func (m *memBlob) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	body, ok := m.data[key]
	if !ok {
		return nil, core.NewError(core.CodeNotFound, "not found", nil)
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func (m *memBlob) Exists(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.data[key]
	return ok, nil
}

func (m *memBlob) Delete(context.Context, string) error { return nil }

func (m *memBlob) SignedURL(context.Context, string, time.Duration) (string, error) {
	return "", nil
}

var _ platform.Blob = (*memBlob)(nil)

// routerInvoker carries a router descriptor; the router scenarios do not call
// it.
type routerInvoker struct {
	desc model.Descriptor
}

func (r *routerInvoker) Provider() string { return "sim" }
func (r *routerInvoker) Describe(context.Context) ([]model.Descriptor, error) {
	return []model.Descriptor{r.desc}, nil
}
func (*routerInvoker) Start(context.Context, model.Request) (model.Handle, error) {
	return model.Handle{}, nil
}
func (*routerInvoker) Events(context.Context, model.Handle, int64) (<-chan model.Event, error) {
	return nil, nil
}
func (*routerInvoker) Result(context.Context, model.Handle) (model.Result, error) {
	return model.Result{}, nil
}
func (*routerInvoker) Cancel(context.Context, model.Handle) error { return nil }
func (*routerInvoker) Lookup(context.Context, string) (*model.Handle, error) {
	return nil, nil
}

func (w *providerWorld) buildRouter() error {
	routes := make([]providers.Route, 0, len(w.routerModels))
	for _, m := range w.routerModels {
		routes = append(routes, providers.Route{Invoker: m.invoker, Descriptor: m.invoker.desc})
	}
	r, err := providers.NewRouter(routes...)
	if err != nil {
		return err
	}
	w.router = r
	return nil
}

func registerProviders(sc *godog.ScenarioContext) {
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		if w := world(ctx).Providers; w != nil {
			w.teardown()
		}
		return ctx, nil
	})

	sc.Step(`^a simulated image provider that returns 2 images$`, func(ctx context.Context) error {
		return world(ctx).Providers.imageProvider(ctx)
	})
	sc.Step(`^a run invokes "([^"]*)" with seed (\d+)$`, func(ctx context.Context, op string, seed int64) error {
		w := world(ctx).Providers
		if err := w.imageProvider(ctx); err != nil {
			return err
		}
		return w.invoke(ctx, op, seed)
	})
	sc.Step(`^(\d+) assets? (?:is|are) stored by content hash$`, func(ctx context.Context, want int) error {
		w := world(ctx).Providers
		assets, err := w.assets()
		if err != nil {
			return err
		}
		if len(assets) != want {
			return fmt.Errorf("stored %d assets, want %d", len(assets), want)
		}
		for i, asset := range assets {
			w.blobs.mu.Lock()
			data, ok := w.blobs.data[asset.Ref.BlobKey]
			w.blobs.mu.Unlock()
			if !ok {
				return fmt.Errorf("asset %d bytes were not stored", i)
			}
			if asset.Ref.BlobKey != blob.ContentKeyOf(data) {
				return fmt.Errorf("asset %d key %q is not its content hash", i, asset.Ref.BlobKey)
			}
			if asset.Ref.SHA256 == "" {
				return fmt.Errorf("asset %d has no content hash", i)
			}
		}
		return nil
	})
	sc.Step(`^each asset records the model, the parameter hash, seed (\d+) and the cost$`, func(ctx context.Context, seed int64) error {
		w := world(ctx).Providers
		assets, err := w.assets()
		if err != nil {
			return err
		}
		w.mu.Lock()
		out := w.out
		w.mu.Unlock()
		wantHash := node.ParamHash(json.RawMessage(`{"prompt":"a cat"}`))
		for i, asset := range assets {
			p := asset.Provenance
			if p.Model != "sim-image" {
				return fmt.Errorf("asset %d model = %q, want sim-image", i, p.Model)
			}
			if p.ParamsHash != wantHash {
				return fmt.Errorf("asset %d parameter hash = %q, want %q", i, p.ParamsHash, wantHash)
			}
			if p.Seed == nil || *p.Seed != seed {
				return fmt.Errorf("asset %d seed = %v, want %d", i, p.Seed, seed)
			}
			if p.CostMpts == 0 || p.CostMpts != out.CostMpts {
				return fmt.Errorf("asset %d cost = %d, want the settled cost %d", i, p.CostMpts, out.CostMpts)
			}
		}
		return nil
	})

	sc.Step(`^models "([^"]*)" with locality "([^"]*)" and "([^"]*)" with locality "([^"]*)"$`, func(ctx context.Context, first, firstLocality, second, secondLocality string) error {
		w := world(ctx).Providers
		makeModel := func(id, locality string) routerModel {
			d := model.Descriptor{
				ID:       id,
				Provider: "sim",
				Locality: model.Locality(locality),
			}
			return routerModel{id: id, invoker: &routerInvoker{desc: d}}
		}
		w.routerModels = []routerModel{makeModel(first, firstLocality), makeModel(second, secondLocality)}
		return nil
	})
	sc.Step(`^both serve capability "([^"]*)"$`, func(ctx context.Context, capability string) error {
		w := world(ctx).Providers
		for i := range w.routerModels {
			w.routerModels[i].invoker.desc.Caps = []model.Capability{model.Capability(capability)}
		}
		return w.buildRouter()
	})
	sc.Step(`^the router picks a model for "([^"]*)" in profile "([^"]*)"$`, func(ctx context.Context, capability, profile string) error {
		w := world(ctx).Providers
		w.picked, w.pickErr = w.router.Pick(providers.Profile(profile), model.Capability(capability))
		return nil
	})
	sc.Step(`^the router picks "([^"]*)"$`, func(ctx context.Context, id string) error {
		w := world(ctx).Providers
		if w.pickErr != nil {
			return w.pickErr
		}
		got := w.picked.Descriptor.ID
		if got != id {
			return fmt.Errorf("the router picked %q, want %q", got, id)
		}
		return nil
	})
}
