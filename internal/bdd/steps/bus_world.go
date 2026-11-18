package steps

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/internal/bdd/bustest"
	"github.com/epzero/basamento/internal/bdd/natstest"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/platform/bus"
)

const busSentinel = "\x7fBDD-SENTINEL"

type busKV interface {
	platform.Bus
	platform.KV
}

type published struct {
	subject string
	data    []byte
	seq     uint64
}

type subState struct {
	name    string
	subject string
	ch      <-chan platform.Message
	cancel  context.CancelFunc
	got     []platform.Message
}

type busWorld struct {
	mu      sync.Mutex
	backend string
	profile string

	bus     busKV
	raw     *bus.Store
	fake    *bustest.Fake
	nats    *natstest.Server
	dataDir string

	msgs      map[string]published
	revisions map[string]uint64
	lastRev   map[string]uint64
	subs      map[string]*subState

	lastErr       error
	subErr        error
	subBuf        int
	maxMsg        int
	serverDown    bool
	publishOK     []uint64
	putRevs       []uint64
	swapWinner    string
	swapWins      int
	swapConflicts int
}

func scenarioUsesBus(scn *godog.Scenario) bool {
	for _, t := range scn.Tags {
		if t.Name == "@wp:WP-L1-03" || strings.HasPrefix(t.Name, "@REQ-BUS-") {
			return true
		}
	}
	return false
}

func newBusWorld() *busWorld {
	backend := os.Getenv("BDD_BUS_BACKEND")
	if backend == "" {
		backend = "fake"
	}
	return &busWorld{
		backend:   backend,
		profile:   backend, // embedded/cloud profile alignment
		msgs:      map[string]published{},
		revisions: map[string]uint64{},
		lastRev:   map[string]uint64{},
		subs:      map[string]*subState{},
	}
}

func skipBusByProfile(scn *godog.Scenario, backend string) error {
	hasEmbedded, hasCloud := false, false
	for _, t := range scn.Tags {
		if t.Name == "@profile:embedded" {
			hasEmbedded = true
		}
		if t.Name == "@profile:cloud" {
			hasCloud = true
		}
	}
	if backend == "fake" && hasEmbedded && !hasCloud {
		return godog.ErrSkip
	}
	if backend == "embedded" && hasCloud && !hasEmbedded {
		return godog.ErrSkip
	}
	if backend == "cloud" && hasEmbedded && !hasCloud {
		return godog.ErrSkip
	}
	return nil
}

func (w *busWorld) ensureOpen(ctx context.Context) error {
	if w.serverDown && w.bus != nil {
		return nil
	}
	if w.serverDown {
		return core.NewError(core.CodeUnavailable, "bus unavailable", nil)
	}
	if w.bus != nil {
		return nil
	}
	clk := world(ctx).Core.clock
	switch w.backend {
	case "fake":
		w.fake = bustest.NewFake(clk)
		if w.subBuf > 0 {
			w.fake.SetSubBuffer(w.subBuf)
		}
		if w.maxMsg > 0 {
			w.fake.SetMaxMessageSize(w.maxMsg)
		}
		w.bus = w.fake
	case "embedded":
		if w.dataDir == "" {
			dir, err := os.MkdirTemp("", "bus-embed-*")
			if err != nil {
				return err
			}
			w.dataDir = dir
		}
		opts := bus.Options{Clock: clk, SubBuffer: bus.DefaultSubBuffer, MaxMessageSize: bus.DefaultMaxMessageSize}
		if w.subBuf > 0 {
			opts.SubBuffer = w.subBuf
		}
		if w.maxMsg > 0 {
			opts.MaxMessageSize = w.maxMsg
		}
		st, err := bus.OpenEmbedded(ctx, w.dataDir, opts)
		if err != nil {
			return err
		}
		w.raw = st
		w.bus = st
	case "cloud":
		if w.nats == nil {
			dir, err := os.MkdirTemp("", "bus-cloud-*")
			if err != nil {
				return err
			}
			w.dataDir = dir
			srv, err := natstest.Start(dir)
			if err != nil {
				return err
			}
			w.nats = srv
		}
		opts := bus.Options{Clock: clk, SubBuffer: bus.DefaultSubBuffer, MaxMessageSize: bus.DefaultMaxMessageSize}
		if w.subBuf > 0 {
			opts.SubBuffer = w.subBuf
		}
		if w.maxMsg > 0 {
			opts.MaxMessageSize = w.maxMsg
		}
		st, err := bus.OpenCloud(ctx, w.nats.URL, opts)
		if err != nil {
			return err
		}
		w.raw = st
		w.bus = st
	default:
		return fmt.Errorf("unknown bus backend %q", w.backend)
	}
	return nil
}

