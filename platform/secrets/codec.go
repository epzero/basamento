package secrets

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/epzero/basamento/core"
	"github.com/tink-crypto/tink-go/v2/aead"
	"github.com/tink-crypto/tink-go/v2/keyset"
)

type cacheEntry struct {
	handle   *keyset.Handle
	revision uint64
}

// Codec encrypts and decrypts payloads with tenant data keys (WP-L1-04).
type Codec struct {
	store KeysetStore
	keys  []KeyService
	mu    sync.Mutex
	cache map[string]cacheEntry
}

// NewCodec returns a payload codec. keys[0] is the current master key; the rest
// are previous keys still able to unwrap stored keysets.
func NewCodec(store KeysetStore, keys ...KeyService) (*Codec, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("master key is required")
	}
	return &Codec{store: store, keys: keys, cache: map[string]cacheEntry{}}, nil
}

func (c *Codec) tenant(ctx context.Context) (string, error) {
	id, ok := core.TenantFrom(ctx)
	if !ok || id == "" {
		return "", errInvalidArgument(msgTenantRequired)
	}
	return string(id), nil
}

// Encode encrypts p for the tenant in ctx.
func (c *Codec) Encode(ctx context.Context, p Payload) (Payload, error) {
	tenant, err := c.tenant(ctx)
	if err != nil {
		return Payload{}, err
	}
	h, err := c.keysetForEncode(ctx, tenant)
	if err != nil {
		return Payload{}, err
	}
	keyID := primaryKeyID(h)
	inner, err := marshalInner(p)
	if err != nil {
		return Payload{}, err
	}
	prim, err := aead.New(h)
	if err != nil {
		return Payload{}, err
	}
	idStr := strconv.FormatUint(uint64(keyID), 10)
	ct, err := prim.Encrypt(inner, payloadAssocData(tenant, idStr))
	if err != nil {
		return Payload{}, errInvalidArgument(msgPayloadCannotDecrypt)
	}
	return Payload{
		Metadata: map[string][]byte{
			"encoding":          []byte(EncodingEncrypted),
			MetaEncryptionKeyID: []byte(idStr),
		},
		Data: ct,
	}, nil
}

// Decode decrypts an encoded payload for the tenant in ctx. It never creates a keyset.
func (c *Codec) Decode(ctx context.Context, p Payload) (Payload, error) {
	tenant, err := c.tenant(ctx)
	if err != nil {
		return Payload{}, err
	}
	enc := p.Metadata["encoding"]
	if string(enc) != EncodingEncrypted {
		return Payload{}, errInvalidArgument(msgPayloadNotEncrypted)
	}
	idBytes := p.Metadata[MetaEncryptionKeyID]
	if len(idBytes) == 0 {
		return Payload{}, errInvalidArgument(msgPayloadCannotDecrypt)
	}
	idStr := string(idBytes)
	reloaded := false
	for {
		h, err := c.loadCached(ctx, tenant, false)
		if err != nil {
			if core.CodeOf(err) == core.CodeInternal || core.CodeOf(err) == core.CodeUnavailable {
				return Payload{}, err
			}
			return Payload{}, errInvalidArgument(msgPayloadCannotDecrypt)
		}
		if !handleHasKey(h, idStr) && !reloaded {
			c.invalidate(tenant)
			reloaded = true
			continue
		}
		out, decErr := c.decryptWithHandle(h, tenant, idStr, p.Data)
		if decErr == nil {
			return out, nil
		}
		if !reloaded {
			c.invalidate(tenant)
			reloaded = true
			continue
		}
		return Payload{}, errInvalidArgument(msgPayloadCannotDecrypt)
	}
}

func (c *Codec) decryptWithHandle(h *keyset.Handle, tenant, idStr string, ct []byte) (Payload, error) {
	prim, err := aead.New(h)
	if err != nil {
		return Payload{}, errInvalidArgument(msgPayloadCannotDecrypt)
	}
	keyID, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		return Payload{}, errInvalidArgument(msgPayloadCannotDecrypt)
	}
	if !primitiveHasKey(h, uint32(keyID)) {
		return Payload{}, errInvalidArgument(msgPayloadCannotDecrypt)
	}
	inner, err := prim.Decrypt(ct, payloadAssocData(tenant, idStr))
	if err != nil {
		return Payload{}, errInvalidArgument(msgPayloadCannotDecrypt)
	}
	return unmarshalInner(inner)
}

