package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/epzero/basamento/auth/authn"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/internal/config"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/worker"
	"github.com/epzero/basamento/platform/blob"
	"github.com/epzero/basamento/platform/bus"
	"github.com/epzero/basamento/platform/credentials"
	"github.com/epzero/basamento/platform/secrets"
	"github.com/epzero/basamento/platform/sql"
	"google.golang.org/grpc"
	grpccreds "google.golang.org/grpc/credentials"
)

// wirePort is one port the composition root opens, in start-up order.
type wirePort struct {
	name           string
	implementation string
	open           func(ctx context.Context) (config.Port, error)
}

// runServe validates the configuration before opening anything, opens the
// ports in order, binds the listener and prints the ready line, then serves
// until a termination signal (REQ-CONFIG-005, -020 through -024).
func runServe(args []string) {
	f := newFlags("kit serve")
	if err := f.parse(args); err != nil {
		os.Exit(2)
	}
	result, err := config.Load(f.options(os.Stdin))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if errs := result.Validate(); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, e)
		}
		os.Exit(1)
	}

	// Refuse an unsafe listener before opening ports or binding: request
	// authentication is not wired yet, so every mode is loopback-only
	// (REQ-AUTH-001 for none).
	mode, err := authn.ParseMode(result.Config.Auth.Mode)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := authn.ValidateListen(mode, result.Config.Listen.Addr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	wiring := f.workerWiring()
	ports, runtimeName := wire(result.Config)

	// Install the handler before a port opens or the listener binds: a signal
	// in that window must cancel the openers and close whatever is already
	// open, not kill the process with a port left open.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	ln, opened, err := startPorts(ctx, ports, result.Config.Listen.Addr)
	if err != nil {
		// Read the context before stop: stop itself cancels it.
		cancelled := ctx.Err() != nil
		stop()
		if cancelled {
			// Terminated before ready: the openers were cancelled and their
			// ports closed. There is nothing to serve.
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// The platform owns queue time: the janitor runs here, not only in a
	// worker process, so a pool with no worker still times out and expires
	// (ADR-0012 §11). The worker composition root (providers, routing) is a
	// separate work package. A platform that cannot open its worker store
	// must not serve as if time-driven work were running.
	store, err := startWorkerJanitor(ctx, opened)
	if err != nil {
		closePorts(opened)
		stop()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var platform *workerPlatform
	if wiring["worker.listen"] != "" {
		platform, err = startWorkerPlatform(ctx, store, wiring)
		if err != nil {
			closePorts(opened)
			stop()
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	_, _ = fmt.Fprintf(os.Stdout, "ready profile=%s runtime=%s listen=%s", result.Config.Profile, runtimeName, ln.Addr())
	for _, p := range ports {
		_, _ = fmt.Fprintf(os.Stdout, " %s=%s", p.name, p.implementation)
	}
	if platform != nil {
		for _, field := range platform.readyFields() {
			_, _ = fmt.Fprintf(os.Stdout, " %s", field)
		}
	}
	_, _ = fmt.Fprintln(os.Stdout)

	srv := config.NewServer(ln, mux, opened, result.Config.Shutdown.Timeout, nil)
	code := srv.Serve(ctx)
	stop()
	os.Exit(code)
}

// startWorkerJanitor runs the worker queue's time-driven rules over the
// platform's SQL port until the process ends. It reports an error when the
// platform has a worker store it cannot open: serving without the janitor
// would silently stop queue timeouts, lease expiry and retention.
func startWorkerJanitor(ctx context.Context, opened []config.Port) (*worker.Store, error) {
	for _, p := range opened {
		db, ok := p.(*sql.Store)
		if !ok {
			continue
		}
		store, err := worker.Open(ctx, db, worker.Options{
			Clock:   core.SystemClock{},
			Audit:   credentials.NewSQLAudit(db),
			Recover: true,
		})
		if err != nil {
			return nil, fmt.Errorf("kit serve: worker store: %w", err)
		}
		go store.RunJanitor(ctx, time.Second, func(err error) {
			fmt.Fprintln(os.Stderr, "worker janitor:", err)
		})
		return store, nil
	}
	return nil, nil
}

// workerPlatform is the worker side of kit serve: the pool it registers, the
// platform model.Invoker for that pool, the router with its configured
// remotes, and the WorkerService listener workers dial in to (ADR-0012,
// REQ-MODEL-090). It owns no worker address: the platform never dials one.
type workerPlatform struct {
	ln      net.Listener
	srv     *grpc.Server
	invoker *worker.Invoker
	router  *worker.Router
	pool    string
	remote  string
	labels  map[string]string
	model   string
}

// startWorkerPlatform composes the worker side and serves WorkerService on the
// loopback listener the operator named. The main service listener stays the
// process's public one; this one is only for workers that dial out.
func startWorkerPlatform(ctx context.Context, store *worker.Store, wiring map[string]string) (*workerPlatform, error) {
	if store == nil {
		return nil, fmt.Errorf("kit serve: worker listener needs the SQL port")
	}
	tlsConfig := worker.TLSConfig{
		CertFile: wiring["worker.tls.cert-file"],
		KeyFile:  wiring["worker.tls.key-file"],
		CAFile:   wiring["worker.tls.ca-file"],
	}
	var serverCreds grpccreds.TransportCredentials
	if !tlsConfig.Empty() {
		var err error
		serverCreds, err = tlsConfig.ServerCredentials()
		if err != nil {
			return nil, fmt.Errorf("kit serve: %w", err)
		}
	}
	if err := validateWorkerListen(wiring["worker.listen"], serverCreds != nil); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", wiring["worker.listen"])
	if err != nil {
		return nil, fmt.Errorf("kit serve: worker listener: %w", err)
	}
	pool := wiring["worker.pool"]
	if pool != "" {
		spec := worker.PoolSpec{ID: pool, Lease: 5 * time.Minute, Grace: time.Minute}
		if tenant := wiring["worker.pool-tenant"]; tenant != "" {
			spec.Tenant = core.TenantID(tenant)
		}
		if path := wiring["worker.pool-token-file"]; path != "" {
			raw, err := os.ReadFile(path)
			if err != nil {
				_ = ln.Close()
				return nil, fmt.Errorf("kit serve: pool token file: %w", err)
			}
			token := strings.TrimSpace(string(raw))
			if _, _, err := store.EnsurePoolWithToken(ctx, spec, token); err != nil {
				_ = ln.Close()
				return nil, fmt.Errorf("kit serve: pool %q: %w", pool, err)
			}
		} else if _, _, err := store.EnsurePool(ctx, spec); err != nil {
			_ = ln.Close()
			return nil, fmt.Errorf("kit serve: pool %q: %w", pool, err)
		}
	}
	var remotes []worker.Remote
	remote := wiring["worker.remote"]
	remoteModel := wiring["worker.remote-model"]
	if remote != "" {
		if remoteModel == "" {
			_ = ln.Close()
			return nil, fmt.Errorf("kit serve: --worker.remote needs --worker.remote-model")
		}
		remotes = append(remotes, worker.Remote{
			Name: remote,
			Descriptors: []model.Descriptor{{
				ID:       remoteModel,
				Provider: remote,
				Caps:     []model.Capability{model.CapChat},
				Params:   json.RawMessage(`{"type":"object"}`),
				Protocol: model.ProtocolSingle,
				Locality: model.LocalityCloud,
			}},
			Labels: parseLabels(wiring["worker.remote-labels"]),
		})
	}
	// The platform side is a model.Invoker over the pool queue, and the router
	// chooses between it and the configured remotes (ADR-0012 §8, REQ-MODEL-090).
	platform := &workerPlatform{
		ln:      ln,
		invoker: &worker.Invoker{Store: store, Pool: pool},
		router:  worker.NewRouter(store, remotes...),
		pool:    pool,
		remote:  remote,
		model:   remoteModel,
		labels:  parseLabels(wiring["worker.remote-labels"]),
	}
	platform.srv = worker.NewService(store).GRPCServer(serverCreds)
	go func() {
		if err := worker.ServeWorkerService(ctx, ln, platform.srv, serverCreds != nil); err != nil {
			fmt.Fprintln(os.Stderr, "worker service:", err)
		}
	}()
	return platform, nil
}

// validateWorkerListen mirrors the rule authn applies to the main listener:
// a plaintext worker listener is loopback-only because it carries pool tokens
// and worker credentials. TLS lifts that limit and is the documented way to
// serve workers on another machine.
func validateWorkerListen(addr string, secure bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("kit serve: --worker.listen %q is not a host:port address", addr)
	}
	if secure || loopbackHost(host) {
		return nil
	}
	return fmt.Errorf("kit serve: --worker.listen %q is refused: without --worker.tls.cert-file and --worker.tls.key-file it accepts loopback addresses only", addr)
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// readyFields are the worker-side facts the ready line reports.
func (p *workerPlatform) readyFields() []string {
	out := []string{"worker.listen=" + p.ln.Addr().String()}
	if p.pool != "" {
		out = append(out, "worker.pool="+p.pool)
	}
	if p.remote != "" {
		out = append(out, "worker.remote="+p.remote, "worker.remote-model="+p.model)
		if len(p.labels) > 0 {
			out = append(out, "worker.remote-labels="+formatLabels(p.labels))
		}
	}
	return out
}

func formatLabels(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+labels[key])
	}
	return strings.Join(parts, ",")
}

// startPorts opens the ports in order and binds the listener, all under ctx.
// On cancellation or the first failure it closes what it opened and returns
// the error; the ready line is printed only after it returns successfully.
func startPorts(ctx context.Context, ports []wirePort, addr string) (net.Listener, []config.Port, error) {
	opened := make([]config.Port, 0, len(ports))
	for _, p := range ports {
		if err := ctx.Err(); err != nil {
			closePorts(opened)
			return nil, nil, err
		}
		port, err := p.open(ctx)
		if err != nil {
			closePorts(opened)
			return nil, nil, fmt.Errorf("port %q could not open: %w", p.name, err)
		}
		opened = append(opened, port)
	}
	if err := ctx.Err(); err != nil {
		closePorts(opened)
		return nil, nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		closePorts(opened)
		return nil, nil, fmt.Errorf("listen.addr %q could not be bound: %w", addr, err)
	}
	if err := ctx.Err(); err != nil {
		_ = ln.Close()
		closePorts(opened)
		return nil, nil, err
	}
	return ln, opened, nil
}

func closePorts(ports []config.Port) {
	for i := len(ports) - 1; i >= 0; i-- {
		_ = ports[i].Close()
	}
}

// wire returns the port openers in start-up order and the runtime name for the
// ready line. The temporal runtime is WP-L5-02/03; until it lands the runtime
// step opens nothing.
func wire(cfg config.Config) ([]wirePort, string) {
	if cfg.Profile == config.ProfileCloud {
		return wireCloud(cfg), "temporal"
	}
	return wireEmbedded(cfg), "temporal"
}

func wireEmbedded(cfg config.Config) []wirePort {
	return []wirePort{
		{name: config.PortSQL, implementation: cfg.Implementation(config.PortSQL), open: func(ctx context.Context) (config.Port, error) {
			return openSQL(ctx, cfg.SQL.URL)
		}},
		{name: config.PortBlob, implementation: cfg.Implementation(config.PortBlob), open: func(ctx context.Context) (config.Port, error) {
			return openBlob(ctx, cfg.Blob.URL)
		}},
		{name: config.PortBus, implementation: cfg.Implementation(config.PortBus), open: func(ctx context.Context) (config.Port, error) {
			return openBus(ctx, cfg.Bus.URL)
		}},
		{name: config.PortSecrets, implementation: cfg.Implementation(config.PortSecrets), open: func(ctx context.Context) (config.Port, error) {
			return openLocalSecrets(ctx, cfg.Secrets.URL)
		}},
	}
}

func wireCloud(cfg config.Config) []wirePort {
	return []wirePort{
		{name: config.PortSQL, implementation: cfg.Implementation(config.PortSQL), open: func(ctx context.Context) (config.Port, error) {
			return openSQL(ctx, cfg.SQL.URL)
		}},
		{name: config.PortBlob, implementation: cfg.Implementation(config.PortBlob), open: func(ctx context.Context) (config.Port, error) {
			return openBlob(ctx, cfg.Blob.URL)
		}},
		{name: config.PortBus, implementation: cfg.Implementation(config.PortBus), open: func(ctx context.Context) (config.Port, error) {
			return bus.OpenCloud(ctx, cfg.Bus.URL, bus.Options{})
		}},
		{name: config.PortSecrets, implementation: cfg.Implementation(config.PortSecrets), open: func(ctx context.Context) (config.Port, error) {
			return secrets.OpenURL(ctx, cfg.Secrets.URL)
		}},
	}
}

// openBus opens the embedded bus store its URL names. platform/bus keeps its
// JetStream store in <dataDir>/nats, so the URL path is the store directory
// and the composition root passes its parent: an operator who sets bus.url
// moves the store instead of leaving it under data.dir.
func openBus(ctx context.Context, raw string) (config.Port, error) {
	dir, err := embeddedBusDataDir(raw)
	if err != nil {
		return nil, err
	}
	return bus.OpenEmbedded(ctx, dir, bus.Options{})
}

// embeddedBusDataDir derives the bus data directory from a nats-embedded URL.
// A path whose last segment is the conventional "nats" names the store itself
// (the default form, <data.dir>/nats); any other path names the bus data
// directory, under which platform/bus creates its store. A URL with a host is
// treated like a file URL: the host is a path segment, and localhost is
// dropped.
func embeddedBusDataDir(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "nats-embedded" {
		return "", core.NewError(core.CodeInvalidArgument, "embedded bus URL cannot be parsed", nil)
	}
	path := u.Path
	if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
		path = filepath.Join(u.Host, path)
	}
	if path == "" {
		return "", core.NewError(core.CodeInvalidArgument, "embedded bus URL has no directory", nil)
	}
	path = filepath.Clean(path)
	if filepath.Base(path) == "nats" {
		return filepath.Dir(path), nil
	}
	return path, nil
}

func openSQL(ctx context.Context, raw string) (config.Port, error) {
	// The audit tables are owned by platform/credentials; their migration is
	// the single source, so the kit does not keep a copy that can drift.
	return sql.Open(ctx, raw, credentials.Migrations())
}

func openBlob(ctx context.Context, raw string) (config.Port, error) {
	store, _, err := blob.Open(ctx, raw, blob.Options{})
	if err != nil {
		return nil, err
	}
	return store, nil
}

// openLocalSecrets accepts either a base64 master key (the gocloud form) or a
// path to a key file, as the embedded default names one. The forms are
// unambiguous: a value that is neither key material nor path-shaped is an
// error, and a decode failure never falls back to creating a file. A missing
// key file is created with 32 random bytes and mode 0600; an existing one must
// be a regular file no group or other user can access.
func openLocalSecrets(ctx context.Context, raw string) (config.Port, error) {
	const prefix = "localsecrets://"
	if !strings.HasPrefix(raw, prefix) {
		return nil, invalidSecretKey("secrets key URL must start with localsecrets://")
	}
	rest := strings.TrimPrefix(raw, prefix)
	if rest == "" {
		return nil, invalidSecretKey("secrets key is empty")
	}
	if key, ok := decodeMasterKey(rest); ok {
		return secrets.OpenURL(ctx, prefix+base64.StdEncoding.EncodeToString(key))
	}
	if base64KeyCandidate(rest) {
		return nil, invalidSecretKey("secrets key must be 32 bytes, base64 encoded")
	}
	if !looksLikeKeyPath(rest) {
		return nil, invalidSecretKey("secrets key must be 32 bytes, base64 encoded, or a key file path")
	}
	path, err := keyFilePath(rest)
	if err != nil {
		return nil, err
	}
	key, err := loadOrCreateKey(path)
	if err != nil {
		return nil, err
	}
	return secrets.OpenURL(ctx, prefix+base64.StdEncoding.EncodeToString(key))
}

func invalidSecretKey(reason string) error {
	return core.NewError(core.CodeInvalidArgument, reason, nil)
}

func decodeMasterKey(s string) ([]byte, bool) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		if key, err := enc.DecodeString(s); err == nil && len(key) == 32 {
			return key, true
		}
	}
	return nil, false
}

