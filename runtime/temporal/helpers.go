package temporal

import (
	"encoding/json"
	"fmt"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

const defaultAttempts = 3

func mergeOptions(o, d runtime.StepOptions) runtime.StepOptions {
	if o.Queue == "" {
		o.Queue = d.Queue
	}
	if o.StartToClose == 0 {
		o.StartToClose = d.StartToClose
	}
	if o.Heartbeat == 0 {
		o.Heartbeat = d.Heartbeat
	}
	if o.MaxAttempts == 0 {
		o.MaxAttempts = d.MaxAttempts
	}
	if len(o.NonRetryable) == 0 {
		o.NonRetryable = d.NonRetryable
	}
	if o.IdempotencyKey == "" {
		o.IdempotencyKey = d.IdempotencyKey
	}
	return o
}

func rawInput(in any) (json.RawMessage, error) {
	switch value := in.(type) {
	case nil:
		return nil, nil
	case json.RawMessage:
		return append(json.RawMessage(nil), value...), nil
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, core.NewError(core.CodeInvalidArgument, "temporal: encode step input", map[string]any{"cause": err.Error()})
		}
		return raw, nil
	}
}

func fnv64(s string) uint64 {
	const offset, prime = 14695981039346656037, 1099511628211
	h := uint64(offset)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime
	}
	return h
}

func flowPanicErr() error {
	return core.NewError(core.CodeInternal, "temporal: the flow host panicked", nil)
}

func stepPanicErr(step string) error {
	return core.NewError(core.CodeInternal, "temporal: step panicked", map[string]any{"step": step})
}

func unsupportedOp(name string) error {
	return core.NewError(core.CodeUnsupported,
		fmt.Sprintf("temporal: %s arrives with the next change of WP-L5-02 (EP0-31)", name), nil)
}
