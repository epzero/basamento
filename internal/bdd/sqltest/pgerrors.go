package sqltest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5/pgconn"
)

// urlProbeClass names why BDD_POSTGRES_URL could not be used (never echoes driver text).
type urlProbeClass string

const (
	urlClassInvalidURL      urlProbeClass = "invalid url"
	urlClassConnRefused     urlProbeClass = "connection refused"
	urlClassTimeout         urlProbeClass = "timeout"
	urlClassAuthFailed      urlProbeClass = "authentication failed"
	urlClassUnknown         urlProbeClass = "unknown"
	embeddedClassSupervisor embeddedClass = "supervisor missing"
	embeddedClassBundle     embeddedClass = "bundle not cached"
	embeddedClassUnknown    embeddedClass = "unknown"
)

type embeddedClass string

// urlProbeError is returned when a non-empty BDD_POSTGRES_URL cannot be reached.
type urlProbeError struct {
	class urlProbeClass
	cause error
}

func (e *urlProbeError) Error() string {
	return fmt.Sprintf("BDD_POSTGRES_URL is set but the server is unavailable (%s)", e.class)
}

func (e *urlProbeError) Unwrap() error { return e.cause }

// embeddedInitError is returned when embedded Postgres cannot start under strict gate.
type embeddedInitError struct {
	class embeddedClass
	cause error
}

func (e *embeddedInitError) Error() string {
	return fmt.Sprintf("embedded postgres unavailable (%s); run make pg-prefetch", e.class)
}

func (e *embeddedInitError) Unwrap() error { return e.cause }

func newURLProbeError(cause error) error {
	if cause == nil {
		return nil
	}
	return &urlProbeError{class: classifyURLProbeErr(cause), cause: cause}
}

func newEmbeddedInitError(cause error) error {
	if cause == nil {
		return nil
	}
	return &embeddedInitError{class: classifyEmbeddedInitErr(cause), cause: cause}
}

func classifyURLProbeErr(err error) urlProbeClass {
	if err == nil {
		return urlClassUnknown
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlClassInvalidURL
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if strings.HasPrefix(pgErr.Code, "28") {
			return urlClassAuthFailed
		}
	}
	var op *net.OpError
	if errors.As(err, &op) {
		if errors.Is(op.Err, syscall.ECONNREFUSED) {
			return urlClassConnRefused
		}
		if errors.Is(op.Err, syscall.ETIMEDOUT) || errors.Is(op.Err, syscall.ECONNRESET) {
			return urlClassTimeout
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return urlClassTimeout
	}
	low := strings.ToLower(err.Error())
	if strings.Contains(low, "invalid") && strings.Contains(low, "url") {
		return urlClassInvalidURL
	}
	if strings.Contains(low, "connection refused") {
		return urlClassConnRefused
	}
	if strings.Contains(low, "timeout") || strings.Contains(low, "deadline") {
		return urlClassTimeout
	}
	if strings.Contains(low, "password authentication failed") {
		return urlClassAuthFailed
	}
	return urlClassUnknown
}

func classifyEmbeddedInitErr(err error) embeddedClass {
	if err == nil {
		return embeddedClassUnknown
	}
	low := strings.ToLower(err.Error())
	if strings.Contains(low, "not cached") || strings.Contains(low, "offline mode") {
		return embeddedClassBundle
	}
	if strings.Contains(low, "embedded-postgres") || strings.Contains(low, "ep_supervisor") ||
		strings.Contains(low, "supervisor") || strings.Contains(low, "missing; run make pg-prefetch") {
		return embeddedClassSupervisor
	}
	return embeddedClassUnknown
}

// PostgresResolveSource reports embedded vs url from BDD_POSTGRES_URL (whitespace-only counts as unset).
func PostgresResolveSource() PostgresServerMode {
	if strings.TrimSpace(os.Getenv("BDD_POSTGRES_URL")) == "" {
		return PostgresServerEmbedded
	}
	return PostgresServerURL
}
