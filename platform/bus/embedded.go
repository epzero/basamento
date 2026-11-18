package bus

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// OpenEmbedded starts an in-process NATS server with JetStream file storage
// under dataDir/nats and returns a Store connected via in-process transport only
// (no TCP listener).
func OpenEmbedded(ctx context.Context, dataDir string, opts Options) (*Store, error) {
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
	maxPayload := opts.MaxMessageSize + 512
	srvOpts := &server.Options{
		Host:                   "127.0.0.1",
		Port:                   -1,
		DontListen:             true,
		NoSigs:                 true,
		JetStream:              true,
		StoreDir:               storeDir,
		MaxPayload:             int32(maxPayload),
		ServerName:             "kit-embedded",
		HTTPPort:               -1,
		DisableJetStreamBanner: true,
	}
	srv, err := server.NewServer(srvOpts)
	if err != nil {
		return nil, err
	}
	srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		srv.Shutdown()
		return nil, embeddedNotReady()
	}
	nc, err := nats.Connect("", nats.InProcessServer(srv), nats.Timeout(2*time.Second))
	if err != nil {
		srv.Shutdown()
		return nil, mapConnErr(err)
	}
	closeFn := func() { srv.Shutdown() }
	st, err := connectAndOpen(nc, opts, closeFn)
	if err != nil {
		srv.Shutdown()
		return nil, err
	}
	_ = ctx
	return st, nil
}
