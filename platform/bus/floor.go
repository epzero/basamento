package bus

import (
	"context"
	"errors"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/epzero/basamento/core"
)

// raisePurgeFloor sets pf.<subject> to at least want, using create/update with
// revision checks so it only ever moves upward. A crash after this write still
// leaves retained messages until Purge completes; retrying Purge is idempotent
// and closes REQ-BUS-008 (floor-first, not purge-first, which could leave a
// gap if the floor write never ran).
func raisePurgeFloor(ctx context.Context, meta jetstream.KeyValue, subject string, want uint64) error {
	if want == 0 {
		return nil
	}
	key := metaKey(metaPF, subject)
	val := uint64Bytes(want)
	for {
		ent, err := meta.Get(ctx, key)
		if err != nil {
			if errorsIsNotFound(err) {
				_, err := meta.Create(ctx, key, val)
				if err == nil {
					return nil
				}
				if errorsIsConflict(err) {
					continue
				}
				return mapConnErr(err)
			}
			return mapConnErr(err)
		}
		cur, err := parseUint64(ent.Value())
		if err != nil {
			return core.NewError(core.CodeInternal, "purge floor corrupt", nil)
		}
		if want <= cur {
			return nil
		}
		_, err = meta.Update(ctx, key, val, ent.Revision())
		if err == nil {
			return nil
		}
		if errorsIsConflict(err) {
			continue
		}
		return mapConnErr(err)
	}
}

func purgeFloor(ctx context.Context, meta jetstream.KeyValue, subject string) (uint64, error) {
	ent, err := meta.Get(ctx, metaKey(metaPF, subject))
	if err != nil {
		if errorsIsNotFound(err) {
			return 0, nil
		}
		return 0, mapConnErr(err)
	}
	v, err := parseUint64(ent.Value())
	if err != nil {
		return 0, core.NewError(core.CodeInternal, "purge floor corrupt", nil)
	}
	return v, nil
}

func lastSeqForSubject(ctx context.Context, str jetstream.Stream, meta jetstream.KeyValue, logicalSubject string) (uint64, error) {
	floor, err := purgeFloor(ctx, meta, logicalSubject)
	if err != nil {
		return 0, err
	}
	wire := wireSubject(logicalSubject)
	msg, err := str.GetLastMsgForSubject(ctx, wire)
	if err != nil {
		if errors.Is(err, jetstream.ErrMsgNotFound) {
			return floor, nil
		}
		return 0, mapConnErr(err)
	}
	if msg == nil {
		return floor, nil
	}
	if msg.Sequence > floor {
		return msg.Sequence, nil
	}
	return floor, nil
}

func assetMismatch(name string) error {
	return core.NewError(core.CodeInvalidArgument, "jetstream asset incompatible", map[string]any{"asset": name})
}
