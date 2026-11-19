package steps

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform/bus"
)

func registerBusLimits(sc *godog.ScenarioContext) {
	registerBusLimitsOutcomes(sc)
	sc.Step(`^the entry "([^"]*)" is overwritten outside the store with "([^"]*)"$`, func(ctx context.Context, key, value string) error {
		w := world(ctx).Bus
		if err := w.ensureOpen(ctx); err != nil {
			return err
		}
		raw := []byte(value)
		if w.fake != nil {
			w.fake.WriteForeignRecord(key, raw)
			return nil
		}
		st, ok := w.bus.(*bus.Store)
		if !ok {
			return fmt.Errorf("foreign record needs store")
		}
		return st.WriteForeignKVRecord(ctx, key, raw)
	})

	sc.Step(`^the bus receives call "([^"]*)" on subject "([^"]*)" and key "([^"]*)" with a cancelled context$`, func(ctx context.Context, call, subject, key string) error {
		return busCallWithContext(ctx, call, subject, key, ctxCancelled())
	})

	sc.Step(`^the bus receives call "([^"]*)" on subject "([^"]*)" and key "([^"]*)" with a context whose deadline has passed$`, func(ctx context.Context, call, subject, key string) error {
		dead, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
		cancel()
		return busCallWithContext(ctx, call, subject, key, dead)
	})

	sc.Step(`^none of the errors contains "([^"]*)" or "([^"]*)"$`, func(ctx context.Context, a, b string) error {
		w := world(ctx).Bus
		if w.lastErr == nil {
			return fmt.Errorf("no error recorded")
		}
		msg := w.lastErr.Error()
		if strings.Contains(msg, a) || strings.Contains(msg, b) {
			return fmt.Errorf("error contains forbidden text: %v", w.lastErr)
		}
		return nil
	})

	sc.Step(`^the subject and the key hold no message and no entry$`, func(ctx context.Context) error {
		w := world(ctx).Bus
		if err := w.ensureOpen(ctx); err != nil {
			return err
		}
		readCtx := context.Background()
		ch, err := w.bus.Subscribe(readCtx, "run.r1.deltas", 0)
		if err != nil {
			return err
		}
		select {
		case m, ok := <-ch:
			if ok {
				return fmt.Errorf("unexpected message %q", m.Data)
			}
		default:
		}
		_ = ch
		_, _, err = w.bus.Get(readCtx, "cursor.r1")
		if err == nil {
			return fmt.Errorf("key should be absent")
		}
		if string(core.CodeOf(err)) != string(core.CodeNotFound) {
			return fmt.Errorf("get: %v", err)
		}
		return nil
	})
}

func ctxCancelled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func busCallWithContext(parent context.Context, call, subject, key string, callCtx context.Context) error {
	w := world(parent).Bus
	if err := w.ensureOpen(parent); err != nil {
		w.lastErr = err
		return nil
	}
	var err error
	switch call {
	case "Publish":
		_, err = w.bus.Publish(callCtx, subject, []byte("probe"))
	case "Subscribe from the start":
		_, err = w.bus.Subscribe(callCtx, subject, 0)
	case "LastSeq":
		_, err = w.bus.LastSeq(callCtx, subject)
	case "Purge":
		err = w.bus.Purge(callCtx, subject)
	case "Get":
		_, _, err = w.bus.Get(callCtx, key)
	case "CompareAndSwap":
		_, err = w.bus.CompareAndSwap(callCtx, key, []byte("v"), 1, 0)
	case "Delete":
		err = w.bus.Delete(callCtx, key)
	default:
		return fmt.Errorf("unknown call %q", call)
	}
	w.lastErr = err
	return nil
}

