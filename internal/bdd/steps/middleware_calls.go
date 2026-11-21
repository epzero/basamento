package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/catalog"
	"github.com/epzero/basamento/model/middleware"
	"github.com/epzero/basamento/model/structured"
)

const (
	invalidAnswer = `{"answer": "seven"}`
	validAnswer   = `{"answer": 7}`
)

func (p *scriptedChat) everyFrom(first, every time.Duration, text string) chatBehavior {
	return func(ctx context.Context, emit func(model.Event)) (model.Result, error) {
		if first > 0 {
			if err := p.rt.Sleep(ctx, first); err != nil {
				return model.Result{}, err
			}
		}
		for {
			emit(model.Event{Kind: model.EventText, Text: text})
			if err := p.rt.Sleep(ctx, every); err != nil {
				return model.Result{}, err
			}
		}
	}
}

func (cw *callWorld) replay(ctx context.Context) error {
	cw.ensure(world(ctx).Core.clock)
	out := &callOutcome{done: make(chan struct{}), invStart: len(cw.provider.records), logStart: len(cw.calls.entries), meterStart: len(cw.meter.entries)}
	cw.current = out
	cw.soFar = append(cw.soFar, out)
	cfg, err := cw.config("call-1", false)
	if err != nil {
		return err
	}
	cfg.Provider = nil
	cfg.Replay = cw.recorded
	go func() {
		defer close(out.done)
		res, callErr := middleware.Call(cw.ctx, cfg)
		out.result, out.err = res, callErr
		out.invEnd = len(cw.provider.records)
		out.logEnd = len(cw.calls.entries)
		out.meterEnd = len(cw.meter.entries)
		cw.mw.result = res.Result
		cw.rt.finish()
	}()
	cw.rt.autoPump()
	<-out.done
	return nil
}

