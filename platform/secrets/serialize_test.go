package secrets_test

import (
	"testing"

	"github.com/epzero/basamento/platform/secrets"
)

func TestInnerPayloadRoundTrip(t *testing.T) {
	t.Parallel()
	in := secrets.Payload{
		Metadata: map[string][]byte{
			"encoding": []byte("json/plain"),
			"z":        []byte("last"),
			"a":        []byte("first"),
		},
		Data: []byte("body"),
	}
	b, err := secrets.MarshalInnerForTest(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := secrets.UnmarshalInnerForTest(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Data) != "body" || string(out.Metadata["encoding"]) != "json/plain" {
		t.Fatalf("round trip %+v", out)
	}
}
