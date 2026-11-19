package sqltest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const (
	leakUser = "leakuser"
	leakPass = "leakpass"
	leakHost = "leak-host.invalid"
	leakDB   = "leakdb"
)

func leakTokens() []string {
	return []string{leakUser, leakPass, leakHost, leakDB, "postgres://", "127.0.0.1"}
}

func assertNoSecretLeak(t *testing.T, msg string, parts []string) {
	t.Helper()
	for _, p := range append(leakTokens(), parts...) {
		if p == "" {
			continue
		}
		if strings.Contains(msg, p) {
			t.Fatalf("leaks %q in %q", p, msg)
		}
	}
}

func TestURLProbeErrorsSecretFree(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	host, port, _ := net.SplitHostPort(addr)
	_ = ln.Close()

	refused := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?connect_timeout=1&sslmode=disable", leakUser, leakPass, host, port, leakDB)

	cases := []struct {
		name      string
		admin     string
		ctx       func() context.Context
		wantClass urlProbeClass
		wantIs    error
	}{
		{
			name:      "invalid url",
			admin:     "://%3Aleakuser:leakpass@leak-host.invalid/leakdb",
			ctx:       func() context.Context { return context.Background() },
			wantClass: urlClassInvalidURL,
		},
		{
			name:      "connection refused",
			admin:     refused,
			ctx:       func() context.Context { return context.Background() },
			wantClass: urlClassConnRefused,
			wantIs:    syscall.ECONNREFUSED,
		},
		{
			name:  "timeout/cancelled",
			admin: fmt.Sprintf("postgres://%s:%s@%s:9/%s?connect_timeout=1", leakUser, leakPass, leakHost, leakDB),
			ctx: func() context.Context {
				c, cancel := context.WithCancel(context.Background())
				cancel()
				return c
			},
			wantClass: urlClassTimeout,
			wantIs:    context.Canceled,
		},
		{
			name:      "open with garbage driver args",
			admin:     fmt.Sprintf("postgres://%s:%s@%s:9/%s", leakUser, leakPass, "0.0.0.0", leakDB),
			ctx:       func() context.Context { return context.Background() },
			wantClass: urlClassConnRefused,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := captureProbeOutput(t, tc.admin, tc.ctx())
			err := probeAdminURL(tc.ctx(), tc.admin)
			if err == nil {
				t.Fatal("expected probe error")
			}
			var pe *urlProbeError
			if !errors.As(err, &pe) {
				t.Fatalf("want *urlProbeError, got %T", err)
			}
			if pe.class != tc.wantClass {
				t.Fatalf("class %q, want %q", pe.class, tc.wantClass)
			}
			msg := err.Error()
			if !strings.Contains(msg, "BDD_POSTGRES_URL is set but the server is unavailable") {
				t.Fatalf("unexpected operator message: %q", msg)
			}
			if !strings.Contains(msg, string(tc.wantClass)) {
				t.Fatalf("missing class %q in %q", tc.wantClass, msg)
			}
			assertNoSecretLeak(t, msg, []string{port, host})
			assertNoSecretLeak(t, out, nil)
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Fatalf("errors.Is(%v): got %v", tc.wantIs, err)
			}
			if ErrorContainsSecret(err, leakPass) || ErrorContainsSecret(err, leakUser) {
				t.Fatal("ErrorContainsSecret reported leak")
			}
		})
	}
}

func captureProbeOutput(t *testing.T, admin string, ctx context.Context) string {
	t.Helper()
	var buf bytes.Buffer
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = old })
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	_ = probeAdminURL(ctx, admin)
	_ = w.Close()
	<-done
	return buf.String()
}

func TestInitPostgresStrictURLLeakFree(t *testing.T) {
	ResetPostgresForTest()
	t.Setenv("GATE_STRICT", "1")
	t.Setenv("BDD_POSTGRES_URL", fmt.Sprintf("postgres://%s:%s@%s:9/%s?connect_timeout=1", leakUser, leakPass, leakHost, leakDB))
	err := InitPostgres(context.Background())
	if err == nil {
		t.Fatal("expected failure")
	}
	assertNoSecretLeak(t, err.Error(), []string{"127.0.0.1"})
	if !strings.Contains(err.Error(), "connection refused") && !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("got %q", err.Error())
	}
	if PostgresAvailable() {
		t.Fatal("must not become available")
	}
	if PostgresServerSource() != "" {
		t.Fatalf("source %q", PostgresServerSource())
	}
}

