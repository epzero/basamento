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

// Execer runs SQL with dialect-correct placeholders.
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

func (e *Execer) Exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	res, err := e.runner().ExecContext(ctx, e.rewrite(q), args...)
	return res, mapExecErr(err)
}

func (e *Execer) Query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return e.runner().QueryContext(ctx, e.rewrite(q), args...)
}

func (e *Execer) QueryRow(ctx context.Context, q string, args ...any) *sql.Row {
	return e.runner().QueryRowContext(ctx, e.rewrite(q), args...)
}

func validateText(s string) error {
	if strings.ContainsRune(s, 0) {
		return core.NewError(core.CodeInvalidArgument, "sql: text contains nul", nil)
	}
	return nil
}

// truncateMicrosecond converts to UTC and truncates to whole microseconds (REQ-SQL-016).
func truncateMicrosecond(t time.Time) time.Time {
	return t.UTC().Truncate(time.Microsecond)
}

func textOrderClause(d dialect, col string) string {
	if d == dialectSQLite {
		return col + ` COLLATE BINARY`
	}
	return col + ` COLLATE "C"`
}
