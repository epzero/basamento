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

	opened := make([]config.Port, 0, len(ports))
	for _, p := range ports {
		port, err := p.open(context.Background())
		if err != nil {
			closePorts(opened)
			fmt.Fprintf(os.Stderr, "port %q could not open: %v\n", p.name, err)
			os.Exit(1)
		}
		opened = append(opened, port)
	}

	ln, err := net.Listen("tcp", result.Config.Listen.Addr)
	if err != nil {
		closePorts(opened)
		fmt.Fprintf(os.Stderr, "listen.addr %q could not be bound: %v\n", result.Config.Listen.Addr, err)
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	srv := config.NewServer(ln, mux, opened, result.Config.Shutdown.Timeout, nil)
	code := srv.Serve(ctx)
	stop()
	os.Exit(code)
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
	store, _, err := blob.Open(ctx, localBlobURL(raw), blob.Options{})
	if err != nil {
		return nil, err
	}
	return store, nil
}

// localBlobURL turns file://./<path> and file://../<path> into an absolute
// file URL. Go parses the leading dot as the URL host and the rest as an
// absolute path, which would point at the filesystem root.
func localBlobURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" || (u.Host != "." && u.Host != "..") {
		return raw
	}
	abs, err := filepath.Abs(u.Host + u.Path)
	if err != nil {
		return raw
	}
	return "file://" + filepath.ToSlash(abs)
}

// openLocalSecrets accepts either a base64 master key (the gocloud form) or a
// path to a key file, as the embedded default names one. A missing key file is
// created with 32 random bytes and mode 0600.
func openLocalSecrets(ctx context.Context, raw string) (config.Port, error) {
	rest := strings.TrimPrefix(raw, "localsecrets://")
	if key, err := base64.StdEncoding.DecodeString(rest); err == nil && len(key) == 32 {
		return secrets.OpenURL(ctx, raw)
	}
	key, err := loadOrCreateKey(rest)
	if err != nil {
		return nil, err
	}
	return secrets.OpenURL(ctx, "localsecrets://"+base64.StdEncoding.EncodeToString(key))
}

func loadOrCreateKey(path string) ([]byte, error) {
	if path == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "secrets key path is empty", nil)
	}
	if data, err := os.ReadFile(path); err == nil {
		if len(data) == 32 {
			return data, nil
		}
		if key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data))); err == nil && len(key) == 32 {
			return key, nil
		}
		return nil, core.NewError(core.CodeInvalidArgument, "secrets key file must hold 32 bytes", nil)
	} else if !os.IsNotExist(err) {
		return nil, core.NewError(core.CodeInvalidArgument, "secrets key file could not be read", nil)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "secrets key directory could not be created", nil)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, core.NewError(core.CodeInternal, "secrets key could not be generated", nil)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "secrets key file could not be written", nil)
	}
	return key, nil
}
