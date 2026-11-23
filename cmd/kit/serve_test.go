package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/internal/config"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/worker"
	"github.com/epzero/basamento/platform/credentials"
	psql "github.com/epzero/basamento/platform/sql"
)

func testKey() []byte { return bytes.Repeat([]byte{0x42}, 32) }

func TestOpenLocalSecretsAcceptsBase64Key(t *testing.T) {
	raw := "localsecrets://" + base64.StdEncoding.EncodeToString(testKey())
	p, err := openLocalSecrets(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
}

func TestOpenLocalSecretsRejectsMistypedBase64WithoutCreatingAFile(t *testing.T) {
	short := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 31))
	_, err := openLocalSecrets(context.Background(), "localsecrets://"+short)
	if err == nil {
		t.Fatal("openLocalSecrets accepted a 31-byte key")
	}
	if code := core.CodeOf(err); code != core.CodeInvalidArgument {
		t.Fatalf("code = %q, want invalid_argument: %v", code, err)
	}
	if _, statErr := os.Stat(short); !os.IsNotExist(statErr) {
		t.Fatalf("a mistyped key created a file: %v", statErr)
	}
}

func TestOpenLocalSecretsRejectsMistypedKeyContainingSlash(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	// 44 characters, so it has the shape of a master key, but neither
	// character set decodes it and it contains a path separator.
	bad := strings.Repeat("A", 21) + "/" + strings.Repeat("B", 21) + "!"
	if len(bad) != 44 {
		t.Fatalf("test value has %d characters, want 44", len(bad))
	}
	_, err := openLocalSecrets(context.Background(), "localsecrets://"+bad)
	if err == nil {
		t.Fatal("openLocalSecrets accepted a mistyped 44-character key")
	}
	if code := core.CodeOf(err); code != core.CodeInvalidArgument {
		t.Fatalf("code = %q, want invalid_argument: %v", code, err)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("a mistyped key created %d entries, want none", len(entries))
	}
}

func TestOpenLocalSecretsRejectsAnInvalidPathEscape(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	_, err := openLocalSecrets(context.Background(), "localsecrets://secrets%zz.key")
	if err == nil {
		t.Fatal("openLocalSecrets accepted a path with a bad percent escape")
	}
	if code := core.CodeOf(err); code != core.CodeInvalidArgument {
		t.Fatalf("code = %q, want invalid_argument: %v", code, err)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("a bad path escape created %d entries, want none", len(entries))
	}
}

func TestOpenLocalSecretsUsesTheDefaultURLPathWithASpace(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "kit data")
	got, err := config.Load(config.Options{Env: []string{"KIT_DATA_DIR=" + dataDir}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _, ok := got.Setting("secrets.url")
	if !ok {
		t.Fatal("secrets.url has no embedded default")
	}
	p, err := openLocalSecrets(context.Background(), raw)
	if err != nil {
		t.Fatalf("openLocalSecrets(%q): %v", raw, err)
	}
	t.Cleanup(func() { _ = p.Close() })
	keyPath := filepath.Join(dataDir, "secrets.key")
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("key file was not created at %q: %v", keyPath, err)
	}
	escaped := filepath.Join(filepath.Dir(dataDir), "kit%20data", "secrets.key")
	if _, err := os.Stat(escaped); !os.IsNotExist(err) {
		t.Fatalf("the percent-encoded path %q exists: %v", escaped, err)
	}
}

func TestOpenLocalSecretsCreatesMissingKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "secrets.key")
	p, err := openLocalSecrets(context.Background(), "localsecrets://"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("key file mode = %o, want 600", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 32 {
		t.Fatalf("key file holds %d bytes, want 32", len(data))
	}
}

func TestOpenLocalSecretsRejectsLooseKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.key")
	if err := os.WriteFile(path, testKey(), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := openLocalSecrets(context.Background(), "localsecrets://"+path)
	if err == nil {
		t.Fatal("openLocalSecrets accepted a group/other-readable key file")
	}
	if code := core.CodeOf(err); code != core.CodeInvalidArgument {
		t.Fatalf("code = %q, want invalid_argument: %v", code, err)
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("key file mode changed to %o, want 644 left alone", got)
	}
}

func TestOpenLocalSecretsRejectsDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := openLocalSecrets(context.Background(), "localsecrets://"+path)
	if err == nil {
		t.Fatal("openLocalSecrets accepted a directory")
	}
	if code := core.CodeOf(err); code != core.CodeInvalidArgument {
		t.Fatalf("code = %q, want invalid_argument: %v", code, err)
	}
}

