package sql

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/epzero/basamento/core"
)

// Execer runs SQL with dialect-correct placeholders for either the pool or an
// active transaction when created by Store.InTx. Repository SQL uses ? on both
// dialects; on Postgres each ? outside single- and double-quoted text, line and
// block comments, and dollar-quoted bodies becomes $n in order. Authored $n
// markers are rejected with invalid_argument. Any ? outside those regions is a
// bind marker, so Postgres jsonb operators ?, ?|, and ?& cannot appear in SQL;
// use jsonb_exists, jsonb_exists_any, and jsonb_exists_all instead. A
// driver.Valuer bind argument is converted once for this check and once by
// database/sql, so Valuers must be side-effect free.
type Execer struct {
	db      *sql.DB
	tx      *sql.Tx
	dialect dialect
}

func (e *Execer) rewrite(q string) (string, error) {
	if e.dialect == dialectSQLite {
		return q, nil
	}
	return rewritePostgresPlaceholders(q)
}

// rewritePostgresPlaceholders converts ? bind markers to $n for Postgres, without
// touching ? inside single-quoted strings, Postgres escape and typed strings
// (E/e, B/b, X/x, U&/u& only when not preceded by an identifier character),
// double-quoted identifiers, line or block comments, or dollar-quoted bodies
// ($ only when not preceded by an identifier character). Queries must not use $n markers.
func rewritePostgresPlaceholders(q string) (string, error) {
	var b strings.Builder
	n := 0
	for i := 0; i < len(q); {
		switch {
		case q[i] == '-' && i+1 < len(q) && q[i+1] == '-':
			j := i
			for j < len(q) && q[j] != '\n' {
				j++
			}
			b.WriteString(q[i:j])
			i = j
		case q[i] == '/' && i+1 < len(q) && q[i+1] == '*':
			j := skipBlockCommentPostgres(q, i)
			b.WriteString(q[i:j])
			i = j
		default:
			if j, ok, err := skipPostgresPrefixedString(q, i); ok {
				if err != nil {
					return "", err
				}
				b.WriteString(q[i:j])
				i = j
				break
			}
			switch q[i] {
			case '\'':
				j := skipSingleQuotedString(q, i)
				b.WriteString(q[i:j])
				i = j
			case '"':
				j := i + 1
				for j < len(q) {
					if q[j] == '"' {
						if j+1 < len(q) && q[j+1] == '"' {
							j += 2
							continue
						}
						j++
						break
					}
					j++
				}
				b.WriteString(q[i:j])
				i = j
			case '$':
				if i > 0 && postgresIdentTailByte(q[i-1]) {
					b.WriteByte(q[i])
					i++
					break
				}
				if _, j, ok := skipDollarQuoted(q, i); ok {
					b.WriteString(q[i:j])
					i = j
					break
				}
				if i+1 < len(q) && q[i+1] >= '0' && q[i+1] <= '9' {
					return "", core.NewError(core.CodeInvalidArgument, "sql: query uses $n placeholders; use ?", nil)
				}
				b.WriteByte(q[i])
				i++
			case '?':
				n++
				fmt.Fprintf(&b, "$%d", n)
				i++
			default:
				b.WriteByte(q[i])
				i++
			}
		}
	}
	return b.String(), nil
}

func skipBlockCommentPostgres(q string, i int) int {
	depth := 1
	j := i + 2
	for j < len(q) && depth > 0 {
		if j+1 < len(q) && q[j] == '/' && q[j+1] == '*' {
			depth++
			j += 2
			continue
		}
		if j+1 < len(q) && q[j] == '*' && q[j+1] == '/' {
			depth--
			j += 2
			continue
		}
		j++
	}
	return j
}

func postgresIdentTailByte(b byte) bool {
	if b == '_' || b == '$' {
		return true
	}
	if b >= '0' && b <= '9' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' {
		return true
	}
	return b >= 0x80
}

func skipSingleQuotedString(q string, i int) int {
	if i >= len(q) || q[i] != '\'' {
		return i
	}
	j := i + 1
	for j < len(q) {
		if q[j] == '\'' {
			if j+1 < len(q) && q[j+1] == '\'' {
				j += 2
				continue
			}
			j++
			break
		}
		j++
	}
	return j
}

// skipPostgresPrefixedString skips typed string literals (E'/e', B'/b', X'/x', U&'/u&')
// when not preceded by an identifier character. The second bool is true when i was advanced.
func skipPostgresPrefixedString(q string, i int) (int, bool, error) {
	if i > 0 && postgresIdentTailByte(q[i-1]) {
		return i, false, nil
	}
	if i+2 < len(q) && (q[i] == 'U' || q[i] == 'u') && q[i+1] == '&' && q[i+2] == '\'' {
		j, err := skipPostgresEscapeString(q, i+2)
		return j, true, err
	}
	if i+1 < len(q) {
		switch q[i] {
		case 'E', 'e':
			if q[i+1] == '\'' {
				j, err := skipPostgresEscapeString(q, i+1)
				return j, true, err
			}
		case 'B', 'b', 'X', 'x':
			if q[i+1] == '\'' {
				return skipSingleQuotedString(q, i+1), true, nil
			}
		}
	}
	return i, false, nil
}

