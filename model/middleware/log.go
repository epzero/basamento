package middleware

import (
	"log/slog"
)

// SlogCallLog writes one structured line per attempt. It carries the model,
// the attempt's client ref, the credential's reference (never its value), the
// rung, the times, the outcome and the cost; never message text or output
// (REQ-MODEL-063).
type SlogCallLog struct {
	Logger *slog.Logger
}

// Record writes one call-log entry.
func (l SlogCallLog) Record(e CallLogEntry) {
	logger := l.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info("model call attempt",
		slog.String("model", e.Model),
		slog.Int("attempt", e.Attempt),
		slog.String("client_ref", e.ClientRef),
		slog.String("credential_ref", e.CredentialRef),
		slog.String("rung", string(e.Rung)),
		slog.Time("started_at", e.Start),
		slog.Time("ended_at", e.End),
		slog.String("outcome", string(e.State)),
		slog.String("code", string(e.Code)),
		slog.Int64("cost_mpts", e.CostMpts),
	)
}