func TestOpenLocalSecretsAcceptsExistingKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.key")
	if err := os.WriteFile(path, testKey(), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := openLocalSecrets(context.Background(), "localsecrets://"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
}

func TestOpenLocalSecretsAcceptsFileURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.key")
	if err := os.WriteFile(path, testKey(), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := openLocalSecrets(context.Background(), "localsecrets://file://"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
}

type recordingPort struct{ closed bool }

func (p *recordingPort) Close() error {
	p.closed = true
	return nil
}

func TestStartPortsClosesOpenedPortsWhenCancelledDuringOpen(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	first := &recordingPort{}
	ports := []wirePort{
		{name: "sql", open: func(context.Context) (config.Port, error) { return first, nil }},
		{name: "blob", open: func(context.Context) (config.Port, error) {
			cancel()
			return nil, context.Canceled
		}},
	}
	ln, opened, err := startPorts(ctx, ports, "127.0.0.1:0")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("startPorts error = %v, want context.Canceled", err)
	}
	if ln != nil || opened != nil {
		t.Fatalf("startPorts returned a listener or ports after cancellation: %v %v", ln, opened)
	}
	if !first.closed {
		t.Fatal("already-opened port was not closed")
	}
}

func TestStartPortsStopsBeforeListeningWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	port := &recordingPort{}
	var seen context.Context
	ports := []wirePort{{name: "sql", open: func(c context.Context) (config.Port, error) {
		seen = c
		cancel()
		return port, nil
	}}}
	ln, _, err := startPorts(ctx, ports, "127.0.0.1:0")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("startPorts error = %v, want context.Canceled", err)
	}
	if seen != ctx {
		t.Fatal("opener did not receive the server context")
	}
	if ln != nil {
		t.Fatal("startPorts bound a listener after cancellation")
	}
	if !port.closed {
		t.Fatal("port was not closed after cancellation")
	}
}

func TestStartPortsNamesFailedPortAndClosesOpened(t *testing.T) {
	first := &recordingPort{}
	ports := []wirePort{
		{name: "sql", open: func(context.Context) (config.Port, error) { return first, nil }},
		{name: "blob", open: func(context.Context) (config.Port, error) { return nil, errors.New("boom") }},
	}
	_, _, err := startPorts(context.Background(), ports, "127.0.0.1:0")
	if err == nil || !strings.Contains(err.Error(), `port "blob" could not open`) {
		t.Fatalf("startPorts error = %v, want the failed port named", err)
	}
	if !first.closed {
		t.Fatal("already-opened port was not closed")
	}
}

func TestStartPortsClosesPortsWhenListenerCannotBind(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	port := &recordingPort{}
	ports := []wirePort{{name: "sql", open: func(context.Context) (config.Port, error) { return port, nil }}}
	_, _, err = startPorts(context.Background(), ports, held.Addr().String())
	if err == nil || !strings.Contains(err.Error(), "listen.addr") || !strings.Contains(err.Error(), "could not be bound") {
		t.Fatalf("startPorts error = %v, want listen.addr could not be bound", err)
	}
	if !port.closed {
		t.Fatal("port was not closed after the listener failed")
	}
}

