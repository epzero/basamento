package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/internal/config"
)

func testKey() []byte { return bytes.Repeat([]byte{0x42}, 32) }

func TestOpenLocalSecretsAcceptsBase64Key(t *testing.T) {
	raw := "localsecrets://" + base64.StdEncoding.EncodeToString(testKey())
	p, err := openLocalSecrets(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
}

func TestOpenLocalSecretsRejectsMistypedBase64WithoutCreatingAFile(t *testing.T) {
	short := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 31))
	_, err := openLocalSecrets(context.Background(), "localsecrets://"+short)
	if err == nil {
		t.Fatal("openLocalSecrets accepted a 31-byte key")
	}
	if code := core.CodeOf(err); code != core.CodeInvalidArgument {
		t.Fatalf("code = %q, want invalid_argument: %v", code, err)
	}
	if _, statErr := os.Stat(short); !os.IsNotExist(statErr) {
		t.Fatalf("a mistyped key created a file: %v", statErr)
	}
}

func TestOpenLocalSecretsCreatesMissingKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "secrets.key")
	p, err := openLocalSecrets(context.Background(), "localsecrets://"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("key file mode = %o, want 600", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 32 {
		t.Fatalf("key file holds %d bytes, want 32", len(data))
	}
}

func TestOpenLocalSecretsRejectsLooseKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.key")
	if err := os.WriteFile(path, testKey(), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := openLocalSecrets(context.Background(), "localsecrets://"+path)
	if err == nil {
		t.Fatal("openLocalSecrets accepted a group/other-readable key file")
	}
	if code := core.CodeOf(err); code != core.CodeInvalidArgument {
		t.Fatalf("code = %q, want invalid_argument: %v", code, err)
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("key file mode changed to %o, want 644 left alone", got)
	}
}

func TestOpenLocalSecretsRejectsDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := openLocalSecrets(context.Background(), "localsecrets://"+path)
	if err == nil {
		t.Fatal("openLocalSecrets accepted a directory")
	}
	if code := core.CodeOf(err); code != core.CodeInvalidArgument {
		t.Fatalf("code = %q, want invalid_argument: %v", code, err)
	}
}

func TestOpenLocalSecretsAcceptsExistingKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.key")
	if err := os.WriteFile(path, testKey(), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := openLocalSecrets(context.Background(), "localsecrets://"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
}

func TestOpenLocalSecretsAcceptsFileURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.key")
	if err := os.WriteFile(path, testKey(), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := openLocalSecrets(context.Background(), "localsecrets://file://"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
}

type recordingPort struct{ closed bool }

func (p *recordingPort) Close() error {
	p.closed = true
	return nil
}

func TestStartPortsClosesOpenedPortsWhenCancelledDuringOpen(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	first := &recordingPort{}
	ports := []wirePort{
		{name: "sql", open: func(context.Context) (config.Port, error) { return first, nil }},
		{name: "blob", open: func(context.Context) (config.Port, error) {
			cancel()
			return nil, context.Canceled
		}},
	}
	ln, opened, err := startPorts(ctx, ports, "127.0.0.1:0")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("startPorts error = %v, want context.Canceled", err)
	}
	if ln != nil || opened != nil {
		t.Fatalf("startPorts returned a listener or ports after cancellation: %v %v", ln, opened)
	}
	if !first.closed {
		t.Fatal("already-opened port was not closed")
	}
}

func TestStartPortsStopsBeforeListeningWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	port := &recordingPort{}
	var seen context.Context
	ports := []wirePort{{name: "sql", open: func(c context.Context) (config.Port, error) {
		seen = c
		cancel()
		return port, nil
	}}}
	ln, _, err := startPorts(ctx, ports, "127.0.0.1:0")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("startPorts error = %v, want context.Canceled", err)
	}
	if seen != ctx {
		t.Fatal("opener did not receive the server context")
	}
	if ln != nil {
		t.Fatal("startPorts bound a listener after cancellation")
	}
	if !port.closed {
		t.Fatal("port was not closed after cancellation")
	}
}

func TestStartPortsNamesFailedPortAndClosesOpened(t *testing.T) {
	first := &recordingPort{}
	ports := []wirePort{
		{name: "sql", open: func(context.Context) (config.Port, error) { return first, nil }},
		{name: "blob", open: func(context.Context) (config.Port, error) { return nil, errors.New("boom") }},
	}
	_, _, err := startPorts(context.Background(), ports, "127.0.0.1:0")
	if err == nil || !strings.Contains(err.Error(), `port "blob" could not open`) {
		t.Fatalf("startPorts error = %v, want the failed port named", err)
	}
	if !first.closed {
		t.Fatal("already-opened port was not closed")
	}
}

func TestStartPortsClosesPortsWhenListenerCannotBind(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	port := &recordingPort{}
	ports := []wirePort{{name: "sql", open: func(context.Context) (config.Port, error) { return port, nil }}}
	_, _, err = startPorts(context.Background(), ports, held.Addr().String())
	if err == nil || !strings.Contains(err.Error(), "listen.addr") || !strings.Contains(err.Error(), "could not be bound") {
		t.Fatalf("startPorts error = %v, want listen.addr could not be bound", err)
	}
	if !port.closed {
		t.Fatal("port was not closed after the listener failed")
	}
}
