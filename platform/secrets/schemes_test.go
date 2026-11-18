package secrets

import (
	"context"
	"testing"

	"gocloud.dev/secrets"
)

func TestRegisteredKeeperSchemes(t *testing.T) {
	t.Parallel()
	mux := secrets.DefaultURLMux()
	for _, scheme := range CloudKeeperSchemes {
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

func TestHashivaultUnsupported(t *testing.T) {
	t.Parallel()
	_, err := OpenKeyService(context.Background(), "hashivault://vault.example/v1/transit/keys/k")
	if err == nil {
		t.Fatal("expected error")
	}
}
