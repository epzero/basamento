package secrets

import (
	"context"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"gocloud.dev/secrets"
)

func TestRegisteredKeeperSchemes(t *testing.T) {
	t.Parallel()
	mux := secrets.DefaultURLMux()
	for _, scheme := range cloudKeeperSchemes() {
		found := false
		for _, s := range mux.KeeperSchemes() {
			if s == scheme {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("scheme %q not registered", scheme)
		}
	}
}

func TestOpenKeyServiceUnsupportedOrInvalidURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw          string
		wantContains string
		mustNot      string
	}{
		{
			raw:          "hashivault://vault.example/v1/transit/keys/k",
			wantContains: `unsupported key service scheme "hashivault"`,
		},
		{
			raw:          "notascheme://x",
			wantContains: `unsupported key service scheme "notascheme"`,
		},
		{
			raw:          "awskms://",
			wantContains: `invalid key service URL for scheme "awskms"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			_, err := OpenKeyService(context.Background(), tc.raw)
			if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
				t.Fatalf("err=%v", err)
			}
			if core.IsRetryable(err) {
				t.Fatal("retryable")
			}
			if !strings.Contains(err.Error(), tc.wantContains) {
				t.Fatalf("message %q want contains %q", err.Error(), tc.wantContains)
			}
			if strings.Contains(err.Error(), "vault.example") || strings.Contains(err.Error(), "notascheme://") {
				t.Fatalf("echoed url: %q", err.Error())
			}
		})
	}
}
