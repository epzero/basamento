package sqltest

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/epzero/basamento/internal/bdd/modroot"
)

func TestPostgresURLPrecedenceNoEmbeddedFallback(t *testing.T) {
	ResetPostgresForTest()
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
	ResetPostgresForTest()
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
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing-supervisor")
	t.Setenv("EP_SUPERVISOR", missing)
	if _, err := resolveSupervisorPath(); err == nil {
		t.Fatal("expected missing supervisor error")
	}
	valid := filepath.Join(dir, "supervisor-stub")
	if err := os.WriteFile(valid, []byte{0}, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EP_SUPERVISOR", valid)
	if got, err := resolveSupervisorPath(); err != nil || got != valid {
		t.Fatalf("got %q err %v", got, err)
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
	root, err := modroot.Find()
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
