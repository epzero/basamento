package secrets

import (
	"encoding/binary"
	"fmt"
	"sort"
)

const innerVersion = 1

// marshalInner serializes payload metadata and data for encryption inside the
// ciphertext (design §3: protobuf Payload shape, length-prefixed keys).
func marshalInner(p Payload) ([]byte, error) {
	keys := make([]string, 0, len(p.Metadata))
	for k := range p.Metadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	n := 1 + 4 // version + metadata count
	for _, k := range keys {
		n += 4 + len(k) + 4 + len(p.Metadata[k])
	}
	n += 4 + len(p.Data)
	buf := make([]byte, n)
	off := 0
	buf[off] = innerVersion
	off++
	binary.BigEndian.PutUint32(buf[off:], uint32(len(keys)))
	off += 4
	for _, k := range keys {
		off = putPrefixed(buf, off, []byte(k))
		off = putPrefixed(buf, off, p.Metadata[k])
	}
	putPrefixed(buf, off, p.Data)
	return buf, nil
}

func unmarshalInner(b []byte) (Payload, error) {
	if len(b) < 1 {
		return Payload{}, errInvalidArgument(msgPayloadCannotDecrypt)
	}
	if b[0] != innerVersion {
		return Payload{}, errInvalidArgument(msgPayloadCannotDecrypt)
	}
	off := 1
	if len(b) < off+4 {
		return Payload{}, errInvalidArgument(msgPayloadCannotDecrypt)
	}
	n := binary.BigEndian.Uint32(b[off:])
	off += 4
	meta := make(map[string][]byte, n)
	for i := uint32(0); i < n; i++ {
		var key []byte
		var err error
		key, off, err = readPrefixed(b, off)
		if err != nil {
			return Payload{}, errInvalidArgument(msgPayloadCannotDecrypt)
		}
		var val []byte
		val, off, err = readPrefixed(b, off)
		if err != nil {
			return Payload{}, errInvalidArgument(msgPayloadCannotDecrypt)
		}
		meta[string(key)] = val
	}
	var data []byte
	var err error
	data, off, err = readPrefixed(b, off)
	if err != nil || off != len(b) {
		return Payload{}, errInvalidArgument(msgPayloadCannotDecrypt)
	}
	return Payload{Metadata: meta, Data: data}, nil
}

func putPrefixed(buf []byte, off int, v []byte) int {
	binary.BigEndian.PutUint32(buf[off:], uint32(len(v)))
	off += 4
	copy(buf[off:], v)
	return off + len(v)
}

func readPrefixed(b []byte, off int) ([]byte, int, error) {
	if len(b) < off+4 {
		return nil, off, fmt.Errorf("truncated")
	}
	n := int(binary.BigEndian.Uint32(b[off:]))
	off += 4
	if len(b) < off+n {
		return nil, off, fmt.Errorf("truncated")
	}
	return append([]byte(nil), b[off:off+n]...), off + n, nil
}

// MarshalInnerForTest exports inner serialization for unit tests.
func MarshalInnerForTest(p Payload) ([]byte, error) { return marshalInner(p) }

// UnmarshalInnerForTest exports inner parsing for unit tests.
func UnmarshalInnerForTest(b []byte) (Payload, error) { return unmarshalInner(b) }

func payloadAssocData(tenant string, keyID string) []byte {
	// basamento.payload.v1, zero, tenant, zero, key id (design §3).
	out := make([]byte, 0, len("basamento.payload.v1")+1+len(tenant)+1+len(keyID))
	out = append(out, "basamento.payload.v1"...)
	out = append(out, 0)
	out = append(out, tenant...)
	out = append(out, 0)
	out = append(out, keyID...)
	return out
}
