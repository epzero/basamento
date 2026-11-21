package temporal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

func TestCodecClaimCheckRoundTripsLargePlaintexts(t *testing.T) {
	enc := testSecretsCodec(t)
	c, err := newPayloadCodec(enc, testBlobStore(t), 5*1024)
	if err != nil {
		t.Fatal(err)
	}
	ctx := testTenantCtx("acme")
	sizes := []int{
		5*1024 + 1,
		600 * 1024,
		800 * 1024,
		2 * 1024 * 1024,
		8 * 1024 * 1024,
	}
	for _, n := range sizes {
		n := n
		t.Run(formatSize(n), func(t *testing.T) {
			plain := bytes.Repeat([]byte("x"), n)
			got, err := c.Encode(ctx, plainPayload(string(plain)))
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			back, err := c.Decode(ctx, got)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !bytes.Equal(back.Data, plain) {
				t.Fatalf("round-trip size %d: got %d bytes", n, len(back.Data))
			}
		})
	}
}

func formatSize(n int) string {
	if n >= 1024*1024 {
		return fmt.Sprintf("%dMiB", n/(1024*1024))
	}
	if n >= 1024 {
		return fmt.Sprintf("%dKiB", n/1024)
	}
	return fmt.Sprintf("%dB", n)
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

func TestCodecClaimCheckBoundaryByEncryptedSize(t *testing.T) {
	enc := testSecretsCodec(t)
	blobs := testBlobStore(t)
	threshold := 5 * 1024
	c, err := newPayloadCodec(enc, blobs, threshold)
	if err != nil {
		t.Fatal(err)
	}
	ctx := testTenantCtx("acme")
	inlineMax := largestInlinePlaintextSize(t, enc, threshold)
	secret := []byte("PLAINTEXT_MARKER")
	plain := plainPayload(string(make([]byte, inlineMax)))
	hist, err := c.Encode(ctx, plain)
	if err != nil {
		t.Fatal(err)
	}
	markerPlain, err := enc.Decode(ctx, hist)
	if err != nil {
		t.Fatal(err)
	}
	if string(markerPlain.Metadata["encoding"]) == ClaimCheckEncoding {
		t.Fatal("unexpected claim-check at inline boundary")
	}
	stored, err := json.Marshal(storedPayload{Metadata: markerPlain.Metadata, Data: markerPlain.Data})
	if err != nil {
		t.Fatal(err)
	}
	key := blob.ContentKeyOf(stored)
	exists, err := blobs.Exists(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("inline payload should not be stored")
	}
	assertClaimChecked := func(t *testing.T, pad int) {
		t.Helper()
		claimPlain := plainPayload(string(append(secret, bytes.Repeat([]byte("b"), pad)...)))
		hist2, err := c.Encode(ctx, claimPlain)
		if err != nil {
			t.Fatal(err)
		}
		if len(hist2.Data) > threshold {
			t.Fatal("claim-checked history should be a small marker")
		}
		markerEnc, err := enc.Decode(ctx, hist2)
		if err != nil {
			t.Fatal(err)
		}
		if string(markerEnc.Metadata["encoding"]) != ClaimCheckEncoding {
			t.Fatal("expected claim-check marker")
		}
		var marker claimCheckMarker
		if err := json.Unmarshal(markerEnc.Data, &marker); err != nil {
			t.Fatal(err)
		}
		ok, err := blobs.Exists(ctx, marker.Key)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatal("claim-checked blob missing")
		}
		r, err := blobs.Get(ctx, marker.Key)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.Close() }()
		raw, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		var blob storedPayload
		if err := json.Unmarshal(raw, &blob); err != nil {
			t.Fatal(err)
		}
		if string(blob.Metadata["encoding"]) != secrets.EncodingEncrypted {
			t.Fatalf("blob encoding %q", blob.Metadata["encoding"])
		}
		if bytes.Contains(blob.Data, secret) {
			t.Fatal("encrypted blob data holds plaintext marker")
		}
		decrypted, err := c.Decode(ctx, hist2)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(decrypted.Data, claimPlain.Data) {
			t.Fatal("codec round-trip mismatch")
		}
	}
	t.Run("smallest_over_threshold", func(t *testing.T) {
		assertClaimChecked(t, inlineMax+1)
	})
	t.Run("well_over_threshold", func(t *testing.T) {
		assertClaimChecked(t, inlineMax+500)
	})
}

