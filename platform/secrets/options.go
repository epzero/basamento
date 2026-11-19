package secrets

import "log/slog"

// Option configures secrets components (Codec, Port).
type Option func(*options)

type options struct {
	logger *slog.Logger
}

func applyOptions(opts []Option) options {
	var o options
	for _, fn := range opts {
		if fn != nil {
			fn(&o)
		}
	}
	return o
}

// WithLogger directs operational log records from the component. When nil, each
// call site uses slog.Default() at that moment.
func WithLogger(l *slog.Logger) Option {
	return func(o *options) { o.logger = l }
}

func (o *options) loggerOrDefault() *slog.Logger {
	if o != nil && o.logger != nil {
		return o.logger
	}
	return slog.Default()
}
