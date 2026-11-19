package sqltest

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres/v2"
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
	pgAdmin        string
	pgReady        bool
	pgFailed       bool
	pgEmb          *embeddedpostgres.EmbeddedPostgres
	reportedSource string
)

// InitPostgres resolves BDD_POSTGRES_URL or starts embedded Postgres 16 once per process.
func InitPostgres(ctx context.Context) error {
	var err error
	pgInit.Do(func() {
		err = initPostgres(ctx)
	})
	return err
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
	envURL := strings.TrimSpace(os.Getenv("BDD_POSTGRES_URL"))
	if envURL != "" {
		if err := probeAdminURL(ctx, envURL); err != nil {
			pgFailed = true
			if GateStrict() {
				return fmt.Errorf("postgres url unavailable: %w", err)
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
		pgFailed = true
		if GateStrict() {
			return fmt.Errorf("embedded postgres: %w; run make pg-prefetch", err)
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
		return err
	}
	q := u.Query()
	if q.Get("connect_timeout") == "" {
		q.Set("connect_timeout", "2")
		u.RawQuery = q.Encode()
		admin = u.String()
	}
	conn, err := sql.Open("pgx", admin)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return conn.PingContext(ctx)
}

func startEmbedded(ctx context.Context) (string, *embeddedpostgres.EmbeddedPostgres, error) {
	supervisor, err := resolveSupervisorPath()
	if err != nil {
		return "", nil, err
	}
	cache := PostgresCacheDir()
	provider := embeddedpostgres.DownloadProvider{CacheDir: cache, Offline: GateStrict()}
	cfg := embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).
		Port(0).
		Password(processPostgresPassword()).
		Storage(embeddedpostgres.Storage{CacheDir: cache}).
		Provider(provider).
		Supervisor(supervisor)
	if os.Geteuid() == 0 {
		cfg = cfg.RunAs(embeddedpostgres.User{UID: 1000, GID: 1000})
	}
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

func processPostgresPassword() string {
	return fmt.Sprintf("bddpw_%d", os.Getpid())
}

// PostgresFailed reports whether resolution tried and failed (URL or embedded).
func PostgresFailed() bool {
	pgMu.Lock()
	defer pgMu.Unlock()
	return pgFailed
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
	root, err := moduleRoot()
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(root, supervisorRel)
	if st, err := os.Stat(candidate); err == nil && st.Mode().IsRegular() {
		return candidate, nil
	}
	return "", fmt.Errorf("%s missing; run make pg-prefetch to build the supervisor", candidate)
}

func moduleRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("bdd: cannot locate module root")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("bdd: go.mod not found")
		}
		dir = parent
	}
}