func registerMiddlewareCalls(sc *godog.ScenarioContext) {
	sc.Step(`^a scripted chat model that fails attempt (\d+) with code "([^"]*)" marked retryable and streams "([^"]*)" on later attempts$`, func(ctx context.Context, attempt int, code, text string) error {
		cw := callOf(ctx)
		cw.provider.set(attempt, cw.provider.fail(core.Code(code), true, 0, nil))
		cw.provider.set(0, cw.provider.stream(text, nil, 1))
		return nil
	})
	sc.Step(`^a caller invokes "([^"]*)"$`, func(ctx context.Context, _ string) error {
		return callOf(ctx).invoke(ctx, false, "call-1", false)
	})
	sc.Step(`^a caller invokes "([^"]*)" with client ref "([^"]*)"$`, func(ctx context.Context, _, ref string) error {
		return callOf(ctx).invoke(ctx, false, ref, false)
	})
	sc.Step(`^a caller invokes "([^"]*)" with the message "([^"]*)"$`, func(ctx context.Context, _, message string) error {
		cw := callOf(ctx)
		cw.message = message
		return cw.invoke(ctx, false, "call-1", false)
	})
	sc.Step(`^a caller invokes "([^"]*)" and records the call$`, func(ctx context.Context, _ string) error {
		return callOf(ctx).invoke(ctx, false, "call-1", true)
	})
	sc.Step(`^a caller invokes "([^"]*)" replaying that recording$`, func(ctx context.Context, _ string) error {
		return callOf(ctx).replay(ctx)
	})
	sc.Step(`^the call succeeded with message text "([^"]*)"$`, func(ctx context.Context, text string) error {
		cw := callOf(ctx)
		if cw.current.err != nil {
			return fmt.Errorf("the call failed: %w", cw.current.err)
		}
		if got := textOf(cw.current.result.Result); got != text {
			return fmt.Errorf("the call returned %q, want %q", got, text)
		}
		return nil
	})
	sc.Step(`^a scripted chat model that fails attempt (\d+) with code "([^"]*)" marked retryable and fails every later attempt the same way$`, func(ctx context.Context, attempt int, code string) error {
		cw := callOf(ctx)
		fail := cw.provider.fail(core.Code(code), true, 0, nil)
		cw.provider.set(attempt, fail)
		cw.provider.set(0, fail)
		return nil
	})
	sc.Step(`^invocation (\d+) started (\d+) milliseconds after invocation (\d+) ended$`, func(ctx context.Context, later, ms, earlier int) error {
		cw := callOf(ctx)
		inv := cw.invocations()
		if later < 1 || later > len(inv) || earlier < 1 || earlier > len(inv) {
			return fmt.Errorf("invocation %d or %d is missing", later, earlier)
		}
		got := inv[later-1].Start.Sub(inv[earlier-1].End)
		if got != time.Duration(ms)*time.Millisecond {
			return fmt.Errorf("invocation %d started %v after invocation %d ended, want %dms", later, got, earlier, ms)
		}
		return nil
	})
	sc.Step(`^a scripted chat model that fails attempt (\d+) with an? "([^"]*)" error that is not retryable$`, func(ctx context.Context, attempt int, name string) error {
		cw := callOf(ctx)
		code, err := nonRetryableCode(name)
		if err != nil {
			return err
		}
		cw.provider.set(attempt, cw.provider.fail(code, false, 0, nil))
		cw.provider.set(0, cw.provider.fail(code, false, 0, nil))
		return nil
	})
	sc.Step(`^a scripted chat model that streams (\d+) deltas and then fails attempt (\d+) with code "([^"]*)" marked retryable$`, func(ctx context.Context, deltas, attempt int, code string) error {
		cw := callOf(ctx)
		parts := make([]string, deltas)
		for i := range parts {
			parts[i] = fmt.Sprintf("part-%d ", i+1)
		}
		cw.provider.set(attempt, cw.provider.deltasThenFail(parts, core.Code(code), true, nil))
		return nil
	})
	sc.Step(`^the caller received (\d+) text events$`, func(ctx context.Context, n int) error {
		cw := callOf(ctx)
		if got := len(cw.textEvents(cw.current.events)); got != n {
			return fmt.Errorf("the caller received %d text events, want %d", got, n)
		}
		return nil
	})
	sc.Step(`^a scripted chat model that fails attempt (\d+) with code "([^"]*)" marked retryable$`, func(ctx context.Context, attempt int, code string) error {
		cw := callOf(ctx)
		cw.provider.set(attempt, cw.provider.fail(core.Code(code), true, 0, nil))
		cw.provider.set(0, cw.provider.stream("Hello", nil, 1))
		return nil
	})
	sc.Step(`^a scripted chat model that streams "([^"]*)" on every attempt$`, func(ctx context.Context, text string) error {
		cw := callOf(ctx)
		cw.provider.set(0, cw.provider.stream(text, nil, 1))
		return nil
	})
	sc.Step(`^the metering recorded attempt (\d+) at (\d+) milli-point and attempt (\d+) at (\d+) milli-points$`, func(ctx context.Context, first, firstCost, second, secondCost int) error {
		meters := callOf(ctx).meters()
		for _, tc := range []struct {
			attempt int
			cost    int
		}{{first, firstCost}, {second, secondCost}} {
			entry, ok := findMeter(meters, tc.attempt)
			if !ok {
				return fmt.Errorf("the metering recorded no attempt %d", tc.attempt)
			}
			if entry.CostMpts != int64(tc.cost) {
				return fmt.Errorf("attempt %d cost %d milli-points, want %d", tc.attempt, entry.CostMpts, tc.cost)
			}
		}
		return nil
	})
	sc.Step(`^the caller receives exactly (\d+) text events$`, func(ctx context.Context, n int) error {
		cw := callOf(ctx)
		if got := len(cw.textEvents(cw.current.events)); got != n {
			return fmt.Errorf("the caller received %d text events, want %d", got, n)
		}
		return nil
	})
	sc.Step(`^the caller receives no "([^"]*)" event$`, func(ctx context.Context, kind string) error {
		cw := callOf(ctx)
		for _, e := range cw.current.events {
			if string(e.Kind) == kind {
				return fmt.Errorf("the caller received a %q event", kind)
			}
		}
		return nil
	})
	sc.Step(`^a scripted chat model that fails its first (\d+) attempts with code "([^"]*)" marked retryable and no Retry-After$`, func(ctx context.Context, attempts int, code string) error {
		cw := callOf(ctx)
		for n := 1; n <= attempts; n++ {
			cw.provider.set(n, cw.provider.fail(core.Code(code), true, 0, nil))
		}
		cw.provider.set(0, cw.provider.stream("Hello", nil, 1))
		return nil
	})
	sc.Step(`^a scripted chat model that fails attempt (\d+) with code "([^"]*)" marked retryable and a Retry-After of (\d+) (seconds?|milliseconds?)$`, func(ctx context.Context, attempt int, code string, n int, unit string) error {
		cw := callOf(ctx)
		after := time.Duration(n) * time.Second
		if strings.HasPrefix(unit, "millisecond") {
			after = time.Duration(n) * time.Millisecond
		}
		cw.provider.set(attempt, cw.provider.fail(core.Code(code), true, after, nil))
		cw.provider.set(0, cw.provider.stream("Hello", nil, 1))
		return nil
	})
	sc.Step(`^a caller starts an invocation with a call total timeout of (\d+) seconds$`, func(ctx context.Context, seconds int) error {
		cw := callOf(ctx)
		cw.timeouts.CallTotal = time.Duration(seconds) * time.Second
		return cw.follow(ctx, false, "call-1")
	})
	sc.Step(`^a caller starts a call that asks "([^"]*)" for output that satisfies the output schema with a call total timeout of (\d+) seconds$`, func(ctx context.Context, _ string, seconds int) error {
		cw := callOf(ctx)
		cw.timeouts.CallTotal = time.Duration(seconds) * time.Second
		return cw.follow(ctx, true, "call-1")
	})
	sc.Step(`^a scripted chat model that fails attempt (\d+) with code "([^"]*)" marked retryable and streams "([^"]*)" in (\d+) deltas on later attempts$`, func(ctx context.Context, attempt int, code, text string, deltas int) error {
		cw := callOf(ctx)
		cw.provider.set(attempt, cw.provider.fail(core.Code(code), true, 0, nil))
		cw.provider.set(0, cw.provider.stream(text, nil, deltas))
		return nil
	})
	sc.Step(`^the caller receives a "reset" event for attempt (\d+) with attempt number (\d+)$`, func(ctx context.Context, forAttempt, number int) error {
		cw := callOf(ctx)
		for _, e := range cw.current.events {
			if e.Kind == model.EventReset && e.Attempt == number {
				return nil
			}
		}
		return fmt.Errorf("no reset event for attempt %d with attempt number %d", forAttempt, number)
	})
	sc.Step(`^the caller receives exactly (\d+) text events, all of attempt (\d+)$`, func(ctx context.Context, n, attempt int) error {
		cw := callOf(ctx)
		texts := cw.textEvents(cw.current.events)
		if len(texts) != n {
			return fmt.Errorf("the caller received %d text events, want %d", len(texts), n)
		}
		for _, e := range texts {
			if e.Attempt != attempt {
				return fmt.Errorf("a text event names attempt %d, want %d", e.Attempt, attempt)
			}
		}
		return nil
	})
	sc.Step(`^a scripted chat model that never produces a first event on attempt (\d+) and streams "([^"]*)" on attempt (\d+)$`, func(ctx context.Context, silent int, text string, streaming int) error {
		cw := callOf(ctx)
		cw.provider.set(silent, cw.provider.silent)
		cw.provider.set(streaming, cw.provider.stream(text, nil, 1))
		return nil
	})
	sc.Step(`^a caller starts an invocation with a first-chunk timeout of (\d+) seconds and follows its events$`, func(ctx context.Context, seconds int) error {
		cw := callOf(ctx)
		cw.timeouts.FirstChunk = time.Duration(seconds) * time.Second
		return cw.follow(ctx, false, "call-1")
	})
	sc.Step(`^attempt (\d+) was cancelled with code "([^"]*)"$`, func(ctx context.Context, n int, code string) error {
		cw := callOf(ctx)
		rec, ok := cw.attempt(n)
		if !ok {
			return fmt.Errorf("attempt %d is missing", n)
		}
		if rec.Outcome != middleware.OutcomeCancelled || string(rec.Code) != code {
			return fmt.Errorf("attempt %d = %+v, want cancelled with code %s", n, rec, code)
		}
		return nil
	})
	sc.Step(`^a scripted chat model that streams 1 delta at (\d+) seconds and then falls quiet$`, func(ctx context.Context, at int) error {
		cw := callOf(ctx)
		cw.provider.set(0, cw.provider.firstAt([]time.Duration{time.Duration(at) * time.Second}, "x", "", nil))
		return nil
	})
	sc.Step(`^a caller starts an invocation with an idle timeout of (\d+) seconds and follows its events$`, func(ctx context.Context, seconds int) error {
		cw := callOf(ctx)
		cw.timeouts.Idle = time.Duration(seconds) * time.Second
		return cw.follow(ctx, false, "call-1")
	})
	sc.Step(`^a scripted chat model that streams 1 delta every (\d+) seconds starting at (\d+) seconds$`, func(ctx context.Context, every, first int) error {
		cw := callOf(ctx)
		cw.provider.set(0, cw.provider.everyFrom(time.Duration(first)*time.Second, time.Duration(every)*time.Second, "x"))
		return nil
	})
	sc.Step(`^a caller starts an invocation with an attempt total timeout of (\d+) seconds and follows its events$`, func(ctx context.Context, seconds int) error {
		cw := callOf(ctx)
		cw.timeouts.AttemptTotal = time.Duration(seconds) * time.Second
		return cw.follow(ctx, false, "call-1")
	})
	sc.Step(`^a scripted chat model that never produces a first event$`, func(ctx context.Context) error {
		cw := callOf(ctx)
		cw.provider.set(0, cw.provider.silent)
		return nil
	})
	sc.Step(`^attempt (\d+) was not cancelled$`, func(ctx context.Context, n int) error {
		cw := callOf(ctx)
		if rec, ok := cw.attempt(n); ok && rec.Outcome == middleware.OutcomeCancelled {
			return fmt.Errorf("attempt %d was cancelled", n)
		}
		return nil
	})
	sc.Step(`^a scripted chat model that streams 1 delta every (\d+) seconds$`, func(ctx context.Context, every int) error {
		cw := callOf(ctx)
		cw.provider.set(0, cw.provider.everyFrom(0, time.Duration(every)*time.Second, "x"))
		return nil
	})
	sc.Step(`^a model "([^"]*)" priced at "(.*)" milli-points$`, func(ctx context.Context, id, formula string) error {
		// The node scenarios (WP-L3-03) price the model in the catalog; the
		// middleware scenarios price the call itself by usage.
		if !strings.Contains(formula, "usage.") {
			n := world(ctx).Node
			n.update(ctx, func(d *model.Descriptor) { d.ID = id })
			return n.ensureCatalog(ctx).SetSnapshotPrice(id, catalog.Price{Formula: formula})
		}
		if normalizeSpace(formula) != normalizeSpace("(usage.input_tokens * 1 + usage.output_tokens * 8) / 1000") {
			return fmt.Errorf("unsupported fixture price formula %q", formula)
		}
		callOf(ctx).price = func(u model.Usage) int64 {
			return (u.InputTokens*1 + u.OutputTokens*8) / 1000
		}
		return nil
	})
	sc.Step(`^a scripted chat model that fails attempt (\d+) with code "([^"]*)" marked retryable, using (\d+) input tokens and (\d+) output tokens$`, func(ctx context.Context, attempt int, code string, input, output int) error {
		cw := callOf(ctx)
		usage := &model.Usage{InputTokens: int64(input), OutputTokens: int64(output)}
		cw.provider.set(attempt, cw.provider.fail(core.Code(code), true, 0, usage))
		return nil
	})
	sc.Step(`^the scripted model streams "([^"]*)" on attempt (\d+), using (\d+) input tokens and (\d+) output tokens$`, func(ctx context.Context, text string, attempt, input, output int) error {
		cw := callOf(ctx)
		usage := &model.Usage{InputTokens: int64(input), OutputTokens: int64(output)}
		cw.provider.set(attempt, cw.provider.stream(text, usage, 1))
		return nil
	})
	sc.Step(`^the scripted model streams "([^"]*)" on attempt (\d+)$`, func(ctx context.Context, text string, attempt int) error {
		cw := callOf(ctx)
		cw.provider.set(attempt, cw.provider.stream(text, nil, 1))
		return nil
	})
	sc.Step(`^a scripted chat model that fails attempt (\d+) with code "([^"]*)" marked retryable, without reporting usage$`, func(ctx context.Context, attempt int, code string) error {
		cw := callOf(ctx)
		cw.provider.set(attempt, cw.provider.fail(core.Code(code), true, 0, nil))
		return nil
	})
	sc.Step(`^the metering recorded (\d+) input tokens and (\d+) output tokens$`, func(ctx context.Context, input, output int) error {
		in, out := int64(0), int64(0)
		for _, e := range callOf(ctx).meters() {
			in += e.Usage.InputTokens
			out += e.Usage.OutputTokens
		}
		if in != int64(input) || out != int64(output) {
			return fmt.Errorf("the metering recorded %d input and %d output tokens, want %d and %d", in, out, input, output)
		}
		return nil
	})
	sc.Step(`^the metering recorded attempt (\d+) at (\d+) milli-points?$`, func(ctx context.Context, attempt, cost int) error {
		entry, ok := findMeter(callOf(ctx).meters(), attempt)
		if !ok {
			return fmt.Errorf("the metering recorded no attempt %d", attempt)
		}
		if entry.CostMpts != int64(cost) {
			return fmt.Errorf("attempt %d cost %d milli-points, want %d", attempt, entry.CostMpts, cost)
		}
		return nil
	})
	sc.Step(`^the metering recorded attempt (\d+) at (\d+) input tokens and (\d+) milli-points$`, func(ctx context.Context, attempt, input, cost int) error {
		entry, ok := findMeter(callOf(ctx).meters(), attempt)
		if !ok {
			return fmt.Errorf("the metering recorded no attempt %d", attempt)
		}
		if entry.Usage.InputTokens != int64(input) || entry.CostMpts != int64(cost) {
			return fmt.Errorf("attempt %d = %d input and %d milli-points, want %d and %d", attempt, entry.Usage.InputTokens, entry.CostMpts, input, cost)
		}
		return nil
	})
	sc.Step(`^the call log holds (\d+) entr(?:y|ies)$`, func(ctx context.Context, n int) error {
		if got := len(callOf(ctx).logs()); got != n {
			return fmt.Errorf("the call log holds %d entries, want %d", got, n)
		}
		return nil
	})
	sc.Step(`^the call log holds (\d+) entries for attempt (\d+) and attempt (\d+) of the call$`, func(ctx context.Context, n, first, second int) error {
		cw := callOf(ctx)
		logs := cw.logs()
		if len(logs) != n {
			return fmt.Errorf("the call log holds %d entries, want %d", len(logs), n)
		}
		if logs[0].Attempt != first || logs[1].Attempt != second {
			return fmt.Errorf("the call log names attempts %d and %d, want %d and %d", logs[0].Attempt, logs[1].Attempt, first, second)
		}
		return nil
	})
	sc.Step(`^entry (\d+) names the client ref "([^"]*)" and entry (\d+) names the distinct deterministic client ref derived from "([^"]*)" for attempt (\d+)$`, func(ctx context.Context, first int, ref string, second int, callRef string, attempt int) error {
		cw := callOf(ctx)
		logs := cw.logs()
		if first < 1 || first > len(logs) || second < 1 || second > len(logs) {
			return fmt.Errorf("the call log holds %d entries", len(logs))
		}
		if logs[first-1].ClientRef != ref {
			return fmt.Errorf("entry %d names %q, want %q", first, logs[first-1].ClientRef, ref)
		}
		want := structured.ClientRef(callRef, attempt)
		if logs[second-1].ClientRef != want {
			return fmt.Errorf("entry %d names %q, want %q", second, logs[second-1].ClientRef, want)
		}
		return nil
	})
	sc.Step(`^each entry names model "([^"]*)", the rung "([^"]*)", its start time and its outcome$`, func(ctx context.Context, modelID, rung string) error {
		cw := callOf(ctx)
		for i, e := range cw.logs() {
			if e.Model != modelID {
				return fmt.Errorf("entry %d names model %q, want %q", i+1, e.Model, modelID)
			}
			if e.Rung.Display() != rung {
				return fmt.Errorf("entry %d names rung %q, want %q", i+1, e.Rung.Display(), rung)
			}
			if e.Start.IsZero() {
				return fmt.Errorf("entry %d has no start time", i+1)
			}
			if e.State == "" {
				return fmt.Errorf("entry %d has no outcome", i+1)
			}
		}
		return nil
	})
	sc.Step(`^chat model "([^"]*)" uses the credential "([^"]*)" whose value is "([^"]*)"$`, func(ctx context.Context, _, ref, secret string) error {
		cw := callOf(ctx)
		cw.credential = ref
		cw.secret = secret
		return nil
	})
	sc.Step(`^no entry contains "([^"]*)", "([^"]*)" or "([^"]*)"$`, func(ctx context.Context, first, second, third string) error {
		cw := callOf(ctx)
		forbidden := []string{first, second, third}
		if cw.secret != "" {
			forbidden = append(forbidden, cw.secret)
		}
		for i, e := range cw.logs() {
			encoded, err := json.Marshal(e)
			if err != nil {
				return err
			}
			for _, value := range forbidden {
				if value != "" && strings.Contains(string(encoded), value) {
					return fmt.Errorf("entry %d contains %q", i+1, value)
				}
			}
		}
		return nil
	})
	sc.Step(`^the entry names the credential by its reference "([^"]*)"$`, func(ctx context.Context, ref string) error {
		cw := callOf(ctx)
		logs := cw.logs()
		if len(logs) == 0 {
			return fmt.Errorf("the call log is empty")
		}
		if logs[0].CredentialRef != ref {
			return fmt.Errorf("entry 1 names credential %q, want %q", logs[0].CredentialRef, ref)
		}
		return nil
	})
	sc.Step(`^attempt (\d+) was cancelled with code "([^"]*)" before the retry middleware started attempt (\d+)$`, func(ctx context.Context, first int, code string, second int) error {
		cw := callOf(ctx)
		a, ok := cw.attempt(first)
		if !ok {
			return fmt.Errorf("attempt %d is missing", first)
		}
		if a.Outcome != middleware.OutcomeCancelled || string(a.Code) != code {
			return fmt.Errorf("attempt %d = %+v, want cancelled with code %s", first, a, code)
		}
		b, ok := cw.attempt(second)
		if !ok {
			return fmt.Errorf("attempt %d is missing", second)
		}
		if b.Start.Before(a.End) {
			return fmt.Errorf("attempt %d started before attempt %d ended", second, first)
		}
		return nil
	})
	sc.Step(`^the metering recorded (\d+) attempts while the fallback middleware was not triggered$`, func(ctx context.Context, n int) error {
		cw := callOf(ctx)
		if got := len(cw.meters()); got != n {
			return fmt.Errorf("the metering recorded %d attempts, want %d", got, n)
		}
		if cw.current.result.FallbackTriggered {
			return fmt.Errorf("the fallback middleware was triggered")
		}
		return nil
	})
	sc.Step(`^the call log holds (\d+) entries and the recording holds (\d+) calls? with (\d+) attempts$`, func(ctx context.Context, n, calls, attempts int) error {
		cw := callOf(ctx)
		if got := len(cw.logs()); got != n {
			return fmt.Errorf("the call log holds %d entries, want %d", got, n)
		}
		if cw.current.result.Recording == nil {
			return fmt.Errorf("the call has no recording")
		}
		if got := len(cw.current.result.Recording.Attempts); got != attempts {
			return fmt.Errorf("the recording holds %d attempts, want %d", got, attempts)
		}
		_ = calls
		return nil
	})
	sc.Step(`^the first call failed with code "([^"]*)" after (\d+) attempts$`, func(ctx context.Context, code string, attempts int) error {
		cw := callOf(ctx)
		if len(cw.soFar) == 0 {
			return fmt.Errorf("no call was made")
		}
		first := cw.soFar[0]
		if first.err == nil || string(core.CodeOf(first.err)) != code {
			return fmt.Errorf("the first call failed with %w, want code %s", first.err, code)
		}
		if got := len(first.result.Attempts); got != attempts {
			return fmt.Errorf("the first call made %d attempts, want %d", got, attempts)
		}
		return nil
	})
	sc.Step(`^the replay failed with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		cw := callOf(ctx)
		if cw.current.err == nil || string(core.CodeOf(cw.current.err)) != code {
			return fmt.Errorf("the replay failed with %w, want code %s", cw.current.err, code)
		}
		return nil
	})
	sc.Step(`^the provider accepted (\d+) invocations in total across both calls$`, func(ctx context.Context, n int) error {
		cw := callOf(ctx)
		if got := len(cw.provider.records); got != n {
			return fmt.Errorf("the provider accepted %d invocations in total, want %d", got, n)
		}
		return nil
	})
	sc.Step(`^the metering recorded (\d+) attempts in total across both calls$`, func(ctx context.Context, n int) error {
		cw := callOf(ctx)
		if got := len(cw.meter.entries); got != n {
			return fmt.Errorf("the metering recorded %d attempts in total, want %d", got, n)
		}
		return nil
	})
	sc.Step(`^the call log holds (\d+) entries across both calls$`, func(ctx context.Context, n int) error {
		cw := callOf(ctx)
		if got := len(cw.calls.entries); got != n {
			return fmt.Errorf("the call log holds %d entries in total, want %d", got, n)
		}
		return nil
	})
}

func findMeter(entries []middleware.AttemptMeter, attempt int) (middleware.AttemptMeter, bool) {
	for _, e := range entries {
		if e.Attempt == attempt {
			return e, true
		}
	}
	return middleware.AttemptMeter{}, false
}

func normalizeSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

func nonRetryableCode(name string) (core.Code, error) {
	switch name {
	case "vendor request":
		return core.CodeVendor, nil
	case "authentication":
		return core.CodeUnauthenticated, nil
	case "invalid argument":
		return core.CodeInvalidArgument, nil
	case "budget":
		return core.CodeBudgetExceeded, nil
	case "cancelled attempt":
		return core.CodeCancelled, nil
	case "rate limit by itself":
		return core.CodeRateLimited, nil
	default:
		return "", fmt.Errorf("unknown error name %q", name)
	}
}
