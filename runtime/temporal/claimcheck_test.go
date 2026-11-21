package temporal

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/json"
	"sync"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/blob"
	"github.com/epzero/basamento/platform/secrets"
)

func testTenantCtx(tenant string) context.Context {
	return core.WithTenant(context.Background(), core.TenantID(tenant))
}

type testKeyService struct{ id string }

func (k testKeyService) MasterKeyID() string { return k.id }
func (k testKeyService) Close() error        { return nil }

func (k testKeyService) Wrap(_ context.Context, tenant string, plaintext []byte) ([]byte, error) {
	key := sha256.Sum256([]byte(k.id + "/" + tenant))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	return gcm.Seal(nonce, nonce, plaintext, []byte(tenant)), nil
}

func (k testKeyService) Unwrap(_ context.Context, tenant string, ciphertext []byte) ([]byte, error) {
	key := sha256.Sum256([]byte(k.id + "/" + tenant))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, core.NewError(core.CodeInvalidArgument, "test key service: short ciphertext", nil)
	}
	return gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], []byte(tenant))
}

type testKeysetStore struct {
	mu   sync.Mutex
	data map[string]secrets.StoredKeyset
}

func newTestKeysetStore() *testKeysetStore {
	return &testKeysetStore{data: map[string]secrets.StoredKeyset{}}
}

func (s *testKeysetStore) Get(_ context.Context, tenant string) (secrets.StoredKeyset, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.data[tenant]
	return r, ok, nil
}

func (s *testKeysetStore) CreateIfAbsent(_ context.Context, tenant string, record secrets.StoredKeyset) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[tenant]; ok {
		return false, nil
	}
	s.data[tenant] = record
	return true, nil
}

func (s *testKeysetStore) CompareAndSwap(_ context.Context, tenant string, expect uint64, record secrets.StoredKeyset) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.data[tenant]
	if !ok || old.Revision != expect {
		return core.NewError(core.CodeConflict, "keyset revision conflict", nil)
	}
	s.data[tenant] = record
	return nil
}

func (s *testKeysetStore) Count(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.data), nil
}

func (s *testKeysetStore) Tenants(context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.data))
	for tenant := range s.data {
		out = append(out, tenant)
	}
	return out, nil
}

func newTestSecrets(t *testing.T) *secrets.Codec {
	t.Helper()
	c, err := secrets.NewCodec(newTestKeysetStore(), []secrets.KeyService{testKeyService{id: "mk1"}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func newTestBlob(t *testing.T) *blob.Store {
	t.Helper()
	st, _, err := blob.Open(context.Background(), "file://"+t.TempDir(), blob.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func plainPayload(data string) secrets.Payload {
	return secrets.Payload{
		Metadata: map[string][]byte{"encoding": []byte("json/plain")},
		Data:     []byte(data),
	}
}

func TestCodecInlinePayloadRoundTrips(t *testing.T) {
	enc := newTestSecrets(t)
	c, err := NewCodec(enc, newTestBlob(t), 5*1024)
	if err != nil {
		t.Fatal(err)
	}
	ctx := testTenantCtx("acme")
	got, err := c.Encode(ctx, plainPayload(`{"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.ClaimCheck {
		t.Fatal("small payload was claim-checked")
	}
	if encoding := string(got.Payload.Metadata["encoding"]); encoding != secrets.EncodingEncrypted {
		t.Fatalf("history encoding %q, want %q", encoding, secrets.EncodingEncrypted)
	}
	if bytes.Contains(got.Payload.Data, []byte(`{"ok":true}`)) {
		t.Fatal("history payload holds the plaintext")
	}
	back, err := c.Decode(ctx, got.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if string(back.Data) != `{"ok":true}` || string(back.Metadata["encoding"]) != "json/plain" {
		t.Fatalf("decoded %+v, want the original payload", back)
	}
}

func TestCodecClaimChecksLargePayloads(t *testing.T) {
	enc := newTestSecrets(t)
	c, err := NewCodec(enc, newTestBlob(t), 5*1024)
	if err != nil {
		t.Fatal(err)
	}
	ctx := testTenantCtx("acme")
	big := bytes.Repeat([]byte("x"), 8192)
	got, err := c.Encode(ctx, plainPayload(string(big)))
	if err != nil {
		t.Fatal(err)
	}
	if !got.ClaimCheck {
		t.Fatal("large payload was not claim-checked")
	}
	if len(got.Payload.Data) > 5*1024 {
		t.Fatalf("history payload is %d bytes, want at most 5 KB", len(got.Payload.Data))
	}
	back, err := c.Decode(ctx, got.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back.Data, []byte(string(big))) {
		t.Fatal("claim-checked payload did not round-trip")
	}
}

func TestCodecRefusesUnencryptedPayloads(t *testing.T) {
	c, err := NewCodec(newTestSecrets(t), newTestBlob(t), 5*1024)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Decode(testTenantCtx("acme"), plainPayload(`{"ok":true}`))
	if code := core.CodeOf(err); code != core.CodeInvalidArgument {
		t.Fatalf("code %q, want invalid_argument", code)
	}
}

func TestCodecRefusesEncodeWithoutTenant(t *testing.T) {
	c, err := NewCodec(newTestSecrets(t), newTestBlob(t), 5*1024)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Encode(context.Background(), plainPayload(`{}`))
	if code := core.CodeOf(err); code != core.CodeInvalidArgument {
		t.Fatalf("code %q, want invalid_argument", code)
	}
}

func TestCodecClaimCheckIsTenantBound(t *testing.T) {
	enc := newTestSecrets(t)
	blobs := newTestBlob(t)
	c, err := NewCodec(enc, blobs, 5*1024)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := json.Marshal(storedPayload{Metadata: map[string][]byte{"encoding": []byte("json/plain")}, Data: bytes.Repeat([]byte("y"), 8192)})
	if err != nil {
		t.Fatal(err)
	}
	key := blob.ContentKeyOf(stored)
	if err := blobs.Put(testTenantCtx("globex"), key, bytes.NewReader(stored), "application/json"); err != nil {
		t.Fatal(err)
	}
	marker, _ := json.Marshal(claimCheckMarker{Key: key, Size: len(stored)})
	encMarker, err := enc.Encode(testTenantCtx("acme"), secrets.Payload{Metadata: map[string][]byte{"encoding": []byte(ClaimCheckEncoding)}, Data: marker})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Decode(testTenantCtx("acme"), encMarker)
	if code := core.CodeOf(err); code != core.CodeNotFound {
		t.Fatalf("cross-tenant read code %q, want not_found", code)
	}
}

func TestCodecThresholdIsOnTheEncryptedForm(t *testing.T) {
	enc := newTestSecrets(t)
	c, err := NewCodec(enc, newTestBlob(t), 5*1024)
	if err != nil {
		t.Fatal(err)
	}
	ctx := testTenantCtx("acme")
	small, err := c.Encode(ctx, plainPayload(string(make([]byte, 1024))))
	if err != nil {
		t.Fatal(err)
	}
	if small.ClaimCheck {
		t.Fatal("1 KB payload was claim-checked")
	}
	medium, err := c.Encode(ctx, plainPayload(string(make([]byte, 6000))))
	if err != nil {
		t.Fatal(err)
	}
	if !medium.ClaimCheck {
		t.Fatal("6 KB payload was not claim-checked")
	}
}
