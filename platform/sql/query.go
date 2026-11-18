package sql

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/epzero/basamento/core"
)

var placeholderRe = regexp.MustCompile(`\?`)

// Execer runs SQL with dialect-correct placeholders for either the pool or an
// active transaction when created by Store.InTx.
type Execer struct {
	db      *sql.DB
	tx      *sql.Tx
	dialect dialect
}

func (e *Execer) rewrite(q string) string {
	if e.dialect == dialectSQLite {
		return q
	}
	n := 0
	return placeholderRe.ReplaceAllStringFunc(q, func(string) string {
		n++
		return fmt.Sprintf("$%d", n)
	})
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
	res, err := e.runner().ExecContext(ctx, e.rewrite(q), args...)
	return res, mapExecErr(err)
}

// Query runs a read query on the pool or transaction, rejecting NUL in string arguments.
func (e *Execer) Query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	if err := validateArgs(args); err != nil {
		return nil, err
	}
	rows, err := e.runner().QueryContext(ctx, e.rewrite(q), args...)
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
	return &Row{inner: e.runner().QueryRowContext(ctx, e.rewrite(q), args...)}
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
