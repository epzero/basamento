package sql

import (
	"net/url"
	"path/filepath"
	"strings"

	"github.com/epzero/basamento/core"
)

type dialect int

const (
	dialectSQLite dialect = iota
	dialectPostgres
)

type parsedURL struct {
	dialect dialect
	// sqlitePath is the database file path for SQLite URLs (:memory: or absolute/relative path).
	sqlitePath string
	// pgConn is the full raw Postgres DSN, including credentials; only redactURL
	// output may appear in errors and logs.
	pgConn string
	raw    string
	user   string
}

func parseStoreURL(raw string) (parsedURL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "kit.db") {
		return parsedURL{}, core.NewError(core.CodeInvalidArgument, "sql: unsupported url", nil)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return parsedURL{}, core.NewError(core.CodeInvalidArgument, "sql: unsupported url", nil)
	}
	switch u.Scheme {
	case "sqlite":
		if u.RawQuery != "" || u.Fragment != "" {
			return parsedURL{}, core.NewError(core.CodeInvalidArgument, "sql: sqlite url must not carry query or fragment", nil)
		}
		path, err := sqlitePathFromURL(u, raw)
		if err != nil {
			return parsedURL{}, err
		}
		resolved, err := resolveSQLitePath(path)
		if err != nil {
			return parsedURL{}, err
		}
		return parsedURL{dialect: dialectSQLite, sqlitePath: resolved, raw: raw}, nil
	case "postgres":
		if u.Path == "" || u.Path == "/" {
			return parsedURL{}, core.NewError(core.CodeInvalidArgument, "sql: postgres url has no database", nil)
		}
		user := ""
		if u.User != nil {
			user = u.User.Username()
		}
		return parsedURL{dialect: dialectPostgres, pgConn: raw, raw: raw, user: user}, nil
	default:
		return parsedURL{}, core.NewError(core.CodeInvalidArgument, "sql: unsupported url scheme", nil)
	}
}

// sqlitePathFromURL derives the database path from a parsed sqlite URL (RFC 3986).
func sqlitePathFromURL(u *url.URL, raw string) (string, error) {
	if strings.HasPrefix(raw, "sqlite:///") {
		if u.Path == "" {
			return "", core.NewError(core.CodeInvalidArgument, "sql: sqlite url has no database path", nil)
		}
		return u.Path, nil
	}
	if u.Host == ":memory:" && u.Path == "" {
		return ":memory:", nil
	}
	if u.Host != "" {
		return u.Host + u.Path, nil
	}
	if u.Path != "" {
		p := strings.TrimPrefix(u.Path, "/")
		if p == "" {
			return "", core.NewError(core.CodeInvalidArgument, "sql: sqlite url has no database path", nil)
		}
		return p, nil
	}
	if u.Opaque != "" {
		return u.Opaque, nil
	}
	rest := strings.TrimPrefix(raw, "sqlite://")
	if rest != "" && !strings.HasPrefix(rest, "/") {
		return rest, nil
	}
	return "", core.NewError(core.CodeInvalidArgument, "sql: sqlite url has no database path", nil)
}

func resolveSQLitePath(path string) (string, error) {
	if path == "/:memory:" {
		path = ":memory:"
	}
	if sqliteMemoryPath(path) {
		return path, nil
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path), nil
	}
	return abs, nil
}

func sqliteDSN(dbPath string) string {
	filePart := dbPath
	if !sqliteMemoryPath(dbPath) {
		filePart = escapeSQLiteFilePath(dbPath)
	}
	return "file:" + filePart +
		"?_pragma=busy_timeout(10000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_txlock=immediate"
}

func escapeSQLiteFilePath(path string) string {
	sep := string(filepath.Separator)
	parts := strings.Split(path, sep)
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, sep)
}

// redactURL removes credentials from raw for safe inclusion in errors.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "postgres://***"
	}
	if u.User != nil {
		u.User = url.UserPassword(u.User.Username(), "***")
	}
	q := u.Query()
	for _, k := range libpqCredentialQueryKeys {
		if q.Get(k) != "" {
			q.Set(k, "***")
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// libpqCredentialQueryKeys are query parameters that may carry secrets (libpq/pgx).
var libpqCredentialQueryKeys = []string{
	"password",
	"sslpassword",
	"passfile",
	"sslcert",
	"sslkey",
	"sslrootcert",
}
