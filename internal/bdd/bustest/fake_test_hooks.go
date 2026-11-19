package bustest

// ArmReplayGate blocks replay delivery on the next Subscribe until ReleaseReplayGate.
func (f *Fake) ArmReplayGate() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextReplayGate = make(chan struct{})
}

// ReleaseReplayGate unblocks replay gated by ArmReplayGate.
func (f *Fake) ReleaseReplayGate() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nextReplayGate != nil {
		close(f.nextReplayGate)
		f.nextReplayGate = nil
		return
	}
	for _, s := range f.subs {
		if s.replayGate != nil {
			close(s.replayGate)
			s.replayGate = nil
			return
		}
	}
}

// ReplaySettled returns a channel closed once initial replay delivery has finished.
func (f *Fake) ReplaySettled(subject string) <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.subs {
		if s.subject == subject {
			return s.replaySettled
		}
	}
	return nil
}

// SubscriptionOccupiedAtLeast reports whether undelivered replay plus channel length
// meets n on the first live subscription for subject (test oracle for REQ-BUS-010).
func (f *Fake) SubscriptionOccupiedAtLeast(subject string, n int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.subs {
		if s.subject == subject {
			return s.bufferOccupied() >= n
		}
	}
	return false
}

// SubscriptionEnded reports whether the subscription on subject has ended (fast overflow check).
func (f *Fake) SubscriptionEnded(subject string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.subs {
		if s.subject == subject {
			return s.ended
		}
	}
	return true
}

// SubscriptionDone returns the end signal for the subscription on subject (capture before terminate).
func (f *Fake) SubscriptionDone(subject string) <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.subs {
		if s.subject == subject {
			return s.done
		}
	}
	return nil
}
