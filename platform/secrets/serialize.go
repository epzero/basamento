package secrets

import (
	"fmt"
	"sort"

	"google.golang.org/protobuf/encoding/protowire"
)

// marshalInner serializes p as the protobuf wire encoding of Temporal's Payload
// message (map<string,bytes> metadata = 1; bytes data = 2) without importing
// go.temporal.io (design §3).
func marshalInner(p Payload) ([]byte, error) {
	keys := make([]string, 0, len(p.Metadata))
	for k := range p.Metadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []byte
	for _, k := range keys {
		var entry []byte
		entry = protowire.AppendTag(entry, 1, protowire.BytesType)
		entry = protowire.AppendBytes(entry, []byte(k))
		entry = protowire.AppendTag(entry, 2, protowire.BytesType)
		entry = protowire.AppendBytes(entry, p.Metadata[k])
		out = protowire.AppendTag(out, 1, protowire.BytesType)
		out = protowire.AppendBytes(out, entry)
	}
	if len(p.Data) > 0 {
		out = protowire.AppendTag(out, 2, protowire.BytesType)
		out = protowire.AppendBytes(out, p.Data)
	}
	return out, nil
}

func unmarshalInner(b []byte) (Payload, error) {
	if len(b) == 0 {
		return Payload{}, nil
	}
	meta := make(map[string][]byte)
	var data []byte
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return Payload{}, errInvalidArgument(msgPayloadCannotDecrypt)
		}
		b = b[n:]
		switch num {
		case 1:
			if typ != protowire.BytesType {
				return Payload{}, errInvalidArgument(msgPayloadCannotDecrypt)
			}
			entry, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return Payload{}, errInvalidArgument(msgPayloadCannotDecrypt)
			}
			b = b[n:]
			k, v, err := parseMapEntry(entry)
			if err != nil {
				return Payload{}, errInvalidArgument(msgPayloadCannotDecrypt)
			}
			meta[k] = v
		case 2:
			if typ != protowire.BytesType {
				return Payload{}, errInvalidArgument(msgPayloadCannotDecrypt)
			}
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return Payload{}, errInvalidArgument(msgPayloadCannotDecrypt)
			}
			b = b[n:]
			data = append([]byte(nil), v...)
		default:
			n := protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return Payload{}, errInvalidArgument(msgPayloadCannotDecrypt)
			}
			b = b[n:]
		}
	}
	return Payload{Metadata: meta, Data: data}, nil
}

func parseMapEntry(entry []byte) (string, []byte, error) {
	var key string
	var val []byte
	var haveKey, haveVal bool
	for len(entry) > 0 {
		num, typ, n := protowire.ConsumeTag(entry)
		if n < 0 {
			return "", nil, fmt.Errorf("bad entry")
		}
		entry = entry[n:]
		switch num {
		case 1:
			if typ != protowire.BytesType {
				return "", nil, fmt.Errorf("bad key")
			}
			k, n := protowire.ConsumeBytes(entry)
			if n < 0 {
				return "", nil, fmt.Errorf("bad key")
			}
			entry = entry[n:]
			key = string(k)
			haveKey = true
		case 2:
			if typ != protowire.BytesType {
				return "", nil, fmt.Errorf("bad val")
			}
			v, n := protowire.ConsumeBytes(entry)
			if n < 0 {
				return "", nil, fmt.Errorf("bad val")
			}
			entry = entry[n:]
			val = append([]byte(nil), v...)
			haveVal = true
		default:
			n := protowire.ConsumeFieldValue(num, typ, entry)
			if n < 0 {
				return "", nil, fmt.Errorf("bad entry field")
			}
			entry = entry[n:]
		}
	}
	if !haveKey || !haveVal {
		return "", nil, fmt.Errorf("incomplete map entry")
	}
	return key, val, nil
}

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
