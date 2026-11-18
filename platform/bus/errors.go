package bus

import (
	"errors"
	"strings"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/epzero/basamento/core"
)

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

func mapConnErr(err error) error {
	if err == nil {
		return nil
	}
	var ce *core.Error
	if errors.As(err, &ce) {
		return err
	}
	if errors.Is(err, nats.ErrMaxPayload) || isMessageTooLarge(err) {
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

func isMessageTooLarge(err error) bool {
	var api *jetstream.APIError
	if errors.As(err, &api) {
		desc := strings.ToLower(api.Description)
		return strings.Contains(desc, "max message") ||
			strings.Contains(desc, "message size") ||
			strings.Contains(desc, "maximum payload")
	}
	return false
}
