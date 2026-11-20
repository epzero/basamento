package bus

import (
	"time"

	"github.com/nats-io/nats-server/v2/server"
)

func embeddedServerOptions(storeDir string, maxPayload int32) *server.Options {
	return &server.Options{
		Host:                   "127.0.0.1",
		Port:                   -1,
		DontListen:             true,
		NoSigs:                 true,
		JetStream:              true,
		StoreDir:               storeDir,
		MaxPayload:             maxPayload,
		ServerName:             "kit-embedded",
		DisableJetStreamBanner: true,
	}
}

func startEmbeddedServer(storeDir string, maxPayload int32) (*server.Server, error) {
	srv, err := server.NewServer(embeddedServerOptions(storeDir, maxPayload))
	if err != nil {
		return nil, err
	}
	srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		shutdownEmbedded(srv)
		return nil, embeddedNotReady()
	}
	return srv, nil
}

func shutdownEmbedded(srv *server.Server) {
	if srv == nil {
		return
	}
	srv.Shutdown()
	srv.WaitForShutdown()
}
