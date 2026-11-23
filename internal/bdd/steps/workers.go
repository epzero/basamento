package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing/fstest"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/worker"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/platform/credentials"
	psql "github.com/epzero/basamento/platform/sql"
)

// workerWorld drives the worker layer over a real SQL queue and the fake
// clock. A "run" here is the platform's reservation around one queue row: the
// queue, leases and settlements are the production code in model/worker.
type workerWorld struct {
	store *worker.Store
	pools map[string]worker.PoolSpec

	lease    time.Duration
	grace    time.Duration
	ageing   time.Duration
	leaseSet bool

	workers map[string]*joinedWorker
	// workerOrder keeps the join order deterministic.
	workerOrder []string

	run *workerRun

	cloud   *cloudSim
	lastErr error

	lastLease     []worker.Invocation
	lastSpill     []worker.SpillResult
	lastHeartbeat worker.HeartbeatAnswer

	// tokens keeps the pool token each pool was created with.
	tokens map[string]string
	// leasedRefs remembers the client refs the world leased, in order.
	leasedRefs []string
	dir        string

	// token of a pool whose token was revoked, keyed by pool id.
	revoked map[string]string
	seq     int

	audit *auditSink

	// The dial-out transport proofs (MODEL-089-01, MODEL-090-01): a real
	// listener the platform hosts and a client the worker dials.
	platformAddr   string
	platformLn     net.Listener
	platformSrv    interface{ Stop() }
	platformCancel context.CancelFunc
	platform       *worker.Service
	transport      *worker.GRPCTransport
	wireClient     *worker.Client
	workedOverWire bool

	// The kit serve system scenario's process and the store it opened.
	kit      *kitProcess
	kitDir   string
	kitAddr  string
	kitHTTP  string
	kitReady string
	kitToken string
	kitSQL   string
	kitStore *worker.Store
}

type joinedWorker struct {
	worker     worker.Worker
	credential string
	accepted   int
}

type workerRun struct {
	clientRef string
	pool      string
	tenant    core.TenantID
	reserved  int64
	charged   int64
	err       error
	settled   bool
}

type cloudSim struct {
	desc     model.Descriptor
	estimate int64
	accepted int
	refs     []string
}

type auditSink struct {
	events []platform.AuditEvent
}

func (a *auditSink) Record(_ context.Context, e platform.AuditEvent) error {
	a.events = append(a.events, e)
	return nil
}

func newWorkerWorld() *workerWorld {
	return &workerWorld{
		pools:   map[string]worker.PoolSpec{},
		workers: map[string]*joinedWorker{},
		revoked: map[string]string{},
		tokens:  map[string]string{},
		audit:   &auditSink{},
	}
}

// workerMigrations is the placeholder migration set the worker BDD store
// opens with; the worker schema itself comes from worker.Open.
var workerMigrations = fstest.MapFS{"00001_noop.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n")}}

// The worker schema is the same for every scenario, so it is migrated once per
// process into a template database; each scenario starts from a copy instead of
// paying the migration cost ~48 times per suite (WP-L3-07 gate timeout).
var (
	workerTemplateOnce sync.Once
	workerTemplatePath string
	workerTemplateErr  error
)

func workerDBTemplate() (string, error) {
	workerTemplateOnce.Do(func() {
		ctx := context.Background()
		dir, err := os.MkdirTemp("", "worker-bdd-template-*")
		if err != nil {
			workerTemplateErr = err
			return
		}
		dbPath := filepath.Join(dir, "worker.db")
		store, err := psql.Open(ctx, "sqlite:///"+dbPath, workerMigrations)
		if err != nil {
			workerTemplateErr = err
			return
		}
		if _, err := worker.Open(ctx, store, worker.Options{}); err != nil {
			_ = store.Close()
			workerTemplateErr = err
			return
		}
		// Closing checkpoints the WAL, so the main file is a complete copy
		// source; any surviving sidecar is copied with it just in case.
		if err := store.Close(); err != nil {
			workerTemplateErr = err
			return
		}
		workerTemplatePath = dbPath
	})
	return workerTemplatePath, workerTemplateErr
}

// copyWorkerDB copies the migrated template to dst, with the WAL sidecar when
// one survived the template's close, so a scenario still gets a real on-disk
// database of its own.
func copyWorkerDB(src, dst string) error {
	for _, suffix := range []string{"", "-wal"} {
		if err := copyFileIfExists(src+suffix, dst+suffix); err != nil {
			return err
		}
	}
	return nil
}

