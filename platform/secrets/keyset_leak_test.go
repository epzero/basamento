package secrets

import (
	"bytes"
	"testing"

	tinkpb "github.com/tink-crypto/tink-go/v2/proto/tink_go_proto"
	"google.golang.org/protobuf/proto"
)

func dataKeyMaterialBytes(plain []byte) ([][]byte, error) {
	ks := &tinkpb.Keyset{}
	if err := proto.Unmarshal(plain, ks); err != nil {
		return nil, err
	}
	var out [][]byte
	for _, k := range ks.GetKey() {
		if kd := k.GetKeyData(); kd != nil {
			v := kd.GetValue()
			if len(v) == 0 {
				continue
			}
			out = append(out, append([]byte(nil), v...))
		}
	}
	return out, nil
}

func assertWrappedLacksDataKeyMaterial(wrapped, plain []byte) error {
	blobs, err := dataKeyMaterialBytes(plain)
	if err != nil {
		return err
	}
	if len(blobs) == 0 {
		return errInternal("no key material parsed")
	}
	for _, b := range blobs {
		if len(b) > 0 && bytes.Contains(wrapped, b) {
			return errInvalidArgument("wrapped keyset contains data key bytes")
		}
	}
	return nil
}

func TestWrappedRecordRejectsAdjacentDataKeyMaterial(t *testing.T) {
	t.Parallel()
	h, err := newDataKeyset()
	if err != nil {
		t.Fatal(err)
	}
	plain, err := serializeKeyset(h)
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := dataKeyMaterialBytes(plain)
	if err != nil || len(blobs) == 0 {
		t.Fatal("no key material")
	}
	wrapped := append([]byte("ciphertext-only-"), blobs[0]...)
	if err := assertWrappedLacksDataKeyMaterial(wrapped, plain); err == nil {
		t.Fatal("expected leak detection")
	}
}
