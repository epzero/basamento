package sqltest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// PostgresAdminURL is the admin connection (typically ends in /postgres).
func PostgresAdminURL() string {
	pgMu.Lock()
	defer pgMu.Unlock()
	if pgReady && pgAdmin != "" {
		return pgAdmin
	}
	return os.Getenv("BDD_POSTGRES_URL")
}

var bddDBSeq atomic.Uint64

const bddDBCreateMaxAttempts = 8

// bddDatabaseName returns a deterministic, parallel-safe Postgres database name.
func bddDatabaseName(seq uint64) string {
	return fmt.Sprintf("bdd_%d_%d", os.Getpid(), seq)
}

type pgCreateDatabase func(ctx context.Context, name string) error

func allocateBDDDatabase(ctx context.Context, create pgCreateDatabase, startSeq uint64) (string, error) {
	seq := startSeq
	for i := 0; i < bddDBCreateMaxAttempts; i++ {
		name := bddDatabaseName(seq)
		err := create(ctx, name)
		if isDuplicateDatabase(err) {
			seq++
			continue
		}
		if err != nil {
			return "", err
		}
		return name, nil
	}
	return "", fmt.Errorf("bdd database name exhausted")
}

func isDuplicateDatabase(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "42P04"
}

// CreateDatabase allocates a fresh database and returns its connection URL.
func CreateDatabase(ctx context.Context) (dbURL string, cleanup func(), err error) {
	admin := PostgresAdminURL()
	if admin == "" {
		return "", nil, fmt.Errorf("BDD_POSTGRES_URL unset")
	}
	u, err := url.Parse(admin)
	if err != nil {
		return "", nil, err
	}
	adminURL := *u
	if adminURL.Path == "" || adminURL.Path == "/" {
		adminURL.Path = "/postgres"
	}
	conn, err := sql.Open("pgx", adminURL.String())
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = conn.Close() }()

	start := bddDBSeq.Add(1)
	dbName, err := allocateBDDDatabase(ctx, func(ctx context.Context, name string) error {
		_, err := conn.ExecContext(ctx, `CREATE DATABASE "`+name+`"`)
		return err
	}, start)
	if err != nil {
		return "", nil, err
	}

	connURL := *u
	connURL.Path = "/" + dbName
	dbURL = connURL.String()

	cleanup = func() {
		c2, _ := sql.Open("pgx", adminURL.String())
		if c2 != nil {
			_, _ = c2.ExecContext(context.Background(), `DROP DATABASE IF EXISTS "`+dbName+`" WITH (FORCE)`)
			_ = c2.Close()
		}
	}
	return dbURL, cleanup, nil
}

// UnreachablePostgresURL builds a postgres URL against a closed loopback port.
func UnreachablePostgresURL(hostPort string) string {
	return fmt.Sprintf("postgres://kit:secret@%s/nodb?connect_timeout=1", hostPort)
}
