package sqltest

import (
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres/v2"

	"github.com/epzero/basamento/internal/bdd/modroot"
)

// PostgresServerMode names how the gate obtained Postgres: "embedded" or "url" (ADR-0015).
type PostgresServerMode string

const (
	PostgresServerEmbedded PostgresServerMode = "embedded"
	PostgresServerURL      PostgresServerMode = "url"
)

var (
	pgInit         sync.Once
	pgMu           sync.Mutex
	pgInitErr      error
	pgAdmin        string
	pgReady        bool
	pgEmb          *embeddedpostgres.EmbeddedPostgres
	reportedSource string
)

// InitPostgres resolves BDD_POSTGRES_URL or starts embedded Postgres 16 once per
// process (ADR-0015 G3). Every later call returns the first call's outcome.
func InitPostgres(ctx context.Context) error {
	pgInit.Do(func() {
		err := initPostgres(ctx)
		pgMu.Lock()
		pgInitErr = err
		pgMu.Unlock()
	})
	pgMu.Lock()
	defer pgMu.Unlock()
	return pgInitErr
}

// ResetPostgresForTest clears per-process InitPostgres state; tests call this between cases.
func ResetPostgresForTest() {
	_ = ClosePostgres()
	pgMu.Lock()
	pgInit = sync.Once{}
	pgInitErr = nil
	reportedSource = ""
	pgMu.Unlock()
}

// ClosePostgres stops an embedded server started for this process.
func ClosePostgres() error {
	pgMu.Lock()
	defer pgMu.Unlock()
	if pgEmb == nil {
		return nil
	}
	err := pgEmb.Close()
	pgEmb = nil
	pgAdmin = ""
	pgReady = false
	return err
}

// PostgresAvailable reports whether the Postgres dialect pass can run.
func PostgresAvailable() bool {
	pgMu.Lock()
	defer pgMu.Unlock()
	return pgReady
}

// PostgresServerSource returns "embedded", "url", or "" when Postgres is unavailable.
func PostgresServerSource() string {
	pgMu.Lock()
	defer pgMu.Unlock()
	return reportedSource
}

func initPostgres(ctx context.Context) error {
	if PostgresResolveSource() == PostgresServerURL {
		envURL := strings.TrimSpace(os.Getenv("BDD_POSTGRES_URL"))
		if err := probeAdminURL(ctx, envURL); err != nil {
			if GateStrict() {
				return err
			}
			return nil
		}
		pgMu.Lock()
		pgAdmin = envURL
		pgReady = true
		reportedSource = string(PostgresServerURL)
		pgMu.Unlock()
		return nil
	}
	admin, emb, err := startEmbedded(ctx)
	if err != nil {
		if GateStrict() {
			return newEmbeddedInitError(err)
		}
		return nil
	}
	pgMu.Lock()
	pgAdmin = admin
	pgEmb = emb
	pgReady = true
	reportedSource = string(PostgresServerEmbedded)
	pgMu.Unlock()
	return nil
}

func probeAdminURL(ctx context.Context, admin string) error {
	u, err := url.Parse(admin)
	if err != nil {
		return newURLProbeError(err)
	}
	q := u.Query()
	if q.Get("connect_timeout") == "" {
		q.Set("connect_timeout", "2")
		u.RawQuery = q.Encode()
		admin = u.String()
	}
	conn, err := sql.Open("pgx", admin)
	if err != nil {
		return newURLProbeError(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.PingContext(ctx); err != nil {
		return newURLProbeError(err)
	}
	return probeRoleCanCreateDB(ctx, conn)
}

// probeRoleCanCreateDB rejects a URL path whose role cannot create databases
// (ADR-0015 URL path): every scenario would otherwise fail on CREATE DATABASE
// with SQLSTATE 42501 after init called the server available.
func probeRoleCanCreateDB(ctx context.Context, conn *sql.DB) error {
	var createdb bool
	if err := conn.QueryRowContext(ctx, `SELECT rolcreatedb FROM pg_roles WHERE rolname = current_user`).Scan(&createdb); err != nil {
		return newURLProbeError(err)
	}
	return requireCreateDB(createdb)
}

// requireCreateDB maps the role's CREATEDB attribute to the probe's outcome.
func requireCreateDB(createdb bool) error {
	if createdb {
		return nil
	}
	return newURLProbeError(errRoleLacksCreateDB)
}

func gateDownloadProvider() embeddedpostgres.DownloadProvider {
	cache := PostgresCacheDir()
	return embeddedpostgres.DownloadProvider{CacheDir: cache, Offline: GateStrict()}
}

// gateEmbeddedConfig builds loopback embedded settings for the gate (ADR-0015).
func gateEmbeddedConfig(supervisor, password string) embeddedpostgres.Config {
	cache := PostgresCacheDir()
	cfg := embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).
		Port(0).
		Password(password).
		Storage(embeddedpostgres.Storage{CacheDir: cache}).
		Provider(gateDownloadProvider()).
		Supervisor(supervisor)
	if os.Geteuid() == 0 {
		cfg = cfg.RunAs(embeddedpostgres.User{UID: 1000, GID: 1000})
	}
	return cfg
}

func startEmbedded(ctx context.Context) (string, *embeddedpostgres.EmbeddedPostgres, error) {
	supervisor, err := resolveSupervisorPath()
	if err != nil {
		return "", nil, err
	}
	password, err := processPostgresPassword()
	if err != nil {
		return "", nil, err
	}
	cfg := gateEmbeddedConfig(supervisor, password)
	emb := embeddedpostgres.NewDatabase(cfg)
	if err := emb.StartContext(ctx); err != nil {
		return "", nil, err
	}
	return normalizePostgresScheme(emb.ConnectionURL()), emb, nil
}

// normalizePostgresScheme maps postgresql:// URLs from embedded-postgres to postgres:// for platform/sql.
func normalizePostgresScheme(raw string) string {
	const legacy = "postgresql://"
	if strings.HasPrefix(raw, legacy) {
		return "postgres://" + strings.TrimPrefix(raw, legacy)
	}
	return raw
}

// processPostgresPassword returns a random password for this process's embedded
// server (ADR-0015: random password per process, loopback listener only).
func processPostgresPassword() (string, error) {
	b := make([]byte, 16)
	if _, err := cryptorand.Read(b); err != nil {
		return "", fmt.Errorf("embedded postgres password: %w", err)
	}
	return "bddpw_" + hex.EncodeToString(b), nil
}

const supervisorRel = ".gate/bin/embedded-postgres"

// resolveSupervisorPath returns the supervisor built from the pinned module (never a release download).
func resolveSupervisorPath() (string, error) {
	if p := os.Getenv("EP_SUPERVISOR"); p != "" {
		ap, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		st, err := os.Stat(ap)
		if err != nil {
			return "", err
		}
		if !st.Mode().IsRegular() {
			return "", fmt.Errorf("EP_SUPERVISOR is not a regular file")
		}
		return ap, nil
	}
	root, err := modroot.Find()
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(root, supervisorRel)
	if st, err := os.Stat(candidate); err == nil && st.Mode().IsRegular() {
		return candidate, nil
	}
	return "", fmt.Errorf("%s missing; run make pg-prefetch to build the supervisor", candidate)
}
