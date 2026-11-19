package sqltest

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"sync"
)

// PostgresProxy forwards TCP connections to a Postgres server from a local
// listener. A scenario that hardens the server against changes outside the
// store can open a store through the proxy and close it to make the database
// unreachable (REQ-SQL-023).
type PostgresProxy struct {
	listener net.Listener
	target   string

	mu     sync.Mutex
	conns  []net.Conn
	closed bool
}

// StartPostgresProxy listens on a loopback port and forwards to the server of
// targetURL. It returns the proxy and its host:port address; Close makes the
// server unreachable for every connection made through it.
func StartPostgresProxy(targetURL string) (*PostgresProxy, string, error) {
	u, err := url.Parse(targetURL)
	if err != nil {
		return nil, "", err
	}
	target := u.Host
	if target == "" {
		return nil, "", fmt.Errorf("postgres url has no host")
	}
	if _, _, err := net.SplitHostPort(target); err != nil {
		target = net.JoinHostPort(target, "5432")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	p := &PostgresProxy{listener: ln, target: target}
	go p.serve()
	return p, ln.Addr().String(), nil
}

func (p *PostgresProxy) serve() {
	for {
		in, err := p.listener.Accept()
		if err != nil {
			return
		}
		out, err := net.Dial("tcp", p.target)
		if err != nil {
			_ = in.Close()
			continue
		}
		if !p.track(in, out) {
			_ = in.Close()
			_ = out.Close()
			return
		}
		go pipe(out, in)
		go pipe(in, out)
	}
}

// track records a connection pair unless the proxy is already closed.
func (p *PostgresProxy) track(conns ...net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.conns = append(p.conns, conns...)
	return true
}

func pipe(dst, src net.Conn) {
	_, _ = io.Copy(dst, src)
	_ = dst.Close()
	_ = src.Close()
}

// Close stops accepting and closes every forwarded connection.
func (p *PostgresProxy) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	_ = p.listener.Close()
	for _, c := range p.conns {
		_ = c.Close()
	}
	p.conns = nil
	p.mu.Unlock()
}