// base64KeyCandidate reports whether a value has the shape of a base64 master
// key (32 bytes encode to 43 or 44 characters). Such a value must decode to
// exactly 32 bytes and is never treated as a path, even when it contains an
// invalid character or '/': a mistyped key must not create a file. A dot-free
// value of that length is always key material; a path that long uses an
// extension or an explicit file:// URL.
func base64KeyCandidate(s string) bool {
	return len(s) >= 43 && len(s) <= 44 && !strings.Contains(s, ".")
}

// looksLikeKeyPath is the explicit path form: file://, an absolute or relative
// path, a value with a separator, or a name with an extension.
func looksLikeKeyPath(s string) bool {
	if strings.HasPrefix(s, "file://") ||
		strings.HasPrefix(s, "/") ||
		strings.HasPrefix(s, "./") ||
		strings.HasPrefix(s, "../") ||
		strings.HasPrefix(s, "~/") {
		return true
	}
	if strings.ContainsAny(s, `/\\`) {
		return true
	}
	return filepath.Ext(s) != ""
}

// keyFilePath resolves the explicit file:// form and percent-decodes a plain
// path. The built-in default is escaped as a URL path, so a data directory
// with a space arrives as %20; decoding it keeps the key in the real
// directory. A bad escape is invalid_argument.
func keyFilePath(rest string) (string, error) {
	if !strings.HasPrefix(rest, "file://") {
		path, err := url.PathUnescape(rest)
		if err != nil {
			return "", invalidSecretKey("secrets key path has an invalid percent escape")
		}
		return expandHome(path)
	}
	u, err := url.Parse(rest)
	if err != nil || u.Scheme != "file" {
		return "", invalidSecretKey("secrets key file URL cannot be parsed")
	}
	path := u.Path
	if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
		path = filepath.Join(u.Host, path)
	}
	if path == "" {
		return "", invalidSecretKey("secrets key file URL has no path")
	}
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", invalidSecretKey("secrets key file path cannot be resolved")
		}
		path = abs
	}
	return path, nil
}

