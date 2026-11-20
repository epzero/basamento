package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/internal/config"
	"github.com/epzero/basamento/platform/blob"
	"github.com/epzero/basamento/platform/bus"
	"github.com/epzero/basamento/platform/secrets"
	"github.com/epzero/basamento/platform/sql"
)

//go:embed migrations/*.sql
var migrations embed.FS

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

	ports, runtimeName := wire(result.Config)

	// Install the handler before a port opens or the listener binds: a signal
	// in that window must cancel the openers and close whatever is already
	// open, not kill the process with a port left open (CR-04).
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
	_, _ = fmt.Fprintf(os.Stdout, "ready profile=%s runtime=%s listen=%s", result.Config.Profile, runtimeName, ln.Addr())
	for _, p := range ports {
		_, _ = fmt.Fprintf(os.Stdout, " %s=%s", p.name, p.implementation)
	}
	_, _ = fmt.Fprintln(os.Stdout)

	srv := config.NewServer(ln, mux, opened, result.Config.Shutdown.Timeout, nil)
	code := srv.Serve(ctx)
	stop()
	os.Exit(code)
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
			return bus.OpenEmbedded(ctx, cfg.Data.Dir, bus.Options{})
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

func openSQL(ctx context.Context, raw string) (config.Port, error) {
	migrationsFS, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	return sql.Open(ctx, raw, migrationsFS)
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
// be a regular file no group or other user can access (CR-05).
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
// exactly 32 bytes; it is never treated as a path, even when it contains '/'.
func base64KeyCandidate(s string) bool {
	if len(s) < 43 || len(s) > 44 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '+', r == '/', r == '=':
		default:
			return false
		}
	}
	return true
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

// keyFilePath resolves the explicit file:// form and leaves a plain path as is.
func keyFilePath(rest string) (string, error) {
	if !strings.HasPrefix(rest, "file://") {
		return rest, nil
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

func loadOrCreateKey(path string) ([]byte, error) {
	if path == "" {
		return nil, invalidSecretKey("secrets key path is empty")
	}
	if info, err := os.Lstat(path); err == nil {
		return readKeyFile(path, info)
	} else if !os.IsNotExist(err) {
		return nil, invalidSecretKey("secrets key file could not be read")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, invalidSecretKey("secrets key directory could not be created")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, core.NewError(core.CodeInternal, "secrets key could not be generated", nil)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			if info, statErr := os.Lstat(path); statErr == nil {
				return readKeyFile(path, info)
			}
		}
		return nil, invalidSecretKey("secrets key file could not be written")
	}
	if _, err := f.Write(key); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, invalidSecretKey("secrets key file could not be written")
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, invalidSecretKey("secrets key file could not be written")
	}
	return key, nil
}

func readKeyFile(path string, info os.FileInfo) ([]byte, error) {
	if !info.Mode().IsRegular() {
		return nil, invalidSecretKey("secrets key file must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, invalidSecretKey("secrets key file must not be accessible by group or others")
	}
	data, err := os.ReadFile(path)
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
