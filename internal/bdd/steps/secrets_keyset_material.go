package steps

import (
	tinkpb "github.com/tink-crypto/tink-go/v2/proto/tink_go_proto"
	"google.golang.org/protobuf/proto"
)

// dataKeyMaterialBytes returns each key_data.value from a cleartext keyset serialization.
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
