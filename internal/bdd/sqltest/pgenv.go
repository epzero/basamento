package sqltest

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"sync/atomic"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// PostgresAdminURL is the admin connection (typically ends in /postgres).
func PostgresAdminURL() string {
	return os.Getenv("BDD_POSTGRES_URL")
}

// PostgresConfigured reports whether the cloud BDD pass should run.
func PostgresConfigured() bool {
	return PostgresAdminURL() != ""
}

var bddDBSeq atomic.Uint64

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
	dbName := "bdd_" + strconv.FormatUint(bddDBSeq.Add(1), 10)
	connURL := *u
	connURL.Path = "/" + dbName
	dbURL = connURL.String()

	adminURL := *u
	if adminURL.Path == "" || adminURL.Path == "/" {
		adminURL.Path = "/postgres"
	}
	conn, err := sql.Open("pgx", adminURL.String())
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `CREATE DATABASE "`+dbName+`"`); err != nil {
		return "", nil, err
	}
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
