package bus

import (
	"errors"

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

func messageTooLarge() error {
	return core.NewError(core.CodeInvalidArgument, "message too large", nil)
}

func unavailable() error {
	return core.NewError(core.CodeUnavailable, "bus unavailable", nil)
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
	return core.NewError(core.CodeUnavailable, "bus unavailable", map[string]any{"cause": err.Error()})
}
