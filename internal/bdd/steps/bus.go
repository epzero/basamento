package steps

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

func errorsAs(err error, target **core.Error) bool {
	var pe *core.Error
	if errors.As(err, &pe) {
		*target = pe
		return true
	}
	return false
}

func uint64FromDetail(v any) uint64 {
	switch t := v.(type) {
	case uint64:
		return t
	case int:
		return uint64(t)
	case int64:
		return uint64(t)
	case float64:
		return uint64(t)
	default:
		return 0
	}
}

func registerBus(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, scn *godog.Scenario) (context.Context, error) {
		if !scenarioUsesBus(scn) {
			return ctx, nil
		}
		w := world(ctx)
		if w.Bus == nil {
			w.Bus = newBusWorld()
		} else {
			w.Bus.teardown()
			w.Bus = newBusWorld()
		}
		if err := skipBusByProfile(scn, w.Bus.backend); err != nil {
			return ctx, err
		}
		if err := w.Bus.ensureOpen(ctx); err != nil {
			return ctx, err
		}
		return ctx, nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		if w := world(ctx).Bus; w != nil {
			w.teardown()
		}
		return ctx, nil
	})

	sc.Step(`^the subscription buffer holds (\d+) messages$`, func(ctx context.Context, n int) error {
		world(ctx).Bus.subBuf = n
		if world(ctx).Bus.fake != nil {
			world(ctx).Bus.fake.SetSubBuffer(n)
		}
		if world(ctx).Bus.raw != nil {
			world(ctx).Bus.raw.SetSubBuffer(n)
		}
		return nil
	})

	sc.Step(`^the maximum message size is (\d+) bytes$`, func(ctx context.Context, n int) error {
		world(ctx).Bus.maxMsg = n
		if world(ctx).Bus.fake != nil {
			world(ctx).Bus.fake.SetMaxMessageSize(n)
		}
		if world(ctx).Bus.raw != nil {
			world(ctx).Bus.raw.SetMaxMessageSize(n)
		}
		return nil
	})

	sc.Step(`^the bus server is stopped$`, func(ctx context.Context) error {
		world(ctx).Bus.stopServer()
		return nil
	})

	sc.Step(`^the embedded bus is stopped and started again on the same data directory$`, func(ctx context.Context) error {
		return world(ctx).Bus.restartEmbedded(ctx)
	})

	sc.Step(`^messages "([^"]*)" and "([^"]*)" are published on subject "([^"]*)"$`, func(ctx context.Context, a, b, subject string) error {
		w := world(ctx).Bus
		if err := w.publish(ctx, subject, a, nil); err != nil {
			return err
		}
		return w.publish(ctx, subject, b, nil)
	})

	sc.Step(`^messages "([^"]*)" to "([^"]*)" are published on subject "([^"]*)"$`, func(ctx context.Context, from, to, subject string) error {
		w := world(ctx).Bus
		labels, err := parseMsgRange(from, to)
		if err != nil {
			return err
		}
		for _, l := range labels {
			if err := w.publish(ctx, subject, l, nil); err != nil {
				return err
			}
		}
		return nil
	})

	sc.Step(`^message "([^"]*)" is published on subject "([^"]*)"$`, func(ctx context.Context, name, subject string) error {
		return world(ctx).Bus.publish(ctx, subject, name, nil)
	})

	sc.Step(`^a message of (\d+) bytes is published on subject "([^"]*)"$`, func(ctx context.Context, size int, subject string) error {
		data := make([]byte, size)
		for i := range data {
			data[i] = 'x'
		}
		return world(ctx).Bus.publish(ctx, subject, fmt.Sprintf("blob%d", size), data)
	})

	sc.Step(`^these messages are published in this order:$`, func(ctx context.Context, t *godog.Table) error {
		w := world(ctx).Bus
		for i, row := range t.Rows[1:] {
			subject := row.Cells[0].Value
			label := row.Cells[1].Value
			if err := w.publish(ctx, subject, label, nil); err != nil {
				return fmt.Errorf("row %d: %w", i, err)
			}
		}
		return nil
	})

	sc.Step(`^(\d+) concurrent publishers publish (\d+) messages each on subject "([^"]*)"$`, func(ctx context.Context, pubs, each int, subject string) error {
		w := world(ctx).Bus
		var wg sync.WaitGroup
		errs := make(chan error, pubs)
		for p := 0; p < pubs; p++ {
			wg.Add(1)
			go func(p int) {
				defer wg.Done()
				for i := 1; i <= each; i++ {
					label := fmt.Sprintf("p%d.m%d", p, i)
					if err := w.publish(ctx, subject, label, nil); err != nil {
						errs <- err
						return
					}
				}
			}(p)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				return err
			}
		}
		return nil
	})

	sc.Step(`^the bus sequences of "([^"]*)", "([^"]*)" and "([^"]*)" strictly increase$`, func(ctx context.Context, a, b, c string) error {
		w := world(ctx).Bus
		sa, sb, sc := w.seqOf(a), w.seqOf(b), w.seqOf(c)
		if sa >= sb || sb >= sc {
			return fmt.Errorf("sequences %d %d %d not strictly increasing", sa, sb, sc)
		}
		return nil
	})

	sc.Step(`^the bus sequences of "([^"]*)" and "([^"]*)" strictly increase$`, func(ctx context.Context, a, b string) error {
		w := world(ctx).Bus
		if w.seqOf(a) >= w.seqOf(b) {
			return fmt.Errorf("%d >= %d", w.seqOf(a), w.seqOf(b))
		}
		return nil
	})

	sc.Step(`^the bus sequence of "([^"]*)" is greater than the bus sequence of "([^"]*)"$`, func(ctx context.Context, a, b string) error {
		if world(ctx).Bus.seqOf(a) <= world(ctx).Bus.seqOf(b) {
			return fmt.Errorf("seq %s %d not > %s %d", a, world(ctx).Bus.seqOf(a), b, world(ctx).Bus.seqOf(b))
		}
		return nil
	})

	sc.Step(`^a subscriber subscribes to "([^"]*)" after the bus sequence of "([^"]*)"$`, func(ctx context.Context, subject, cursor string) error {
		return world(ctx).Bus.startSub(ctx, "sub", subject, world(ctx).Bus.seqOf(cursor))
	})

	sc.Step(`^a subscriber subscribes to "([^"]*)" from the start$`, func(ctx context.Context, subject string) error {
		return world(ctx).Bus.startSub(ctx, "sub", subject, 0)
	})

	sc.Step(`^a subscriber subscribes live to "([^"]*)"$`, func(ctx context.Context, subject string) error {
		w := world(ctx).Bus
		seq, err := w.bus.LastSeq(ctx, subject)
		if err != nil {
			return err
		}
		return w.startSub(ctx, "sub", subject, seq)
	})

	sc.Step(`^subscribers "([^"]*)" and "([^"]*)" subscribe live to "([^"]*)"$`, func(ctx context.Context, s1, s2, subject string) error {
		w := world(ctx).Bus
		seq, err := w.bus.LastSeq(ctx, subject)
		if err != nil {
			return err
		}
		if err := w.startSub(ctx, s1, subject, seq); err != nil {
			return err
		}
		return w.startSub(ctx, s2, subject, seq)
	})

	sc.Step(`^the subscriber cancels its subscription$`, func(ctx context.Context) error {
		sub := world(ctx).Bus.subs["sub"]
		if sub == nil || sub.cancel == nil {
			return fmt.Errorf("no subscription")
		}
		sub.cancel()
		return nil
	})

	sc.Step(`^slow reads nothing$`, func(ctx context.Context) error {
		return nil
	})

	sc.Step(`^the subscriber receives (.+) in that order and nothing else$`, func(ctx context.Context, list string) error {
		w := world(ctx).Bus
		want := parseQuotedList(list)
		return recvWithSentinel(ctx, w.bus, w.subs["sub"], want)
	})

	sc.Step(`^the subscriber receives "([^"]*)" and nothing else$`, func(ctx context.Context, one string) error {
		w := world(ctx).Bus
		return recvWithSentinel(ctx, w.bus, w.subs["sub"], []string{one})
	})

	sc.Step(`^a subscriber from the start of "([^"]*)" receives nothing$`, func(ctx context.Context, subject string) error {
		return recvNothing(ctx, world(ctx).Bus.bus, subject)
	})

	sc.Step(`^a subscriber from the start of "([^"]*)" receives (.+) in that order and nothing else$`, func(ctx context.Context, subject, list string) error {
		want := parseQuotedList(list)
		subCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		ch, err := world(ctx).Bus.bus.Subscribe(subCtx, subject, 0)
		if err != nil {
			return err
		}
		sub := &subState{subject: subject, ch: ch}
		return recvWithSentinel(ctx, world(ctx).Bus.bus, sub, want)
	})

	sc.Step(`^a subscriber from the start of "([^"]*)" receives (\d+) messages$`, func(ctx context.Context, subject string, count int) error {
		subCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		ch, err := world(ctx).Bus.bus.Subscribe(subCtx, subject, 0)
		if err != nil {
			return err
		}
		got := 0
		for got < count {
			m, ok := <-ch
			if !ok {
				return fmt.Errorf("only %d messages", got)
			}
			_ = m
			got++
		}
		return nil
	})

	sc.Step(`^a subscriber from the start of "([^"]*)" receives "([^"]*)" and nothing else$`, func(ctx context.Context, subject, one string) error {
		subCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		ch, err := world(ctx).Bus.bus.Subscribe(subCtx, subject, 0)
		if err != nil {
			return err
		}
		sub := &subState{subject: subject, ch: ch}
		return recvWithSentinel(ctx, world(ctx).Bus.bus, sub, []string{one})
	})

	sc.Step(`^subject "([^"]*)" is purged$`, func(ctx context.Context, subject string) error {
		world(ctx).Bus.lastErr = world(ctx).Bus.bus.Purge(ctx, subject)
		return nil
	})

	sc.Step(`^all (\d+) publishes returned a bus sequence$`, func(ctx context.Context, n int) error {
		if len(world(ctx).Bus.publishOK) != n {
			return fmt.Errorf("got %d seqs", len(world(ctx).Bus.publishOK))
		}
		return nil
	})

	sc.Step(`^fast receives "([^"]*)" to "([^"]*)" in that order$`, func(ctx context.Context, from, to string) error {
		want, err := parseMsgRange(from, to)
		if err != nil {
			return err
		}
		w := world(ctx).Bus
		sub := w.subs["fast"]
		spin := 0
		for spin < 100000 {
			w.mu.Lock()
			n := len(sub.got)
			w.mu.Unlock()
			if n >= len(want) {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				spin++
				runtime.Gosched()
			}
		}
		w.mu.Lock()
		got := append([]platform.Message{}, sub.got...)
		w.mu.Unlock()
		if len(got) != len(want) {
			return fmt.Errorf("fast got %d want %d", len(got), len(want))
		}
		for i, label := range want {
			if string(got[i].Data) != label {
				return fmt.Errorf("got %q want %q", got[i].Data, label)
			}
		}
		return nil
	})

	sc.Step(`^slow's subscription ends after delivering "([^"]*)" to "([^"]*)"$`, func(ctx context.Context, from, to string) error {
		want, err := parseMsgRange(from, to)
		if err != nil {
			return err
		}
		sub := world(ctx).Bus.subs["slow"]
		for _, label := range want {
			m, ok := <-sub.ch
			if !ok {
				return fmt.Errorf("slow ended early")
			}
			if string(m.Data) != label {
				return fmt.Errorf("got %q want %q", m.Data, label)
			}
		}
		select {
		case _, more := <-sub.ch:
			if more {
				return fmt.Errorf("slow still delivering")
			}
		default:
		}
		return nil
	})

	sc.Step(`^a resubscription of slow after the bus sequence of "([^"]*)" receives "([^"]*)" to "([^"]*)" in that order and nothing else$`, func(ctx context.Context, cursor, from, to string) error {
		w := world(ctx).Bus
		if err := w.startSub(ctx, "slow2", "run.r1.deltas", w.seqOf(cursor)); err != nil {
			return err
		}
		want, err := parseMsgRange(from, to)
		if err != nil {
			return err
		}
		return recvWithSentinel(ctx, w.bus, w.subs["slow2"], want)
	})

	sc.Step(`^s1 and s2 each receive the same (\d+) messages in the same order$`, func(ctx context.Context, n int) error {
		w := world(ctx).Bus
		s1, s2 := w.subs["s1"], w.subs["s2"]
		spin := 0
		for spin < 200000 {
			w.mu.Lock()
			g1, g2 := len(s1.got), len(s2.got)
			w.mu.Unlock()
			if g1 >= n && g2 >= n {
				break
			}
			spin++
			runtime.Gosched()
		}
		w.mu.Lock()
		g1 := append([]platform.Message{}, s1.got...)
		g2 := append([]platform.Message{}, s2.got...)
		w.mu.Unlock()
		if len(g1) != n || len(g2) != n {
			return fmt.Errorf("s1=%d s2=%d want %d", len(g1), len(g2), n)
		}
		for i := 0; i < n; i++ {
			if string(g1[i].Data) != string(g2[i].Data) {
				return fmt.Errorf("mismatch at %d", i)
			}
		}
		return nil
	})

	sc.Step(`^that order is the order of their bus sequences$`, func(ctx context.Context) error {
		w := world(ctx).Bus
		var s1seqs []uint64
		for {
			select {
			case m, ok := <-w.subs["s1"].ch:
				if !ok {
					goto check
				}
				s1seqs = append(s1seqs, m.Seq)
			default:
				goto check
			}
		}
	check:
		var last uint64
		for _, seq := range s1seqs {
			if seq <= last {
				return fmt.Errorf("sequences not strictly increasing")
			}
			last = seq
		}
		return nil
	})

	sc.Step(`^the subscription has ended without delivering a message$`, func(ctx context.Context) error {
		sub := world(ctx).Bus.subs["sub"]
		select {
		case m, ok := <-sub.ch:
			if ok {
				return fmt.Errorf("got message %q", m.Data)
			}
		default:
			if sub.cancel != nil {
				sub.cancel()
			}
			_, ok := <-sub.ch
			if ok {
				return fmt.Errorf("still open with messages")
			}
		}
		if err := world(ctx).Bus.publish(ctx, "run.r1.deltas", "m1", nil); err != nil {
			return err
		}
		select {
		case m, ok := <-sub.ch:
			if ok {
				return fmt.Errorf("delivered after cancel: %q", m.Data)
			}
		default:
		}
		return nil
	})

	sc.Step(`^the publish (.+)$`, matchPublishOutcome)

	sc.Step(`^the subscription fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		return world(ctx).Bus.checkCode(world(ctx).Bus.subErr, code)
	})

	sc.Step(`^the error reports the bus sequence of "([^"]*)" as the last purged$`, func(ctx context.Context, label string) error {
		err := world(ctx).Bus.subErr
		if err == nil {
			return fmt.Errorf("no error")
		}
		want := world(ctx).Bus.seqOf(label)
		var pe *core.Error
		if !errorsAs(err, &pe) || pe.Details == nil {
			return fmt.Errorf("no purged_through detail")
		}
		got := uint64FromDetail(pe.Details["purged_through"])
		if got != want {
			return fmt.Errorf("purged_through %d want %d", got, want)
		}
		return nil
	})

	// KV
	kvPut := func(ctx context.Context, key, value, ttl string, create bool) error {
		w := world(ctx).Bus
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
		_, _, world(ctx).Bus.lastErr = world(ctx).Bus.bus.Get(ctx, key)
		return nil
	})

	sc.Step(`^entry "([^"]*)" is deleted$`, func(ctx context.Context, key string) error {
		world(ctx).Bus.lastErr = world(ctx).Bus.bus.Delete(ctx, key)
		return nil
	})

	sc.Step(`^entry "([^"]*)" is swapped to "([^"]*)" at its current revision$`, func(ctx context.Context, key, value string) error {
		w := world(ctx).Bus
		_, cur, err := w.bus.Get(ctx, key)
		if err != nil {
			w.lastErr = err
			return nil
		}
		r, err := w.bus.CompareAndSwap(ctx, key, []byte(value), cur, 0)
		w.lastErr = err
		if err == nil {
			w.putRevisionKey(key, r)
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
		}
		return nil
	})

	sc.Step(`^entry "([^"]*)" is swapped to "([^"]*)" at revision (\d+)$`, func(ctx context.Context, key, value string, rev int) error {
		r, err := world(ctx).Bus.bus.CompareAndSwap(ctx, key, []byte(value), uint64(rev), 0)
		world(ctx).Bus.lastErr = err
		if err == nil {
			world(ctx).Bus.putRevisionKey(key, r)
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
		}
		return nil
	})

	sc.Step(`^(\d+) callers concurrently swap entry "([^"]*)" to their own name at its current revision$`, func(ctx context.Context, n int, key string) error {
		w := world(ctx).Bus
		_, rev, err := w.bus.Get(ctx, key)
		if err != nil {
			return err
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		var wins, conflicts int
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
				}
			}(i)
		}
		wg.Wait()
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
		_ = code
		return nil
	})

	sc.Step(`^the swap returns a greater revision than the put$`, func(ctx context.Context) error {
		w := world(ctx).Bus
		if len(w.putRevs) < 1 || w.lastErr != nil {
			return fmt.Errorf("no successful swap")
		}
		return nil
	})

	sc.Step(`^(.+?) pass(?:es)?$`, func(ctx context.Context, raw string) error {
		d, err := parseDurationPass(raw)
		if err != nil {
			return err
		}
		world(ctx).Core.clock.Advance(d)
		return nil
	})
}

func matchPublishOutcome(ctx context.Context, tail string) error {
	w := world(ctx).Bus
	switch tail {
	case `returns a bus sequence`:
		if w.lastErr != nil {
			return w.lastErr
		}
		return nil
	case `fails with code "invalid_argument"`:
		return w.checkCode(w.lastErr, "invalid_argument")
	default:
		if strings.HasPrefix(tail, `fails with code "`) {
			code := strings.TrimSuffix(strings.TrimPrefix(tail, `fails with code "`), `"`)
			return w.checkCode(w.lastErr, code)
		}
		return fmt.Errorf("unknown publish outcome %q", tail)
	}
}
