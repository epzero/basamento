package secrets

import (
	"context"
	"log/slog"
	"strconv"
	"sync"

	"github.com/epzero/basamento/core"
	"github.com/tink-crypto/tink-go/v2/aead"
	"github.com/tink-crypto/tink-go/v2/keyset"
)

// cacheEntry holds a decoded keyset; every store write bumps revision.
type cacheEntry struct {
	handle   *keyset.Handle
	revision uint64
}

// Codec encrypts and decrypts payloads with tenant data keys (WP-L1-04).
//
// Each encode performs one KeysetStore read (no key-service call). When the
// stored revision matches the cache, the cached handle is used; otherwise the
// record is unwrapped once and the cache is replaced. Decode parses the key id
// first; on authentication failure it does not reload the keyset.
type Codec struct {
	store  KeysetStore
	keysMu sync.RWMutex
	keys   []KeyService
	mu     sync.Mutex
	cache  map[string]cacheEntry
	opts   options
}

// NewCodec returns a payload codec. keys[0] is the current master key; the rest
// are previous keys still able to unwrap stored keysets. A nil store, an empty
// keys slice, a nil KeyService, or duplicate MasterKeyID values are refused with
// invalid_argument.
func NewCodec(store KeysetStore, keys []KeyService, opts ...Option) (*Codec, error) {
	if store == nil {
		return nil, errInvalidArgument("keyset store is required")
	}
	if len(keys) == 0 {
		return nil, errInvalidArgument("master key is required")
	}
	seen := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		if k == nil {
			return nil, errInvalidArgument("master key is required")
		}
		id := k.MasterKeyID()
		if _, dup := seen[id]; dup {
			return nil, errInvalidArgument("duplicate master key id in codec configuration")
		}
		seen[id] = struct{}{}
	}
	o := applyOptions(opts)
	return &Codec{store: store, keys: keys, cache: map[string]cacheEntry{}, opts: o}, nil
}

// Close closes every configured master KeyService.
func (c *Codec) Close() error {
	c.keysMu.RLock()
	defer c.keysMu.RUnlock()
	var first error
	for _, k := range c.keys {
		if err := k.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (c *Codec) tenant(ctx context.Context) (string, error) {
	id, ok := core.TenantFrom(ctx)
	if !ok {
		return "", errInvalidArgument(msgTenantRequired)
	}
	t := string(id)
	if err := ValidateTenant(t); err != nil {
		return "", err
	}
	return t, nil
}

func (c *Codec) snapshotKeys() []KeyService {
	c.keysMu.RLock()
	defer c.keysMu.RUnlock()
	out := make([]KeyService, len(c.keys))
	copy(out, c.keys)
	return out
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
	out := Payload{
		Metadata: map[string][]byte{
			"encoding":          []byte(EncodingEncrypted),
			MetaEncryptionKeyID: []byte(idStr),
		},
		Data: ct,
	}
	c.opts.loggerOrDefault().Debug("payload encoded", slog.String("tenant", tenant))
	return out, nil
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
	keyID, err := parseDataKeyID(p.Metadata[MetaEncryptionKeyID])
	if err != nil {
		return Payload{}, err
	}
	idStr := strconv.FormatUint(uint64(keyID), 10)

	h, err := c.decodeHandle(ctx, tenant, keyID)
	if err != nil {
		return Payload{}, err
	}
	out, decErr := c.decryptWithHandle(h, tenant, idStr, p.Data)
	if decErr != nil {
		return Payload{}, errInvalidArgument(msgPayloadCannotDecrypt)
	}
	c.opts.loggerOrDefault().Debug("payload decoded", slog.String("tenant", tenant))
	return out, nil
}

func (c *Codec) decodeHandle(ctx context.Context, tenant string, keyID uint32) (*keyset.Handle, error) {
	ent, cached := c.cacheEntry(tenant)
	if cached && handleHasKey(ent.handle, keyID) {
		return ent.handle, nil
	}
	rec, found, err := c.store.Get(ctx, tenant)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errInvalidArgument(msgPayloadCannotDecrypt)
	}
	if cached {
		if ent.revision == rec.Revision {
			return nil, errInvalidArgument(msgPayloadCannotDecrypt)
		}
		h, err := c.unwrapAndCache(ctx, tenant, rec)
		if err != nil {
			return nil, err
		}
		if !handleHasKey(h, keyID) {
			return nil, errInvalidArgument(msgPayloadCannotDecrypt)
		}
		return h, nil
	}
	h, err := c.unwrapAndCache(ctx, tenant, rec)
	if err != nil {
		return nil, err
	}
	if !handleHasKey(h, keyID) {
		return nil, errInvalidArgument(msgPayloadCannotDecrypt)
	}
	return h, nil
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
	if !handleHasKey(h, uint32(keyID)) {
		return Payload{}, errInvalidArgument(msgPayloadCannotDecrypt)
	}
	inner, err := prim.Decrypt(ct, payloadAssocData(tenant, idStr))
	if err != nil {
		c.opts.loggerOrDefault().Debug("payload decrypt failed", slog.String("tenant", tenant))
		return Payload{}, errInvalidArgument(msgPayloadCannotDecrypt)
	}
	out, err := unmarshalInner(inner)
	if err != nil {
		c.opts.loggerOrDefault().Debug("payload inner parse failed", slog.String("tenant", tenant))
		return Payload{}, err
	}
	return out, nil
}

func (c *Codec) keysetForEncode(ctx context.Context, tenant string) (*keyset.Handle, error) {
	rec, found, err := c.store.Get(ctx, tenant)
	if err != nil {
		return nil, err
	}
	if found {
		ent, ok := c.cacheEntry(tenant)
		if ok && ent.revision == rec.Revision {
			return ent.handle, nil
		}
		return c.unwrapAndCache(ctx, tenant, rec)
	}
	return c.createKeyset(ctx, tenant)
}

func (c *Codec) createKeyset(ctx context.Context, tenant string) (*keyset.Handle, error) {
	c.keysMu.RLock()
	defer c.keysMu.RUnlock()
	h, err := newDataKeyset()
	if err != nil {
		return nil, err
	}
	master := c.keys[0]
	wrapped, err := wrapKeyset(ctx, master, tenant, h)
	if err != nil {
		return nil, err
	}
	created, err := c.store.CreateIfAbsent(ctx, tenant, wrapped)
	if err != nil {
		return nil, err
	}
	if !created {
		rec, found, err := c.store.Get(ctx, tenant)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, errInternal(msgKeysetCannotUnwrap)
		}
		keys := append([]KeyService(nil), c.keys...)
		uh, err := unwrapKeyset(ctx, tenant, rec, keys)
		if err != nil {
			return nil, err
		}
		c.setCache(tenant, cacheEntry{handle: uh, revision: rec.Revision})
		return uh, nil
	}
	c.setCache(tenant, cacheEntry{handle: h, revision: wrapped.Revision})
	return h, nil
}