func (w *busWorld) teardown() {
	for _, s := range w.subs {
		if s.cancel != nil {
			s.cancel()
		}
	}
	if w.raw != nil {
		_ = w.raw.Close()
		w.raw = nil
	}
	if w.fake != nil {
		_ = w.fake.Close()
		w.fake = nil
	}
	if w.nats != nil {
		w.nats.Close()
		w.nats = nil
	}
	if w.dataDir != "" {
		_ = os.RemoveAll(w.dataDir)
		w.dataDir = ""
	}
	w.bus = nil
}

func (w *busWorld) restartEmbedded(ctx context.Context) error {
	if w.backend != "embedded" {
		return fmt.Errorf("restart only on embedded backend")
	}
	if w.raw != nil {
		_ = w.raw.Close()
		w.raw = nil
		w.bus = nil
	}
	clk := world(ctx).Core.clock
	opts := bus.Options{Clock: clk, SubBuffer: bus.DefaultSubBuffer, MaxMessageSize: bus.DefaultMaxMessageSize}
	if w.subBuf > 0 {
		opts.SubBuffer = w.subBuf
	}
	if w.maxMsg > 0 {
		opts.MaxMessageSize = w.maxMsg
	}
	st, err := bus.OpenEmbedded(ctx, w.dataDir, opts)
	if err != nil {
		return err
	}
	w.raw = st
	w.bus = st
	w.subs = map[string]*subState{}
	return nil
}

func msgBytes(label string) []byte { return []byte(label) }

func (w *busWorld) publish(ctx context.Context, subject, label string, data []byte) error {
	if err := w.ensureOpen(ctx); err != nil {
		return err
	}
	if data == nil {
		data = msgBytes(label)
	}
	seq, err := w.bus.Publish(ctx, subject, data)
	w.mu.Lock()
	w.lastErr = err
	if err == nil {
		w.msgs[label] = published{subject: subject, data: append([]byte(nil), data...), seq: seq}
		w.publishOK = append(w.publishOK, seq)
	}
	w.mu.Unlock()
	return nil
}

func (w *busWorld) seqOf(label string) uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.msgs[label].seq
}

func parseMsgRange(from, to string) ([]string, error) {
	prefix := strings.TrimRight(from, "0123456789")
	if prefix != strings.TrimRight(to, "0123456789") {
		return nil, fmt.Errorf("range mismatch %s %s", from, to)
	}
	fi, err1 := strconv.Atoi(strings.TrimPrefix(from, prefix))
	ti, err2 := strconv.Atoi(strings.TrimPrefix(to, prefix))
	if err1 != nil || err2 != nil || ti < fi {
		return nil, fmt.Errorf("bad range %s %s", from, to)
	}
	out := make([]string, 0, ti-fi+1)
	for i := fi; i <= ti; i++ {
		out = append(out, fmt.Sprintf("%s%d", prefix, i))
	}
	return out, nil
}

