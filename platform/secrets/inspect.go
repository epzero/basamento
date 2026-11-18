package secrets

import (
	"context"

	"github.com/tink-crypto/tink-go/v2/insecurecleartextkeyset"
	aesgcmpb "github.com/tink-crypto/tink-go/v2/proto/aes_gcm_go_proto"
	tinkpb "github.com/tink-crypto/tink-go/v2/proto/tink_go_proto"
	"google.golang.org/protobuf/proto"
)

// DataKeyMaterial returns raw AES-GCM key bytes for enabled data keys. It is
// only for gate assertions that wrapped storage does not leak key material.
func (c *Codec) DataKeyMaterial(ctx context.Context, tenant string) ([][]byte, error) {
	h, err := c.loadCached(ctx, tenant, true)
	if err != nil {
		return nil, err
	}
	mat := insecurecleartextkeyset.KeysetMaterial(h)
	var out [][]byte
	for _, k := range mat.GetKey() {
		if k.GetStatus() != tinkpb.KeyStatusType_ENABLED {
			continue
		}
		if k.GetKeyData().GetTypeUrl() != "type.googleapis.com/google.crypto.tink.AesGcmKey" {
			continue
		}
		var key aesgcmpb.AesGcmKey
		if err := proto.Unmarshal(k.GetKeyData().GetValue(), &key); err != nil {
			continue
		}
		out = append(out, append([]byte(nil), key.GetKeyValue()...))
	}
	return out, nil
}
