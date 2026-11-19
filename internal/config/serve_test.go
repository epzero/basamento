package config

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakePort struct {
	mu     sync.Mutex
	name   string
	closed bool
	order  *[]string
}

func (p *fakePort) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.order != nil {
		*p.order = append(*p.order, p.name)
	}
	return nil
}

func (p *fakePort) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func newTestServer(t *testing.T, handler http.Handler, ports []Port, timeout time.Duration, timer Timer) (*Server, net.Listener) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(ln, handler, ports, timeout, timer), ln
}

func healthMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func TestServeStopsCleanly(t *testing.T) {
	var order []string
	ports := []Port{&fakePort{name: PortSQL, order: &order}, &fakePort{name: PortBlob, order: &order}}
	srv, ln := newTestServer(t, healthMux(), ports, 5*time.Second, nil)
	ctx, cancel := context.WithCancel(context.Background())
	code := make(chan int, 1)
	go func() { code <- srv.Serve(ctx) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d", resp.StatusCode)
	}

	cancel()
	if got := <-code; got != 0 {
		t.Fatalf("exit = %d, want 0", got)
	}
	if want := PortBlob + "," + PortSQL; strings.Join(order, ",") != want {
		t.Fatalf("close order = %v, want %s", order, want)
	}
	for _, p := range ports {
		if !p.(*fakePort).isClosed() {
			t.Fatal("port stayed open")
		}
	}
}

func TestServeTimeoutClosesPortsAndFails(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(entered) })
		<-release
		w.WriteHeader(http.StatusOK)
	})
	var order []string
	ports := []Port{&fakePort{name: PortSQL, order: &order}, &fakePort{name: PortBlob, order: &order}}
	timer := &fakeTimer{ch: make(chan time.Time, 1)}
	srv, ln := newTestServer(t, mux, ports, 5*time.Second, timer)
	ctx, cancel := context.WithCancel(context.Background())
	code := make(chan int, 1)
	go func() { code <- srv.Serve(ctx) }()

	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		resp, err := http.Get("http://" + ln.Addr().String() + "/slow")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-entered
	cancel()
	timer.ch <- time.Unix(0, 0)
	if got := <-code; got != 1 {
		t.Fatalf("exit = %d, want 1", got)
	}
	close(release)
	<-requestDone
	if want := PortBlob + "," + PortSQL; strings.Join(order, ",") != want {
		t.Fatalf("close order = %v, want %s", order, want)
	}
}

func TestServeRefusesNewRequestsAfterStop(t *testing.T) {
	srv, ln := newTestServer(t, healthMux(), nil, 5*time.Second, nil)
	ctx, cancel := context.WithCancel(context.Background())
	code := make(chan int, 1)
	go func() { code <- srv.Serve(ctx) }()
	cancel()
	<-srv.Stopping()
	if resp, err := http.Get("http://" + ln.Addr().String() + "/healthz"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("listener still accepts requests")
	}
	if got := <-code; got != 0 {
		t.Fatalf("exit = %d, want 0", got)
	}
}

type fakeTimer struct{ ch chan time.Time }

func (f *fakeTimer) After(time.Duration) <-chan time.Time { return f.ch }
