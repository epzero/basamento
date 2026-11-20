package bustest

// WriteForeignRecord installs a raw KV value the store cannot decode (REQ-BUS-026 driver).
func (f *Fake) WriteForeignRecord(key string, raw []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kvRevSeq++
	f.kv[key] = kvEntry{value: append([]byte(nil), raw...), rev: f.kvRevSeq}
}
