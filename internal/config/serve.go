package config

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// Port is one opened port the server closes on shutdown. The composition root
// wraps each platform store; this package never imports a port implementation.
type Port interface {
	Close() error
}

// Timer supplies the moment shutdown.timeout elapses. A nil Timer uses system
// time; the component driver injects one driven by coretest.FakeClock so no
// test sleeps.
type Timer interface {
	After(d time.Duration) <-chan time.Time
}

type systemTimer struct{}

func (systemTimer) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Server owns a bound listener and the opened ports for the life of
// `kit serve`. It starts nothing: the composition root opens the ports, binds
// the listener and prints the ready line before calling Serve (design §5).
type Server struct {
	listener net.Listener
	handler  http.Handler
	ports    []Port
	timeout  time.Duration
	timer    Timer
	stopping chan struct{}
}

// NewServer builds a server around an already-bound listener. timer may be nil
// to use system time.
func NewServer(listener net.Listener, handler http.Handler, ports []Port, timeout time.Duration, timer Timer) *Server {
	if timer == nil {
		timer = systemTimer{}
	}
	return &Server{
		listener: listener,
		handler:  handler,
		ports:    ports,
		timeout:  timeout,
		timer:    timer,
		stopping: make(chan struct{}),
	}
}

// Stopping is closed once the server stops accepting new requests, before it
// waits for the requests already in flight.
func (s *Server) Stopping() <-chan struct{} { return s.stopping }

// Serve serves until ctx is cancelled, then applies REQ-CONFIG-023 and -024:
// it stops accepting, gives the requests in flight shutdown.timeout to finish,
// closes every port in reverse open order, and returns 0 when they all
// finished in time or 1 otherwise.
func (s *Server) Serve(ctx context.Context) int {
	hs := &http.Server{Handler: s.handler}
	serveErr := make(chan error, 1)
	go func() { serveErr <- hs.Serve(s.listener) }()

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		// The listener failed under us; there is nothing to drain.
		s.closePorts()
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			return 1
		}
		return 0
	}

	// Stop accepting before waiting, so a request attempted after this point
	// is refused. The listener close makes Shutdown return net.ErrClosed for
	// the listener it also tries to close; that is not a timeout.
	_ = s.listener.Close()
	close(s.stopping)

	shutdownCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timerDone := make(chan struct{})
	go func() {
		defer close(timerDone)
		select {
		case <-s.timer.After(s.timeout):
			cancel()
		case <-shutdownCtx.Done():
		}
	}()
	err := hs.Shutdown(shutdownCtx)
	cancel()
	<-timerDone

	if err != nil && !errors.Is(err, net.ErrClosed) {
		s.closePorts()
		return 1
	}
	s.closePorts()
	return 0
}

func (s *Server) closePorts() {
	for i := len(s.ports) - 1; i >= 0; i-- {
		_ = s.ports[i].Close()
	}
}
