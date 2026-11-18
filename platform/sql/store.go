package sql

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/epzero/basamento/core"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// Store is an opened SQL database with migrations applied.
type Store struct {
	db      *sql.DB
	dialect dialect
	url     parsedURL
	exec    *Execer
}

// Open connects using rawURL, applies migrations from migrations, and returns a Store.
func Open(ctx context.Context, rawURL string, migrations fs.FS) (*Store, error) {
	p, err := parseStoreURL(rawURL)
	if err != nil {
		return nil, err
	}
	db, err := openDB(ctx, p)
	if err != nil {
		return nil, wrapOpenErr(p, err)
	}
	if err := migrateDB(ctx, db, p.dialect, migrations, p.sqlitePath); err != nil {
		_ = db.Close()
		if _, ok := err.(*core.Error); ok {
			return nil, err
		}
		return nil, wrapOpenErr(p, err)
	}
	if p.dialect == dialectSQLite {
		if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
			_ = db.Close()
			return nil, wrapOpenErr(p, err)
		}
	}
	return &Store{
		db:      db,
		dialect: p.dialect,
		url:     p,
		exec:    &Execer{db: db, dialect: p.dialect},
	}, nil
}

func openDB(ctx context.Context, p parsedURL) (*sql.DB, error) {
	switch p.dialect {
	case dialectSQLite:
		dir := filepath.Dir(p.sqlitePath)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)", p.sqlitePath)
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		return db, nil
	case dialectPostgres:
		db, err := sql.Open("pgx", p.pgConn)
		if err != nil {
			return nil, err
		}
		if err := db.PingContext(ctx); err != nil {
			_ = db.Close()
			return nil, err
		}
		return db, nil
	default:
		return nil, core.NewError(core.CodeInvalidArgument, "sql: unsupported dialect", nil)
	}
}

// Close releases the database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Execer returns the query layer for the store.
func (s *Store) Execer() *Execer { return s.exec }

// Dialect reports sqlite or postgres.
func (s *Store) Dialect() string {
	if s.dialect == dialectPostgres {
		return "postgres"
	}
	return "sqlite"
}

// InTx runs fn inside one transaction; rolled back when fn returns an error (REQ-SQL-013).
func (s *Store) InTx(ctx context.Context, fn func(ctx context.Context, exec *Execer) error) error {
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return mapExecErr(err)
	}
	exec := &Execer{db: s.db, tx: sqlTx, dialect: s.dialect}
	if err := fn(ctx, exec); err != nil {
		_ = sqlTx.Rollback()
		return err
	}
	return mapExecErr(sqlTx.Commit())
}

// DB exposes the database handle for conformance tests that need raw access.
func (s *Store) DB() *sql.DB { return s.db }

// ParsedURLRedacted returns the store url with secrets removed.
func (s *Store) ParsedURLRedacted() string { return redactURL(s.url.raw) }
