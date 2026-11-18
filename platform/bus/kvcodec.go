package bus

import (
	"encoding/binary"
	"errors"
	"time"
)

// kvRecord is the logical KV entry: value and optional expiry deadline decided
// solely by the injected core.Clock (REQ-BUS-018).
type kvRecord struct {
	Deadline time.Time // zero means no expiry
	Value    []byte
}

const deadlineSize = 8

var errKVRecordShort = errors.New("short kv record")

// KVDeadlineFromTTL maps ttl at now to a stored KV deadline for callers outside
// the store (for example bustest).
func KVDeadlineFromTTL(now time.Time, ttl time.Duration) time.Time {
	return deadlineFromTTL(now, ttl)
}

// deadlineFromTTL maps ttl to a stored deadline. Over-large ttls that would not
// fit in UnixNano or would wrap are saturated to no expiry.
func deadlineFromTTL(now time.Time, ttl time.Duration) time.Time {
	if ttl <= 0 {
		return time.Time{}
	}
	if ttl >= 200*365*24*time.Hour {
		return time.Time{}
	}
	end := now.Add(ttl)
	if !end.After(now) {
		return time.Time{}
	}
	if end.Year() >= 2262 {
		return time.Time{}
	}
	ns := end.UnixNano()
	if ns <= 0 {
		return time.Time{}
	}
	return end.UTC()
}

func encodeKV(r kvRecord) []byte {
	buf := make([]byte, deadlineSize+len(r.Value))
	var ns int64
	if !r.Deadline.IsZero() {
		ns = r.Deadline.UnixNano()
		if ns < 0 {
			ns = 0
		}
	}
	binary.BigEndian.PutUint64(buf[:deadlineSize], uint64(ns))
	copy(buf[deadlineSize:], r.Value)
	return buf
}

func decodeKV(b []byte) (kvRecord, error) {
	if len(b) < deadlineSize {
		return kvRecord{}, errKVRecordShort
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
