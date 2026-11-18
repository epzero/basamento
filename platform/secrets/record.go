package secrets

// StoredKeyset is one tenant's wrapped Tink keyset on disk.
type StoredKeyset struct {
	Wrapped     []byte
	MasterKeyID string
	Revision    uint64
}

// KeysetStore persists wrapped keysets with compare-and-swap.
type KeysetStore interface {
	// Get returns the stored keyset and true, or false when absent.
	Get(tenant string) (StoredKeyset, bool, error)
	// CreateIfAbsent writes record when no row exists; reports whether it created.
	CreateIfAbsent(tenant string, record StoredKeyset) (created bool, err error)
	// CompareAndSwap updates when revision matches.
	CompareAndSwap(tenant string, expectRevision uint64, record StoredKeyset) error
	// Count returns how many tenants have a stored keyset.
	Count() (int, error)
	// Tenants lists tenants with a stored keyset.
	Tenants() ([]string, error)
}
