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

// PayloadCodec encodes and decodes the payloads a run history holds. The
// runtime composes the tenant encryption codec with the blob claim-check codec
// (specs/runtime/design.md §Payload codec); a composition root may wrap it to
// observe or fault payloads without changing the runtime.
type PayloadCodec interface {
	// Encode turns a plain payload into the form written to a run history.
	Encode(ctx context.Context, p secrets.Payload) (Encoded, error)
	// Decode reads a history payload back. A payload without the encrypted
	// encoding is refused with invalid_argument (REQ-RT-032).
	Decode(ctx context.Context, p secrets.Payload) (secrets.Payload, error)
}

// Encoded is a payload as a run history holds it.
type Encoded struct {
	Payload secrets.Payload
	// ClaimCheck is true when Payload is a claim-check marker. It is for
	// observability only: Decode never trusts it.
	ClaimCheck bool
}

// claimCheckEncoding marks a decrypted claim-check marker. It never reaches a
// run history: markers are encrypted like every other payload (REQ-RT-031).
const claimCheckEncoding = "basamento/claim-check"

// claimCheckMarker is the decrypted form of a marker.
type claimCheckMarker struct {
	Key string `json:"key"`
}

// storedPayload is the form a claim-checked payload takes in blob storage. It
// keeps the encrypted payload's metadata so a read restores its encoding, not
// only its bytes (REQ-RT-029).
type storedPayload struct {
	Metadata map[string][]byte `json:"metadata"`
	Data     []byte            `json:"data"`
}

// Codec composes the tenant encryption codec of WP-L1-04 and the blob
// claim-check codec of WP-L1-02 in that order: encrypt, then replace an
// encrypted payload larger than the threshold with an encrypted marker naming
// its blob (specs/runtime/design.md §Payload codec).
type Codec struct {
	enc       *secrets.Codec
	blobs     platform.Blob
	threshold int
}

// NewCodec returns the runtime's payload codec. A nil encryption codec or blob
// store is refused with invalid_argument; a threshold at or below zero keeps
// every payload inline.
func NewCodec(enc *secrets.Codec, blobs platform.Blob, threshold int) (*Codec, error) {
	if enc == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "temporal: payload codec needs an encryption codec", nil)
	}
	if blobs == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "temporal: payload codec needs a blob store", nil)
	}
	return &Codec{enc: enc, blobs: blobs, threshold: threshold}, nil
}

// Encode encrypts p for the tenant in ctx and claim-checks it when the
// encrypted form exceeds the threshold (REQ-RT-028, REQ-RT-031).
func (c *Codec) Encode(ctx context.Context, p secrets.Payload) (Encoded, error) {
	enc, err := c.enc.Encode(ctx, p)
	if err != nil {
		return Encoded{}, err
	}
	if c.threshold <= 0 || len(enc.Data) <= c.threshold {
		return Encoded{Payload: enc}, nil
	}
	stored, err := json.Marshal(storedPayload{Metadata: enc.Metadata, Data: enc.Data})
	if err != nil {
		return Encoded{}, core.NewError(core.CodeInternal, "temporal: encode claim-checked payload", map[string]any{"cause": err.Error()})
	}
	key := blob.ContentKeyOf(stored)
	if err := c.blobs.Put(ctx, key, bytes.NewReader(stored), "application/json"); err != nil {
		return Encoded{}, err
	}
	marker, err := json.Marshal(claimCheckMarker{Key: key})
	if err != nil {
		return Encoded{}, core.NewError(core.CodeInternal, "temporal: encode claim-check marker", map[string]any{"cause": err.Error()})
	}
	plain := secrets.Payload{
		Metadata: map[string][]byte{"encoding": []byte(claimCheckEncoding)},
		Data:     marker,
	}
	encMarker, err := c.enc.Encode(ctx, plain)
	if err != nil {
		return Encoded{}, err
	}
	return Encoded{Payload: encMarker, ClaimCheck: true}, nil
}

// Decode decrypts a history payload for the tenant in ctx. A marker is read
// back from the tenant's blob store and decrypted again, restoring the
// original content and encoding (REQ-RT-029, REQ-RT-032, REQ-RT-033).
func (c *Codec) Decode(ctx context.Context, p secrets.Payload) (secrets.Payload, error) {
	plain, err := c.enc.Decode(ctx, p)
	if err != nil {
		return secrets.Payload{}, err
	}
	if string(plain.Metadata["encoding"]) != claimCheckEncoding {
		return plain, nil
	}
	var marker claimCheckMarker
	if err := json.Unmarshal(plain.Data, &marker); err != nil || marker.Key == "" {
		return secrets.Payload{}, core.NewError(core.CodeInvalidArgument, "temporal: malformed claim-check marker", nil)
	}
	r, err := c.blobs.Get(ctx, marker.Key)
	if err != nil {
		return secrets.Payload{}, err
	}
	defer func() { _ = r.Close() }()
	raw, err := io.ReadAll(r)
	if err != nil {
		return secrets.Payload{}, core.NewError(core.CodeUnavailable, "temporal: read claim-checked payload", map[string]any{"cause": err.Error()})
	}
	var stored storedPayload
	if err := json.Unmarshal(raw, &stored); err != nil {
		return secrets.Payload{}, core.NewError(core.CodeInvalidArgument, "temporal: malformed claim-checked payload", nil)
	}
	return c.enc.Decode(ctx, secrets.Payload{Metadata: stored.Metadata, Data: stored.Data})
}
