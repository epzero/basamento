package steps

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

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

func registerBusPublish(sc *godog.ScenarioContext) {
	sc.Step(`^the subscription buffer holds (\d+) messages$`, func(ctx context.Context, n int) error {
		world(ctx).Bus.subBuf = n
		return nil
	})

	sc.Step(`^the maximum message size is (\d+) bytes$`, func(ctx context.Context, n int) error {
		world(ctx).Bus.maxMsg = n
		return nil
	})

	sc.Step(`^the bus server is stopped$`, func(ctx context.Context) error {
		w := world(ctx).Bus
		if err := w.ensureOpen(ctx); err != nil {
			return err
		}
		w.stopServer()
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
		for i, l := range labels {
			if err := w.publish(ctx, subject, l, nil); err != nil {
				return err
			}
			if err := w.paceReaders(ctx, i+1); err != nil {
				return err
			}
		}
		return nil
	})

	sc.Step(`^message "([^"]*)" is published on subject "([^"]*)"$`, func(ctx context.Context, name, subject string) error {
		_ = world(ctx).Bus.publish(ctx, subject, name, nil)
		return nil
	})

	sc.Step(`^a message of (\d+) bytes is published on subject "([^"]*)"$`, func(ctx context.Context, size int, subject string) error {
		data := make([]byte, size)
		for i := range data {
			data[i] = 'x'
		}
		_ = world(ctx).Bus.publish(ctx, subject, fmt.Sprintf("blob%d", size), data)
		return nil
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
		return w.paceReaders(ctx, pubs*each)
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
		w := world(ctx).Bus
		if err := w.startSub(ctx, "sub", subject, 0); err != nil {
			return err
		}
		return w.requireSubOK()
	})

	sc.Step(`^a subscriber subscribes live to "([^"]*)"$`, func(ctx context.Context, subject string) error {
		w := world(ctx).Bus
		if err := w.ensureOpen(ctx); err != nil {
			return err
		}
		seq, err := w.bus.LastSeq(ctx, subject)
		if err != nil {
			return err
		}
		if err := w.startSub(ctx, "sub", subject, seq); err != nil {
			return err
		}
		return w.requireSubOK()
	})

	sc.Step(`^subscribers "([^"]*)" and "([^"]*)" subscribe live to "([^"]*)"$`, func(ctx context.Context, s1, s2, subject string) error {
		w := world(ctx).Bus
		if err := w.ensureOpen(ctx); err != nil {
			return err
		}
		seq, err := w.bus.LastSeq(ctx, subject)
		if err != nil {
			return err
		}
		if err := w.startSub(ctx, s1, subject, seq); err != nil {
			return err
		}
		if err := w.requireSubOK(); err != nil {
			return err
		}
		w.subErr = nil
		if err := w.startSub(ctx, s2, subject, seq); err != nil {
			return err
		}
		return w.requireSubOK()
	})

	sc.Step(`^the subscriber cancels its subscription$`, func(ctx context.Context) error {
		sub := world(ctx).Bus.subs["sub"]
		if sub == nil || sub.cancel == nil {
			return fmt.Errorf("no subscription")
		}
		sub.cancel()
		_, ok := <-sub.ch
		if ok {
			return fmt.Errorf("channel open after cancel")
		}
		return nil
	})

	sc.Step(`^slow reads nothing$`, func(ctx context.Context) error {
		w := world(ctx).Bus
		sub := w.subs["slow"]
		if sub == nil {
			return fmt.Errorf("no slow subscriber")
		}
		w.mu.Lock()
		n := len(sub.got)
		w.mu.Unlock()
		if n != 0 {
			return fmt.Errorf("slow already received %d messages", n)
		}
		return nil
	})

	sc.Step(`^the subscriber receives (.+) in that order and nothing else$`, func(ctx context.Context, list string) error {
		w := world(ctx).Bus
		want := parseBusQuotedList(list)
		return recvWithSentinel(ctx, w.bus, w.subs["sub"], want)
	})

	sc.Step(`^the subscriber receives "([^"]*)" and nothing else$`, func(ctx context.Context, one string) error {
		w := world(ctx).Bus
		return recvWithSentinel(ctx, w.bus, w.subs["sub"], []string{one})
	})

	sc.Step(`^a subscriber from the start of "([^"]*)" receives nothing$`, func(ctx context.Context, subject string) error {
		w := world(ctx).Bus
		if err := w.ensureOpen(ctx); err != nil {
			return err
		}
		return recvNothing(ctx, w.bus, subject)
	})

	sc.Step(`^a subscriber from the start of "([^"]*)" receives (.+) in that order and nothing else$`, func(ctx context.Context, subject, list string) error {
		w := world(ctx).Bus
		if err := w.ensureOpen(ctx); err != nil {
			return err
		}
		want := parseBusQuotedList(list)
		subCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		ch, err := w.bus.Subscribe(subCtx, subject, 0)
		if err != nil {
			return err
		}
		sub := &subState{subject: subject, ch: ch}
		return recvWithSentinel(ctx, w.bus, sub, want)
	})

	sc.Step(`^a subscriber from the start of "([^"]*)" receives (\d+) messages$`, func(ctx context.Context, subject string, count int) error {
		w := world(ctx).Bus
		if err := w.ensureOpen(ctx); err != nil {
			return err
		}
		if count == 0 {
			return recvNothing(ctx, w.bus, subject)
		}
		subCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		ch, err := w.bus.Subscribe(subCtx, subject, 0)
		if err != nil {
			return err
		}
		sub := &subState{subject: subject, ch: ch}
		for i := 0; i < count; i++ {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case m, ok := <-ch:
				if !ok {
					return fmt.Errorf("only %d messages", i)
				}
				sub.got = append(sub.got, m)
			}
		}
		_, err = w.bus.Publish(ctx, subject, []byte(busSentinel))
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case m, ok := <-ch:
			if !ok {
				return fmt.Errorf("subscription closed before sentinel")
			}
			if string(m.Data) != busSentinel {
				return fmt.Errorf("got %q want sentinel after %d messages", m.Data, count)
			}
		}
		select {
		case m, ok := <-ch:
			if ok {
				return fmt.Errorf("extra message %q", m.Data)
			}
		default:
		}
		return nil
	})

	sc.Step(`^a subscriber to "([^"]*)" from the start receives "([^"]*)" and nothing else$`, func(ctx context.Context, subject, one string) error {
		w := world(ctx).Bus
		if err := w.ensureOpen(ctx); err != nil {
			return err
		}
		subCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		ch, err := w.bus.Subscribe(subCtx, subject, 0)
		if err != nil {
			return err
		}
		sub := &subState{subject: subject, ch: ch}
		return recvWithSentinel(ctx, w.bus, sub, []string{one})
	})

	sc.Step(`^a subscriber from the start of "([^"]*)" receives "([^"]*)" and nothing else$`, func(ctx context.Context, subject, one string) error {
		w := world(ctx).Bus
		if err := w.ensureOpen(ctx); err != nil {
			return err
		}
		subCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		ch, err := w.bus.Subscribe(subCtx, subject, 0)
		if err != nil {
			return err
		}
		sub := &subState{subject: subject, ch: ch}
		return recvWithSentinel(ctx, w.bus, sub, []string{one})
	})

	sc.Step(`^subject "([^"]*)" is purged$`, func(ctx context.Context, subject string) error {
		w := world(ctx).Bus
		if err := w.ensureOpen(ctx); err != nil {
			return err
		}
		err := w.bus.Purge(ctx, subject)
		w.lastErr = err
		return err
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
		if err := w.waitSubMsgs(ctx, sub, len(want)); err != nil {
			return err
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
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-sub.ch:
			if ok {
				return fmt.Errorf("slow still delivering")
			}
		}
		return nil
	})

	sc.Step(`^a resubscription of slow after the bus sequence of "([^"]*)" receives "([^"]*)" to "([^"]*)" in that order and nothing else$`, func(ctx context.Context, cursor, from, to string) error {
		w := world(ctx).Bus
		if err := w.startSub(ctx, "slow2", "run.r1.deltas", w.seqOf(cursor)); err != nil {
			return err
		}
		if err := w.requireSubOK(); err != nil {
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
		if err := w.waitSubMsgs(ctx, s1, n); err != nil {
			return err
		}
		if err := w.waitSubMsgs(ctx, s2, n); err != nil {
			return err
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
		s1, s2 := w.subs["s1"], w.subs["s2"]
		w.mu.Lock()
		g1 := append([]platform.Message{}, s1.got...)
		g2 := append([]platform.Message{}, s2.got...)
		w.mu.Unlock()
		if len(g1) != len(g2) || len(g1) == 0 {
			return fmt.Errorf("s1=%d s2=%d messages for sequence check", len(g1), len(g2))
		}
		var last uint64
		for i, m := range g1 {
			if m.Seq <= last {
				return fmt.Errorf("s1 sequences not strictly increasing at %d", i)
			}
			last = m.Seq
			if g2[i].Seq != m.Seq {
				return fmt.Errorf("seq mismatch at %d: s1=%d s2=%d", i, m.Seq, g2[i].Seq)
			}
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
		if !errors.As(err, &pe) || pe.Details == nil {
			return fmt.Errorf("no purged_through detail")
		}
		got := uint64FromDetail(pe.Details["purged_through"])
		if got != want {
			return fmt.Errorf("purged_through %d want %d", got, want)
		}
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
