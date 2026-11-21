package temporal

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"github.com/epzero/basamento/platform/blob"
	"github.com/epzero/basamento/platform/secrets"
)

func testSecretsCodec(t *testing.T) *secrets.Codec {
	t.Helper()
	store, err := secrets.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := key32(3)
	url := "localsecrets://" + base64.StdEncoding.EncodeToString(key)
	c, err := secrets.OpenCodec(context.Background(), store, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func testBlobStore(t *testing.T) *blob.Store {
	t.Helper()
	st, _, err := blob.Open(context.Background(), "file://"+t.TempDir(), blob.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func key32(seed int64) []byte {
	h := sha256.Sum256([]byte{byte(seed)})
	return h[:]
}