func copyFileIfExists(src, dst string) error {
	in, err := os.Open(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func (w *workerWorld) open(ctx context.Context) (*worker.Store, error) {
	if w.store != nil {
		return w.store, nil
	}
	template, err := workerDBTemplate()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "worker-bdd-*")
	if err != nil {
		return nil, err
	}
	w.dir = dir
	dbPath := filepath.Join(dir, "worker.db")
	if err := copyWorkerDB(template, dbPath); err != nil {
		_ = os.RemoveAll(dir)
		w.dir = ""
		return nil, err
	}
	store, err := psql.Open(ctx, "sqlite:///"+dbPath, workerMigrations)
	if err != nil {
		return nil, err
	}
	ws, err := worker.Open(ctx, store, worker.Options{
		Clock: world(ctx).Core.clock,
		IDs:   &coretest.SeqIds{},
		Audit: w.audit,
	})
	if err != nil {
		return nil, err
	}
	w.store = ws
	return ws, nil
}

func (w *workerWorld) teardown() {
	if w.transport != nil {
		_ = w.transport.Close()
		w.transport = nil
	}
	if w.platformCancel != nil {
		w.platformCancel()
		w.platformCancel = nil
	}
	if w.platformSrv != nil {
		w.platformSrv.Stop()
		w.platformSrv = nil
	}
	if w.platformLn != nil {
		_ = w.platformLn.Close()
		w.platformLn = nil
	}
	if w.kit != nil {
		w.kit.kill()
		w.kit = nil
	}
	if w.kitDir != "" {
		_ = os.RemoveAll(w.kitDir)
		w.kitDir = ""
	}
	if w.dir != "" {
		_ = os.RemoveAll(w.dir)
		w.dir = ""
	}
	w.store = nil
	w.kitStore = nil
}

func (w *workerWorld) ensurePool(ctx context.Context, id string) (worker.PoolSpec, error) {
	if p, ok := w.pools[id]; ok {
		return p, nil
	}
	tenant := core.TenantID("")
	if id != "shared" {
		parts := strings.SplitN(id, "-", 2)
		tenant = core.TenantID(parts[0])
	}
	lease := 5 * time.Minute
	if w.leaseSet {
		lease = w.lease
	}
	grace := time.Minute
	if w.grace > 0 {
		grace = w.grace
	}
	spec := worker.PoolSpec{ID: id, Tenant: tenant, Ageing: w.ageing, Lease: lease, Grace: grace}
	st, err := w.open(ctx)
	if err != nil {
		return worker.PoolSpec{}, err
	}
	pool, err := st.CreatePool(ctx, spec)
	if err != nil {
		return worker.PoolSpec{}, err
	}
	w.pools[id] = pool.PoolSpec
	w.tokens[id] = pool.Token
	return pool.PoolSpec, nil
}

func (w *workerWorld) join(ctx context.Context, name, poolID string, descriptors []worker.Descriptor) error {
	spec, err := w.ensurePool(ctx, poolID)
	if err != nil {
		return err
	}
	st, err := w.open(ctx)
	if err != nil {
		return err
	}
	token := w.revoked[poolID]
	if token == "" {
		token = w.poolToken(ctx, poolID)
	}
	if token == "" {
		return fmt.Errorf("pool %q has no token", spec.ID)
	}
	joined, err := st.Join(ctx, worker.JoinRequest{
		PoolToken:       token,
		ProtocolVersion: worker.ProtocolVersion,
		Descriptors:     descriptors,
		Capacity:        capacityFor(descriptors),
	})
	if err != nil {
		return err
	}
	w.workers[name] = &joinedWorker{worker: joined, credential: joined.Credential}
	w.workerOrder = append(w.workerOrder, name)
	return nil
}

// poolToken returns the token the pool was created with.
func (w *workerWorld) poolToken(ctx context.Context, poolID string) string {
	return w.tokens[poolID]
}

func capacityFor(descriptors []worker.Descriptor) int {
	capacity := 0
	for _, d := range descriptors {
		if d.Units > capacity {
			capacity = d.Units
		}
	}
	if capacity < 4 {
		capacity = 4
	}
	return capacity
}

func descriptorForModel(id string, units int, restartSafe bool) worker.Descriptor {
	if units <= 0 {
		units = 1
	}
	return worker.Descriptor{
		Descriptor: model.Descriptor{
			ID:       id,
			Provider: "worker",
			Caps:     []model.Capability{model.CapChat},
			Params:   json.RawMessage(`{"type":"object"}`),
			Protocol: model.ProtocolSingle,
			Locality: model.LocalityWorker,
		},
		Units:       units,
		RestartSafe: restartSafe,
	}
}

func (w *workerWorld) leaseFor(ctx context.Context, name string, free int) error {
	w.lastLease = nil
	w.lastErr = nil
	jw, ok := w.workers[name]
	if !ok {
		return fmt.Errorf("worker %q has not joined", name)
	}
	st, err := w.open(ctx)
	if err != nil {
		return err
	}
	leased, err := st.Lease(ctx, worker.LeaseRequest{
		WorkerID:   jw.worker.ID,
		Credential: jw.credential,
		FreeUnits:  free,
		TimeLeft:   time.Hour,
	})
	if err != nil {
		w.lastErr = err
		return nil
	}
	w.lastLease = leased
	return nil
}

// reportAttempt reads the attempt a lease-scoped report must carry.
func reportAttempt(ctx context.Context, st *worker.Store, clientRef string) (int, error) {
	inv, err := st.Invocation(ctx, clientRef)
	if err != nil {
		return 0, err
	}
	return inv.Attempt, nil
}

// start records that a worker reported starting its provider call.
func (w *workerWorld) start(ctx context.Context, name, clientRef string) error {
	jw, ok := w.workers[name]
	if !ok {
		return fmt.Errorf("worker %q has not joined", name)
	}
	st, err := w.open(ctx)
	if err != nil {
		return err
	}
	attempt, err := reportAttempt(ctx, st, clientRef)
	if err != nil {
		return err
	}
	if err := st.Starting(ctx, jw.worker.ID, jw.credential, clientRef, attempt); err != nil {
		w.lastErr = err
		return nil
	}
	jw.accepted++
	return nil
}

// settle applies the queue's time-driven rules (timeouts, redelivery, in
// doubt, worker_lost) and spill-over, then records the run's outcome.
func (w *workerWorld) settle(ctx context.Context) error {
	if w.store == nil {
		return nil
	}
	if err := w.store.Sweep(ctx); err != nil {
		return err
	}
	if w.cloud != nil {
		spilled, err := w.store.Spill(ctx, w.cloud.remote())
		if err != nil {
			return err
		}
		if len(spilled) > 0 {
			w.lastSpill = append(w.lastSpill, spilled...)
		}
	}
	if w.run != nil && !w.run.settled {
		inv, err := w.store.Invocation(ctx, w.run.clientRef)
		if err != nil {
			return err
		}
		if inv.State == "failed" {
			w.run.err = core.NewError(inv.Code, "the invocation could not be completed", map[string]any{"client_ref": inv.ClientRef})
			w.run.charged = inv.ChargedMpts
			w.run.settled = true
		}
		if inv.State == "succeeded" || inv.State == "cancelled" || inv.State == "remote" {
			w.run.charged = inv.ChargedMpts
			w.run.settled = true
		}
	}
	return nil
}

func (c *cloudSim) remote() worker.Remote {
	return worker.Remote{
		Name:        "cloud",
		Invoker:     c,
		Descriptors: []model.Descriptor{c.desc},
		Estimate:    c.estimate,
	}
}

// cloudSim is another provider of the same model. It accepts a start and
// records its client ref, like a vendor would.
func (c *cloudSim) Describe(context.Context) ([]model.Descriptor, error) {
	return []model.Descriptor{c.desc}, nil
}

func (c *cloudSim) Start(_ context.Context, r model.Request) (model.Handle, error) {
	c.accepted++
	c.refs = append(c.refs, r.ClientRef)
	return model.Handle{Provider: "cloud", ID: r.ClientRef, ClientRef: r.ClientRef}, nil
}

func (c *cloudSim) Events(context.Context, model.Handle, int64) (<-chan model.Event, error) {
	return nil, core.NewError(core.CodeUnsupported, "cloud simulator has no events", nil)
}

func (c *cloudSim) Result(context.Context, model.Handle) (model.Result, error) {
	return model.Result{State: model.StateSucceeded}, nil
}

func (c *cloudSim) Cancel(context.Context, model.Handle) error { return nil }

func (c *cloudSim) Lookup(context.Context, string) (*model.Handle, error) {
	return nil, core.NewError(core.CodeUnsupported, "cloud simulator cannot look up", nil)
}

func (w *workerWorld) queued(ctx context.Context, tenant core.TenantID, poolID, modelID, class, clientRef string) (worker.Invocation, error) {
	spec, err := w.ensurePool(ctx, poolID)
	if err != nil {
		return worker.Invocation{}, err
	}
	if spec.Tenant != "" {
		tenant = spec.Tenant
	}
	st, err := w.open(ctx)
	if err != nil {
		return worker.Invocation{}, err
	}
	parsed, err := worker.ParseClass(class)
	if err != nil {
		return worker.Invocation{}, err
	}
	units := 1
	if descs, err := st.Descriptors(ctx, poolID); err == nil {
		for _, d := range descs {
			if d.ID == modelID && d.Units > 0 {
				units = d.Units
			}
		}
	}
	return st.Enqueue(ctx, worker.Invocation{
		ClientRef: clientRef,
		Pool:      poolID,
		Tenant:    tenant,
		Model:     modelID,
		Class:     parsed,
		Units:     units,
		Request:   json.RawMessage(`{}`),
	})
}

func (w *workerWorld) nextRef(prefix string) string {
	w.seq++
	return fmt.Sprintf("%s%d", prefix, w.seq)
}

func workerHeartbeat(ctx context.Context, name string, minutes int) error {
	w := world(ctx).Worker
	if minutes > 0 {
		world(ctx).Core.clock.Advance(time.Duration(minutes) * time.Minute)
	}
	jw := w.workers[name]
	if jw == nil {
		return fmt.Errorf("worker %q has not joined", name)
	}
	st, err := w.open(ctx)
	if err != nil {
		return err
	}
	answer, err := st.Heartbeat(ctx, jw.worker.ID, jw.credential, 0)
	if err != nil {
		return err
	}
	if answer.Credential != "" {
		jw.credential = answer.Credential
		jw.worker.Credential = answer.Credential
	}
	w.lastHeartbeat = answer
	return nil
}

func workerSpillSetting(ctx context.Context, tenant, onOff string, minutes int) error {
	w := world(ctx).Worker
	pool := ""
	for id, spec := range w.pools {
		if string(spec.Tenant) == tenant {
			pool = id
		}
	}
	if pool == "" {
		return fmt.Errorf("tenant %q has no pool", tenant)
	}
	st, err := w.open(ctx)
	if err != nil {
		return err
	}
	return st.SetTenantSettings(ctx, pool, core.TenantID(tenant), worker.TenantSettings{
		Weight:     1,
		SpillOver:  onOff == "on",
		SpillAfter: time.Duration(minutes) * time.Minute,
	})
}

// startPlatformListener hosts the real basamento.worker.v1 service on a
// loopback listener over the world's store. The worker is the side that
// connects; the platform gets no worker address (MODEL-089-01).
func (w *workerWorld) startPlatformListener(ctx context.Context) error {
	st, err := w.open(ctx)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	svc := worker.NewService(st)
	srv := svc.GRPCServer(nil)
	pctx, cancel := context.WithCancel(ctx)
	w.platformAddr = ln.Addr().String()
	w.platformLn = ln
	w.platformSrv = srv
	w.platform = svc
	w.platformCancel = cancel
	go func() { _ = worker.ServeWorkerService(pctx, ln, srv, false) }()
	return nil
}

// dialPlatform opens the worker's own client connection to the listener. The
// worker behind NAT needs no inbound route: it dials out.
func (w *workerWorld) dialPlatform(ctx context.Context) (*worker.Client, error) {
	if w.platformAddr == "" {
		return nil, fmt.Errorf("the platform is not listening for workers")
	}
	tr, err := worker.DialWorkerService(w.platformAddr)
	if err != nil {
		return nil, err
	}
	w.transport = tr
	return worker.NewClient(tr, nil), nil
}

// wireJoin joins over the dial-out transport rather than the in-process one.
func (w *workerWorld) wireJoin(ctx context.Context, name, poolID string, descriptors []worker.Descriptor, capacity int) error {
	if _, err := w.ensurePool(ctx, poolID); err != nil {
		return err
	}
	token := w.poolToken(ctx, poolID)
	client, err := w.dialPlatform(ctx)
	if err != nil {
		return err
	}
	joined, err := client.Join(ctx, worker.JoinRequest{
		PoolToken:       token,
		ProtocolVersion: worker.ProtocolVersion,
		Descriptors:     descriptors,
		Capacity:        capacity,
	})
	if err != nil {
		return err
	}
	w.workers[name] = &joinedWorker{worker: joined, credential: joined.Credential}
	w.workerOrder = append(w.workerOrder, name)
	w.wireClient = client
	return nil
}

// startKitServe starts the real `kit serve` binary with a declared pool and a
// worker listener, then waits for its ready line (MODEL-090-01).
func (w *workerWorld) startKitServe(ctx context.Context, pool string) error {
	dir, err := os.MkdirTemp("", "worker-kit-*")
	if err != nil {
		return err
	}
	w.kitDir = dir
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	httpAddr := free.Addr().String()
	_ = free.Close()
	w.kitSQL = "sqlite:///" + filepath.Join(dir, "kit.db")
	w.kitToken = "bdd-pool-token-0123456789abcdef"
	tokenFile := filepath.Join(dir, "pool.token")
	if err := os.WriteFile(tokenFile, []byte(w.kitToken+"\n"), 0o600); err != nil {
		return err
	}
	manifest := filepath.Join(dir, "cloud-llm.yaml")
	// The remote is a real manifest-driven provider; the scenario only asks
	// the router to choose it, never calls the vendor.
	if err := os.WriteFile(manifest, []byte(`name: cloud-llm
locality: cloud
protocol: single
http:
  base_url: http://127.0.0.1:1
  start:
    method: POST
    path: /start
models:
  - id: sdxl
    caps: [chat]
`), 0o600); err != nil {
		return err
	}
	args := []string{
		"serve", "--profile", "embedded",
		"--listen.addr", httpAddr,
		"--data.dir", dataDir,
		"--sql.url", w.kitSQL,
		"--worker.listen", "127.0.0.1:0",
		"--worker.pool", pool,
		"--worker.pool-token-file", tokenFile,
		"--worker.remote", "cloud-llm",
		"--worker.remote-model", "sdxl",
		"--worker.remote-manifest", manifest,
		"--worker.remote-labels", "region=eu",
	}
	w.kit, err = startKit(dir, args, nil)
	if err != nil {
		return err
	}
	line, err := w.kit.awaitReady()
	if err != nil {
		return err
	}
	w.kitReady = line
	w.kitHTTP = httpAddr
	w.kitAddr = readyField(line, "worker.listen")
	if w.kitAddr == "" {
		return fmt.Errorf("ready line %q reports no worker listener", line)
	}
	return nil
}

// openKitStore opens the store kit serve created, so the scenario can read the
// pool and the queue over the same database.
func (w *workerWorld) openKitStore(ctx context.Context) (*worker.Store, error) {
	if w.kitStore != nil {
		return w.kitStore, nil
	}
	db, err := psql.Open(ctx, w.kitSQL, credentials.Migrations())
	if err != nil {
		return nil, err
	}
	st, err := worker.Open(ctx, db, worker.Options{Clock: world(ctx).Core.clock, IDs: &coretest.SeqIds{}})
	if err != nil {
		return nil, err
	}
	w.kitStore = st
	return st, nil
}

// kitComposition asks kit serve what its composed Invoker and Router answer.
// The scenario never builds its own Router or Invoker over the database.
func (w *workerWorld) kitComposition(ctx context.Context, values url.Values) (kitComposition, error) {
	var view kitComposition
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+w.kitHTTP+"/worker/composition?"+values.Encode(), nil)
	if err != nil {
		return view, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return view, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return view, fmt.Errorf("kit serve /worker/composition returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		return view, err
	}
	return view, nil
}

// kitComposition mirrors the JSON kit serve reports for its worker side.
type kitComposition struct {
	Invoker string `json:"invoker"`
	Pool    string `json:"pool"`
	Route   struct {
		Model         string `json:"model"`
		Target        string `json:"target"`
		RemoteInvoker string `json:"remote_invoker"`
	} `json:"route"`
	Result *struct {
		State string `json:"state"`
	} `json:"result"`
}

// finishOverKit queues one invocation in kit serve's store and has the worker
// finish it over the dial-out listener.
func (w *workerWorld) finishOverKit(ctx context.Context, name, ref, modelID string) error {
	st, err := w.openKitStore(ctx)
	if err != nil {
		return err
	}
	if _, err := st.Enqueue(ctx, worker.Invocation{
		ClientRef: ref, Pool: "acme-gpu", Tenant: core.TenantID("acme"), Model: modelID,
		Class: worker.ClassNormal, Units: 1, Request: json.RawMessage(`{}`),
	}); err != nil {
		return err
	}
	tr, err := worker.DialWorkerService(w.kitAddr)
	if err != nil {
		return err
	}
	w.transport = tr
	client := worker.NewClient(tr, nil)
	joined, err := client.Join(ctx, worker.JoinRequest{
		PoolToken: w.kitToken, ProtocolVersion: worker.ProtocolVersion,
		Descriptors: []worker.Descriptor{descriptorForModel(modelID, 1, false)}, Capacity: 4,
	})
	if err != nil {
		return err
	}
	w.workers[name] = &joinedWorker{worker: joined, credential: joined.Credential}
	leased, err := client.LeaseOnce(ctx, 4, nil, time.Hour, 0)
	if err != nil {
		return err
	}
	if len(leased) != 1 || leased[0].ClientRef != ref {
		return fmt.Errorf("worker %q leased %v, want invocation %q", name, refsOf(leased), ref)
	}
	w.lastLease = leased
	attempt := leased[0].Attempt
	if err := client.Starting(ctx, ref, attempt); err != nil {
		return err
	}
	if err := client.Finish(ctx, ref, string(model.StateSucceeded), json.RawMessage(`{}`), "", 0, attempt); err != nil {
		return err
	}
	return client.Leave(ctx)
}

func registerWorker(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		world(ctx).Worker = newWorkerWorld()
		return ctx, nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		if w := world(ctx).Worker; w != nil {
			w.teardown()
		}
		return ctx, nil
	})

	// --- pools and tenants ---
	sc.Step(`^tenant "([^"]*)" has pool "([^"]*)" and tenant "([^"]*)" has pool "([^"]*)"$`, func(ctx context.Context, t1, p1, t2, p2 string) error {
		w := world(ctx).Worker
		for _, p := range []struct{ tenant, id string }{{t1, p1}, {t2, p2}} {
			if _, err := w.ensurePool(ctx, p.id); err != nil {
				return err
			}
			spec := w.pools[p.id]
			spec.Tenant = core.TenantID(p.tenant)
			w.pools[p.id] = spec
		}
		return nil
	})
	sc.Step(`^tenant "([^"]*)" has pool "([^"]*)" serving model "([^"]*)"$`, func(ctx context.Context, tenant, poolID, modelID string) error {
		return workerDeclare(ctx, tenant, poolID, modelID, -1)
	})
	sc.Step(`^tenant "([^"]*)" has pool "([^"]*)" serving model "([^"]*)" priced at (\d+) milli-points, with no present worker$`, workerDeclare)
	sc.Step(`^pool "([^"]*)" serves model "([^"]*)" and has no present worker$`, func(ctx context.Context, poolID, modelID string) error {
		return workerDeclare(ctx, "", poolID, modelID, -1)
	})
	sc.Step(`^pool "([^"]*)" has an ageing period of (\d+) minutes?$`, func(ctx context.Context, poolID string, minutes int) error {
		w := world(ctx).Worker
		w.ageing = time.Duration(minutes) * time.Minute
		spec, err := w.ensurePool(ctx, poolID)
		if err != nil {
			return err
		}
		spec.Ageing = w.ageing
		w.pools[poolID] = spec
		st, err := w.open(ctx)
		if err != nil {
			return err
		}
		return st.SetAgeing(ctx, poolID, w.ageing)
	})
	sc.Step(`^pool "([^"]*)" has a pool token that was revoked$`, func(ctx context.Context, poolID string) error {
		w := world(ctx).Worker
		if _, err := w.ensurePool(ctx, poolID); err != nil {
			return err
		}
		token := w.poolToken(ctx, poolID)
		st, err := w.open(ctx)
		if err != nil {
			return err
		}
		if err := st.RevokeToken(ctx, poolID); err != nil {
			return err
		}
		w.revoked[poolID] = token
		return nil
	})
	sc.Step(`^tenants "([^"]*)" and "([^"]*)" each have weight (\d+) in the shared pool$`, func(ctx context.Context, t1, t2 string, weight int) error {
		w := world(ctx).Worker
		if _, err := w.ensurePool(ctx, "shared"); err != nil {
			return err
		}
		st, err := w.open(ctx)
		if err != nil {
			return err
		}
		for _, tenant := range []string{t1, t2} {
			if err := st.SetTenantSettings(ctx, "shared", core.TenantID(tenant), worker.TenantSettings{Weight: weight}); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^tenant "([^"]*)" has a concurrency limit of (\d+) in the shared pool$`, func(ctx context.Context, tenant string, limit int) error {
		w := world(ctx).Worker
		if _, err := w.ensurePool(ctx, "shared"); err != nil {
			return err
		}
		st, err := w.open(ctx)
		if err != nil {
			return err
		}
		return st.SetTenantSettings(ctx, "shared", core.TenantID(tenant), worker.TenantSettings{Weight: 1, Concurrency: limit})
	})
	sc.Step(`^tenant "([^"]*)" has spill-over (on|off)$`, func(ctx context.Context, tenant, onOff string) error {
		return workerSpillSetting(ctx, tenant, onOff, 0)
	})
	sc.Step(`^tenant "([^"]*)" has spill-over (on|off) after (\d+) minutes$`, workerSpillSetting)
	sc.Step(`^tenant "([^"]*)" has pool "([^"]*)" serving model "([^"]*)" with no present worker$`, func(ctx context.Context, tenant, poolID, modelID string) error {
		return workerDeclare(ctx, tenant, poolID, modelID, -1)
	})
	// --- workers ---
	sc.Step(`^worker "([^"]*)" joined pool "([^"]*)" declaring model "([^"]*)"$`, func(ctx context.Context, name, poolID, modelID string) error {
		return world(ctx).Worker.join(ctx, name, poolID, []worker.Descriptor{descriptorForModel(modelID, 1, false)})
	})
	sc.Step(`^worker "([^"]*)" joined pool "([^"]*)" declaring model "([^"]*)" at (\d+) units?$`, func(ctx context.Context, name, poolID, modelID string, units int) error {
		return world(ctx).Worker.join(ctx, name, poolID, []worker.Descriptor{descriptorForModel(modelID, units, false)})
	})
	sc.Step(`^worker "([^"]*)" joined pool "([^"]*)" declaring restart-safe model "([^"]*)"$`, func(ctx context.Context, name, poolID, modelID string) error {
		return world(ctx).Worker.join(ctx, name, poolID, []worker.Descriptor{descriptorForModel(modelID, 1, true)})
	})
	sc.Step(`^worker "([^"]*)" joined the shared pool declaring model "([^"]*)"$`, func(ctx context.Context, name, modelID string) error {
		return world(ctx).Worker.join(ctx, name, "shared", []worker.Descriptor{descriptorForModel(modelID, 1, false)})
	})
	sc.Step(`^worker "([^"]*)" joined pool "([^"]*)" declaring model "([^"]*)" at (\d+) units and model "([^"]*)" at (\d+) units?$`, func(ctx context.Context, name, poolID, m1 string, u1 int, m2 string, u2 int) error {
		return world(ctx).Worker.join(ctx, name, poolID, []worker.Descriptor{
			descriptorForModel(m1, u1, false), descriptorForModel(m2, u2, false),
		})
	})
	sc.Step(`^workers "([^"]*)" and "([^"]*)" joined pool "([^"]*)" declaring model "([^"]*)"$`, func(ctx context.Context, a, b, poolID, modelID string) error {
		w := world(ctx).Worker
		for _, name := range []string{a, b} {
			if err := w.join(ctx, name, poolID, []worker.Descriptor{descriptorForModel(modelID, 1, false)}); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^workers "([^"]*)" and "([^"]*)" joined pool "([^"]*)" declaring restart-safe model "([^"]*)"$`, func(ctx context.Context, a, b, poolID, modelID string) error {
		w := world(ctx).Worker
		for _, name := range []string{a, b} {
			if err := w.join(ctx, name, poolID, []worker.Descriptor{descriptorForModel(modelID, 1, true)}); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^workers "([^"]*)", "([^"]*)" and "([^"]*)" joined pool "([^"]*)" declaring restart-safe model "([^"]*)"$`, func(ctx context.Context, a, b, c, poolID, modelID string) error {
		w := world(ctx).Worker
		for _, name := range []string{a, b, c} {
			if err := w.join(ctx, name, poolID, []worker.Descriptor{descriptorForModel(modelID, 1, true)}); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^worker "([^"]*)" leaves$`, func(ctx context.Context, name string) error {
		w := world(ctx).Worker
		jw := w.workers[name]
		if jw == nil {
			return fmt.Errorf("worker %q has not joined", name)
		}
		st, err := w.open(ctx)
		if err != nil {
			return err
		}
		return st.Leave(ctx, jw.worker.ID, jw.credential)
	})
	sc.Step(`^worker "([^"]*)" is (draining|paused)$`, func(ctx context.Context, name, state string) error {
		w := world(ctx).Worker
		jw := w.workers[name]
		if jw == nil {
			return fmt.Errorf("worker %q has not joined", name)
		}
		st, err := w.open(ctx)
		if err != nil {
			return err
		}
		return st.SetState(ctx, jw.worker.ID, worker.State(state))
	})
	sc.Step(`^an operator removes worker "([^"]*)"$`, func(ctx context.Context, name string) error {
		w := world(ctx).Worker
		jw := w.workers[name]
		if jw == nil {
			return fmt.Errorf("worker %q has not joined", name)
		}
		st, err := w.open(ctx)
		if err != nil {
			return err
		}
		return st.Remove(ctx, jw.worker.ID)
	})
	sc.Step(`^worker "([^"]*)" joins pool "([^"]*)" declaring model "([^"]*)" at (\d+) units?$`, func(ctx context.Context, name, poolID, modelID string, units int) error {
		w := world(ctx).Worker
		w.lastErr = nil
		if err := w.join(ctx, name, poolID, []worker.Descriptor{descriptorForModel(modelID, units, false)}); err != nil {
			w.lastErr = err
		}
		return nil
	})
	sc.Step(`^a worker joins pool "([^"]*)" with that token$`, func(ctx context.Context, poolID string) error {
		w := world(ctx).Worker
		st, err := w.open(ctx)
		if err != nil {
			return err
		}
		token := w.revoked[poolID]
		_, err = st.Join(ctx, worker.JoinRequest{
			PoolToken:       token,
			ProtocolVersion: worker.ProtocolVersion,
			Descriptors:     []worker.Descriptor{descriptorForModel("sdxl", 1, false)},
			Capacity:        4,
		})
		w.lastErr = err
		return nil
	})

	// --- queueing ---
	sc.Step(`^tenant "([^"]*)" queued 1 invocation of "([^"]*)" for pool "([^"]*)"$`, func(ctx context.Context, tenant, modelID, poolID string) error {
		w := world(ctx).Worker
		_, err := w.queued(ctx, core.TenantID(tenant), poolID, modelID, "", w.nextRef("q"))
		return err
	})
	sc.Step(`^tenant "([^"]*)" queued 1 invocation of "([^"]*)" and 1 invocation of "([^"]*)" for pool "([^"]*)"$`, func(ctx context.Context, tenant, m1, m2, poolID string) error {
		w := world(ctx).Worker
		if _, err := w.queued(ctx, core.TenantID(tenant), poolID, m1, "", w.nextRef("q")); err != nil {
			return err
		}
		_, err := w.queued(ctx, core.TenantID(tenant), poolID, m2, "", w.nextRef("q"))
		return err
	})
	sc.Step(`^tenant "([^"]*)" queued invocation "([^"]*)" of "([^"]*)" with class "([^"]*)"$`, func(ctx context.Context, tenant, ref, modelID, class string) error {
		_, err := world(ctx).Worker.queued(ctx, core.TenantID(tenant), workerPoolForTenant(ctx, tenant), modelID, class, ref)
		return err
	})
	sc.Step(`^(\d+) minutes? later tenant "([^"]*)" queued invocation "([^"]*)" of "([^"]*)" with class "([^"]*)"$`, func(ctx context.Context, minutes int, tenant, ref, modelID, class string) error {
		world(ctx).Core.clock.Advance(time.Duration(minutes) * time.Minute)
		_, err := world(ctx).Worker.queued(ctx, core.TenantID(tenant), workerPoolForTenant(ctx, tenant), modelID, class, ref)
		return err
	})
	sc.Step(`^tenant "([^"]*)" has (\d+) invocations of "([^"]*)" leased and queued invocation "([^"]*)" of "([^"]*)"$`, func(ctx context.Context, tenant string, n int, modelID, ref, model2 string) error {
		w := world(ctx).Worker
		pool := "shared"
		for i := 0; i < n; i++ {
			if _, err := w.queued(ctx, core.TenantID(tenant), pool, modelID, "", w.nextRef("leased")); err != nil {
				return err
			}
		}
		if err := w.leaseFor(ctx, w.workerOrder[0], n); err != nil {
			return err
		}
		_, err := w.queued(ctx, core.TenantID(tenant), pool, model2, "", ref)
		return err
	})
	sc.Step(`^tenant "([^"]*)" has (\d+) invocations of "([^"]*)" leased and (\d+) invocation of "([^"]*)" waiting$`, func(ctx context.Context, tenant string, n int, modelID string, waiting int, model2 string) error {
		w := world(ctx).Worker
		for i := 0; i < n+waiting; i++ {
			if _, err := w.queued(ctx, core.TenantID(tenant), "shared", modelID, "", w.nextRef("q")); err != nil {
				return err
			}
		}
		return w.leaseFor(ctx, w.workerOrder[0], n)
	})

	// --- runs, reservations and outcomes ---
	sc.Step(`^a run invoked "([^"]*)" with a queue timeout of (\d+) minutes and (\d+) milli-points reserved$`, func(ctx context.Context, modelID string, minutes int, reserved int64) error {
		w := world(ctx).Worker
		pool := workerPoolForModel(ctx, modelID)
		return w.startRun(ctx, core.TenantID("acme"), pool, modelID, time.Duration(minutes)*time.Minute, reserved)
	})
	sc.Step(`^a run invoked "([^"]*)" with (\d+) milli-points reserved$`, func(ctx context.Context, modelID string, reserved int64) error {
		w := world(ctx).Worker
		return w.startRun(ctx, core.TenantID("acme"), workerPoolForModel(ctx, modelID), modelID, 0, reserved)
	})
	sc.Step(`^a run invoked "([^"]*)"$`, func(ctx context.Context, modelID string) error {
		w := world(ctx).Worker
		return w.startRun(ctx, core.TenantID("acme"), workerPoolForModel(ctx, modelID), modelID, 0, 0)
	})
	sc.Step(`^a run of tenant "([^"]*)" invoked "([^"]*)" with a queue timeout of (\d+) minutes$`, func(ctx context.Context, tenant, modelID string, minutes int) error {
		w := world(ctx).Worker
		return w.startRun(ctx, core.TenantID(tenant), workerPoolForModel(ctx, modelID), modelID, time.Duration(minutes)*time.Minute, 0)
	})
	sc.Step(`^a run of tenant "([^"]*)" invokes "([^"]*)"$`, func(ctx context.Context, tenant, modelID string) error {
		w := world(ctx).Worker
		st, err := w.open(ctx)
		if err != nil {
			return err
		}
		router := worker.NewRouter(st, w.cloudRemotes()...)
		dest, err := router.Select(worker.WithLabels(ctx, nil), core.TenantID(tenant), modelID)
		if err != nil {
			return err
		}
		if dest.IsRemote {
			return w.startRunRemote(ctx, core.TenantID(tenant), modelID, dest.Remote)
		}
		return w.startRun(ctx, core.TenantID(tenant), dest.Pool, modelID, 0, 0)
	})
	sc.Step(`^a run of tenant "([^"]*)" invoked "([^"]*)"$`, func(ctx context.Context, tenant, modelID string) error {
		w := world(ctx).Worker
		return w.startRun(ctx, core.TenantID(tenant), workerPoolForModel(ctx, modelID), modelID, 0, 0)
	})
	sc.Step(`^the lease of worker "([^"]*)" expires$`, func(ctx context.Context, name string) error {
		w := world(ctx).Worker
		return w.expireLeases(ctx, []string{name})
	})
	sc.Step(`^the lease of worker "([^"]*)" expires and the grace period passes$`, func(ctx context.Context, name string) error {
		w := world(ctx).Worker
		if err := w.expireLeases(ctx, []string{name}); err != nil {
			return err
		}
		return w.passGrace(ctx)
	})
	sc.Step(`^the lease of worker "([^"]*)" expired, so invocation "([^"]*)" is in doubt$`, func(ctx context.Context, name, ref string) error {
		w := world(ctx).Worker
		if err := w.expireLeases(ctx, []string{name}); err != nil {
			return err
		}
		inv, err := w.invocation(ctx, ref)
		if err != nil {
			return err
		}
		if !inv.InDoubt {
			return fmt.Errorf("invocation %q is not in doubt (state %s)", ref, inv.State)
		}
		return nil
	})
	sc.Step(`^the leases of workers "([^"]*)" and "([^"]*)" on that invocation expired in turn$`, func(ctx context.Context, a, b string) error {
		w := world(ctx).Worker
		for _, name := range []string{a, b} {
			if err := w.leaseStartExpire(ctx, name); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the lease of worker "([^"]*)" on that invocation expires$`, func(ctx context.Context, name string) error {
		return world(ctx).Worker.leaseStartExpire(ctx, name)
	})
	sc.Step(`^worker "([^"]*)" resumes invocation "([^"]*)" within the grace period$`, func(ctx context.Context, name, ref string) error {
		w := world(ctx).Worker
		jw := w.workers[name]
		if jw == nil {
			return fmt.Errorf("worker %q has not joined", name)
		}
		st, err := w.open(ctx)
		if err != nil {
			return err
		}
		attempt, err := reportAttempt(ctx, st, ref)
		if err != nil {
			return err
		}
		return st.Resume(ctx, jw.worker.ID, jw.credential, ref, attempt)
	})
	sc.Step(`^worker "([^"]*)" reports success for invocation "([^"]*)"$`, func(ctx context.Context, name, ref string) error {
		w := world(ctx).Worker
		jw := w.workers[name]
		if jw == nil {
			return fmt.Errorf("worker %q has not joined", name)
		}
		st, err := w.open(ctx)
		if err != nil {
			return err
		}
		attempt, err := reportAttempt(ctx, st, ref)
		if err != nil {
			return err
		}
		w.lastErr = st.Finish(ctx, jw.worker.ID, jw.credential, ref, "succeeded", json.RawMessage(`{}`), "", 0, attempt)
		return nil
	})
	sc.Step(`^invocation "([^"]*)" of "([^"]*)" ended as "([^"]*)" with code "([^"]*)" while worker "([^"]*)" was away$`, func(ctx context.Context, ref, modelID, state, code, name string) error {
		w := world(ctx).Worker
		return w.endWhileAway(ctx, ref, modelID, state, code, name)
	})
	sc.Step(`^the reservation of (\d+) milli-points is released$`, func(ctx context.Context, want int64) error {
		w := world(ctx).Worker
		if err := w.settle(ctx); err != nil {
			return err
		}
		if w.run == nil {
			return fmt.Errorf("no run is in flight")
		}
		if w.run.charged != 0 {
			return fmt.Errorf("the run was charged %d milli-points, want the %d released", w.run.charged, want)
		}
		inv, err := w.invocation(ctx, w.run.clientRef)
		if err != nil {
			return err
		}
		if inv.State != "failed" && inv.State != "cancelled" {
			return fmt.Errorf("the invocation is %s, want its reservation released", inv.State)
		}
		return nil
	})
	sc.Step(`^worker "([^"]*)" sends a heartbeat$`, func(ctx context.Context, name string) error {
		return workerHeartbeat(ctx, name, 0)
	})
	sc.Step(`^worker "([^"]*)" sends a heartbeat (\d+) minutes? later$`, workerHeartbeat)

	sc.Step(`^leases last (\d+) minutes?$`, func(ctx context.Context, minutes int) error {
		w := world(ctx).Worker
		w.lease = time.Duration(minutes) * time.Minute
		w.leaseSet = true
		return nil
	})
	sc.Step(`^the platform is down for (\d+) minutes and starts again$`, func(ctx context.Context, minutes int) error {
		world(ctx).Core.clock.Advance(time.Duration(minutes) * time.Minute)
		st, err := world(ctx).Worker.open(ctx)
		if err != nil {
			return err
		}
		return st.Recover(ctx)
	})

	sc.Step(`^worker "([^"]*)" leased invocation "([^"]*)" of "([^"]*)" and did not report starting it$`, func(ctx context.Context, name, ref, modelID string) error {
		w := world(ctx).Worker
		if _, err := w.queued(ctx, core.TenantID("acme"), workerPoolForModel(ctx, modelID), modelID, "", ref); err != nil {
			return err
		}
		return w.leaseFor(ctx, name, 1)
	})
	sc.Step(`^worker "([^"]*)" leased invocation "([^"]*)" of "([^"]*)" and reported starting it$`, func(ctx context.Context, name, ref, modelID string) error {
		w := world(ctx).Worker
		if _, err := w.queued(ctx, core.TenantID("acme"), workerPoolForModel(ctx, modelID), modelID, "", ref); err != nil {
			return err
		}
		if err := w.leaseFor(ctx, name, 1); err != nil {
			return err
		}
		w.rememberLease(ref)
		return w.start(ctx, name, ref)
	})
	sc.Step(`^worker "([^"]*)" leased that invocation and reported starting it$`, func(ctx context.Context, name string) error {
		w := world(ctx).Worker
		if w.run == nil {
			return fmt.Errorf("no run is in flight")
		}
		if err := w.leaseFor(ctx, name, 1); err != nil {
			return err
		}
		w.rememberLease(w.run.clientRef)
		return w.start(ctx, name, w.run.clientRef)
	})
	sc.Step(`^(\d+) minutes? later tenant "([^"]*)" queued invocation "([^"]*)" of "([^"]*)"$`, func(ctx context.Context, minutes int, tenant, ref, modelID string) error {
		world(ctx).Core.clock.Advance(time.Duration(minutes) * time.Minute)
		_, err := world(ctx).Worker.queued(ctx, core.TenantID(tenant), workerPoolForTenant(ctx, tenant), modelID, "", ref)
		return err
	})
	sc.Step(`^tenant "([^"]*)" queued invocation "([^"]*)" of "([^"]*)"$`, func(ctx context.Context, tenant, ref, modelID string) error {
		_, err := world(ctx).Worker.queued(ctx, core.TenantID(tenant), workerPoolForTenant(ctx, tenant), modelID, "", ref)
		return err
	})
	sc.Step(`^a run invoked "([^"]*)" and worker "([^"]*)" leased and started the invocation$`, func(ctx context.Context, modelID, name string) error {
		w := world(ctx).Worker
		if err := w.startRun(ctx, core.TenantID("acme"), workerPoolForModel(ctx, modelID), modelID, 0, 0); err != nil {
			return err
		}
		if err := w.leaseFor(ctx, name, 1); err != nil {
			return err
		}
		w.rememberLease(w.run.clientRef)
		return w.start(ctx, name, w.run.clientRef)
	})
	// --- assertions ---
	sc.Step(`^worker "([^"]*)" asks for work with (\d+) free units?$`, func(ctx context.Context, name string, free int) error {
		return world(ctx).Worker.leaseFor(ctx, name, free)
	})
	sc.Step(`^worker "([^"]*)" receives (\d+) invocations?$`, func(ctx context.Context, name string, want int) error {
		w := world(ctx).Worker
		if err := w.settle(ctx); err != nil {
			return err
		}
		if len(w.lastLease) != want {
			return fmt.Errorf("worker %q received %d invocations, want %d", name, len(w.lastLease), want)
		}
		return nil
	})
	sc.Step(`^worker "([^"]*)" receives (\d+) invocation, of model "([^"]*)"$`, func(ctx context.Context, name string, want int, modelID string) error {
		w := world(ctx).Worker
		if len(w.lastLease) != want {
			return fmt.Errorf("worker %q received %d invocations, want %d", name, len(w.lastLease), want)
		}
		if want > 0 && w.lastLease[0].Model != modelID {
			return fmt.Errorf("worker %q received model %q, want %q", name, w.lastLease[0].Model, modelID)
		}
		return nil
	})
	sc.Step(`^worker "([^"]*)" receives invocation "([^"]*)"$`, func(ctx context.Context, name, ref string) error {
		w := world(ctx).Worker
		if err := w.settle(ctx); err != nil {
			return err
		}
		if len(w.lastLease) != 1 || w.lastLease[0].ClientRef != ref {
			return fmt.Errorf("worker %q received %v, want invocation %q", name, refsOf(w.lastLease), ref)
		}
		return nil
	})
	sc.Step(`^worker "([^"]*)" receives invocation "([^"]*)" with the same client ref as worker "([^"]*)" did$`, func(ctx context.Context, name, ref, first string) error {
		w := world(ctx).Worker
		if err := w.settle(ctx); err != nil {
			return err
		}
		if len(w.lastLease) != 1 || w.lastLease[0].ClientRef != ref {
			return fmt.Errorf("worker %q received %v, want invocation %q", name, refsOf(w.lastLease), ref)
		}
		return nil
	})
	sc.Step(`^(\d+) invocations? waits? in pool "([^"]*)"$`, func(ctx context.Context, want int, poolID string) error {
		w := world(ctx).Worker
		if err := w.settle(ctx); err != nil {
			return err
		}
		st, err := w.open(ctx)
		if err != nil {
			return err
		}
		got, err := st.Waiting(ctx, poolID)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("pool %q has %d invocations waiting, want %d", poolID, got, want)
		}
		return nil
	})
	sc.Step(`^invocation "([^"]*)" still waits$`, func(ctx context.Context, ref string) error {
		w := world(ctx).Worker
		inv, err := w.invocation(ctx, ref)
		if err != nil {
			return err
		}
		if inv.State != "waiting" {
			return fmt.Errorf("invocation %q is %s, want waiting", ref, inv.State)
		}
		return nil
	})
	sc.Step(`^the catalog lists "([^"]*)" with availability "([^"]*)"$`, func(ctx context.Context, modelID, want string) error {
		w := world(ctx).Worker
		st, err := w.open(ctx)
		if err != nil {
			return err
		}
		pool := workerPoolForModel(ctx, modelID)
		got, err := st.Availability(ctx, pool, modelID)
		if err != nil {
			return err
		}
		if string(got) != want {
			return fmt.Errorf("availability of %q in pool %q is %q, want %q", modelID, pool, got, want)
		}
		return nil
	})
	sc.Step(`^followers of invocation "([^"]*)" receive a "([^"]*)" event$`, func(ctx context.Context, ref, kind string) error {
		w := world(ctx).Worker
		st, err := w.open(ctx)
		if err != nil {
			return err
		}
		events, err := st.EventsAfter(ctx, ref, 0)
		if err != nil {
			return err
		}
		for _, e := range events {
			if e.Kind == kind {
				return nil
			}
		}
		return fmt.Errorf("invocation %q has no %q event", ref, kind)
	})
	sc.Step(`^the join is refused with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		return workerCode(world(ctx).Worker.lastErr, code)
	})
	sc.Step(`^the report is refused with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		return workerCode(world(ctx).Worker.lastErr, code)
	})
	sc.Step(`^invocation "([^"]*)" remains "([^"]*)" with code "([^"]*)"$`, func(ctx context.Context, ref, state, code string) error {
		w := world(ctx).Worker
		inv, err := w.invocation(ctx, ref)
		if err != nil {
			return err
		}
		if inv.State != state || string(inv.Code) != code {
			return fmt.Errorf("invocation %q is %s with code %q, want %s with code %q", ref, inv.State, inv.Code, state, code)
		}
		return nil
	})
	sc.Step(`^invocation "([^"]*)" ends as "([^"]*)"$`, func(ctx context.Context, ref, state string) error {
		w := world(ctx).Worker
		inv, err := w.invocation(ctx, ref)
		if err != nil {
			return err
		}
		if inv.State != state {
			return fmt.Errorf("invocation %q is %s, want %s", ref, inv.State, state)
		}
		return nil
	})
	sc.Step(`^invocation "([^"]*)" is still leased to worker "([^"]*)"$`, func(ctx context.Context, ref, name string) error {
		w := world(ctx).Worker
		inv, err := w.invocation(ctx, ref)
		if err != nil {
			return err
		}
		jw := w.workers[name]
		if jw == nil {
			return fmt.Errorf("worker %q has not joined", name)
		}
		if inv.State != "leased" || inv.WorkerID != jw.worker.ID {
			return fmt.Errorf("invocation %q is %s on worker %q, want leased to %q", ref, inv.State, inv.WorkerID, jw.worker.ID)
		}
		return nil
	})
	sc.Step(`^the heartbeat answer tells worker "([^"]*)" to cancel that invocation$`, func(ctx context.Context, name string) error {
		w := world(ctx).Worker
		if w.run == nil {
			return fmt.Errorf("no run is in flight")
		}
		for _, ref := range w.lastHeartbeat.Cancel {
			if ref == w.run.clientRef {
				return nil
			}
		}
		return fmt.Errorf("the heartbeat answer cancels %v, want %q", w.lastHeartbeat.Cancel, w.run.clientRef)
	})
	sc.Step(`^the provider of worker "([^"]*)" accepted (\d+) invocations?$`, func(ctx context.Context, name string, want int) error {
		jw := world(ctx).Worker.workers[name]
		if jw == nil {
			return fmt.Errorf("worker %q has not joined", name)
		}
		if jw.accepted != want {
			return fmt.Errorf("the provider of worker %q accepted %d invocations, want %d", name, jw.accepted, want)
		}
		return nil
	})
	sc.Step(`^a cloud provider also serves model "([^"]*)"$`, func(ctx context.Context, modelID string) error {
		return world(ctx).Worker.setCloud(modelID, 0)
	})
	sc.Step(`^a cloud provider also serves model "([^"]*)" at an estimated (\d+) milli-points$`, func(ctx context.Context, modelID string, estimate int64) error {
		return world(ctx).Worker.setCloud(modelID, estimate)
	})
	sc.Step(`^the cloud provider accepted (\d+) invocations?$`, func(ctx context.Context, want int) error {
		c := world(ctx).Worker.cloud
		if c == nil {
			return fmt.Errorf("no cloud provider is configured")
		}
		if c.accepted != want {
			return fmt.Errorf("the cloud provider accepted %d invocations, want %d", c.accepted, want)
		}
		return nil
	})
	sc.Step(`^the cloud provider accepted (\d+) invocation with the same client ref$`, func(ctx context.Context, want int) error {
		w := world(ctx).Worker
		c := w.cloud
		if c == nil {
			return fmt.Errorf("no cloud provider is configured")
		}
		if c.accepted != want {
			return fmt.Errorf("the cloud provider accepted %d invocations, want %d", c.accepted, want)
		}
		if w.run == nil || len(c.refs) == 0 || c.refs[0] != w.run.clientRef {
			return fmt.Errorf("the cloud provider accepted refs %v, want %q", c.refs, runRef(w.run))
		}
		return nil
	})
	sc.Step(`^(\d+) milli-points were reserved before that start$`, func(ctx context.Context, want int64) error {
		w := world(ctx).Worker
		if len(w.lastSpill) == 0 {
			return fmt.Errorf("no invocation spilled")
		}
		if got := w.lastSpill[len(w.lastSpill)-1].Reserved; got != want {
			return fmt.Errorf("%d milli-points were reserved before the start, want %d", got, want)
		}
		return nil
	})
	sc.Step(`^the invocation is queued for pool "([^"]*)"$`, func(ctx context.Context, poolID string) error {
		w := world(ctx).Worker
		if w.run == nil {
			return fmt.Errorf("no run is in flight")
		}
		inv, err := w.invocation(ctx, w.run.clientRef)
		if err != nil {
			return err
		}
		if inv.Pool != poolID {
			return fmt.Errorf("the invocation is queued for pool %q, want %q", inv.Pool, poolID)
		}
		return nil
	})
	sc.Step(`^an audit event records the refused join to pool "([^"]*)"$`, func(ctx context.Context, poolID string) error {
		for _, e := range world(ctx).Worker.audit.events {
			if e.Action == "worker.refuse" && e.Resource == poolID {
				return nil
			}
		}
		return fmt.Errorf("no audit event records a refused join to pool %q", poolID)
	})

	// --- the dial-out transport (MODEL-089-01, MODEL-090-01) ---
	sc.Step(`^a platform listening for workers on a loopback listener and worker "([^"]*)" has no inbound route$`, func(ctx context.Context, name string) error {
		return world(ctx).Worker.startPlatformListener(ctx)
	})
	sc.Step(`^worker "([^"]*)" joined pool "([^"]*)" with its pool token, declaring model "([^"]*)"$`, func(ctx context.Context, name, poolID, modelID string) error {
		return world(ctx).Worker.wireJoin(ctx, name, poolID, []worker.Descriptor{descriptorForModel(modelID, 1, false)}, 4)
	})
	sc.Step(`^worker "([^"]*)" dials out to WorkerService and asks for work with (\d+) free units?$`, func(ctx context.Context, name string, free int) error {
		w := world(ctx).Worker
		client := w.wireClient
		if client == nil {
			var err error
			client, err = w.dialPlatform(ctx)
			if err != nil {
				return err
			}
		}
		leased, err := client.LeaseOnce(ctx, free, nil, time.Hour, 0)
		if err != nil {
			return err
		}
		w.lastLease = leased
		w.workedOverWire = true
		return nil
	})
	sc.Step(`^the platform attempted 0 outbound dials to worker "([^"]*)"$`, func(ctx context.Context, name string) error {
		w := world(ctx).Worker
		if w.platform == nil || !w.workedOverWire {
			return fmt.Errorf("the worker was not served over the platform's listener")
		}
		if dials := w.platform.OutboundDials(); dials != 0 {
			return fmt.Errorf("the platform dialled worker %q %d times, want 0", name, dials)
		}
		return nil
	})
	sc.Step(`^kit serve started with pool "([^"]*)" declared and worker "([^"]*)" configured, listening for workers on a loopback listener$`, func(ctx context.Context, pool, name string) error {
		return world(ctx).Worker.startKitServe(ctx, pool)
	})
	sc.Step(`^worker "([^"]*)" dials out to WorkerService and finishes invocation "([^"]*)" of "([^"]*)"$`, func(ctx context.Context, name, ref, modelID string) error {
		return world(ctx).Worker.finishOverKit(ctx, name, ref, modelID)
	})
	sc.Step(`^pool "([^"]*)" is registered$`, func(ctx context.Context, pool string) error {
		w := world(ctx).Worker
		if got := readyField(w.kitReady, "worker.pool"); got != pool {
			return fmt.Errorf("ready line reports pool %q, want %q", got, pool)
		}
		st, err := w.openKitStore(ctx)
		if err != nil {
			return err
		}
		if _, err := st.Pool(ctx, pool); err != nil {
			return fmt.Errorf("pool %q is not registered: %w", pool, err)
		}
		return nil
	})
	sc.Step(`^the model\.Invoker accounted invocation "([^"]*)"$`, func(ctx context.Context, ref string) error {
		w := world(ctx).Worker
		view, err := w.kitComposition(ctx, url.Values{
			"tenant": {"acme"}, "model": {"sdxl"}, "ref": {ref},
		})
		if err != nil {
			return err
		}
		if view.Invoker != "worker:acme-gpu" {
			return fmt.Errorf("kit serve reports invoker %q, want worker:acme-gpu", view.Invoker)
		}
		if view.Result == nil {
			return fmt.Errorf("kit serve's model.Invoker did not account invocation %q", ref)
		}
		if view.Result.State != string(model.StateSucceeded) {
			return fmt.Errorf("invocation %q is %s, want succeeded", ref, view.Result.State)
		}
		return nil
	})
	sc.Step(`^remote "([^"]*)" serving "([^"]*)" is assigned so the router can choose between worker "([^"]*)" and remote "([^"]*)"$`, func(ctx context.Context, remoteName, modelID, workerName, remoteAgain string) error {
		w := world(ctx).Worker
		if got := readyField(w.kitReady, "worker.remote"); got != remoteName {
			return fmt.Errorf("ready line reports remote %q, want %q", got, remoteName)
		}
		if got := readyField(w.kitReady, "worker.remote-model"); got != modelID {
			return fmt.Errorf("ready line reports remote model %q, want %q", got, modelID)
		}
		if got := readyField(w.kitReady, "worker.routes"); got != modelID+"=remote:"+remoteName {
			return fmt.Errorf("ready line reports routes %q, want %q", got, modelID+"=remote:"+remoteName)
		}
		plain, err := w.kitComposition(ctx, url.Values{"tenant": {"acme"}, "model": {modelID}})
		if err != nil {
			return err
		}
		if plain.Route.Target != "pool:acme-gpu" {
			return fmt.Errorf("kit serve's router chose %q, want the worker pool", plain.Route.Target)
		}
		labelled, err := w.kitComposition(ctx, url.Values{
			"tenant": {"acme"}, "model": {modelID},
			"labels": {readyField(w.kitReady, "worker.remote-labels")},
		})
		if err != nil {
			return err
		}
		if labelled.Route.Target != "remote:"+remoteName || labelled.Route.RemoteInvoker != remoteName {
			return fmt.Errorf("kit serve's labelled route is %+v, want remote %q with its invoker", labelled.Route, remoteName)
		}
		return nil
	})
}

