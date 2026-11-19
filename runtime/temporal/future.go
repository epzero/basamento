package temporal

import (
	"encoding/json"
	"sync"
)

// future is one pending durable operation. A result is stored as the encoded
// history payload; Get decodes it for the run's tenant, which is where an
// unencrypted or foreign payload fails (REQ-RT-032, REQ-RT-033).
type future struct {
	run *run

	mu      sync.Mutex
	done    chan struct{}
	raw     json.RawMessage
	encoded *Encoded
	decode  bool
	err     error
}

func newFuture(r *run, decode bool) *future {
	return &future{run: r, done: make(chan struct{}), decode: decode}
}

// completeRaw completes the future with literal JSON that needs no decoding.
func (f *future) completeRaw(raw json.RawMessage, err error) bool {
	return f.complete(raw, nil, err)
}

// completeEncoded completes the future with a history payload.
func (f *future) completeEncoded(encoded *Encoded, raw json.RawMessage, err error) bool {
	return f.complete(raw, encoded, err)
}

func (f *future) complete(raw json.RawMessage, encoded *Encoded, err error) bool {
	f.mu.Lock()
	select {
	case <-f.done:
		f.mu.Unlock()
		return false
	default:
	}
	f.raw = append(json.RawMessage(nil), raw...)
	f.encoded = encoded
	f.err = err
	close(f.done)
	f.mu.Unlock()
	if f.run != nil {
		f.run.notify()
	}
	return true
}

// Get waits for the future and writes its result into out. The encoded payload
// is decoded on every read, so a history payload that is readable or belongs to
// another tenant fails the step that reads it.
func (f *future) Get(out any) error {
	<-f.done
	f.mu.Lock()
	raw, encoded, decode, err := f.raw, f.encoded, f.decode, f.err
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if decode && encoded != nil {
		plain, decodeErr := f.run.rt.codec.Decode(f.run.ctx, encoded.Payload)
		if decodeErr != nil {
			return decodeErr
		}
		raw = append(json.RawMessage(nil), plain.Data...)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if target, ok := out.(*json.RawMessage); ok {
		*target = raw
		return nil
	}
	if target, ok := out.(*[]byte); ok {
		*target = append([]byte(nil), raw...)
		return nil
	}
	return json.Unmarshal(raw, out)
}

// Ready reports whether the operation has completed.
func (f *future) Ready() bool {
	select {
	case <-f.done:
		return true
	default:
		return false
	}
}

func failedFuture(r *run, err error) *future {
	f := newFuture(r, false)
	f.completeRaw(nil, err)
	return f
}
