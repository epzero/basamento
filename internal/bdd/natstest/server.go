// Package natstest starts a loopback nats-server with JetStream for BDD.
//
// The cloud profile connects through nats://<random-token>@127.0.0.1:<ephemeral-port>
// so only this test process can authenticate; there is no shared anonymous listener.
package natstest

import (
	"fmt"
	"net/url"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nuid"
)

// Server is a test NATS server on loopback with JetStream in storeDir.
type Server struct {
	inner *server.Server
	URL   string
}

// Start runs nats-server with JetStream, loopback listener and token auth.
func Start(storeDir string) (*Server, error) {
	token := nuid.Next()
	opts := &server.Options{
		Host:          "127.0.0.1",
		Port:          -1,
		JetStream:     true,
		StoreDir:      storeDir,
		NoSigs:        true,
		ServerName:    "bdd-nats",
		MaxPayload:    2 << 20,
		Authorization: token,
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
	u, err := url.Parse(srv.ClientURL())
	if err != nil {
		srv.Shutdown()
		return nil, err
	}
	u.User = url.User(token)
	return &Server{inner: srv, URL: u.String()}, nil
}

// Close stops the server and waits for shutdown.
func (s *Server) Close() {
	if s == nil || s.inner == nil {
		return
	}
	s.inner.Shutdown()
	s.inner.WaitForShutdown()
}
