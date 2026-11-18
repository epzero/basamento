package blob

import "sync"

// keyedMu serializes operations per string key (one in-process mutex per key).
type keyedMu struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func (k *keyedMu) lock(key string) func() {
	k.mu.Lock()
	if k.m == nil {
		k.m = make(map[string]*sync.Mutex)
	}
	lk := k.m[key]
	if lk == nil {
		lk = &sync.Mutex{}
		k.m[key] = lk
	}
	k.mu.Unlock()
	lk.Lock()
	return lk.Unlock
}
