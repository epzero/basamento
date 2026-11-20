package local

import "github.com/epzero/basamento/runtime"

// localFutureSnap is the readiness and completion seq of one *future, taken
// under r.mu so a timer batch is visible all-or-nothing (same as fireDue).
type localFutureSnap struct {
	isLocal bool
	ready   bool
	seq     uint64
}

// snapshotLocalFutures reads each run-owned future in fs under r.mu. Foreign
// futures are left zero and evaluated later without holding r.mu.
func (r *run) snapshotLocalFutures(fs []runtime.Future) []localFutureSnap {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]localFutureSnap, len(fs))
	for i, f := range fs {
		local, ok := f.(*future)
		if !ok {
			continue
		}
		out[i].isLocal = true
		local.mu.Lock()
		select {
		case <-local.done:
			out[i].ready = true
			out[i].seq = local.seq
		default:
		}
		local.mu.Unlock()
	}
	return out
}

// lowestReadyAtCall is the pure at-call scan: lowest ready index from a prior
// snapshot for local futures, and Ready() for foreign ones outside r.mu.
func lowestReadyAtCall(fs []runtime.Future, snaps []localFutureSnap) (int, bool) {
	for i, f := range fs {
		if snaps[i].isLocal {
			if snaps[i].ready {
				return i, true
			}
			continue
		}
		if f.Ready() {
			return i, true
		}
	}
	return 0, false
}

// firstCompletedFromSnap is the pure by-seq scan: earliest completion seq from
// a prior snapshot for locals; foreign Ready() runs outside r.mu and wins at once.
func firstCompletedFromSnap(fs []runtime.Future, snaps []localFutureSnap) int {
	index := -1
	var earliest uint64
	for i, f := range fs {
		if !snaps[i].isLocal {
			if f.Ready() {
				return i
			}
			continue
		}
		if !snaps[i].ready {
			continue
		}
		if index < 0 || snaps[i].seq < earliest {
			index, earliest = i, snaps[i].seq
		}
	}
	return index
}

// lowestReady returns the lowest index with a ready future and true, or (_, false).
// It snapshots local futures under r.mu, then runs lowestReadyAtCall.
func (r *run) lowestReady(fs []runtime.Future) (int, bool) {
	return lowestReadyAtCall(fs, r.snapshotLocalFutures(fs))
}
