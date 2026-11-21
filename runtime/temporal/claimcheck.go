package temporal

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/platform/blob"
	"github.com/epzero/basamento/platform/secrets"
)

// ClaimCheckEncoding marks a decrypted claim-check marker. Markers are always
// encrypted before they reach run history (REQ-RT-031).
const ClaimCheckEncoding = "basamento/claim-check"

// claimCheckMarker is the decrypted plaintext of a marker payload. It carries
// only the blob content key and the stored encrypted size in bytes.
type claimCheckMarker struct {
	Key  string `json:"key"`
	Size int    `json:"size"`
}

// storedPayload is the JSON form written to blob storage for a claim-checked
// ciphertext, preserving metadata for decode (REQ-RT-029).
type storedPayload struct {
	Metadata map[string][]byte `json:"metadata"`
	Data     []byte            `json:"data"`
}

// payloadCodec composes tenant encryption (WP-L1-04) and blob claim-check (WP-L1-02).
type payloadCodec struct {
	enc       *secrets.Codec
	blobs     platform.Blob
	threshold int
}

func newPayloadCodec(enc *secrets.Codec, blobs platform.Blob, threshold int) (*payloadCodec, error) {
	if enc == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "temporal: payload codec needs an encryption codec", nil)
	}
	if blobs == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "temporal: payload codec needs a blob store", nil)
	}
	if threshold <= 0 {
		threshold = DefaultClaimCheckBytes
	}
	return &payloadCodec{enc: enc, blobs: blobs, threshold: threshold}, nil
}

// Encode encrypts p for the tenant in ctx and claim-checks when the encrypted
// form exceeds the threshold (REQ-RT-028, REQ-RT-031).
func (c *payloadCodec) Encode(ctx context.Context, p secrets.Payload) (secrets.Payload, error) {
	enc, err := c.enc.Encode(ctx, p)
	if err != nil {
		return secrets.Payload{}, err
	}
	if len(enc.Data) <= c.threshold {
		return enc, nil
	}
	stored, err := json.Marshal(storedPayload{Metadata: enc.Metadata, Data: enc.Data})
	if err != nil {
		return secrets.Payload{}, core.NewError(core.CodeInternal, "temporal: encode claim-checked payload", map[string]any{"cause": err.Error()})
	}
	key := blob.ContentKeyOf(stored)
	if err := c.blobs.Put(ctx, key, bytes.NewReader(stored), "application/json"); err != nil {
		return secrets.Payload{}, err
	}
	marker, err := json.Marshal(claimCheckMarker{Key: key, Size: len(enc.Data)})
	if err != nil {
		return secrets.Payload{}, core.NewError(core.CodeInternal, "temporal: encode claim-check marker", map[string]any{"cause": err.Error()})
	}
	plain := secrets.Payload{
		Metadata: map[string][]byte{"encoding": []byte(ClaimCheckEncoding)},
		Data:     marker,
	}
	return c.enc.Encode(ctx, plain)
}

// Decode decrypts a history payload for the tenant in ctx (REQ-RT-029,
// REQ-RT-032, REQ-RT-033).
func (c *payloadCodec) Decode(ctx context.Context, p secrets.Payload) (secrets.Payload, error) {
	if string(p.Metadata["encoding"]) != secrets.EncodingEncrypted {
		return secrets.Payload{}, core.NewError(core.CodeInvalidArgument, "temporal: history payload is not encrypted", nil)
	}
	plain, err := c.enc.Decode(ctx, p)
	if err != nil {
		return secrets.Payload{}, err
	}
	if string(plain.Metadata["encoding"]) != ClaimCheckEncoding {
		return plain, nil
	}
	var marker claimCheckMarker
	if err := json.Unmarshal(plain.Data, &marker); err != nil || marker.Key == "" {
		return secrets.Payload{}, core.NewError(core.CodeInvalidArgument, "temporal: malformed claim-check marker", nil)
	}
	if marker.Size <= 0 {
		return secrets.Payload{}, core.NewError(core.CodeInvalidArgument, "temporal: malformed claim-check marker", nil)
	}
	r, err := c.blobs.Get(ctx, marker.Key)
	if err != nil {
		return secrets.Payload{}, err
	}
	defer func() { _ = r.Close() }()
	// marker.Size is the encrypted inner Data length; the blob is JSON (base64) around it.
	const maxBlobRead = 1 << 20
	readCap := int64(marker.Size)*2 + 8192
	if readCap > maxBlobRead {
		readCap = maxBlobRead
	}
	raw, err := io.ReadAll(io.LimitReader(r, readCap+1))
	if err != nil {
		return secrets.Payload{}, core.NewError(core.CodeUnavailable, "temporal: read claim-checked payload", map[string]any{"cause": err.Error()})
	}
	if int64(len(raw)) > readCap {
		return secrets.Payload{}, core.NewError(core.CodeInvalidArgument, "temporal: claim-checked payload size mismatch", nil)
	}
	var stored storedPayload
	if err := json.Unmarshal(raw, &stored); err != nil {
		return secrets.Payload{}, core.NewError(core.CodeInvalidArgument, "temporal: malformed claim-checked payload", nil)
	}
	enc := secrets.Payload{Metadata: stored.Metadata, Data: stored.Data}
	if len(enc.Data) != marker.Size {
		return secrets.Payload{}, core.NewError(core.CodeInvalidArgument, "temporal: claim-checked payload size mismatch", nil)
	}
	if string(enc.Metadata["encoding"]) != secrets.EncodingEncrypted {
		return secrets.Payload{}, core.NewError(core.CodeInvalidArgument, "temporal: claim-checked blob is not encrypted", nil)
	}
	return c.enc.Decode(ctx, enc)
}
