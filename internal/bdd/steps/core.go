package steps

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
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

	tenantCtx context.Context
	tenantID  core.TenantID
	tenantOK  bool
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
	sc.Step(`^the clock advances by (\d+) hours?$`, func(ctx context.Context, h int) error {
		world(ctx).Core.clock.Advance(time.Duration(h) * time.Hour)
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

	// reportTenant reads the tenant carried by the scenario's context. It fails
	// when no When step set a context, so a Then cannot pass against a context
	// the scenario never produced.
	reportTenant := func(ctx context.Context) (*coreWorld, error) {
		w := world(ctx).Core
		if w.tenantCtx == nil {
			return nil, fmt.Errorf("no context under test: a When step must carry a tenant id first")
		}
		w.tenantID, w.tenantOK = core.TenantFrom(w.tenantCtx)
		return w, nil
	}
	sc.Step(`^a context carries the tenant id "([^"]*)"$`, func(ctx context.Context, id string) error {
		world(ctx).Core.tenantCtx = core.WithTenant(context.Background(), core.TenantID(id))
		return nil
	})
	sc.Step(`^a context carries the tenant-id literal (.+)$`, func(ctx context.Context, literal string) error {
		id, ok, err := parseTenantLiteral(literal)
		if err != nil {
			return err
		}
		if !ok {
			world(ctx).Core.tenantCtx = context.Background()
			return nil
		}
		world(ctx).Core.tenantCtx = core.WithTenant(context.Background(), id)
		return nil
	})
	sc.Step(`^a derived context carries the tenant id "([^"]*)"$`, func(ctx context.Context, id string) error {
		w := world(ctx).Core
		if w.tenantCtx == nil {
			return fmt.Errorf("no parent context: a context must carry a tenant first")
		}
		w.tenantCtx = core.WithTenant(w.tenantCtx, core.TenantID(id))
		return nil
	})
	sc.Step(`^the context reports no tenant$`, func(ctx context.Context) error {
		w, err := reportTenant(ctx)
		if err != nil {
			return err
		}
		if w.tenantOK || w.tenantID != "" {
			return fmt.Errorf("context reports tenant %q, want none", w.tenantID)
		}
		return nil
	})
	sc.Step(`^the derived context reports no tenant$`, func(ctx context.Context) error {
		w, err := reportTenant(ctx)
		if err != nil {
			return err
		}
		if w.tenantOK || w.tenantID != "" {
			return fmt.Errorf("derived context reports tenant %q, want none", w.tenantID)
		}
		return nil
	})
	sc.Step(`^the context reports the tenant id "([^"]*)"$`, func(ctx context.Context, want string) error {
		w, err := reportTenant(ctx)
		if err != nil {
			return err
		}
		if !w.tenantOK || w.tenantID != core.TenantID(want) {
			return fmt.Errorf("context reports tenant (%q, %v), want (%q, true)", w.tenantID, w.tenantOK, want)
		}
		return nil
	})
	sc.Step(`^the reported tenant id is empty$`, func(ctx context.Context) error {
		w, err := reportTenant(ctx)
		if err != nil {
			return err
		}
		if w.tenantID != "" {
			return fmt.Errorf("reported tenant id is %q, want empty", w.tenantID)
		}
		return nil
	})
}

// parseTenantLiteral parses the tenant-id literal of a feature Examples table:
// a Go interpreted string literal, or the word absent for no id at all.
//
// The Gherkin table layer consumes one backslash: the row "a\\b" reaches the
// step as `"a\b"`. So before strconv.Unquote, a backslash that does not start
// one of the escapes the feature documents (\u, \x and \") is put back as the
// `\\` the author wrote. A backslash the table layer leaves behind is already
// a collapsed pair, so each one is re-doubled, including the second of two and
// the last of them before the closing quote: a run then collapses to the
// literal backslashes the row names. That also keeps Unquote from accepting Go
// escapes the feature does not document: \n, \t, \r, \a, \f, \v, octal, \U and
// \\ all arrive as backslashes and stay literal.
func parseTenantLiteral(literal string) (core.TenantID, bool, error) {
	if literal == "absent" {
		return "", false, nil
	}
	if len(literal) < 2 || literal[0] != '"' || literal[len(literal)-1] != '"' {
		return "", false, fmt.Errorf("tenant-id literal %q is not a Go interpreted string literal", literal)
	}
	s, err := strconv.Unquote(redoubleBackslashes(literal))
	if err != nil {
		return "", false, fmt.Errorf("tenant-id literal %q: %w", literal, err)
	}
	return core.TenantID(s), true, nil
}

// redoubleBackslashes restores the backslash the Gherkin table layer consumes,
// for every backslash that is not the start of a documented escape. A
// backslash immediately before the closing quote is always literal too: \"
// there would leave the literal unterminated.
func redoubleBackslashes(lit string) string {
	var b strings.Builder
	b.Grow(len(lit) + 8)
	for i := 0; i < len(lit); i++ {
		if lit[i] == '\\' && i+1 < len(lit) &&
			(i+1 == len(lit)-1 || !isTenantLiteralEscape(lit[i+1])) {
			b.WriteString(`\\`)
			continue
		}
		b.WriteByte(lit[i])
	}
	return b.String()
}

// isTenantLiteralEscape reports whether c starts an escape the feature
// documents after the Gherkin table layer: \u, \x or \". A backslash after a
// backslash is not an escape: the pair is already collapsed, so the second one
// is re-doubled like any other literal backslash.
func isTenantLiteralEscape(c byte) bool {
	return c == 'u' || c == 'x' || c == '"'
}
