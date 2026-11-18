package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/epzero/basamento/core"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func wrapOpenErr(p parsedURL, err error) error {
	if err == nil {
		return nil
	}
	msg := fmt.Sprintf("sql: open %s", redactURL(p.raw))
	details := map[string]any{"url": redactURL(p.raw)}
	if p.dialect == dialectPostgres && isUnavailable(err) {
		return &core.Error{
			Code:      core.CodeUnavailable,
			Message:   msg,
			Retryable: true,
			Details:   details,
			Cause:     scrubCause(err, p.raw),
		}
	}
	if mapped := mapExecErr(err); core.CodeOf(mapped) != core.CodeInternal {
		return mapped
	}
	return core.NewError(core.CodeInternal, msg, details)
}

func isUnavailable(err error) bool {
	if errorsAsConnectError(err) || errorsAsParseConfigError(err) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	s := strings.ToLower(safeErrString(err))
	return strings.Contains(s, "connection refused") ||
		strings.Contains(s, "no such host") ||
		strings.Contains(s, "dial tcp") && strings.Contains(s, "connect")
}

// scrubCause keeps driver text out of an error. When the URL carried a secret,
// the cause is the fixed text of REQ-SQL-025; a connect failure without one
// still gets a fixed cause, and any other text is stripped of URL secrets.
func scrubCause(err error, rawURL string) error {
	if urlHadSecret(rawURL) {
		return errors.New(withheldDriverText)
	}
	if errorsAsConnectError(err) {
		return errors.New("connection failed")
	}
	return errors.New(redactSecretsInMsg(safeErrString(err), urlSecrets(rawURL)))
}

func errorsAsConnectError(err error) bool {
	var conn *pgconn.ConnectError
	return errors.As(err, &conn)
}

// errorsAsParseConfigError reports a Postgres URL the driver cannot honor
// (for example a credential file that cannot be read). The database is not
// reached, so opening fails as unavailable (REQ-SQL-003).
func errorsAsParseConfigError(err error) bool {
	var perr *pgconn.ParseConfigError
	return errors.As(err, &perr)
}

// isConnectionFailure reports whether a query failed because its connection
// went away (a database that became unreachable, REQ-SQL-023).
func isConnectionFailure(err error) bool {
	if err == nil || errorsAsConnectError(err) {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	s := strings.ToLower(safeErrString(err))
	return strings.Contains(s, "connection reset") ||
		strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "unexpected eof") ||
		strings.Contains(s, "connection refused") ||
		strings.Contains(s, "connection closed") ||
		strings.Contains(s, "bad connection") ||
		strings.Contains(s, "eof")
}

func safeErrString(err error) string {
	if err == nil || errorsAsConnectError(err) {
		return ""
	}
	return err.Error()
}

func urlHadSecret(raw string) bool {
	return len(urlSecrets(raw)) > 0
}

func urlSecrets(raw string) []string {
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	var out []string
	if u.User != nil {
		if p, ok := u.User.Password(); ok && p != "" {
			out = append(out, p)
		}
	}
	q := u.Query()
	for _, k := range libpqCredentialQueryKeys {
		if v := q.Get(k); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func redactSecretsInMsg(msg string, secrets []string) string {
	for _, s := range secrets {
		msg = strings.ReplaceAll(msg, s, "***")
		if enc := url.QueryEscape(s); enc != s {
			msg = strings.ReplaceAll(msg, enc, "***")
		}
		if enc := url.PathEscape(s); enc != s {
			msg = strings.ReplaceAll(msg, enc, "***")
		}
	}
	return msg
}

func mapExecErr(err error) error {
	if err == nil {
		return nil
	}
	var pe *core.Error
	if errors.As(err, &pe) {
		return pe
	}
	if errors.Is(err, context.Canceled) {
		return &core.Error{Code: core.CodeCancelled, Message: "sql: cancelled", Cause: context.Canceled}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &core.Error{Code: core.CodeTimeout, Message: "sql: timeout", Cause: context.DeadlineExceeded}
	}
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
		return core.NewError(core.CodeNotFound, "sql: not found", nil)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return mapPgErr(pgErr)
	}
	if errorsAsConnectError(err) || isConnectionFailure(err) {
		return &core.Error{
			Code:      core.CodeUnavailable,
			Message:   "sql: connection failed",
			Retryable: true,
		}
	}
	if sqliteRetryable(err) {
		return &core.Error{
			Code:      core.CodeUnavailable,
			Message:   "sql: database busy",
			Retryable: true,
			Details:   sqliteDetails(err),
		}
	}
	if code, ok := sqliteConstraintClass(err); ok {
		return core.NewError(code, "sql: constraint violation", sqliteDetails(err))
	}
	return core.NewError(core.CodeInternal, "sql: execution failed", nil)
}

// mapMigrationErr maps migration failures to CodeInternal except for cancelled,
// timeout, and unavailable conditions that mapExecErr recognizes.
func mapMigrationErr(err error) error {
	if err == nil {
		return nil
	}
	var pe *core.Error
	if errors.As(err, &pe) {
		switch pe.Code {
		case core.CodeCancelled, core.CodeTimeout, core.CodeUnavailable:
			return pe
		}
		return core.NewError(core.CodeInternal, "sql: migration failed", nil)
	}
	mapped := mapExecErr(err)
	switch core.CodeOf(mapped) {
	case core.CodeCancelled, core.CodeTimeout, core.CodeUnavailable:
		return mapped
	default:
		return core.NewError(core.CodeInternal, "sql: migration failed", nil)
	}
}

func mapPgErr(pgErr *pgconn.PgError) error {
	details := map[string]any{"sqlstate": pgErr.Code}
	code, retryable := pgSQLStateMapping(pgErr.Code)
	return &core.Error{
		Code:      code,
		Message:   "sql: execution failed",
		Retryable: retryable,
		Details:   details,
	}
}

func pgSQLStateMapping(sqlstate string) (core.Code, bool) {
	switch sqlstate {
	case "23505", "23503":
		return core.CodeConflict, false
	case "23502", "23514", "42601", "22P02", "22023":
		return core.CodeInvalidArgument, false
	case "40001", "40P01":
		return core.CodeUnavailable, true
	case "57P01", "57P02", "57P03", "08000", "08003", "08006", "08001", "08004", "08007", "08P01":
		return core.CodeUnavailable, true
	default:
		if len(sqlstate) >= 2 && sqlstate[:2] == "53" {
			return core.CodeUnavailable, true
		}
		if len(sqlstate) >= 2 && sqlstate[:2] == "08" {
			return core.CodeUnavailable, true
		}
		return core.CodeInternal, false
	}
}

// withheldDriverText replaces driver error text when the store URL carried a
// secret (REQ-SQL-025).
const withheldDriverText = "driver error withheld"

func sqliteDetails(err error) map[string]any {
	if code := sqliteResultCode(err); code != 0 {
		return map[string]any{"sqlite_code": code}
	}
	return nil
}
