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

// redactURL replaces the password of the user information and the values of
// the credential query parameters with the literal *** (REQ-SQL-024). It edits
// the raw URL text: other parameters keep their order and escaping, and the
// marker itself is never percent-encoded.
func redactURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return redactedURLMarker
	}
	prefix := u.Scheme + "://"
	if !strings.HasPrefix(raw, prefix) {
		return redactedURLMarker
	}
	rest := raw[len(prefix):]
	authorityEnd := strings.IndexAny(rest, "/?#")
	authority, tail := rest, ""
	if authorityEnd >= 0 {
		authority, tail = rest[:authorityEnd], rest[authorityEnd:]
	}
	path, query, fragment := splitURLTail(tail)
	return prefix + redactAuthority(authority) + path + redactQuery(query) + fragment
}

const redactedURLMarker = "***"

// redactAuthority replaces the password of a userinfo segment, keeping the
// user name and the host as written.
func redactAuthority(authority string) string {
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return authority
	}
	userinfo, host := authority[:at], authority[at+1:]
	colon := strings.Index(userinfo, ":")
	if colon < 0 {
		return authority
	}
	return userinfo[:colon] + ":" + redactedURLMarker + "@" + host
}

// splitURLTail splits a URL tail into path, raw query (without ?) and fragment
// (with #).
func splitURLTail(tail string) (path, query, fragment string) {
	if i := strings.Index(tail, "#"); i >= 0 {
		tail, fragment = tail[:i], tail[i:]
	}
	if i := strings.Index(tail, "?"); i >= 0 {
		return tail[:i], tail[i+1:], fragment
	}
	return tail, "", fragment
}

// redactQuery rewrites the credential parameters of a raw query without
// re-encoding the other parameters.
func redactQuery(query string) string {
	if query == "" {
		return ""
	}
	parts := strings.Split(query, "&")
	for i, part := range parts {
		key, _, hasValue := strings.Cut(part, "=")
		if !hasValue {
			continue
		}
		decoded, err := url.QueryUnescape(key)
		if err != nil {
			decoded = key
		}
		if isCredentialQueryKey(decoded) {
			parts[i] = key + "=" + redactedURLMarker
		}
	}
	return "?" + strings.Join(parts, "&")
}

func isCredentialQueryKey(key string) bool {
	for _, k := range libpqCredentialQueryKeys {
		if key == k {
			return true
		}
	}
	return false
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
