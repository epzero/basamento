package steps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/internal/bdd/natstest"
	"github.com/epzero/basamento/platform/bus"
)

func registerBusOpen(sc *godog.ScenarioContext) {
	sc.Step(`^the store directory of the embedded data directory has mode "([^"]*)"$`, func(ctx context.Context, mode string) error {
		w := world(ctx).Bus
		if w.backend != "embedded" {
			return godog.ErrSkip
		}
		var perm os.FileMode
		switch mode {
		case "0755":
			perm = 0o755
		default:
			return fmt.Errorf("unsupported mode %q", mode)
		}
		if w.dataDir == "" {
			dir, err := os.MkdirTemp("", "bus-embed-*")
			if err != nil {
				return err
			}
			w.dataDir = dir
		}
		natsDir := filepath.Join(w.dataDir, "nats")
		return os.MkdirAll(natsDir, perm)
	})

	sc.Step(`^the embedded bus is opened$`, func(ctx context.Context) error {
		return world(ctx).Bus.openEmbeddedOnly(ctx)
	})

	sc.Step(`^the store directory has mode "0700"$`, func(ctx context.Context) error {
		w := world(ctx).Bus
		info, err := os.Stat(filepath.Join(w.dataDir, "nats"))
		if err != nil {
			return err
		}
		if info.Mode().Perm() != 0o700 {
			return fmt.Errorf("perm=%o", info.Mode().Perm())
		}
		return nil
	})

	sc.Step(`^the cloud bus opens on "([^"]*)"$`, func(ctx context.Context, rawURL string) error {
		return world(ctx).Bus.openCloudURL(ctx, rawURL)
	})

	sc.Step(`^the cloud bus opens on the cloud test server$`, func(ctx context.Context) error {
		w := world(ctx).Bus
		if err := w.ensureCloudServer(); err != nil {
			w.lastErr = err
			return nil
		}
		return w.openCloudURL(ctx, w.nats.URL)
	})

	sc.Step(`^the open (.+)$`, matchOpenOutcome)
}

func matchOpenOutcome(ctx context.Context, tail string) error {
	w := world(ctx).Bus
	if tail == `succeeds` {
		return w.checkCode(w.lastErr, "")
	}
	if strings.HasPrefix(tail, `fails with code "`) && strings.Contains(tail, `without "`) {
		parts := strings.SplitN(tail, ` without "`, 2)
		codePart := strings.TrimPrefix(parts[0], `fails with code "`)
		code := strings.TrimSuffix(codePart, `"`)
		if err := w.checkCode(w.lastErr, code); err != nil {
			return err
		}
		if w.lastErr == nil {
			return fmt.Errorf("no error")
		}
		msg := w.lastErr.Error()
		for _, chunk := range strings.Split(parts[1], `" or "`) {
			chunk = strings.Trim(chunk, `"`)
			if strings.Contains(msg, chunk) {
				return fmt.Errorf("error echoes %q", chunk)
			}
		}
		return nil
	}
	if strings.HasPrefix(tail, `fails with code "`) {
		if idx := strings.Index(tail, ` with detail "asset" set to "`); idx >= 0 {
			rest := strings.TrimPrefix(tail, `fails with code "`)
			code := strings.Split(rest, `"`)[0]
			if err := w.checkCode(w.lastErr, code); err != nil {
				return err
			}
			var pe *core.Error
			if !errors.As(w.lastErr, &pe) || pe.Details == nil {
				return fmt.Errorf("no details")
			}
			asset, _ := pe.Details["asset"].(string)
			want := strings.TrimSuffix(tail[idx+len(` with detail "asset" set to "`):], `"`)
			if asset != want {
				return fmt.Errorf("asset detail %q want %q", asset, want)
			}
			return nil
		}
		code := strings.TrimSuffix(strings.TrimPrefix(tail, `fails with code "`), `"`)
		return w.checkCode(w.lastErr, code)
	}
	return fmt.Errorf("unknown open outcome %q", tail)
}

func (w *busWorld) ensureCloudServer() error {
	if w.nats != nil {
		return nil
	}
	dir, err := os.MkdirTemp("", "bus-cloud-*")
	if err != nil {
		return err
	}
	w.dataDir = dir
	w.nats, err = natstest.Start(dir)
	return err
}

func (w *busWorld) openEmbeddedOnly(ctx context.Context) error {
	if w.backend != "embedded" {
		return godog.ErrSkip
	}
	if st, ok := w.bus.(*bus.Store); ok && st != nil {
		_ = st.Close()
		w.bus = nil
	}
	clk := world(ctx).Core.clock
	if w.dataDir == "" {
		dir, err := os.MkdirTemp("", "bus-embed-*")
		if err != nil {
			w.lastErr = err
			return nil
		}
		w.dataDir = dir
	}
	st, err := bus.OpenEmbedded(ctx, w.dataDir, w.opts(clk))
	w.lastErr = err
	if err == nil {
		w.bus = st
	}
	return nil
}

func (w *busWorld) openCloudURL(ctx context.Context, rawURL string) error {
	if w.backend != "cloud" {
		return godog.ErrSkip
	}
	if st, ok := w.bus.(*bus.Store); ok && st != nil {
		_ = st.Close()
		w.bus = nil
	}
	if rawURL == "cloud test server" {
		return w.openCloudURL(ctx, w.nats.URL)
	}
	clk := world(ctx).Core.clock
	st, err := bus.OpenCloud(ctx, rawURL, w.opts(clk))
	w.lastErr = err
	if err == nil {
		w.bus = st
	}
	return nil
}
