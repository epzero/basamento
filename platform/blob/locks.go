package blob

import "sync"

// keyedMu serializes operations per string key with bounded memory.
type keyedMu struct {
	mu sync.Mutex
	m  map[string]*keyedEntry
}

type keyedEntry struct {
	mu  sync.Mutex
	ref int
}

func (k *keyedMu) lock(key string) func() {
	k.mu.Lock()
	if k.m == nil {
		k.m = make(map[string]*keyedEntry)
	}
	ent := k.m[key]
	if ent == nil {
		ent = &keyedEntry{}
		k.m[key] = ent
	}
	ent.ref++
	k.mu.Unlock()

	ent.mu.Lock()
	return func() {
		ent.mu.Unlock()
		k.mu.Lock()
		ent.ref--
		if ent.ref == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
}

func (k *keyedMu) activeKeys() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.m)
}