func registerBusLimitsOutcomes(sc *godog.ScenarioContext) {
	sc.Step(`^entry "([^"]*)" is put with a value of (\d+) bytes$`, func(ctx context.Context, key string, n int) error {
		w := world(ctx).Bus
		if err := w.ensureOpen(ctx); err != nil {
			w.lastErr = err
			return nil
		}
		val := make([]byte, n)
		rev, err := w.bus.Put(ctx, key, val, 0)
		w.lastErr = err
		if err == nil {
			w.putRevisionKey(key, rev)
		}
		return nil
	})

	sc.Step(`^entry "([^"]*)" is swapped at revision 1 to a value of (\d+) bytes$`, func(ctx context.Context, key string, n int) error {
		w := world(ctx).Bus
		if err := w.ensureOpen(ctx); err != nil {
			w.lastErr = err
			return nil
		}
		val := make([]byte, n)
		r, err := w.bus.CompareAndSwap(ctx, key, val, 1, 0)
		w.lastErr = err
		if err == nil {
			w.putRevisionKey(key, r)
		}
		return nil
	})

	sc.Step(`^the put (succeeds|fails with code "[^"]+")$`, matchPutOutcome)
	sc.Step(`^the swap (succeeds|fails with code "[^"]+")$`, matchSwapOutcome)
	sc.Step(`^reading entry "([^"]*)" returns (\d+) bytes$`, func(ctx context.Context, key string, n int) error {
		got, _, err := world(ctx).Bus.bus.Get(ctx, key)
		if err != nil {
			return err
		}
		if len(got) != n {
			return fmt.Errorf("len %d want %d", len(got), n)
		}
		return nil
	})
	sc.Step(`^reading entry "([^"]*)" returns the (\d+) bytes of "([^"]*)"$`, func(ctx context.Context, key string, n int, seed string) error {
		got, _, err := world(ctx).Bus.bus.Get(ctx, key)
		if err != nil {
			return err
		}
		if len(got) != n || string(got) != seed {
			return fmt.Errorf("got %q len %d", got, len(got))
		}
		return nil
	})
}

func matchPutOutcome(ctx context.Context, tail string) error {
	w := world(ctx).Bus
	switch tail {
	case `succeeds`:
		return w.checkCode(w.lastErr, "")
	}
	if strings.HasPrefix(tail, `fails with code "`) {
		code := strings.TrimSuffix(strings.TrimPrefix(tail, `fails with code "`), `"`)
		return w.checkCode(w.lastErr, code)
	}
	return fmt.Errorf("unknown put outcome %q", tail)
}

func matchSwapOutcome(ctx context.Context, tail string) error {
	w := world(ctx).Bus
	err := w.lastSwapErr
	if err == nil {
		err = w.lastErr
	}
	switch tail {
	case `succeeds`:
		return w.checkCode(err, "")
	}
	if strings.HasPrefix(tail, `fails with code "`) {
		code := strings.TrimSuffix(strings.TrimPrefix(tail, `fails with code "`), `"`)
		return w.checkCode(err, code)
	}
	return fmt.Errorf("unknown swap outcome %q", tail)
}

func matchReadingEntryOutcome(ctx context.Context, key, tail string) error {
	w := world(ctx).Bus
	if strings.HasPrefix(tail, `returns `) && strings.HasSuffix(tail, ` bytes`) {
		var n int
		if _, err := fmt.Sscanf(tail, "returns %d bytes", &n); err != nil {
			return err
		}
		got, _, err := w.bus.Get(ctx, key)
		if err != nil {
			return err
		}
		if len(got) != n {
			return fmt.Errorf("len %d want %d", len(got), n)
		}
		return nil
	}
	if strings.HasPrefix(tail, `returns the `) {
		want := strings.TrimPrefix(tail, `returns the `)
		want = strings.TrimSuffix(want, ` of "seed"`)
		var n int
		if _, err := fmt.Sscanf(want, "%d bytes", &n); err != nil {
			return err
		}
		got, _, err := w.bus.Get(ctx, key)
		if err != nil {
			return err
		}
		if len(got) != n || string(got) != "seed" {
			return fmt.Errorf("got %q len %d", got, len(got))
		}
		return nil
	}
	if strings.HasPrefix(tail, `fails with code "`) {
		_, _, err := w.bus.Get(ctx, key)
		code := strings.TrimSuffix(strings.TrimPrefix(tail, `fails with code "`), `"`)
		return w.checkCode(err, code)
	}
	return fmt.Errorf("unknown reading outcome %q", tail)
}