func (c *Codec) keysetForEncode(ctx context.Context, tenant string) (*keyset.Handle, error) {
	if h, ok := c.cached(tenant); ok {
		return h, nil
	}
	rec, found, err := c.store.Get(tenant)
	if err != nil {
		return nil, err
	}
	if found {
		return c.loadAndCache(ctx, tenant, rec)
	}
	h, err := newDataKeyset()
	if err != nil {
		return nil, err
	}
	wrapped, err := wrapKeyset(ctx, c.keys[0], tenant, h)
	if err != nil {
		return nil, err
	}
	created, err := c.store.CreateIfAbsent(tenant, wrapped)
	if err != nil {
		return nil, err
	}
	if !created {
		rec, found, err = c.store.Get(tenant)
		if err != nil || !found {
			return nil, errInternal(msgKeysetCannotUnwrap)
		}
		return c.loadAndCache(ctx, tenant, rec)
	}
	c.mu.Lock()
	c.cache[tenant] = cacheEntry{handle: h, revision: wrapped.Revision}
	c.mu.Unlock()
	return h, nil
}

func (c *Codec) loadCached(ctx context.Context, tenant string, create bool) (*keyset.Handle, error) {
	if h, ok := c.cached(tenant); ok {
		return h, nil
	}
	rec, found, err := c.store.Get(tenant)
	if err != nil {
		return nil, err
	}
	if !found {
		if create {
			return c.keysetForEncode(ctx, tenant)
		}
		return nil, errInvalidArgument(msgPayloadCannotDecrypt)
	}
	return c.loadAndCache(ctx, tenant, rec)
}

func (c *Codec) loadAndCache(ctx context.Context, tenant string, rec StoredKeyset) (*keyset.Handle, error) {
	h, err := unwrapKeyset(ctx, tenant, rec, c.keys)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.cache[tenant] = cacheEntry{handle: h, revision: rec.Revision}
	c.mu.Unlock()
	return h, nil
}

func (c *Codec) cached(tenant string) (*keyset.Handle, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[tenant]
	if !ok {
		return nil, false
	}
	return e.handle, true
}

func (c *Codec) invalidate(tenant string) {
	c.mu.Lock()
	delete(c.cache, tenant)
	c.mu.Unlock()
}

// RotateDataKey adds a new primary data key for tenant.
func (c *Codec) RotateDataKey(ctx context.Context, tenant string) error {
	rec, found, err := c.store.Get(tenant)
	if err != nil {
		return err
	}
	if !found {
		return errInvalidArgument(msgPayloadCannotDecrypt)
	}
	h, err := unwrapKeyset(ctx, tenant, rec, c.keys)
	if err != nil {
		return err
	}
	rotated, err := rotateDataKey(h)
	if err != nil {
		return err
	}
	wrapped, err := wrapKeyset(ctx, c.keys[0], tenant, rotated)
	if err != nil {
		return err
	}
	wrapped.Revision = rec.Revision + 1
	if err := c.store.CompareAndSwap(tenant, rec.Revision, wrapped); err != nil {
		return err
	}
	c.mu.Lock()
	c.cache[tenant] = cacheEntry{handle: rotated, revision: wrapped.Revision}
	c.mu.Unlock()
	return nil
}

// RotateMasterKey rewraps every stored keyset under keys[0] (the new master).
func (c *Codec) RotateMasterKey(ctx context.Context, newMaster KeyService) error {
	tenants, err := c.store.Tenants()
	if err != nil {
		return err
	}
	for _, tenant := range tenants {
		rec, _, err := c.store.Get(tenant)
		if err != nil {
			return err
		}
		if rec.MasterKeyID == newMaster.MasterKeyID() {
			continue
		}
		h, err := unwrapKeyset(ctx, tenant, rec, c.keys)
		if err != nil {
			return err
		}
		wrapped, err := wrapKeyset(ctx, newMaster, tenant, h)
		if err != nil {
			return err
		}
		wrapped.Revision = rec.Revision + 1
		if err := c.store.CompareAndSwap(tenant, rec.Revision, wrapped); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.cache = map[string]cacheEntry{}
	c.mu.Unlock()
	c.keys = append([]KeyService{newMaster}, c.keys...)
	return nil
}

// PrimaryDataKeyID returns the decimal primary key id for tenant after load.
func (c *Codec) PrimaryDataKeyID(ctx context.Context, tenant string) (string, error) {
	h, err := c.loadCached(ctx, tenant, true)
	if err != nil {
		return "", err
	}
	id := primaryKeyID(h)
	return strconv.FormatUint(uint64(id), 10), nil
}

// DataKeyCount returns how many enabled data keys the tenant keyset holds.
func (c *Codec) DataKeyCount(ctx context.Context, tenant string) (int, error) {
	h, err := c.loadCached(ctx, tenant, true)
	if err != nil {
		return 0, err
	}
	return keyCount(h), nil
}

func handleHasKey(h *keyset.Handle, idStr string) bool {
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		return false
	}
	return primitiveHasKey(h, uint32(id))
}

func primitiveHasKey(h *keyset.Handle, id uint32) bool {
	return keyStatusEnabled(h, id)
}
