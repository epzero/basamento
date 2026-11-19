package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
)

func registerBusKV(sc *godog.ScenarioContext) {
	// KV
	kvPut := func(ctx context.Context, key, value, ttl string, create bool) error {
		w := world(ctx).Bus
		if err := w.ensureOpen(ctx); err != nil {
			w.lastErr = err
			return nil
		}
		var d time.Duration
		if ttl != "" {
			var err error
			d, err = time.ParseDuration(ttl)
			if err != nil {
				return err
			}
		}
		var rev uint64
		var err error
		if create {
			rev, err = w.bus.CompareAndSwap(ctx, key, []byte(value), 0, d)
		} else {
			rev, err = w.bus.Put(ctx, key, []byte(value), d)
		}
		w.lastErr = err
		if err == nil {
			w.putRevisionKey(key, rev)
			w.lastRev["lastPut"] = rev
			w.putRevs = append(w.putRevs, rev)
			if create && w.revisions["firstCreate"] == 0 {
				w.revisions["firstCreate"] = rev
			}
		}
		return nil
	}

	sc.Step(`^entry "([^"]*)" is put with value "([^"]*)"$`, func(ctx context.Context, key, value string) error {
		return kvPut(ctx, key, value, "", false)
	})

	sc.Step(`^entry "([^"]*)" is put with value "([^"]*)" and TTL "([^"]*)"$`, func(ctx context.Context, key, value, ttl string) error {
		return kvPut(ctx, key, value, ttl, false)
	})

	sc.Step(`^entry "([^"]*)" is created with value "([^"]*)"$`, func(ctx context.Context, key, value string) error {
		return kvPut(ctx, key, value, "", true)
	})

	sc.Step(`^entry "([^"]*)" is created with value "([^"]*)" and TTL "([^"]*)"$`, func(ctx context.Context, key, value, ttl string) error {
		return kvPut(ctx, key, value, ttl, true)
	})

	sc.Step(`^entry "([^"]*)" is read$`, func(ctx context.Context, key string) error {
		w := world(ctx).Bus
		if err := w.ensureOpen(ctx); err != nil {
			w.lastErr = err
			return nil
		}
		readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		_, _, err := w.bus.Get(readCtx, key)
		w.lastErr = err
		return nil
	})

	sc.Step(`^entry "([^"]*)" is deleted$`, func(ctx context.Context, key string) error {
		w := world(ctx).Bus
		if err := w.ensureOpen(ctx); err != nil {
			w.lastErr = err
			return nil
		}
		w.lastErr = w.bus.Delete(ctx, key)
		return nil
	})

	sc.Step(`^entry "([^"]*)" is swapped to "([^"]*)" at its current revision$`, func(ctx context.Context, key, value string) error {
		w := world(ctx).Bus
		if err := w.ensureOpen(ctx); err != nil {
			w.lastErr = err
			return nil
		}
		_, cur, err := w.bus.Get(ctx, key)
		if err != nil {
			w.lastErr = err
			return nil
		}
		r, err := w.bus.CompareAndSwap(ctx, key, []byte(value), cur, 0)
		w.lastErr = err
		if err == nil {
			w.putRevisionKey(key, r)
			w.lastSwapRev = r
		}
		return nil
	})

	sc.Step(`^entry "([^"]*)" is swapped to "([^"]*)" at the revision the first put returned$`, func(ctx context.Context, key, value string) error {
		w := world(ctx).Bus
		if len(w.putRevs) < 1 {
			return fmt.Errorf("no put revision")
		}
		rev := w.putRevs[0]
		r, err := w.bus.CompareAndSwap(ctx, key, []byte(value), rev, 0)
		w.lastErr = err
		if err == nil {
			w.putRevisionKey(key, r)
			w.lastSwapRev = r
		}
		return nil
	})

	sc.Step(`^entry "([^"]*)" is swapped to "([^"]*)" at the revision the create returned$`, func(ctx context.Context, key, value string) error {
		w := world(ctx).Bus
		rev := w.revisions[key]
		r, err := w.bus.CompareAndSwap(ctx, key, []byte(value), rev, 0)
		w.lastErr = err
		if err == nil {
			w.putRevisionKey(key, r)
			w.lastSwapRev = r
		}
		return nil
	})

	sc.Step(`^entry "([^"]*)" is swapped to "([^"]*)" at revision (\d+)$`, func(ctx context.Context, key, value string, rev int) error {
		w := world(ctx).Bus
		if err := w.ensureOpen(ctx); err != nil {
			w.lastErr = err
			return nil
		}
		r, err := w.bus.CompareAndSwap(ctx, key, []byte(value), uint64(rev), 0)
		w.lastErr = err
		if err == nil {
			w.putRevisionKey(key, r)
			w.lastSwapRev = r
		}
		return nil
	})

	sc.Step(`^entry "([^"]*)" is swapped to "([^"]*)" with TTL "([^"]*)" at its current revision$`, func(ctx context.Context, key, value, ttl string) error {
		w := world(ctx).Bus
		_, rev, err := w.bus.Get(ctx, key)
		if err != nil {
			w.lastErr = err
			return nil
		}
		d, err := time.ParseDuration(ttl)
		if err != nil {
			return err
		}
		r, err := w.bus.CompareAndSwap(ctx, key, []byte(value), rev, d)
		w.lastErr = err
		if err == nil {
			w.putRevisionKey(key, r)
			w.lastSwapRev = r
		}
		return nil
	})

	sc.Step(`^entry "([^"]*)" is swapped to "([^"]*)" with TTL "([^"]*)" at the revision the create returned$`, func(ctx context.Context, key, value, ttl string) error {
		w := world(ctx).Bus
		d, err := time.ParseDuration(ttl)
		if err != nil {
			return err
		}
		r, err := w.bus.CompareAndSwap(ctx, key, []byte(value), w.revisions[key], d)
		w.lastErr = err
		if err == nil {
			w.putRevisionKey(key, r)
			w.lastSwapRev = r
		}
		return nil
	})

	sc.Step(`^entry "([^"]*)" is swapped to "([^"]*)" with TTL "([^"]*)" at the revision the first create returned$`, func(ctx context.Context, key, value, ttl string) error {
		w := world(ctx).Bus
		if w.revisions["firstCreate"] == 0 {
			return fmt.Errorf("no first create revision")
		}
		d, err := time.ParseDuration(ttl)
		if err != nil {
			return err
		}
		r, err := w.bus.CompareAndSwap(ctx, key, []byte(value), w.revisions["firstCreate"], d)
		w.lastErr = err
		if err == nil {
			w.putRevisionKey(key, r)
			w.lastSwapRev = r
		}
		return nil
	})

	sc.Step(`^(\d+) callers concurrently swap entry "([^"]*)" to their own name at its current revision$`, func(ctx context.Context, n int, key string) error {
		w := world(ctx).Bus
		if err := w.ensureOpen(ctx); err != nil {
			return err
		}
		_, rev, err := w.bus.Get(ctx, key)
		if err != nil {
			return err
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		var wins, conflicts int
		var other []error
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				name := fmt.Sprintf("caller-%d", i)
				_, swapErr := w.bus.CompareAndSwap(ctx, key, []byte(name), rev, 0)
				mu.Lock()
				defer mu.Unlock()
				if swapErr == nil {
					wins++
					w.swapWinner = name
				} else if string(core.CodeOf(swapErr)) == "conflict" {
					conflicts++
				} else {
					other = append(other, swapErr)
				}
			}(i)
		}
		wg.Wait()
		if len(other) > 0 {
			return fmt.Errorf("unexpected swap errors: %v", other)
		}
		w.swapWins, w.swapConflicts = wins, conflicts
		return nil
	})

	sc.Step(`^the second put returned a greater revision than the first$`, func(ctx context.Context) error {
		w := world(ctx).Bus
		if len(w.putRevs) < 2 || w.putRevs[1] <= w.putRevs[0] {
			return fmt.Errorf("revisions did not increase: %v", w.putRevs)
		}
		return nil
	})

	sc.Step(`^reading entry "([^"]*)" returns value "([^"]*)"$`, func(ctx context.Context, key, want string) error {
		got, _, err := world(ctx).Bus.bus.Get(ctx, key)
		if err != nil {
			return err
		}
		if string(got) != want {
			return fmt.Errorf("got %q", got)
		}
		return nil
	})

	sc.Step(`^reading entry "([^"]*)" returns value "([^"]*)" with the revision the second put returned$`, func(ctx context.Context, key, want string) error {
		got, rev, err := world(ctx).Bus.bus.Get(ctx, key)
		if err != nil {
			return err
		}
		if string(got) != want || rev != world(ctx).Bus.lastRev["lastPut"] {
			return fmt.Errorf("value/rev mismatch")
		}
		return nil
	})

	sc.Step(`^reading entry "([^"]*)" returns value "([^"]*)" with that revision$`, func(ctx context.Context, key, want string) error {
		got, rev, err := world(ctx).Bus.bus.Get(ctx, key)
		if err != nil {
			return err
		}
		if string(got) != want || rev != world(ctx).Bus.revisions[key] {
			return fmt.Errorf("mismatch")
		}
		return nil
	})

	sc.Step(`^reading entry "([^"]*)" returns value "([^"]*)" with the revision its put returned$`, func(ctx context.Context, key, want string) error {
		got, rev, err := world(ctx).Bus.bus.Get(ctx, key)
		if err != nil {
			return err
		}
		if string(got) != want || rev != world(ctx).Bus.revisions[key] {
			return fmt.Errorf("mismatch")
		}
		return nil
	})

	sc.Step(`^reading entry "([^"]*)" returns the name of the caller whose swap succeeded$`, func(ctx context.Context, key string) error {
		got, _, err := world(ctx).Bus.bus.Get(ctx, key)
		if err != nil {
			return err
		}
		if string(got) != world(ctx).Bus.swapWinner {
			return fmt.Errorf("got %q winner %q", got, world(ctx).Bus.swapWinner)
		}
		return nil
	})

	sc.Step(`^the read fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		return world(ctx).Bus.checkCode(world(ctx).Bus.lastErr, code)
	})

	sc.Step(`^reading entry "([^"]*)" fails with code "([^"]*)"$`, func(ctx context.Context, key, code string) error {
		_, _, err := world(ctx).Bus.bus.Get(ctx, key)
		return world(ctx).Bus.checkCode(err, code)
	})

	sc.Step(`^the put fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		return world(ctx).Bus.checkCode(world(ctx).Bus.lastErr, code)
	})

	sc.Step(`^the swap fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		return world(ctx).Bus.checkCode(world(ctx).Bus.lastErr, code)
	})

	sc.Step(`^the create fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		return world(ctx).Bus.checkCode(world(ctx).Bus.lastErr, code)
	})

	sc.Step(`^the create returns a revision$`, func(ctx context.Context) error {
		if world(ctx).Bus.lastErr != nil {
			return world(ctx).Bus.lastErr
		}
		return nil
	})

	sc.Step(`^the delete succeeds$`, func(ctx context.Context) error {
		return world(ctx).Bus.checkCode(world(ctx).Bus.lastErr, "")
	})

	sc.Step(`^exactly (\d+) swap succeeds and (\d+) fail with code "([^"]*)"$`, func(ctx context.Context, ok, fail int, code string) error {
		w := world(ctx).Bus
		if w.swapWins != ok || w.swapConflicts != fail {
			return fmt.Errorf("wins=%d conflicts=%d want %d/%d", w.swapWins, w.swapConflicts, ok, fail)
		}
		if code != "conflict" {
			return fmt.Errorf("unsupported code check %q", code)
		}
		if w.swapConflicts > 0 && code != string(core.CodeConflict) {
			return fmt.Errorf("want conflict failures")
		}
		return nil
	})

	sc.Step(`^the swap returns a greater revision than the put$`, func(ctx context.Context) error {
		w := world(ctx).Bus
		if w.lastErr != nil {
			return w.lastErr
		}
		if len(w.putRevs) < 1 {
			return fmt.Errorf("no put revision recorded")
		}
		if w.lastSwapRev <= w.putRevs[0] {
			return fmt.Errorf("swap rev %d not greater than put rev %d", w.lastSwapRev, w.putRevs[0])
		}
		return nil
	})

	sc.Step(`^the embedded bus is opened$`, func(ctx context.Context) error {
		return world(ctx).Bus.ensureOpen(ctx)
	})
	sc.Step(`^the open succeeds$`, func(ctx context.Context) error {
		if err := world(ctx).Bus.lastErr; err != nil {
			return err
		}
		return nil
	})
	sc.Step(`^the store directory of the embedded data directory has mode "([^"]*)"$`, func(ctx context.Context, mode string) error {
		w := world(ctx).Bus
		if w.dataDir == "" {
			dir, err := os.MkdirTemp("", "bus-embed-*")
			if err != nil {
				return err
			}
			w.dataDir = dir
		}
		storeDir := filepath.Join(w.dataDir, "nats")
		if err := os.MkdirAll(storeDir, 0o755); err != nil {
			return err
		}
		perm, err := strconv.ParseUint(mode, 8, 32)
		if err != nil {
			return err
		}
		return os.Chmod(storeDir, os.FileMode(perm))
	})
	sc.Step(`^the store directory has mode "([^"]*)"$`, func(ctx context.Context, mode string) error {
		w := world(ctx).Bus
		info, err := os.Stat(filepath.Join(w.dataDir, "nats"))
		if err != nil {
			return err
		}
		if got := fmt.Sprintf("%04o", info.Mode().Perm()); got != mode {
			return fmt.Errorf("store directory mode = %s, want %s", got, mode)
		}
		return nil
	})

}