// expandHome expands a leading ~/ in a plain key path. A configuration value
// is not a shell word, so the home-relative form is the only one expanded; a
// file:// URL and a bare ~ are left alone.
func expandHome(path string) (string, error) {
	if !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", invalidSecretKey("secrets key path home directory cannot be resolved")
	}
	return filepath.Join(home, path[2:]), nil
}

func loadOrCreateKey(path string) ([]byte, error) {
	if path == "" {
		return nil, invalidSecretKey("secrets key path is empty")
	}
	if key, err := readKeyFile(path); err == nil {
		return key, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, invalidSecretKey("secrets key directory could not be created")
	}
	// MkdirAll keeps the mode of a directory that already exists, so tighten
	// it before the key is written, as platform/secrets does.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, invalidSecretKey("secrets key directory could not be secured")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, core.NewError(core.CodeInternal, "secrets key could not be generated", nil)
	}
	tmp, err := writeKeyTemp(dir, key)
	if err != nil {
		return nil, invalidSecretKey("secrets key file could not be written")
	}
	defer func() { _ = os.Remove(tmp) }()
	if err := publishKey(tmp, path); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return nil, invalidSecretKey("secrets key file could not be written")
		}
		existing, err := readKeyFile(path)
		if err != nil {
			return nil, invalidSecretKey("secrets key file could not be read")
		}
		return existing, nil
	}
	return key, nil
}

