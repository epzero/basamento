package sql

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"strconv"
	"strings"

	"github.com/epzero/basamento/core"
	"github.com/pressly/goose/v3"
)

const migrateLockID int64 = 0x626173616d656e74

func gooseDialect(d dialect) goose.Dialect {
	if d == dialectPostgres {
		return goose.DialectPostgres
	}
	return goose.DialectSQLite3
}

func migrateDB(ctx context.Context, db *sql.DB, d dialect, migrations fs.FS, sqlitePath string) error {
	if d == dialectSQLite {
		run := func() error {
			if err := checkSchemaVersion(ctx, db, d, migrations); err != nil {
				return err
			}
			return runMigrations(ctx, db, d, migrations)
		}
		if sqlitePath != "" && !sqliteMemoryPath(sqlitePath) {
			return withSQLiteMigrateFileLock(ctx, sqlitePath, run)
		}
		return run()
	}
	if err := checkSchemaVersion(ctx, db, d, migrations); err != nil {
		return err
	}
	return withPostgresMigrateLock(ctx, db, func() error {
		if err := checkSchemaVersion(ctx, db, d, migrations); err != nil {
			return err
		}
		return runMigrations(ctx, db, d, migrations)
	})
}

// withPostgresMigrateLock takes the session advisory lock on one dedicated pool
// connection, runs fn while that connection holds the lock, and unlocks on the
// same connection before returning. Waits on ctx while acquiring the lock.
func withPostgresMigrateLock(ctx context.Context, db *sql.DB, fn func() error) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, migrateLockID); err != nil {
		return err
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, migrateLockID) }()
	return fn()
}

func runMigrations(ctx context.Context, db *sql.DB, d dialect, migrations fs.FS) error {
	provider, err := goose.NewProvider(gooseDialect(d), db, migrations)
	if err != nil {
		return mapMigrationErr(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return mapMigrationErr(err)
	}
	return nil
}

func checkSchemaVersion(ctx context.Context, db *sql.DB, d dialect, migrations fs.FS) error {
	maxSet, err := maxMigrationVersion(migrations)
	if err != nil {
		return err
	}
	cur, found, err := currentVersion(ctx, db, d)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if cur > maxSet {
		return core.NewError(core.CodeUnsupported, "sql: database schema newer than binary", map[string]any{
			"version": cur,
			"max":     maxSet,
		})
	}
	return nil
}

func maxMigrationVersion(migrations fs.FS) (int64, error) {
	var max int64
	err := fs.WalkDir(migrations, ".", func(path string, de fs.DirEntry, err error) error {
		if err != nil || de.IsDir() || !strings.HasSuffix(path, ".sql") {
			return err
		}
		base := path
		if i := strings.LastIndex(path, "/"); i >= 0 {
			base = path[i+1:]
		}
		if i := strings.Index(base, "_"); i > 0 {
			v, err := strconv.ParseInt(base[:i], 10, 64)
			if err == nil && v > max {
				max = v
			}
		}
		return nil
	})
	return max, err
}

func currentVersion(ctx context.Context, db *sql.DB, d dialect) (int64, bool, error) {
	var ver int64
	var q string
	if d == dialectPostgres {
		q = `SELECT version_id FROM goose_db_version WHERE is_applied = true ORDER BY version_id DESC LIMIT 1`
	} else {
		q = `SELECT version_id FROM goose_db_version WHERE is_applied = 1 ORDER BY version_id DESC LIMIT 1`
	}
	err := db.QueryRowContext(ctx, q).Scan(&ver)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if isMissingTable(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return ver, true, nil
}

func isMissingTable(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "no such table") || strings.Contains(s, "does not exist")
}

// SchemaVersion returns the newest applied goose migration version, or zero when none apply.
func (s *Store) SchemaVersion(ctx context.Context) (int64, error) {
	ver, found, err := currentVersion(ctx, s.db, s.dialect)
	if err != nil {
		return 0, mapExecErr(err)
	}
	if !found {
		return 0, nil
	}
	return ver, nil
}

func sqliteMemoryPath(path string) bool {
	return path == ":memory:" || strings.HasSuffix(path, ":memory:")
}
