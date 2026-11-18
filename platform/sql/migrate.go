package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	if d == dialectSQLite && sqlitePath != "" {
		mu := sqliteMigrateMutex(sqlitePath)
		mu.Lock()
		defer mu.Unlock()
		if err := checkSchemaVersion(db, d, migrations); err != nil {
			return err
		}
		if err := sqliteRecheckVersion(ctx, db, migrations); err != nil {
			return err
		}
		return runMigrations(ctx, db, d, migrations)
	}
	if err := checkSchemaVersion(db, d, migrations); err != nil {
		return err
	}
	if err := acquirePostgresMigrateLock(ctx, db); err != nil {
		return err
	}
	defer func() { _ = releasePostgresMigrateLock(ctx, db) }()
	if err := checkSchemaVersion(db, d, migrations); err != nil {
		return err
	}
	return runMigrations(ctx, db, d, migrations)
}

func sqliteRecheckVersion(ctx context.Context, db *sql.DB, migrations fs.FS) error {
	if _, err := db.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	defer func() { _, _ = db.ExecContext(ctx, `ROLLBACK`) }()
	return checkSchemaVersion(db, dialectSQLite, migrations)
}

func runMigrations(ctx context.Context, db *sql.DB, d dialect, migrations fs.FS) error {
	provider, err := goose.NewProvider(gooseDialect(d), db, migrations)
	if err != nil {
		return fmt.Errorf("sql: migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return core.NewError(core.CodeInternal, "sql: migration failed", map[string]any{"reason": err.Error()})
	}
	return nil
}

func checkSchemaVersion(db *sql.DB, d dialect, migrations fs.FS) error {
	maxSet, err := maxMigrationVersion(migrations)
	if err != nil {
		return err
	}
	cur, found, err := currentVersion(db, d)
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

func currentVersion(db *sql.DB, d dialect) (int64, bool, error) {
	var ver int64
	q := `SELECT version_id FROM goose_db_version WHERE is_applied ORDER BY version_id DESC LIMIT 1`
	if d == dialectPostgres {
		q = `SELECT version_id FROM goose_db_version WHERE is_applied = true ORDER BY version_id DESC LIMIT 1`
	} else {
		q = `SELECT version_id FROM goose_db_version WHERE is_applied = 1 ORDER BY version_id DESC LIMIT 1`
	}
	err := db.QueryRow(q).Scan(&ver)
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

func acquirePostgresMigrateLock(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, migrateLockID)
	return err
}

func releasePostgresMigrateLock(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, migrateLockID)
	return err
}

// SchemaVersion returns the newest applied migration version.
func (s *Store) SchemaVersion(ctx context.Context) (int64, error) {
	ver, found, err := currentVersion(s.db, s.dialect)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, nil
	}
	_ = ctx
	return ver, nil
}

// MaxMigrationVersion reports the highest version in migrations.
func MaxMigrationVersion(migrations fs.FS) (int64, error) {
	return maxMigrationVersion(migrations)
}