// writeKeyTemp writes key to a 0600 temporary file in dir and fsyncs it before
// closing, so the final path never holds a partial key. The caller publishes
// it with publishKey and removes it afterwards.
func writeKeyTemp(dir string, key []byte) (string, error) {
	f, err := os.CreateTemp(dir, ".secrets-*.tmp")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	published := false
	defer func() {
		if !published {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return "", err
	}
	if _, err := f.Write(key); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	published = true
	return tmp, nil
}

// publishKey makes tmp appear at path atomically without replacing a key
// another process wrote: a hard link fails with ErrExist instead of
// overwriting, and the caller then reads the existing key. A file system
// without hard links falls back to rename, as platform/secrets does. The
// directory is synced so the new name survives a crash.
func publishKey(tmp, path string) error {
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
	}
	dir := filepath.Dir(path)
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

// readKeyFile opens the key without following a symlink, checks the mode of
// the open file and reads from that same descriptor, so a path swapped
// between a check and the read cannot redirect it. A missing file keeps
// its not-exist error so the caller can create it; every other failure is
// invalid_argument.
func readKeyFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, err
		}
		return nil, invalidSecretKey("secrets key file could not be read")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, invalidSecretKey("secrets key file could not be read")
	}
	if !info.Mode().IsRegular() {
		return nil, invalidSecretKey("secrets key file must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, invalidSecretKey("secrets key file must not be accessible by group or others")
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, invalidSecretKey("secrets key file could not be read")
	}
	if len(data) == 32 {
		return data, nil
	}
	if key, ok := decodeMasterKey(strings.TrimSpace(string(data))); ok {
		return key, nil
	}
	return nil, invalidSecretKey("secrets key file must hold 32 bytes")
}