func TestEmbeddedBusDataDirFollowsTheURL(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"nats-embedded:///var/lib/kit/nats", "/var/lib/kit"},
		{"nats-embedded:///srv/busdata", "/srv/busdata"},
		{"nats-embedded://data/nats", "data"},
		{"nats-embedded://localhost/srv/kit/nats", "/srv/kit"},
	}
	for _, c := range cases {
		got, err := embeddedBusDataDir(c.raw)
		if err != nil {
			t.Fatalf("embeddedBusDataDir(%q): %v", c.raw, err)
		}
		if got != c.want {
			t.Errorf("embeddedBusDataDir(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestEmbeddedBusDataDirRejectsAForeignURL(t *testing.T) {
	for _, raw := range []string{"nats://bus.example.test:4222", "nats-embedded://"} {
		_, err := embeddedBusDataDir(raw)
		if err == nil {
			t.Fatalf("embeddedBusDataDir(%q) accepted it", raw)
		}
		if code := core.CodeOf(err); code != core.CodeInvalidArgument {
			t.Fatalf("embeddedBusDataDir(%q) code = %q, want invalid_argument", raw, code)
		}
	}
}

func TestWireEmbeddedBusUsesTheBusURLNotTheDataDir(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	storeDir := filepath.Join(root, "elsewhere", "nats")
	var cfg config.Config
	cfg.Data.Dir = dataDir
	cfg.Bus.URL = "nats-embedded://" + filepath.ToSlash(storeDir)

	var open func(context.Context) (config.Port, error)
	for _, p := range wireEmbedded(cfg) {
		if p.name == config.PortBus {
			open = p.open
		}
	}
	if open == nil {
		t.Fatal("wireEmbedded has no bus port")
	}
	port, err := open(context.Background())
	if err != nil {
		t.Fatalf("open bus at %q: %v", cfg.Bus.URL, err)
	}
	if err := port.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(storeDir); err != nil {
		t.Fatalf("the bus store is not at the bus.url path %q: %v", storeDir, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "nats")); !os.IsNotExist(err) {
		t.Fatalf("the bus store was also created under data.dir: %v", err)
	}
}

func TestOpenLocalSecretsExpandsHomeInKeyPath(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Chdir(work)
	t.Setenv("HOME", home)
	_, err := openLocalSecrets(context.Background(), "localsecrets://~/keys/secrets.key")
	if err != nil {
		t.Fatalf("openLocalSecrets(~): %v", err)
	}
	keyPath := filepath.Join(home, "keys", "secrets.key")
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("the key was not created under the home directory %q: %v", keyPath, err)
	}
	if _, err := os.Stat("~"); !os.IsNotExist(err) {
		t.Fatalf("a literal ~ directory was created: %v", err)
	}
}

func TestOpenLocalSecretsRejectsSymlinkedKeyFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.key")
	if err := os.WriteFile(target, testKey(), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "secrets.key")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err := openLocalSecrets(context.Background(), "localsecrets://"+link)
	if err == nil {
		t.Fatal("openLocalSecrets followed a symlinked key file")
	}
	if code := core.CodeOf(err); code != core.CodeInvalidArgument {
		t.Fatalf("code = %q, want invalid_argument: %v", code, err)
	}
}

func TestKeyPublishKeepsThePartialKeyInvisible(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.key")
	tmp, err := writeKeyTemp(dir, testKey())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the key is visible before publish: %v", err)
	}
	if err := publishKey(tmp, path); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(tmp)
	got, err := readKeyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, testKey()) {
		t.Fatal("the published key differs from the key written")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("published key mode = %o, want 600", got)
	}
}

func TestPublishKeyDoesNotReplaceAnExistingKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.key")
	first := bytes.Repeat([]byte{1}, 32)
	tmp, err := writeKeyTemp(dir, first)
	if err != nil {
		t.Fatal(err)
	}
	if err := publishKey(tmp, path); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(tmp)

	tmp, err = writeKeyTemp(dir, bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(tmp) })
	if err := publishKey(tmp, path); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("publishKey error = %v, want fs.ErrExist", err)
	}
	got, err := readKeyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, first) {
		t.Fatal("publishKey replaced the existing key")
	}
}

func TestLoadOrCreateKeyLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.key")
	if _, err := loadOrCreateKey(path); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "secrets.key" {
		t.Fatalf("directory holds %v, want only secrets.key", entries)
	}
}

func TestLoadOrCreateKeyTightensAnExistingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "secrets.key")
	if _, err := loadOrCreateKey(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("key directory mode = %o, want 700", got)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("key file mode = %o, want 600", got)
	}
}

func TestStartWorkerJanitorFailsWhenTheWorkerStoreCannotOpen(t *testing.T) {
	dir := t.TempDir()
	db, err := psql.Open(context.Background(), "sqlite:///"+filepath.Join(dir, "kit.db"), credentials.Migrations())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := startWorkerJanitor(context.Background(), []config.Port{db}); err == nil {
		t.Fatal("startWorkerJanitor accepted a store it cannot open; serve would run without the janitor")
	}
}

// compositionInvoker is enough of a model.Invoker for routing: the endpoint
// only names it, so the test never reaches a vendor.
type compositionInvoker struct{ name string }

func (c compositionInvoker) Provider() string { return c.name }
func (compositionInvoker) Describe(context.Context) ([]model.Descriptor, error) {
	return nil, nil
}
func (compositionInvoker) Start(context.Context, model.Request) (model.Handle, error) {
	return model.Handle{}, errors.New("not called")
}
func (compositionInvoker) Events(context.Context, model.Handle, int64) (<-chan model.Event, error) {
	return nil, errors.New("not called")
}
func (compositionInvoker) Result(context.Context, model.Handle) (model.Result, error) {
	return model.Result{}, errors.New("not called")
}
func (compositionInvoker) Cancel(context.Context, model.Handle) error { return nil }
func (compositionInvoker) Lookup(context.Context, string) (*model.Handle, error) {
	return nil, nil
}