func (c *Codec) unwrapAndCache(ctx context.Context, tenant string, rec StoredKeyset) (*keyset.Handle, error) {
	keys := c.snapshotKeys()
	h, err := unwrapKeyset(ctx, tenant, rec, keys)
	if err != nil {
		return nil, err
	}
	c.setCache(tenant, cacheEntry{handle: h, revision: rec.Revision})
	return h, nil
}

func (c *Codec) cacheEntry(tenant string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[tenant]
	return e, ok
}

func (c *Codec) setCache(tenant string, e cacheEntry) {
	c.mu.Lock()
	c.cache[tenant] = e
	c.mu.Unlock()
}

// RotateDataKey adds a new primary data key for the tenant in ctx.
func (c *Codec) RotateDataKey(ctx context.Context) error {
	tenant, err := c.tenant(ctx)
	if err != nil {
		return err
	}
	rec, found, err := c.store.Get(ctx, tenant)
	if err != nil {
		return err
	}
	if !found {
		return errNotFound(msgTenantNoKeyset)
	}
	keys := c.snapshotKeys()
	h, err := unwrapKeyset(ctx, tenant, rec, keys)
	if err != nil {
		return err
	}
	rotated, err := rotateDataKey(h)
	if err != nil {
		return err
	}
	c.keysMu.RLock()
	master := c.keys[0]
	c.keysMu.RUnlock()
	wrapped, err := wrapKeyset(ctx, master, tenant, rotated)
	if err != nil {
		return err
	}
	wrapped.Revision = rec.Revision + 1
	if err := c.store.CompareAndSwap(ctx, tenant, rec.Revision, wrapped); err != nil {
		return err
	}
	c.setCache(tenant, cacheEntry{handle: rotated, revision: wrapped.Revision})
	return nil
}

const maxRotationPasses = 3
const maxCASRetries = 3

// RotateMasterKey rewraps every stored keyset under keys[0] (the new master).
func (c *Codec) RotateMasterKey(ctx context.Context, newMaster KeyService) error {
	if err := canaryMaster(ctx, newMaster); err != nil {
		return err
	}
	c.keysMu.Lock()
	c.prependMasterKeyLocked(newMaster)
	newKeys := append([]KeyService(nil), c.keys...)
	c.keysMu.Unlock()

	for pass := 0; pass < maxRotationPasses; pass++ {
		tenants, err := c.store.Tenants(ctx)
		if err != nil {
			return err
		}
		changed := false
		for _, tenant := range tenants {
			if err := ValidateTenant(tenant); err != nil {
				return err
			}
			done, err := c.rewrapTenant(ctx, tenant, newMaster, newKeys)
			if err != nil {
				return err
			}
			if done {
				changed = true
			}
		}
		if !changed {
			c.mu.Lock()
			c.cache = map[string]cacheEntry{}
			c.mu.Unlock()
			return nil
		}
	}
	return errConflict(msgKeysetRevisionConflict)
}

