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

func plainPayload(data string) secrets.Payload {
	return secrets.Payload{
		Metadata: map[string][]byte{"encoding": []byte("json/plain")},
		Data:     []byte(data),
	}
}

func TestCodecInlinePayloadRoundTrips(t *testing.T) {
	enc := testSecretsCodec(t)
	c, err := newPayloadCodec(enc, testBlobStore(t), 5*1024)
	if err != nil {
		t.Fatal(err)
	}
	ctx := testTenantCtx("acme")
	got, err := c.Encode(ctx, plainPayload(`{"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if encoding := string(got.Metadata["encoding"]); encoding != secrets.EncodingEncrypted {
		t.Fatalf("history encoding %q, want %q", encoding, secrets.EncodingEncrypted)
	}
	if bytes.Contains(got.Data, []byte(`{"ok":true}`)) {
		t.Fatal("history payload holds the plaintext")
	}
	back, err := c.Decode(ctx, got)
	if err != nil {
		t.Fatal(err)
	}
	if string(back.Data) != `{"ok":true}` || string(back.Metadata["encoding"]) != "json/plain" {
		t.Fatalf("decoded %+v, want the original payload", back)
	}
}

func TestCodecClaimChecksLargePayloads(t *testing.T) {
	enc := testSecretsCodec(t)
	c, err := newPayloadCodec(enc, testBlobStore(t), 5*1024)
	if err != nil {
		t.Fatal(err)
	}
	ctx := testTenantCtx("acme")
	big := bytes.Repeat([]byte("x"), 8192)
	got, err := c.Encode(ctx, plainPayload(string(big)))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Data) > 5*1024 {
		t.Fatalf("history payload is %d bytes, want at most 5 KB", len(got.Data))
	}
	back, err := c.Decode(ctx, got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back.Data, []byte(string(big))) {
		t.Fatal("claim-checked payload did not round-trip")
	}
}

func TestCodecRefusesUnencryptedPayloads(t *testing.T) {
	c, err := newPayloadCodec(testSecretsCodec(t), testBlobStore(t), 5*1024)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Decode(testTenantCtx("acme"), plainPayload(`{"ok":true}`))
	if code := core.CodeOf(err); code != core.CodeInvalidArgument {
		t.Fatalf("code %q, want invalid_argument", code)
	}
}

func TestCodecRefusesEncodeWithoutTenant(t *testing.T) {
	c, err := newPayloadCodec(testSecretsCodec(t), testBlobStore(t), 5*1024)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Encode(context.Background(), plainPayload(`{}`))
	if code := core.CodeOf(err); code != core.CodeInvalidArgument {
		t.Fatalf("code %q, want invalid_argument", code)
	}
}

func TestCodecClaimCheckIsTenantBound(t *testing.T) {
	enc := testSecretsCodec(t)
	blobs := testBlobStore(t)
	c, err := newPayloadCodec(enc, blobs, 5*1024)
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
	enc := testSecretsCodec(t)
	c, err := newPayloadCodec(enc, testBlobStore(t), 5*1024)
	if err != nil {
		t.Fatal(err)
	}
	ctx := testTenantCtx("acme")
	small, err := c.Encode(ctx, plainPayload(string(make([]byte, 1024))))
	if err != nil {
		t.Fatal(err)
	}
	if len(small.Data) > 5*1024 {
		t.Fatal("1 KB payload was claim-checked")
	}
	large, err := c.Encode(ctx, plainPayload(string(make([]byte, 20000))))
	if err != nil {
		t.Fatal(err)
	}
	if len(large.Data) > 5*1024 {
		t.Fatal("large payload history was not claim-checked to marker size")
	}
}
