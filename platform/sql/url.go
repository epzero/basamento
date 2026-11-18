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
	// sqlitePath is the absolute database file path for SQLite URLs.
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
		path := strings.TrimPrefix(u.Path, "/")
		if path == "" && u.Opaque != "" {
			path = u.Opaque
		}
		if path == "" && strings.HasPrefix(raw, "sqlite://") {
			rest := strings.TrimPrefix(raw, "sqlite://")
			if rest != "" && !strings.HasPrefix(rest, "/") {
				path = rest
			}
		}
		if path == "" {
			return parsedURL{}, core.NewError(core.CodeInvalidArgument, "sql: sqlite url has no database path", nil)
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = filepath.Clean(path)
		}
		return parsedURL{dialect: dialectSQLite, sqlitePath: abs, raw: raw}, nil
	case "postgres", "postgresql":
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

// redactURL removes credentials from raw for safe inclusion in errors.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "postgres://***"
	}
	if u.User != nil {
		u.User = url.UserPassword(u.User.Username(), "***")
	}
	return u.String()
}
