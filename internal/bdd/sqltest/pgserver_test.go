package sqltest

import (
	"context"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"strconv"
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

// TestInitPostgresReturnsFirstOutcome proves a later call cannot report a
// success the first call did not achieve (the once body kept the error only in
// its own call's local).
func TestInitPostgresReturnsFirstOutcome(t *testing.T) {
	ResetPostgresForTest()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	t.Setenv("BDD_POSTGRES_URL", UnreachablePostgresURL(addr))
	t.Setenv("GATE_STRICT", "1")
	first := InitPostgres(context.Background())
	second := InitPostgres(context.Background())
	if first == nil {
		t.Fatal("expected the strict URL probe to fail")
	}
	if second == nil {
		t.Fatal("a later call must repeat the first call's error, not return nil")
	}
	if first.Error() != second.Error() {
		t.Fatalf("second call changed the outcome: %v vs %v", first, second)
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
	cfg := gateEmbeddedConfig(filepath.Join(t.TempDir(), "supervisor-stub"), "bddpw_test")
	url := cfg.GetConnectionURL()
	if strings.Contains(url, ":5432/") {
		t.Fatalf("must not bind default 5432, got %q", url)
	}
	if !strings.Contains(url, "127.0.0.1") {
		t.Fatalf("want loopback host in %q", url)
	}
	if !strings.Contains(url, "bddpw_test") {
		t.Fatalf("want the process password in %q", url)
	}
}

// TestProcessPostgresPasswordUnguessable pins ADR-0015's random password per
// process: 128 bits from crypto/rand, never the PID (enumerable via ps).
func TestProcessPostgresPasswordUnguessable(t *testing.T) {
	first, err := processPostgresPassword()
	if err != nil {
		t.Fatal(err)
	}
	second, err := processPostgresPassword()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("two calls returned the same password %q", first)
	}
	const prefix = "bddpw_"
	if !strings.HasPrefix(first, prefix) || len(first) != len(prefix)+32 {
		t.Fatalf("want %q + 16 hex-encoded bytes, got %q", prefix, first)
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(first, prefix)); err != nil {
		t.Fatalf("password body is not hex: %v", err)
	}
	pid := strconv.Itoa(os.Getpid())
	for _, pw := range []string{first, second} {
		if strings.Contains(pw, pid) || strings.Contains(pw, "bddpw_"+pid) {
			t.Fatalf("password %q carries the process id", pw)
		}
	}
}
