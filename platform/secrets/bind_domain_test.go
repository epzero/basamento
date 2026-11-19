package secrets

import (
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestValueFrameRejectsKeysetPlaintext(t *testing.T) {
	t.Parallel()
	fr, err := FrameBoundPlaintext("acme", []byte("keyset"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = parseValuePlaintext(fr)
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("parseValuePlaintext(keyset frame): err=%v", err)
	}
	if !strings.Contains(err.Error(), msgValueCannotDecrypt) {
		t.Fatalf("message: %v", err)
	}
}

func TestKeysetFrameRejectsValueDomain(t *testing.T) {
	t.Parallel()
	keysetBytes := []byte("serialized-keyset-body")
	framed, err := frameTagged(frameKeysetV1, "acme", keysetBytes)
	if err != nil {
		t.Fatal(err)
	}
	body := framed[len(frameKeysetV1)+1:]
	attack := frameValuePlaintext(append([]byte{'x'}, body...))
	_, err = ParseBoundPlaintext("acme", attack)
	if err == nil || core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(err.Error(), msgKeysetCannotUnwrap) {
		t.Fatalf("message: %v", err)
	}
}
