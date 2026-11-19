package bustest

// WriteForeignRecord installs a raw KV value the store cannot decode (REQ-BUS-026 driver).
func (f *Fake) WriteForeignRecord(key string, raw []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kv[key] = kvEntry{value: append([]byte(nil), raw...), rev: 1}
}

// SetKVRevision sets the revision seen by the fake after a corrupt install.
func (f *Fake) SetKVRevision(key string, rev uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.kv[key]; ok {
		e.rev = rev
		f.kv[key] = e
	}
}
