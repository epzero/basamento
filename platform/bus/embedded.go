package bus

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/nats-io/nats.go"
)

// OpenEmbedded starts an in-process NATS server with JetStream file storage
// under dataDir/nats and returns a Store connected via in-process transport only
// (no TCP or monitoring listeners). ctx cancels asset setup; the store lifetime
// is not tied to ctx after a successful open.
func OpenEmbedded(ctx context.Context, dataDir string, opts Options) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, mapConnErr(err)
	}
	opts = opts.withDefaults()
	storeDir := filepath.Join(dataDir, "nats")
	absDir, err := filepath.Abs(storeDir)
	if err != nil {
		return nil, err
	}
	storeDir = absDir
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		return nil, err
	}
	maxPayload := int32(opts.MaxMessageSize + 512)
	srv, err := startEmbeddedServer(storeDir, maxPayload)
	if err != nil {
		return nil, err
	}
	nc, err := nats.Connect("", nats.InProcessServer(srv), nats.Timeout(2*time.Second))
	if err != nil {
		shutdownEmbedded(srv)
		return nil, mapConnErr(err)
	}
	closeFn := func() { shutdownEmbedded(srv) }
	st, err := connectAndOpen(ctx, nc, opts, closeFn)
	if err != nil {
		nc.Close()
		shutdownEmbedded(srv)
		return nil, err
	}
	return st, nil
}