func TestEmbeddedInitErrorSecretFree(t *testing.T) {
	pw := processPostgresPassword()
	cause := errors.New("start " + pw + " postgresql://ignored")
	err := newEmbeddedInitError(cause)
	assertNoSecretLeak(t, err.Error(), []string{pw})
	if !strings.Contains(err.Error(), "make pg-prefetch") {
		t.Fatalf("want prefetch hint: %q", err.Error())
	}
	if ErrorContainsSecret(err, pw) {
		t.Fatal("password in error details")
	}
}

func TestPostgresResolveSource(t *testing.T) {
	t.Setenv("BDD_POSTGRES_URL", "   ")
	if got := PostgresResolveSource(); got != PostgresServerEmbedded {
		t.Fatalf("whitespace: got %q", got)
	}
	t.Setenv("BDD_POSTGRES_URL", "postgres://example")
	if got := PostgresResolveSource(); got != PostgresServerURL {
		t.Fatalf("non-empty: got %q", got)
	}
	t.Setenv("BDD_POSTGRES_URL", "")
	if got := PostgresResolveSource(); got != PostgresServerEmbedded {
		t.Fatalf("empty: got %q", got)
	}
}

func TestEmbeddedColdCacheErrorsHermetic(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing-supervisor")
	t.Setenv("EP_SUPERVISOR", missing)
	if _, err := resolveSupervisorPath(); err == nil {
		t.Fatal("expected error")
	}

	supervisor := newEmbeddedInitError(errors.New(".gate/bin/embedded-postgres missing; run make pg-prefetch to build the supervisor"))
	if !strings.Contains(supervisor.Error(), "make pg-prefetch") {
		t.Fatalf("supervisor hint: %v", supervisor)
	}
	var se *embeddedInitError
	if !errors.As(supervisor, &se) || se.class != embeddedClassSupervisor {
		t.Fatalf("supervisor class: %v", supervisor)
	}

	bundle := newEmbeddedInitError(errors.New("PostgreSQL 16.15.1 is not cached (offline mode)"))
	if !strings.Contains(bundle.Error(), "make pg-prefetch") {
		t.Fatalf("bundle hint: %v", bundle)
	}
	var ee *embeddedInitError
	if !errors.As(bundle, &ee) || ee.class != embeddedClassBundle {
		t.Fatalf("class %v", bundle)
	}
}

func TestWhitespaceURLStrictUsesEmbeddedNotURLProbe(t *testing.T) {
	ResetPostgresForTest()
	t.Setenv("BDD_POSTGRES_URL", " \t ")
	t.Setenv("GATE_STRICT", "1")
	t.Setenv("EP_SUPERVISOR", filepath.Join(t.TempDir(), "missing-supervisor"))
	t.Setenv(BDDPostgresCacheDir, t.TempDir())
	if PostgresResolveSource() != PostgresServerEmbedded {
		t.Fatal("whitespace must select embedded source")
	}
	err := InitPostgres(context.Background())
	if err == nil {
		t.Fatal("expected embedded failure without supervisor")
	}
	if strings.Contains(err.Error(), "BDD_POSTGRES_URL is set") {
		t.Fatalf("must not use URL probe path: %v", err)
	}
	if !strings.Contains(err.Error(), "make pg-prefetch") {
		t.Fatalf("want prefetch hint: %v", err)
	}
}

func TestClassifyEmbeddedInitErr(t *testing.T) {
	if classifyEmbeddedInitErr(errors.New(".gate/bin/embedded-postgres missing")) != embeddedClassSupervisor {
		t.Fatal("supervisor")
	}
	if classifyEmbeddedInitErr(errors.New("not cached (offline mode)")) != embeddedClassBundle {
		t.Fatal("bundle")
	}
}
