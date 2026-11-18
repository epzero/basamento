package secrets_test

import (
	"encoding/hex"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/secrets"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestInnerProtobufGoldenEmptyMeta(t *testing.T) {
	t.Parallel()
	// Hand-built: field 2 (data) = "x" -> 12 01 78
	want, _ := hex.DecodeString("120178")
	got, err := secrets.MarshalInnerForTest(secrets.Payload{Data: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("golden: got %x want %x", got, want)
	}
	out, err := secrets.UnmarshalInnerForTest(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Data) != "x" || len(out.Metadata) != 0 {
		t.Fatalf("round trip %+v", out)
	}
}

func TestInnerProtobufGoldenWithMeta(t *testing.T) {
	t.Parallel()
	in := secrets.Payload{
		Metadata: map[string][]byte{"encoding": []byte("json/plain")},
		Data:     []byte("body"),
	}
	got, err := secrets.MarshalInnerForTest(in)
	if err != nil {
		t.Fatal(err)
	}
	// Recompute expected with protowire the same way production marshals.
	var expect []byte
	var entry []byte
	entry = protowire.AppendTag(entry, 1, protowire.BytesType)
	entry = protowire.AppendBytes(entry, []byte("encoding"))
	entry = protowire.AppendTag(entry, 2, protowire.BytesType)
	entry = protowire.AppendBytes(entry, []byte("json/plain"))
	expect = protowire.AppendTag(expect, 1, protowire.BytesType)
	expect = protowire.AppendBytes(expect, entry)
	expect = protowire.AppendTag(expect, 2, protowire.BytesType)
	expect = protowire.AppendBytes(expect, []byte("body"))
	if string(got) != string(expect) {
		t.Fatalf("got %x want %x", got, expect)
	}
	out, err := secrets.UnmarshalInnerForTest(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Data) != "body" || string(out.Metadata["encoding"]) != "json/plain" {
		t.Fatalf("round trip %+v", out)
	}
}

func TestInnerEmptyMetadata(t *testing.T) {
	t.Parallel()
	b, err := secrets.MarshalInnerForTest(secrets.Payload{Metadata: map[string][]byte{}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := secrets.UnmarshalInnerForTest(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Metadata) != 0 {
		t.Fatalf("meta %+v", out.Metadata)
	}
}

func TestInnerLargeData(t *testing.T) {
	t.Parallel()
	data := make([]byte, 64*1024)
	for i := range data {
		data[i] = byte(i)
	}
	in := secrets.Payload{Data: data}
	b, err := secrets.MarshalInnerForTest(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := secrets.UnmarshalInnerForTest(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Data) != len(data) {
		t.Fatalf("len %d", len(out.Data))
	}
}

func TestInnerSkipsUnknownFields(t *testing.T) {
	t.Parallel()
	b, err := secrets.MarshalInnerForTest(secrets.Payload{Data: []byte("ok")})
	if err != nil {
		t.Fatal(err)
	}
	// Append unknown field 99 (varint).
	b = protowire.AppendTag(b, 99, protowire.VarintType)
	b = protowire.AppendVarint(b, 1)
	out, err := secrets.UnmarshalInnerForTest(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Data) != "ok" {
		t.Fatalf("%+v", out)
	}
}

func TestInnerMalformedFailsClosed(t *testing.T) {
	t.Parallel()
	cases := [][]byte{
		{0x12},
		{0x0a, 0x01, 0x00},
		{0xff},
	}
	for _, c := range cases {
		_, err := secrets.UnmarshalInnerForTest(c)
		if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("input %x: err=%v", c, err)
		}
	}
}

func TestInnerMalformedAfterAEADUsesInvalidArgument(t *testing.T) {
	t.Parallel()
	// design §4: authentication fails / unknown key / other tenant -> payload cannot be decrypted
	// Malformed inner after successful AEAD open is covered by decryptWithHandle -> invalid_argument.
	_, err := secrets.UnmarshalInnerForTest([]byte{0x12, 0x05, 0x01, 0x02, 0x03})
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err=%v", err)
	}
}
