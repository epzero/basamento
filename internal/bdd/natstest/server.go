// Package natstest starts a loopback nats-server with JetStream for BDD.
package natstest

import (
	"fmt"
	"time"

	"github.com/nats-io/nats-server/v2/server"
)

// Server is a test NATS server on loopback with JetStream in storeDir.
type Server struct {
	inner *server.Server
	URL   string
}

// Start runs nats-server with JetStream and a random loopback port.
func Start(storeDir string) (*Server, error) {
	opts := &server.Options{
		Host:       "127.0.0.1",
		Port:       -1,
		JetStream:  true,
		StoreDir:   storeDir,
		NoSigs:     true,
		ServerName: "bdd-nats",
		MaxPayload: 2 << 20,
	}
	srv, err := server.NewServer(opts)
	if err != nil {
		return nil, err
	}
	srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		srv.Shutdown()
		return nil, fmt.Errorf("nats-server not ready")
	}
	return &Server{inner: srv, URL: srv.ClientURL()}, nil
}

// Close stops the server and waits for shutdown.
func (s *Server) Close() {
	if s == nil || s.inner == nil {
		return
	}
	s.inner.Shutdown()
	s.inner.WaitForShutdown()
}