func skipPostgresEscapeString(q string, i int) (int, error) {
	if i >= len(q) || q[i] != '\'' {
		return i, nil
	}
	j := i + 1
	for j < len(q) {
		if q[j] == '\\' && j+1 < len(q) {
			j += 2
			continue
		}
		if q[j] == '\'' {
			if j+1 < len(q) && q[j+1] == '\'' {
				j += 2
				continue
			}
			j++
			break
		}
		j++
	}
	return j, nil
}

func skipDollarQuoted(q string, i int) (delim string, end int, ok bool) {
	if i >= len(q) || q[i] != '$' {
		return "", i, false
	}
	j := i + 1
	for j < len(q) {
		r := rune(q[j])
		if r == '$' {
			break
		}
		if j == i+1 {
			if r == '_' || unicode.IsLetter(r) {
				j++
				continue
			}
			return "", i, false
		}
		if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			j++
			continue
		}
		return "", i, false
	}
	if j >= len(q) || q[j] != '$' {
		return "", i, false
	}
	delim = q[i : j+1]
	k := j + 1
	pos := strings.Index(q[k:], delim)
	if pos < 0 {
		return "", i, false
	}
	return delim, k + pos + len(delim), true
}

var sqlIdentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (e *Execer) runner() queryR {
	if e.tx != nil {
		return e.tx
	}
	return e.db
}

type queryR interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Exec runs q with args rewritten for the store dialect. String arguments must
// not contain NUL (REQ-SQL-018). Driver errors are mapped by mapExecErr.
func (e *Execer) Exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	if err := validateArgs(args); err != nil {
		return nil, err
	}
	rq, err := e.rewrite(q)
	if err != nil {
		return nil, err
	}
	res, err := e.runner().ExecContext(ctx, rq, args...)
	return res, mapExecErr(err)
}

// Query runs a read query on the pool or transaction, rejecting NUL in string arguments.
func (e *Execer) Query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	if err := validateArgs(args); err != nil {
		return nil, err
	}
	rq, err := e.rewrite(q)
	if err != nil {
		return nil, err
	}
	rows, err := e.runner().QueryContext(ctx, rq, args...)
	return rows, mapExecErr(err)
}

// Row holds the result of Execer.QueryRow; Scan reads the row and maps driver errors.
type Row struct {
	inner *sql.Row
	err   error
}

// Scan reads one row and maps driver errors.
func (r *Row) Scan(dest ...any) error {
	if r == nil {
		return core.NewError(core.CodeInternal, "sql: nil row", nil)
	}
	if r.err != nil {
		return r.err
	}
	return mapExecErr(r.inner.Scan(dest...))
}

// QueryRow runs a single-row read, rejecting NUL in string arguments before touching the database.
func (e *Execer) QueryRow(ctx context.Context, q string, args ...any) *Row {
	if err := validateArgs(args); err != nil {
		return &Row{err: err}
	}
	rq, err := e.rewrite(q)
	if err != nil {
		return &Row{err: err}
	}
	return &Row{inner: e.runner().QueryRowContext(ctx, rq, args...)}
}

// ValidateText rejects strings that contain a NUL byte (REQ-SQL-018).
func ValidateText(s string) error {
	if strings.ContainsRune(s, 0) {
		return core.NewError(core.CodeInvalidArgument, "sql: text contains nul", nil)
	}
	return nil
}

// TruncateMicrosecond converts to UTC and truncates to whole microseconds (REQ-SQL-016).
func TruncateMicrosecond(t time.Time) time.Time {
	return t.UTC().Truncate(time.Microsecond)
}

// TextOrderClause returns an ORDER BY column expression with dialect collation (REQ-SQL-017).
// col must be a simple identifier matching [A-Za-z_][A-Za-z0-9_]*; otherwise invalid_argument.
// On Postgres the result appends COLLATE "C"; on SQLite it appends COLLATE BINARY.
func TextOrderClause(dialectName, col string) (string, error) {
	if !sqlIdentRe.MatchString(col) {
		return "", core.NewError(core.CodeInvalidArgument, "sql: invalid order column", nil)
	}
	if dialectName == "postgres" {
		return col + ` COLLATE "C"`, nil
	}
	return col + ` COLLATE BINARY`, nil
}
