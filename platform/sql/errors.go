package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	if errorsAsConnectError(err) {
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

func scrubCause(err error, rawURL string) error {
	if urlHadSecret(rawURL) || errorsAsConnectError(err) {
		return errors.New("connection failed")
	}
	pass := urlPassword(rawURL)
	if pass == "" {
		return errors.New("connection failed")
	}
	return errors.New(redactSecretsInMsg(safeErrString(err), urlSecrets(rawURL)))
}

func errorsAsConnectError(err error) bool {
	var conn *pgconn.ConnectError
	return errors.As(err, &conn)
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

func urlPassword(raw string) string {
	for _, s := range urlSecrets(raw) {
		return s
	}
	return ""
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
	if errorsAsConnectError(err) {
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
			Details:   map[string]any{"sqlite": "busy"},
		}
	}
	if ce := sqliteConstraintCode(err); ce != 0 {
		return core.NewError(core.CodeConflict, "sql: constraint violation", map[string]any{"sqlite_code": ce})
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
	case "23505", "23503", "23502", "23514":
		return core.CodeConflict, false
	case "42601", "22P02", "22023":
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
		return core.CodeInvalidArgument, false
	}
}

func sqliteRetryable(err error) bool {
	if err == nil || errorsAsConnectError(err) {
		return false
	}
	s := strings.ToUpper(safeErrString(err))
	return strings.Contains(s, "SQLITE_BUSY") ||
		strings.Contains(s, "SQLITE_LOCKED") ||
		strings.Contains(s, "DATABASE IS LOCKED")
}
