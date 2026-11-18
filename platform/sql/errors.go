package sql

import (
	"context"
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
	if pe, ok := err.(*core.Error); ok {
		return pe
	}
	return core.NewError(core.CodeInternal, msg, details)
}

func isUnavailable(err error) bool {
	var pgErr *pgconn.ConnectError
	if errors.As(err, &pgErr) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "connection refused") ||
		strings.Contains(s, "no such host") ||
		strings.Contains(s, "dial tcp") && strings.Contains(s, "connect")
}

func scrubCause(err error, rawURL string) error {
	pass := urlPassword(rawURL)
	if pass == "" {
		return errors.New(err.Error())
	}
	return errors.New(redactDriverMsg(err.Error(), pass))
}

func urlPassword(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return ""
	}
	p, _ := u.User.Password()
	return p
}

func redactDriverMsg(msg, password string) string {
	if password != "" {
		msg = strings.ReplaceAll(msg, password, "***")
	}
	return msg
}

// ErrorContainsSecret reports whether err exposes secret in message or details.
func ErrorContainsSecret(err error, secret string) bool {
	if err == nil || secret == "" {
		return false
	}
	if strings.Contains(err.Error(), secret) {
		return true
	}
	var pe *core.Error
	if errors.As(err, &pe) && pe.Details != nil {
		for _, v := range pe.Details {
			if strings.Contains(fmt.Sprint(v), secret) {
				return true
			}
		}
	}
	return false
}

func mapExecErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return core.NewError(core.CodeNotFound, "sql: not found", nil)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return core.NewError(core.CodeConflict, "sql: unique violation", nil)
		case "23503":
			return core.NewError(core.CodeConflict, "sql: foreign key violation", nil)
		}
	}
	if ce := sqliteConstraintCode(err); ce != 0 {
		return core.NewError(core.CodeConflict, "sql: constraint violation", nil)
	}
	if strings.Contains(strings.ToLower(err.Error()), "unique") {
		return core.NewError(core.CodeConflict, "sql: unique violation", nil)
	}
	if strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		return core.NewError(core.CodeConflict, "sql: foreign key violation", nil)
	}
	return fmt.Errorf("sql: %w", err)
}
