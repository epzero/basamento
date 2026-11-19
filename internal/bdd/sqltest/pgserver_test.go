package sqltest

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
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

func TestGateDownloadProviderOfflineUnderStrict(t *testing.T) {
	t.Setenv("GATE_STRICT", "1")
	p := gateDownloadProvider()
	if !p.Offline {
		t.Fatal("strict gate must prefetch bundles offline")
	}
	t.Setenv("GATE_STRICT", "")
	if gateDownloadProvider().Offline {
		t.Fatal("non-strict may download on first start")
	}
}

func TestGateEmbeddedConfigLoopback(t *testing.T) {
	cfg := gateEmbeddedConfig(filepath.Join(t.TempDir(), "supervisor-stub"))
	url := cfg.GetConnectionURL()
	if strings.Contains(url, ":5432/") {
		t.Fatalf("must not bind default 5432, got %q", url)
	}
	if !strings.Contains(url, "127.0.0.1") {
		t.Fatalf("want loopback host in %q", url)
	}
}

func TestWhitespacePostgresURLUsesEmbeddedPath(t *testing.T) {
	resetPostgresSession()
	ResetSkipCounts()
	t.Setenv("BDD_POSTGRES_URL", "   ")
	t.Setenv("GATE_STRICT", "1")
	if _, err := resolveSupervisorPath(); err != nil {
		if PostgresCacheDir() == "" {
			t.Setenv(BDDPostgresCacheDir, t.TempDir())
		}
		err = InitPostgres(context.Background())
		if err == nil {
			t.Fatal("expected embedded start failure without supervisor")
		}
		if strings.Contains(err.Error(), "url unavailable") {
			t.Fatal("whitespace URL must count as unset, not URL path")
		}
		if !strings.Contains(err.Error(), "pg-prefetch") {
			t.Fatalf("want prefetch hint, got %v", err)
		}
		return
	}
	if PostgresCacheDir() == "" {
		t.Skip("BDD_POSTGRES_CACHE_DIR unset; cannot start embedded in gate")
	}
	if err := InitPostgres(context.Background()); err != nil {
		if strings.Contains(err.Error(), "url unavailable") {
			t.Fatal("whitespace URL must count as unset, not URL path")
		}
		t.Fatalf("embedded postgres: %v", err)
	}
	defer func() { _ = ClosePostgres() }()
	if got := PostgresServerSource(); got != string(PostgresServerEmbedded) {
		t.Fatalf("postgres_server = %q, want embedded", got)
	}
}
