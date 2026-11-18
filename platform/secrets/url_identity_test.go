package secrets

import (
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestPublicURLIDStripsSecrets(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"hashivault://TOKEN@vault.example.com/v1/transit/keys/mykey": "hashivault://vault.example.com/v1/transit/keys/mykey",
		"hashivault://user:pass@host:8200/v1/transit/keys/k":         "hashivault://host:8200/v1/transit/keys/k",
		"awskms://key/alias/foo?token=secret":                        "awskms://key/alias/foo",
		"gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k":    "gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k",
		"hashivault://[2001:db8::1]/v1/transit/keys/k":               "hashivault://[2001:db8::1]/v1/transit/keys/k",
	}
	for in, want := range cases {
		got, err := publicURLID(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got != want {
			t.Fatalf("%q: got %q want %q", in, got, want)
		}
		if strings.Contains(got, "TOKEN") || strings.Contains(got, "secret") {
			t.Fatalf("secret leaked in %q", got)
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
}