// --- helpers ---

// workerDeclare gives a pool a descriptor without a present worker, as after a
// rollout drained the workers. price < 0 leaves the descriptor's price empty.
func workerDeclare(ctx context.Context, tenant, poolID, modelID string, price int64) error {
	w := world(ctx).Worker
	spec, err := w.ensurePool(ctx, poolID)
	if err != nil {
		return err
	}
	if tenant != "" {
		spec.Tenant = core.TenantID(tenant)
		w.pools[poolID] = spec
	}
	d := descriptorForModel(modelID, 1, false)
	if price >= 0 {
		d.Price = strconv.FormatInt(price, 10)
	}
	temp := "declarer"
	if err := w.join(ctx, temp, poolID, []worker.Descriptor{d}); err != nil {
		return err
	}
	jw := w.workers[temp]
	st, err := w.open(ctx)
	if err != nil {
		return err
	}
	return st.Leave(ctx, jw.worker.ID, jw.credential)
}

func workerPoolForTenant(ctx context.Context, tenant string) string {
	w := world(ctx).Worker
	for _, id := range sortedPoolIDs(w.pools) {
		if string(w.pools[id].Tenant) == tenant {
			return id
		}
	}
	if _, ok := w.pools["shared"]; ok {
		return "shared"
	}
	return tenant + "-gpu"
}

