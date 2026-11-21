package secrets

import (
	"bytes"
	"context"

	"github.com/tink-crypto/tink-go/v2/aead"
	"github.com/tink-crypto/tink-go/v2/insecurecleartextkeyset"
	"github.com/tink-crypto/tink-go/v2/keyset"
	tinkpb "github.com/tink-crypto/tink-go/v2/proto/tink_go_proto"
)

func newDataKeyset() (*keyset.Handle, error) {
	return keyset.NewHandle(aead.AES256GCMKeyTemplate())
}

func rotateDataKey(h *keyset.Handle) (*keyset.Handle, error) {
	km := keyset.NewManagerFromHandle(h)
	id, err := km.Add(aead.AES256GCMKeyTemplate())
	if err != nil {
		return nil, err
	}
	if err := km.SetPrimary(id); err != nil {
		return nil, err
	}
	return km.Handle()
}

func serializeKeyset(h *keyset.Handle) ([]byte, error) {
	buf := &bytes.Buffer{}
	w := keyset.NewBinaryWriter(buf)
	if err := insecurecleartextkeyset.Write(h, w); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func parseKeyset(b []byte) (*keyset.Handle, error) {
	return insecurecleartextkeyset.Read(keyset.NewBinaryReader(bytes.NewReader(b)))
}

func wrapKeyset(ctx context.Context, ks KeyService, tenant string, h *keyset.Handle) (StoredKeyset, error) {
	if err := tenantStringOK(tenant); err != nil {
		return StoredKeyset{}, err
	}
	plain, err := serializeKeyset(h)
	if err != nil {
		return StoredKeyset{}, err
	}
	wrapped, err := ks.Wrap(ctx, tenant, plain)
	if err != nil {
		return StoredKeyset{}, err
	}
	return StoredKeyset{
		Wrapped:     wrapped,
		MasterKeyID: ks.MasterKeyID(),
		Revision:    1,
	}, nil
}

func unwrapKeyset(ctx context.Context, tenant string, rec StoredKeyset, keys []KeyService) (*keyset.Handle, error) {
	plain, err := unwrapWithKeys(ctx, tenant, rec.Wrapped, rec.MasterKeyID, keys)
	if err != nil {
		return nil, err
	}
	return parseKeyset(plain)
}

func primaryKeyID(h *keyset.Handle) uint32 {
	return h.KeysetInfo().GetPrimaryKeyId()
}

func keyCount(h *keyset.Handle) int {
	return len(h.KeysetInfo().GetKeyInfo())
}

func keyStatusEnabled(h *keyset.Handle, id uint32) bool {
	for _, ki := range h.KeysetInfo().GetKeyInfo() {
		if ki.GetKeyId() == id && ki.GetStatus() == tinkpb.KeyStatusType_ENABLED {
			return true
		}
	}
	return false
}

func handleHasKey(h *keyset.Handle, id uint32) bool {
	return keyStatusEnabled(h, id)
}

func parseDataKeyID(idBytes []byte) (uint32, error) {
	idStr := string(idBytes)
	if len(idStr) == 0 || len(idStr) > 10 {
		return 0, errInvalidArgument(msgPayloadCannotDecrypt)
	}
	for _, c := range idStr {
		if c < '0' || c > '9' {
			return 0, errInvalidArgument(msgPayloadCannotDecrypt)
		}
	}
	var n uint64
	for _, c := range idStr {
		n = n*10 + uint64(c-'0')
	}
	if n > uint64(^uint32(0)) {
		return 0, errInvalidArgument(msgPayloadCannotDecrypt)
	}
	return uint32(n), nil
}
