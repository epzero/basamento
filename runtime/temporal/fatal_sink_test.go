package temporal

import (
	"errors"
	"testing"
	"testing/synctest"
)

func TestFatalSink(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cb, ch := newFatalSink()
		done := make(chan struct{})
		go func() {
			cb(errors.New("first"))
			cb(errors.New("second"))
			cb(errors.New("third"))
			close(done)
		}()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("sink blocked")
		}
		err := <-ch
		if err == nil || err.Error() != "first" {
			t.Fatalf("delivered error: %v want first", err)
		}
		select {
		case extra := <-ch:
			t.Fatalf("unexpected extra error: %v", extra)
		default:
		}
	})
}
