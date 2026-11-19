package secrets

import (
	"context"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestDecodeFailuresReturnZeroPayload(t *testing.T) {
	t.Parallel()
	ks := &budgetKey{id: "mk"}
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	c, err := NewCodec(store, []KeyService{ks})
	if err != nil {
		t.Fatal(err)
	}
	acme := core.WithTenant(context.Background(), "acme")
	globex := core.WithTenant(context.Background(), "globex")
	enc, err := c.Encode(acme, plainPayload("data"))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		ctx  context.Context
		p    Payload
	}{
		{
			name: "tampered",
			ctx:  acme,
			p: func() Payload {
				bad := enc
				bad.Data = append([]byte(nil), enc.Data...)
				if len(bad.Data) > 0 {
					bad.Data[0] ^= 1
				}
				return bad
			}(),
		},
		{
			name: "other tenant",
			ctx:  globex,
			p:    enc,
		},
		{
			name: "unknown key id",
			ctx:  acme,
			p: Payload{
				Metadata: map[string][]byte{
					"encoding":          []byte(EncodingEncrypted),
					MetaEncryptionKeyID: []byte("999999"),
				},
				Data: enc.Data,
			},
		},
		{
			name: "not encrypted",
			ctx:  acme,
			p:    plainPayload("plain"),
		},
		{
			name: "no tenant",
			ctx:  context.Background(),
			p:    enc,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := c.Decode(tc.ctx, tc.p)
			if err == nil {
				t.Fatal("expected error")
			}
			if !payloadIsZero(out) {
				t.Fatalf("non-zero payload on error: %+v", out)
			}
		})
	}
}

func payloadIsZero(p Payload) bool {
	if len(p.Data) != 0 {
		return false
	}
	for k, v := range p.Metadata {
		if len(k) > 0 || len(v) > 0 {
			return false
		}
	}
	return true
}
