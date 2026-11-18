package steps

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
)

type coreWorld struct {
	clock   *coretest.FakeClock
	ids     core.Ids
	issued  []string
	named   map[string]string
	lastErr error
}

func newCoreWorld() *coreWorld {
	c := coretest.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	return &coreWorld{clock: c, named: map[string]string{}}
}

func (w *coreWorld) generator() core.Ids {
	if w.ids == nil {
		w.ids = core.NewULIDs(w.clock, nil)
	}
	return w.ids
}

var idBody = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

func registerCore(sc *godog.ScenarioContext) {
	sc.Step(`^the clock is frozen at "([^"]*)"$`, func(ctx context.Context, at string) error {
		t, err := time.Parse(time.RFC3339, at)
		if err != nil {
			return err
		}
		world(ctx).Core.clock.Set(t)
		world(ctx).Core.ids = nil
		return nil
	})
	sc.Step(`^the clock advances by (\d+) milliseconds?$`, func(ctx context.Context, ms int) error {
		world(ctx).Core.clock.Advance(time.Duration(ms) * time.Millisecond)
		return nil
	})
	sc.Step(`^an id generator$`, func(ctx context.Context) error {
		world(ctx).Core.generator()
		return nil
	})
	sc.Step(`^the id generator restarts$`, func(ctx context.Context) error {
		world(ctx).Core.ids = nil // a new process: fresh generator, no shared state
		return nil
	})
	issue := func(ctx context.Context, n int, kind string) error {
		w := world(ctx).Core
		for i := 0; i < n; i++ {
			id, err := w.generator().New(kind)
			if err != nil {
				return err
			}
			w.issued = append(w.issued, id)
		}
		return nil
	}
	sc.Step(`^(\d+) ids of kind "([^"]*)" are generated$`, issue)
	sc.Step(`^(\d+) more ids of kind "([^"]*)" are generated$`, issue)
	sc.Step(`^all (\d+) ids are distinct$`, func(ctx context.Context, n int) error {
		w := world(ctx).Core
		if len(w.issued) != n {
			return fmt.Errorf("issued %d ids, want %d", len(w.issued), n)
		}
		seen := map[string]bool{}
		for _, id := range w.issued {
			if seen[id] {
				return fmt.Errorf("duplicate id %s", id)
			}
			seen[id] = true
		}
		return nil
	})
	sc.Step(`^an id of kind "([^"]*)" is generated as "([^"]*)"$`, func(ctx context.Context, kind, name string) error {
		w := world(ctx).Core
		id, err := w.generator().New(kind)
		if err != nil {
			return err
		}
		w.named[name] = id
		return nil
	})
	sc.Step(`^id "([^"]*)" sorts before id "([^"]*)"$`, func(ctx context.Context, a, b string) error {
		w := world(ctx).Core
		if w.named[a] >= w.named[b] {
			return fmt.Errorf("%s (%s) does not sort before %s (%s)", a, w.named[a], b, w.named[b])
		}
		return nil
	})
	sc.Step(`^id "([^"]*)" is "([^"]*)" followed by 26 Crockford base32 characters$`, func(ctx context.Context, name, prefix string) error {
		id := world(ctx).Core.named[name]
		if len(id) <= len(prefix) || id[:len(prefix)] != prefix || !idBody.MatchString(id[len(prefix):]) {
			return fmt.Errorf("id %q does not match %q + 26 Crockford base32 characters", id, prefix)
		}
		return nil
	})
	sc.Step(`^an id of kind "([^"]*)" is requested$`, func(ctx context.Context, kind string) error {
		w := world(ctx).Core
		_, w.lastErr = w.generator().New(kind)
		return nil
	})
	sc.Step(`^the request fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		err := world(ctx).Core.lastErr
		if err == nil {
			return fmt.Errorf("request succeeded, want code %s", code)
		}
		if got := core.CodeOf(err); string(got) != code {
			return fmt.Errorf("got code %s, want %s", got, code)
		}
		return nil
	})
}
