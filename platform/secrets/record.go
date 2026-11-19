package secrets

import "context"

// StoredKeyset is one tenant's wrapped Tink keyset on disk.
type StoredKeyset struct {
	Wrapped     []byte
	MasterKeyID string
	Revision    uint64
}

// KeysetStore persists wrapped keysets with compare-and-swap.
type KeysetStore interface {
	Get(ctx context.Context, tenant string) (StoredKeyset, bool, error)
	CreateIfAbsent(ctx context.Context, tenant string, record StoredKeyset) (created bool, err error)
	CompareAndSwap(ctx context.Context, tenant string, expectRevision uint64, record StoredKeyset) error
	Count(ctx context.Context) (int, error)
	Tenants(ctx context.Context) ([]string, error)
}
