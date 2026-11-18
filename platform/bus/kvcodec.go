package bus

import (
	"encoding/binary"
	"errors"
	"time"
)

// kvRecord is the logical KV entry: value and optional expiry deadline from the
// injected clock (not the server wall clock alone).
type kvRecord struct {
	Deadline time.Time // zero — no expiry
	Value    []byte
}

const deadlineSize = 8

func encodeKV(r kvRecord) []byte {
	buf := make([]byte, deadlineSize+len(r.Value))
	var ns int64
	if !r.Deadline.IsZero() {
		ns = r.Deadline.UnixNano()
	}
	binary.BigEndian.PutUint64(buf[:deadlineSize], uint64(ns))
	copy(buf[deadlineSize:], r.Value)
	return buf
}

func decodeKV(b []byte) (kvRecord, error) {
	if len(b) < deadlineSize {
		return kvRecord{}, errors.New("short kv record")
	}
	ns := int64(binary.BigEndian.Uint64(b[:deadlineSize]))
	var deadline time.Time
	if ns > 0 {
		deadline = time.Unix(0, ns).UTC()
	}
	return kvRecord{Deadline: deadline, Value: append([]byte(nil), b[deadlineSize:]...)}, nil
}

func expired(clock interface{ Now() time.Time }, r kvRecord) bool {
	return !r.Deadline.IsZero() && !clock.Now().Before(r.Deadline)
}
