package secrets

import (
	"encoding/hex"
	"testing"

	"github.com/epzero/basamento/core"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestInnerProtobufGoldenEmptyMeta(t *testing.T) {
	t.Parallel()
	want, _ := hex.DecodeString("120178")
	got, err := marshalInner(Payload{Data: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("golden: got %x want %x", got, want)
	}
	out, err := unmarshalInner(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Data) != "x" || len(out.Metadata) != 0 {
		t.Fatalf("round trip %+v", out)
	}
}

func TestInnerProtobufGoldenWithMeta(t *testing.T) {
	t.Parallel()
	in := Payload{
		Metadata: map[string][]byte{"encoding": []byte("json/plain")},
		Data:     []byte("body"),
	}
	got, err := marshalInner(in)
	if err != nil {
		t.Fatal(err)
	}
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
}

func TestInnerMalformedFailsClosed(t *testing.T) {
	t.Parallel()
	for _, c := range [][]byte{{0x12}, {0x0a, 0x01, 0x00}, {0xff}} {
		_, err := unmarshalInner(c)
		if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("input %x: err=%v", c, err)
		}
	}
}
