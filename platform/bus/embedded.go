package bus

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// OpenEmbedded starts an in-process NATS server with JetStream file storage
// under dataDir/nats and returns a Store connected to it.
func OpenEmbedded(ctx context.Context, dataDir string, opts Options) (*Store, error) {
	opts = opts.withDefaults()
	storeDir := filepath.Join(dataDir, "nats")
	absDir, err := filepath.Abs(storeDir)
	if err != nil {
		return nil, err
	}
	storeDir = absDir
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return nil, err
	}
	maxPayload := opts.MaxMessageSize + 512
	srvOpts := &server.Options{
		Host:       "127.0.0.1",
		Port:       -1,
		JetStream:  true,
		StoreDir:   storeDir,
		NoSigs:     true,
		MaxPayload: int32(maxPayload),
		ServerName: "kit-embedded",
		DontListen: false,
	}
	srv, err := server.NewServer(srvOpts)
	if err != nil {
		return nil, err
	}
	srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		srv.Shutdown()
		return nil, fmt.Errorf("embedded nats not ready")
	}
	url := srv.ClientURL()
	nc, err := nats.Connect(url, nats.Timeout(2*time.Second))
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