func TestCompositionEndpointUsesTheComposedInvokerAndRouter(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	db, err := psql.Open(ctx, "sqlite:///"+t.TempDir()+"/worker.db", fstest.MapFS{
		"00001_noop.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := worker.Open(ctx, db, worker.Options{Clock: coretest.NewFakeClock(at), IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := store.CreatePool(ctx, worker.PoolSpec{ID: "acme-gpu", Lease: 5 * time.Minute, Grace: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Join(ctx, worker.JoinRequest{
		PoolToken: pool.Token, ProtocolVersion: worker.ProtocolVersion,
		Descriptors: []worker.Descriptor{{
			Descriptor: model.Descriptor{ID: "sdxl", Caps: []model.Capability{model.CapChat}},
			Units:      1,
		}},
		Capacity: 4,
	}); err != nil {
		t.Fatal(err)
	}
	remote := worker.Remote{
		Name:        "cloud-llm",
		Invoker:     compositionInvoker{name: "cloud-llm"},
		Descriptors: []model.Descriptor{{ID: "sdxl", Caps: []model.Capability{model.CapChat}}},
		Labels:      map[string]string{"region": "eu"},
	}
	platform := &workerPlatform{
		invoker: &worker.Invoker{Store: store, Pool: "acme-gpu"},
		router:  worker.NewRouter(store, remote),
		pool:    "acme-gpu",
	}

	rec := httptest.NewRecorder()
	platform.serveComposition(rec, httptest.NewRequest(http.MethodGet, "/worker/composition?tenant=acme&model=sdxl", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("composition status = %d: %s", rec.Code, rec.Body.String())
	}
	var plain compositionView
	if err := json.Unmarshal(rec.Body.Bytes(), &plain); err != nil {
		t.Fatal(err)
	}
	if plain.Invoker != "worker:acme-gpu" || plain.Route.Target != "pool:acme-gpu" {
		t.Fatalf("composition = %+v, want the kit serve invoker and pool route", plain)
	}

	rec = httptest.NewRecorder()
	platform.serveComposition(rec, httptest.NewRequest(http.MethodGet, "/worker/composition?tenant=acme&model=sdxl&labels=region%3Deu", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("labelled composition status = %d: %s", rec.Code, rec.Body.String())
	}
	var labelled compositionView
	if err := json.Unmarshal(rec.Body.Bytes(), &labelled); err != nil {
		t.Fatal(err)
	}
	if labelled.Route.Target != "remote:cloud-llm" || labelled.Route.RemoteInvoker != "cloud-llm" {
		t.Fatalf("labelled composition = %+v, want the composed remote and its invoker", labelled.Route)
	}
}

// TestCompositionEndpointFailsWithoutComposition is the guard the BDD scenario
// relies on: if startWorkerPlatform stops assigning invoker/router, the
// endpoint refuses and MODEL-090-01 fails instead of passing vacuously.
func TestCompositionEndpointFailsWithoutComposition(t *testing.T) {
	for name, platform := range map[string]*workerPlatform{
		"nil platform": {},
		"no invoker":   {router: worker.NewRouter(nil)},
		"no router":    {invoker: &worker.Invoker{}},
	} {
		rec := httptest.NewRecorder()
		platform.serveComposition(rec, httptest.NewRequest(http.MethodGet, "/worker/composition?tenant=acme&model=sdxl", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: status = %d, want 503", name, rec.Code)
		}
	}
}

func TestValidateWorkerWiringNamesTheMissingFlag(t *testing.T) {
	for _, tc := range []struct {
		name   string
		wiring map[string]string
		want   string
	}{
		{"pool without listener", map[string]string{"worker.pool": "acme-gpu"}, "--worker.pool needs --worker.listen"},
		{"token file without pool", map[string]string{"worker.listen": "127.0.0.1:0", "worker.pool-token-file": "token"}, "--worker.pool-token-file needs --worker.pool"},
		{"remote without manifest", map[string]string{"worker.listen": "127.0.0.1:0", "worker.remote": "cloud", "worker.remote-model": "sdxl"}, "--worker.remote needs --worker.remote-manifest"},
		{"tls without cert", map[string]string{"worker.listen": "127.0.0.1:0", "worker.tls.key-file": "key.pem"}, "--worker.tls.cert-file"},
	} {
		err := validateWorkerWiring(tc.wiring)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: validateWorkerWiring = %v, want %q", tc.name, err, tc.want)
		}
	}
	if err := validateWorkerWiring(map[string]string{}); err != nil {
		t.Fatalf("empty wiring = %v, want nil", err)
	}
}
