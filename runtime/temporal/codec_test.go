package temporal

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/blob"
	"github.com/epzero/basamento/platform/secrets"
)

func testTenantCtx(tenant string) context.Context {
	return core.WithTenant(context.Background(), core.TenantID(tenant))
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
	if string(got.Payload.Metadata["encoding"]) != secrets.EncodingEncrypted {
		t.Fatalf("marker encoding %q, want %q", got.Payload.Metadata["encoding"], secrets.EncodingEncrypted)
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

func TestCodecClaimCheckIsTenantBound(t *testing.T) {
	enc := newTestSecrets(t)
	blobs := newTestBlob(t)
	c, err := NewCodec(enc, blobs, 5*1024)
	if err != nil {
		t.Fatal(err)
	}
	// The blob belongs to globex, but the marker in acme's history names it.
	stored, err := json.Marshal(storedPayload{Metadata: map[string][]byte{"encoding": []byte("json/plain")}, Data: bytes.Repeat([]byte("y"), 8192)})
	if err != nil {
		t.Fatal(err)
	}
	key := blob.ContentKeyOf(stored)
	if err := blobs.Put(testTenantCtx("globex"), key, bytes.NewReader(stored), "application/json"); err != nil {
		t.Fatal(err)
	}
	marker, _ := json.Marshal(claimCheckMarker{Key: key})
	encMarker, err := enc.Encode(testTenantCtx("acme"), secrets.Payload{Metadata: map[string][]byte{"encoding": []byte(claimCheckEncoding)}, Data: marker})
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
	blobs := newTestBlob(t)
	c, err := NewCodec(enc, blobs, 5*1024)
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

func TestStoredPayloadKeepsEncoding(t *testing.T) {
	// The blob holds the encrypted payload's metadata, not just its bytes, so
	// a read restores the original encoding (REQ-RT-029).
	raw, err := json.Marshal(storedPayload{Metadata: map[string][]byte{"encoding": []byte("application/json")}, Data: []byte("ct")})
	if err != nil {
		t.Fatal(err)
	}
	var back storedPayload
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if string(back.Metadata["encoding"]) != "application/json" {
		t.Fatalf("encoding %q", back.Metadata["encoding"])
	}
}