func TestCodecClaimCheckDecodeRejectsOversizeBlob(t *testing.T) {
	enc := testSecretsCodec(t)
	blobs := testBlobStore(t)
	c, err := newPayloadCodec(enc, blobs, 1024)
	if err != nil {
		t.Fatal(err)
	}
	ctx := testTenantCtx("acme")
	inner, err := enc.Encode(ctx, plainPayload("payload-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := json.Marshal(storedPayload{Metadata: inner.Metadata, Data: inner.Data})
	if err != nil {
		t.Fatal(err)
	}
	key := blob.ContentKeyOf(stored)
	if err := blobs.Put(ctx, key, bytes.NewReader(stored), "application/json"); err != nil {
		t.Fatal(err)
	}
	marker, err := json.Marshal(claimCheckMarker{Key: key, Size: len(inner.Data) - 10})
	if err != nil {
		t.Fatal(err)
	}
	encMarker, err := enc.Encode(ctx, secrets.Payload{Metadata: map[string][]byte{"encoding": []byte(ClaimCheckEncoding)}, Data: marker})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Decode(ctx, encMarker)
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %v", core.CodeOf(err))
	}
}

func TestCodecClaimCheckDecodeRejectsUndersizePayload(t *testing.T) {
	enc := testSecretsCodec(t)
	blobs := testBlobStore(t)
	c, err := newPayloadCodec(enc, blobs, 1024)
	if err != nil {
		t.Fatal(err)
	}
	ctx := testTenantCtx("acme")
	inner, err := enc.Encode(ctx, plainPayload("payload-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := json.Marshal(storedPayload{Metadata: inner.Metadata, Data: inner.Data})
	if err != nil {
		t.Fatal(err)
	}
	key := blob.ContentKeyOf(stored)
	if err := blobs.Put(ctx, key, bytes.NewReader(stored), "application/json"); err != nil {
		t.Fatal(err)
	}
	marker, err := json.Marshal(claimCheckMarker{Key: key, Size: len(inner.Data) + 50})
	if err != nil {
		t.Fatal(err)
	}
	encMarker, err := enc.Encode(ctx, secrets.Payload{Metadata: map[string][]byte{"encoding": []byte(ClaimCheckEncoding)}, Data: marker})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Decode(ctx, encMarker)
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %v", core.CodeOf(err))
	}
}

func TestCodecClaimCheckSizeMismatchRejected(t *testing.T) {
	enc := testSecretsCodec(t)
	blobs := testBlobStore(t)
	c, err := newPayloadCodec(enc, blobs, 1024)
	if err != nil {
		t.Fatal(err)
	}
	ctx := testTenantCtx("acme")
	body := []byte("xy")
	stored, err := json.Marshal(storedPayload{Metadata: map[string][]byte{"encoding": []byte(secrets.EncodingEncrypted)}, Data: body})
	if err != nil {
		t.Fatal(err)
	}
	key := blob.ContentKeyOf(stored)
	if err := blobs.Put(ctx, key, bytes.NewReader(stored), "application/json"); err != nil {
		t.Fatal(err)
	}
	marker, err := json.Marshal(claimCheckMarker{Key: key, Size: 9999})
	if err != nil {
		t.Fatal(err)
	}
	encMarker, err := enc.Encode(ctx, secrets.Payload{Metadata: map[string][]byte{"encoding": []byte(ClaimCheckEncoding)}, Data: marker})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Decode(ctx, encMarker)
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %v", core.CodeOf(err))
	}
}

func largestInlinePlaintextSize(t *testing.T, enc *secrets.Codec, threshold int) int {
	t.Helper()
	lo, hi := 1, 65536
	ctx := testTenantCtx("acme")
	for lo < hi {
		mid := (lo + hi + 1) / 2
		p, err := enc.Encode(ctx, plainPayload(string(make([]byte, mid))))
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Data) <= threshold {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}
