package sql

import (
	"context"
	"database/sql"
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

// Open parses rawURL, connects, applies migrations from migrations, and returns a Store.
// See package documentation for supported URL forms, migration locking, SQLite file modes,
// and error mapping during open and migrate.
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
		return nil, mapExecErr(err)
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
		if !sqliteMemoryPath(p.sqlitePath) {
			if err := ensureSQLiteDataDir(p.sqlitePath); err != nil {
				return nil, err
			}
			if err := ensureSQLiteDBFile(p.sqlitePath); err != nil {
				return nil, err
			}
		}
		dsn := sqliteDSN(p.sqlitePath)
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

func ensureSQLiteDataDir(dbPath string) error {
	dir := filepath.Dir(dbPath)
	return os.MkdirAll(dir, 0o700)
}

func ensureSQLiteDBFile(dbPath string) error {
	if _, err := os.Stat(dbPath); err == nil {
		return chmodSQLiteDBFile(dbPath)
	} else if !os.IsNotExist(err) {
		return err
	}
	return createSQLiteDBFile(dbPath)
}

// Close releases the database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return mapExecErr(s.db.Close())
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

// InTx runs fn inside one transaction on this store. The transaction rolls back
// when fn returns an error or panics, and commits only when fn returns nil.
// A nested InTx on the same store returns invalid_argument when fn passes the
// ctx InTx injects into each InTx on that store; passing a different ctx will
// not see the marker and can deadlock on SQLite (one connection). Different
// stores may nest independently.
func (s *Store) InTx(ctx context.Context, fn func(ctx context.Context, exec *Execer) error) (err error) {
	if ctx.Value(txActiveKey{s}) != nil {
		return core.NewError(core.CodeInvalidArgument, "sql: nested transaction", nil)
	}
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return mapExecErr(err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = sqlTx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = sqlTx.Rollback()
		}
	}()
	ctx = context.WithValue(ctx, txActiveKey{s}, true)
	exec := &Execer{db: s.db, tx: sqlTx, dialect: s.dialect}
	if err = fn(ctx, exec); err != nil {
		return err
	}
	err = mapExecErr(sqlTx.Commit())
	return err
}

// ParsedURLRedacted returns the store URL with userinfo and credential query parameters
// (password, sslpassword, and other libpq secret keys) replaced for safe logging and errors.
func (s *Store) ParsedURLRedacted() string { return redactURL(s.url.raw) }
