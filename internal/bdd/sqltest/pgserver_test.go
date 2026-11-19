package sqltest

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func resetPostgresSession() {
	_ = ClosePostgres()
	pgMu.Lock()
	pgInit = sync.Once{}
	pgFailed = false
	reportedSource = ""
	pgMu.Unlock()
}

func TestPostgresURLPrecedenceNoEmbeddedFallback(t *testing.T) {
	resetPostgresSession()
	ResetSkipCounts()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	t.Setenv("BDD_POSTGRES_URL", UnreachablePostgresURL(addr))
	t.Setenv("GATE_STRICT", "")
	_ = ClosePostgres()
	// Reset once semantics for a second init in-process tests cannot re-run pgInit;
	// this test must run in isolation (go test -run TestPostgresURLPrecedence).
	if err := InitPostgres(context.Background()); err != nil {
		t.Fatal(err)
	}
	if PostgresAvailable() {
		t.Fatal("unreachable url must not be available")
	}
	if PostgresServerSource() != "" {
		t.Fatalf("source %q", PostgresServerSource())
	}
}

func TestPostgresStrictFailsUnreachableURL(t *testing.T) {
	resetPostgresSession()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	t.Setenv("BDD_POSTGRES_URL", UnreachablePostgresURL(addr))
	t.Setenv("GATE_STRICT", "1")
	if err := InitPostgres(context.Background()); err == nil {
		t.Fatal("expected failure under strict")
	}
}

func TestResolveSupervisorRequiresBuild(t *testing.T) {
	t.Setenv("EP_SUPERVISOR", filepath.Join(t.TempDir(), "missing-supervisor"))
	if _, err := resolveSupervisorPath(); err == nil {
		t.Fatal("expected missing supervisor error")
	}
	t.Setenv("EP_SUPERVISOR", "")
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	built := filepath.Join(root, supervisorRel)
	if st, err := os.Stat(built); err == nil && st.Mode().IsRegular() {
		if _, err := resolveSupervisorPath(); err != nil {
			t.Fatal(err)
		}
		return
	}
	if _, err := resolveSupervisorPath(); err == nil {
		t.Fatal("expected missing supervisor error")
	}
}

func TestPostgresCacheDirEnv(t *testing.T) {
	t.Setenv(BDDPostgresCacheDir, "/tmp/cache")
	if got := PostgresCacheDir(); got != "/tmp/cache" {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizePostgresScheme(t *testing.T) {
	if got := normalizePostgresScheme("postgresql://u:p@127.0.0.1:1/db?sslmode=disable"); got != "postgres://u:p@127.0.0.1:1/db?sslmode=disable" {
		t.Fatalf("got %q", got)
	}
}

func TestModuleRoot(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root + "/go.mod"); err != nil {
		t.Fatal(err)
	}
}
