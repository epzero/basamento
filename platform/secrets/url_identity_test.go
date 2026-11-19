package secrets

import (
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestPublicURLIDStripsSecrets(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"awskms://TOKEN@key/alias/foo?region=us-east-1":                "awskms://key/alias/foo?region=us-east-1",
		"awskms://key/alias/foo?z=west&a=east":                         "awskms://key/alias/foo?a=east&z=west",
		"awskms:///arn:aws:kms:us-east-1:123:key/abc?region=us-east-1": "awskms:///arn:aws:kms:us-east-1:123:key/abc?region=us-east-1",
		"gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k":      "gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k",
		"awskms://[2001:db8::1]/alias/foo":                             "awskms://[2001:db8::1]/alias/foo",
	}
	for in, want := range cases {
		got, err := publicURLID(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got != want {
			t.Fatalf("%q: got %q want %q", in, got, want)
		}
		if strings.Contains(got, "TOKEN") {
			t.Fatalf("user info leaked in %q", got)
		}
	}
}

func TestPublicURLIDInvalid(t *testing.T) {
	t.Parallel()
	_, err := publicURLID("://missing-scheme")
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err=%v", err)
	}
	if strings.Contains(err.Error(), "missing-scheme") {
		t.Fatal("echoed raw url")
	}
	_, err = publicURLID("awskms://")
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(err.Error(), `invalid key service URL for scheme "awskms"`) {
		t.Fatalf("message %q", err.Error())
	}
}