func (c *Codec) prependMasterKeyLocked(ks KeyService) {
	id := ks.MasterKeyID()
	out := []KeyService{ks}
	for _, k := range c.keys {
		if k.MasterKeyID() == id {
			continue
		}
		out = append(out, k)
	}
	c.keys = out
}

func canaryMaster(ctx context.Context, ks KeyService) error {
	plain := []byte("canary")
	ct, err := ks.Wrap(ctx, canaryTenant, plain)
	if err != nil {
		return err
	}
	out, err := ks.Unwrap(ctx, canaryTenant, ct)
	if err != nil {
		return err
	}
	if string(out) != string(plain) {
		return errInternal(msgKeyServiceRefused)
	}
	return nil
}

func (c *Codec) rewrapTenant(ctx context.Context, tenant string, newMaster KeyService, keys []KeyService) (bool, error) {
	for attempt := 0; attempt < maxCASRetries; attempt++ {
		rec, found, err := c.store.Get(ctx, tenant)
		if err != nil {
			return false, err
		}
		if !found {
			return false, nil
		}
		if rec.MasterKeyID == newMaster.MasterKeyID() {
			if _, err := unwrapWithKeys(ctx, tenant, rec.Wrapped, rec.MasterKeyID, keys); err != nil {
				return false, err
			}
			return false, nil
		}
		h, err := unwrapKeyset(ctx, tenant, rec, keys)
		if err != nil {
			return false, err
		}
		wrapped, err := wrapKeyset(ctx, newMaster, tenant, h)
		if err != nil {
			return false, err
		}
		wrapped.Revision = rec.Revision + 1
		if err := c.store.CompareAndSwap(ctx, tenant, rec.Revision, wrapped); err != nil {
			if core.CodeOf(err) == core.CodeConflict {
				continue
			}
			return false, err
		}
		return true, nil
	}
	return false, errConflict(msgKeysetRevisionConflict)
}

// PrimaryDataKeyID returns the decimal primary key id for the tenant in ctx.
func (c *Codec) PrimaryDataKeyID(ctx context.Context) (string, error) {
	tenant, err := c.tenant(ctx)
	if err != nil {
		return "", err
	}
	h, err := c.keysetReadOnly(ctx, tenant)
	if err != nil {
		return "", err
	}
	id := primaryKeyID(h)
	return strconv.FormatUint(uint64(id), 10), nil
}

// DataKeyCount returns how many data keys the tenant keyset holds.
func (c *Codec) DataKeyCount(ctx context.Context) (int, error) {
	tenant, err := c.tenant(ctx)
	if err != nil {
		return 0, err
	}
	h, err := c.keysetReadOnly(ctx, tenant)
	if err != nil {
		return 0, err
	}
	return keyCount(h), nil
}

func (c *Codec) keysetReadOnly(ctx context.Context, tenant string) (*keyset.Handle, error) {
	rec, found, err := c.store.Get(ctx, tenant)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errNotFound(msgTenantNoKeyset)
	}
	ent, ok := c.cacheEntry(tenant)
	if ok && ent.revision == rec.Revision {
		return ent.handle, nil
	}
	return c.unwrapAndCache(ctx, tenant, rec)
}

type keyServiceOpener func(context.Context, string) (KeyService, error)

// OpenCodec opens a payload codec with the current master key url and optional
// previous master key urls (design §2). Duplicate master ids across those urls
// are refused with invalid_argument.
func OpenCodec(ctx context.Context, store KeysetStore, currentURL string, prevURLs []string, opts ...Option) (*Codec, error) {
	return openCodec(ctx, store, currentURL, prevURLs, OpenKeyService, opts...)
}

func openCodec(ctx context.Context, store KeysetStore, currentURL string, prevURLs []string, open keyServiceOpener, opts ...Option) (*Codec, error) {
	cur, err := open(ctx, currentURL)
	if err != nil {
		return nil, err
	}
	keys := []KeyService{cur}
	for _, u := range prevURLs {
		ks, err := open(ctx, u)
		if err != nil {
			closeKeyServices(keys)
			return nil, err
		}
		keys = append(keys, ks)
	}
	c, err := NewCodec(store, keys, opts...)
	if err != nil {
		closeKeyServices(keys)
		return nil, err
	}
	return c, nil
}

func closeKeyServices(keys []KeyService) {
	for _, k := range keys {
		_ = k.Close()
	}
}