func (w *busWorld) startSub(ctx context.Context, name, subject string, after uint64) error {
	if err := w.ensureOpen(ctx); err != nil {
		return err
	}
	subCtx, cancel := context.WithCancel(ctx)
	var ch <-chan platform.Message
	var err error
	if w.fake != nil && w.subBuf > 0 && w.subBuf < bus.DefaultSubBuffer && name != "slow" {
		ch, err = w.fake.SubscribeWithBuffer(subCtx, subject, after, bus.DefaultSubBuffer, bus.DefaultSubBuffer)
	} else {
		ch, err = w.bus.Subscribe(subCtx, subject, after)
	}
	w.subErr = err
	if err != nil {
		cancel()
		return nil
	}
	w.subs[name] = &subState{name: name, subject: subject, ch: ch, cancel: cancel}
	if name == "fast" || name == "s1" || name == "s2" {
		go func(sn string, inbox <-chan platform.Message) {
			for {
				m, ok := <-inbox
				if !ok {
					return
				}
				w.mu.Lock()
				w.subs[sn].got = append(w.subs[sn].got, m)
				w.mu.Unlock()
			}
		}(name, ch)
	}
	return nil
}

func recvWithSentinel(ctx context.Context, b busKV, sub *subState, want []string) error {
	for _, label := range want {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case m, ok := <-sub.ch:
			if !ok {
				return fmt.Errorf("subscription %s ended early", sub.name)
			}
			sub.got = append(sub.got, m)
			if string(m.Data) != label {
				return fmt.Errorf("got %q want %q", m.Data, label)
			}
		}
	}
	_, err := b.Publish(ctx, sub.subject, []byte(busSentinel))
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case m, ok := <-sub.ch:
		if !ok {
			return fmt.Errorf("subscription ended before sentinel")
		}
		if string(m.Data) != busSentinel {
			return fmt.Errorf("unexpected message %q after expected set", m.Data)
		}
	}
	select {
	case m, ok := <-sub.ch:
		if ok {
			return fmt.Errorf("extra message %q after sentinel", m.Data)
		}
	default:
	}
	return nil
}

func recvNothing(ctx context.Context, b busKV, subject string) error {
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := b.Subscribe(subCtx, subject, 0)
	if err != nil {
		return err
	}
	defer func() { cancel() }()
	_, err = b.Publish(ctx, subject, []byte(busSentinel))
	if err != nil {
		return err
	}
	m, ok := <-ch
	if !ok {
		return fmt.Errorf("subscription closed")
	}
	if string(m.Data) != busSentinel {
		return fmt.Errorf("got %q, want nothing", m.Data)
	}
	select {
	case _, more := <-ch:
		if more {
			return fmt.Errorf("extra message delivered")
		}
	default:
	}
	return nil
}

func parseQuotedList(s string) []string {
	parts := strings.Split(s, `", "`)
	for i, p := range parts {
		p = strings.Trim(p, `" `)
		parts[i] = p
	}
	return parts
}

func parseDurationPass(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, " passes")
	s = strings.TrimSuffix(s, " pass")
	fields := strings.Fields(s)
	if len(fields) == 2 {
		n, err := strconv.Atoi(fields[0])
		if err != nil {
			return 0, err
		}
		switch fields[1] {
		case "millisecond", "milliseconds":
			return time.Duration(n) * time.Millisecond, nil
		case "second", "seconds":
			return time.Duration(n) * time.Second, nil
		case "day", "days":
			return time.Duration(n) * 24 * time.Hour, nil
		}
	}
	return time.ParseDuration(s)
}

func (w *busWorld) stopServer() {
	w.serverDown = true
	if w.fake != nil {
		w.fake.Stop()
		return
	}
	if w.raw != nil {
		_ = w.raw.Close()
	}
}

func (w *busWorld) checkCode(err error, code string) error {
	if code == "" {
		if err != nil {
			return err
		}
		return nil
	}
	if err == nil {
		return fmt.Errorf("succeeded, want %s", code)
	}
	if string(core.CodeOf(err)) != code {
		return fmt.Errorf("got %s, want %s", core.CodeOf(err), code)
	}
	return nil
}

func (w *busWorld) putRevisionKey(key string, rev uint64) {
	w.revisions[key] = rev
	w.lastRev[key] = rev
}
