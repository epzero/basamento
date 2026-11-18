package sql

// txActiveKey marks an open Store.InTx on a specific store in the context chain.
type txActiveKey struct {
	store *Store
}
