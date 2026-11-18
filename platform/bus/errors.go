package bus

import (
	"context"
	"errors"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/epzero/basamento/core"
)

// jsAPIStreamMessageExceedsMaximum is JetStream API err_code 10054
// (nats-server/v2 server/jetstream_errors_generated.go: JSStreamMessageExceedsMaximumErr).
const jsAPIStreamMessageExceedsMaximum uint16 = 10054

func invalidSubject() error {
	return core.NewError(core.CodeInvalidArgument, "malformed subject", nil)
}

func invalidKey() error {
	return core.NewError(core.CodeInvalidArgument, "malformed key", nil)
}

func invalidTTL() error {
	return core.NewError(core.CodeInvalidArgument, "negative ttl", nil)
}

func invalidURL() error {
	return core.NewError(core.CodeInvalidArgument, "malformed nats url", nil)
}

func messageTooLarge() error {
	return core.NewError(core.CodeInvalidArgument, "message too large", nil)
}

func valueTooLarge() error {
	return core.NewError(core.CodeInvalidArgument, "kv value too large", nil)
}

func plaintextCredential() error {
	return core.NewError(core.CodeInvalidArgument, "plaintext credential requires loopback or tls", nil)
}

func clusterPayloadTooSmall() error {
	return core.NewError(core.CodeInvalidArgument, "cluster max_payload too small for bus", nil)
}

func unavailable() error {
	return core.NewError(core.CodeUnavailable, "bus unavailable", nil)
}

func embeddedNotReady() error {
	return core.NewError(core.CodeUnavailable, "embedded nats not ready", nil)
}

func notFoundKV() error {
	return core.NewError(core.CodeNotFound, "key absent", nil)
}

func purged(floor uint64) error {
	return core.NewError(core.CodeNotFound, "purged", map[string]any{"purged_through": floor})
}

func conflict(msg string) error {
	return core.NewError(core.CodeConflict, msg, nil)
}

func kvRecordCorrupt() error {
	return core.NewError(core.CodeInternal, "kv record corrupt", nil)
}

func cancelledErr() error {
	return core.NewError(core.CodeCancelled, "cancelled", nil)
}

func timeoutErr() error {
	return core.NewError(core.CodeTimeout, "timeout", nil)
}

func mapConnErr(err error) error {
	if err == nil {
		return nil
	}
	var ce *core.Error
	if errors.As(err, &ce) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return cancelledErr()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return timeoutErr()
	}
	if errors.Is(err, nats.ErrMaxPayload) {
		return messageTooLarge()
	}
	if errors.Is(err, jetstream.ErrInvalidKey) || errors.Is(err, nats.ErrBadSubject) {
		return invalidKey()
	}
	var api *jetstream.APIError
	if errors.As(err, &api) && uint16(api.ErrorCode) == jsAPIStreamMessageExceedsMaximum {
		return messageTooLarge()
	}
	return unavailable()
}

func errorsIsNotFound(err error) bool {
	return errors.Is(err, jetstream.ErrKeyNotFound)
}

func errorsIsConflict(err error) bool {
	return errors.Is(err, jetstream.ErrKeyExists) || errors.Is(err, jetstream.ErrKeyDeleted)
}
