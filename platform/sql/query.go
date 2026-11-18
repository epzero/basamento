package sql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/epzero/basamento/core"
)

// Execer runs SQL with dialect-correct placeholders for either the pool or an
// active transaction when created by Store.InTx. Postgres queries must use ?
// for bind parameters only (not inside literals); rewrite maps each ? outside
// literals, comments and dollar-quoted strings to $n in order.
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
// touching ? inside single-quoted strings, double-quoted identifiers, line or
// block comments, or dollar-quoted bodies. Queries must not use $n markers.
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
			j := i + 2
			for j+1 < len(q) && (q[j] != '*' || q[j+1] != '/') {
				j++
			}
			if j+1 < len(q) {
				j += 2
			} else {
				j = len(q)
			}
			b.WriteString(q[i:j])
			i = j
		case q[i] == '\'':
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
			b.WriteString(q[i:j])
			i = j
		case q[i] == '"':
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
		case q[i] == '$':
			if _, j, ok := skipDollarQuoted(q, i); ok {
				b.WriteString(q[i:j])
				i = j
				continue
			}
			if i+1 < len(q) && q[i+1] >= '0' && q[i+1] <= '9' {
				return "", core.NewError(core.CodeInvalidArgument, "sql: query uses $n placeholders; use ?", nil)
			}
			b.WriteByte(q[i])
			i++
		case q[i] == '?':
			n++
			fmt.Fprintf(&b, "$%d", n)
			i++
		default:
			b.WriteByte(q[i])
			i++
		}
	}
	return b.String(), nil
}

func skipDollarQuoted(q string, i int) (delim string, end int, ok bool) {
	if i >= len(q) || q[i] != '$' {
		return "", i, false
	}
	j := i + 1
	for j < len(q) && q[j] != '$' {
		j++
	}
	if j >= len(q) {
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

func validateArgs(args []any) error {
	for _, a := range args {
		if s, ok := a.(string); ok {
			if err := ValidateText(s); err != nil {
				return err
			}
		}
	}
	return nil
}

// TruncateMicrosecond converts to UTC and truncates to whole microseconds (REQ-SQL-016).
func TruncateMicrosecond(t time.Time) time.Time {
	return t.UTC().Truncate(time.Microsecond)
}

// TextOrderClause returns an ORDER BY column expression with dialect collation (REQ-SQL-017).
func TextOrderClause(dialectName, col string) string {
	if dialectName == "postgres" {
		return col + ` COLLATE "C"`
	}
	return col + ` COLLATE BINARY`
}
