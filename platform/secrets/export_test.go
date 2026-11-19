package secrets

// TestMemKeysetStore is an in-memory KeysetStore for tests in package secrets_test.
type TestMemKeysetStore = memKeysetStore

// NewTestMemKeysetStore returns an empty in-memory keyset store.
func NewTestMemKeysetStore() *TestMemKeysetStore {
	return &memKeysetStore{data: map[string]StoredKeyset{}}
}