func sortedPoolIDs(pools map[string]worker.PoolSpec) []string {
	out := make([]string, 0, len(pools))
	for id := range pools {
		out = append(out, id)
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// workerPoolForModel returns the pool a model was declared in, preferring the
// pool the scenario already uses.
func workerPoolForModel(ctx context.Context, modelID string) string {
	w := world(ctx).Worker
	st, err := w.open(ctx)
	if err == nil {
		for _, id := range sortedPoolIDs(w.pools) {
			descs, err := st.Descriptors(ctx, id)
			if err != nil {
				continue
			}
			for _, d := range descs {
				if d.ID == modelID {
					return id
				}
			}
		}
	}
	if w.run != nil && w.run.pool != "" {
		return w.run.pool
	}
	for _, id := range sortedPoolIDs(w.pools) {
		return id
	}
	return "acme-gpu"
}

func refsOf(invs []worker.Invocation) []string {
	out := make([]string, 0, len(invs))
	for _, inv := range invs {
		out = append(out, inv.ClientRef)
	}
	return out
}

func runRef(r *workerRun) string {
	if r == nil {
		return ""
	}
	return r.clientRef
}

func workerCode(err error, want string) error {
	if err == nil {
		return fmt.Errorf("the call succeeded, want code %s", want)
	}
	if got := core.CodeOf(err); string(got) != want {
		return fmt.Errorf("got code %s, want %s: %w", got, want, err)
	}
	return nil
}

func (w *workerWorld) invocation(ctx context.Context, ref string) (worker.Invocation, error) {
	st, err := w.open(ctx)
	if err != nil {
		return worker.Invocation{}, err
	}
	return st.Invocation(ctx, ref)
}

func (w *workerWorld) startRun(ctx context.Context, tenant core.TenantID, poolID, modelID string, timeout time.Duration, reserved int64) error {
	if w.run != nil && !w.run.settled {
		return fmt.Errorf("a run is already in flight")
	}
	st, err := w.open(ctx)
	if err != nil {
		return err
	}
	ref := "run"
	inv, err := st.Enqueue(ctx, worker.Invocation{
		ClientRef:    ref,
		Pool:         poolID,
		Tenant:       tenant,
		Model:        modelID,
		Class:        worker.ClassNormal,
		Units:        1,
		Request:      json.RawMessage(`{}`),
		QueueTimeout: timeout,
		ReservedMpts: reserved,
	})
	if err != nil {
		return err
	}
	w.run = &workerRun{clientRef: inv.ClientRef, pool: poolID, tenant: tenant, reserved: reserved}
	return nil
}

func (w *workerWorld) startRunRemote(ctx context.Context, tenant core.TenantID, modelID string, remote *worker.Remote) error {
	if remote == nil || remote.Invoker == nil {
		return fmt.Errorf("no remote provider")
	}
	r := model.Request{Model: modelID, ClientRef: "run"}
	if _, err := remote.Invoker.Start(ctx, r); err != nil {
		return err
	}
	w.run = &workerRun{clientRef: "run", tenant: tenant, reserved: remote.Estimate, settled: true}
	return nil
}

// expireLeases advances past the leases the named workers hold and sweeps.
func (w *workerWorld) expireLeases(ctx context.Context, names []string) error {
	st, err := w.open(ctx)
	if err != nil {
		return err
	}
	now := world(ctx).Core.clock.Now()
	horizon := time.Duration(0)
	for _, name := range names {
		jw := w.workers[name]
		if jw == nil {
			return fmt.Errorf("worker %q has not joined", name)
		}
		held, err := st.Leased(ctx, jw.worker.ID)
		if err != nil {
			return err
		}
		for _, inv := range held {
			if d := inv.LeaseExpires.Sub(now); d > horizon {
				horizon = d
			}
			w.rememberLease(inv.ClientRef)
		}
	}
	world(ctx).Core.clock.Advance(horizon + time.Second)
	return st.Sweep(ctx)
}

func (w *workerWorld) rememberLease(ref string) {
	for _, got := range w.leasedRefs {
		if got == ref {
			return
		}
	}
	w.leasedRefs = append(w.leasedRefs, ref)
}

func (w *workerWorld) passGrace(ctx context.Context) error {
	world(ctx).Core.clock.Advance(time.Minute + time.Second)
	st, err := w.open(ctx)
	if err != nil {
		return err
	}
	return st.Sweep(ctx)
}

// leaseStartExpire leases the run's invocation to the named worker, reports
// it started and lets its lease expire, so the next worker can take it.
func (w *workerWorld) leaseStartExpire(ctx context.Context, name string) error {
	if w.run == nil {
		return fmt.Errorf("no run is in flight")
	}
	if err := w.leaseFor(ctx, name, 1); err != nil {
		return err
	}
	if len(w.lastLease) != 1 {
		return fmt.Errorf("worker %q did not receive the invocation", name)
	}
	ref := w.lastLease[0].ClientRef
	w.rememberLease(ref)
	if err := w.start(ctx, name, ref); err != nil {
		return err
	}
	return w.expireLeases(ctx, []string{name})
}

func (w *workerWorld) cancelRun(ctx context.Context) error {
	if w.run == nil {
		return fmt.Errorf("no run is in flight")
	}
	st, err := w.open(ctx)
	if err != nil {
		return err
	}
	return st.Cancel(ctx, w.run.clientRef)
}

func (w *workerWorld) assertNodeFails(ctx context.Context, code string) error {
	if err := w.settle(ctx); err != nil {
		return err
	}
	if w.run == nil {
		return fmt.Errorf("no run is in flight")
	}
	if w.run.err == nil {
		return fmt.Errorf("the node succeeded, want code %s", code)
	}
	if got := core.CodeOf(w.run.err); string(got) != code {
		return fmt.Errorf("got code %s, want %s: %w", got, code, w.run.err)
	}
	return nil
}

func (w *workerWorld) assertCharged(ctx context.Context, want int64) error {
	if err := w.settle(ctx); err != nil {
		return err
	}
	if w.run == nil {
		return fmt.Errorf("no run is in flight")
	}
	inv, err := w.invocation(ctx, w.run.clientRef)
	if err != nil {
		return err
	}
	if inv.ChargedMpts != want {
		return fmt.Errorf("charged %d milli-points, want %d", inv.ChargedMpts, want)
	}
	if inv.State != "failed" && inv.State != "succeeded" && inv.State != "cancelled" {
		return fmt.Errorf("the reservation is still open (invocation is %s)", inv.State)
	}
	return nil
}

func (w *workerWorld) assertReservationReleased(ctx context.Context) error {
	if err := w.settle(ctx); err != nil {
		return err
	}
	if w.run == nil {
		return fmt.Errorf("no run is in flight")
	}
	inv, err := w.invocation(ctx, w.run.clientRef)
	if err != nil {
		return err
	}
	if inv.ChargedMpts != 0 {
		return fmt.Errorf("%d milli-points were charged, want the reservation released", inv.ChargedMpts)
	}
	return nil
}

func (w *workerWorld) endWhileAway(ctx context.Context, ref, modelID, state, code, name string) error {
	w.run = nil
	jw := w.workers[name]
	if jw == nil {
		return fmt.Errorf("worker %q has not joined", name)
	}
	st, err := w.open(ctx)
	if err != nil {
		return err
	}
	inv, err := st.Invocation(ctx, ref)
	if core.CodeOf(err) == core.CodeNotFound {
		if _, err := w.queued(ctx, core.TenantID("acme"), workerPoolForModel(ctx, modelID), modelID, "", ref); err != nil {
			return err
		}
		if err := w.leaseFor(ctx, name, 1); err != nil {
			return err
		}
		w.rememberLease(ref)
		if err := w.start(ctx, name, ref); err != nil {
			return err
		}
		if err := w.expireLeases(ctx, []string{name}); err != nil {
			return err
		}
		inv, err = st.Invocation(ctx, ref)
	}
	if err != nil {
		return err
	}
	if inv.Model != modelID {
		return fmt.Errorf("invocation %q is model %q, want %q", ref, inv.Model, modelID)
	}
	if !inv.InDoubt {
		return fmt.Errorf("invocation %q is not in doubt", ref)
	}
	if err := w.passGrace(ctx); err != nil {
		return err
	}
	got, err := st.Invocation(ctx, ref)
	if err != nil {
		return err
	}
	if got.State != state || string(got.Code) != code {
		return fmt.Errorf("invocation %q is %s with code %q, want %s with code %q", ref, got.State, got.Code, state, code)
	}
	return nil
}

func (w *workerWorld) setCloud(modelID string, estimate int64) error {
	w.cloud = &cloudSim{
		desc: model.Descriptor{
			ID:       modelID,
			Provider: "cloud",
			Caps:     []model.Capability{model.CapChat},
			Params:   json.RawMessage(`{"type":"object"}`),
			Protocol: model.ProtocolSingle,
			Locality: model.LocalityCloud,
		},
		estimate: estimate,
	}
	return nil
}

func (w *workerWorld) cloudRemotes() []worker.Remote {
	if w.cloud == nil {
		return nil
	}
	return []worker.Remote{w.cloud.remote()}
}

var _ = godog.ErrSkip
