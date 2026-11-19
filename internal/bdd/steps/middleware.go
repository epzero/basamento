package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/middleware"
	"github.com/epzero/basamento/model/structured"
)

// callWorld drives the middleware and structured-output scenarios. One call
// runs in a goroutine on a virtual runtime; the steps advance the scenario
// clock and read the call's stable state after each advance.
type callWorld struct {
	mw    *modelWorld
	clock *coretest.FakeClock
	rt    *vRuntime
	ctx   context.Context
	stop  context.CancelFunc

	schema     json.RawMessage
	rungs      []structured.Rung
	rungsSet   bool
	budget     int
	jitter     []time.Duration
	price      middleware.PriceFunc
	credential string
	secret     string
	timeouts   middleware.Timeouts
	message    string

	provider *scriptedChat
	meter    *meterCollector
	calls    *callLogCollector

	current  *callOutcome
	soFar    []*callOutcome
	recorded *middleware.Recording
}

type callOutcome struct {
	result   middleware.CallResult
	err      error
	events   []model.Event
	attempts []middleware.AttemptRecord
	done     chan struct{}

	invStart   int
	logStart   int
	meterStart int
	invEnd     int
	logEnd     int
	meterEnd   int
}

type meterCollector struct {
	entries []middleware.AttemptMeter
}

func (m *meterCollector) Record(e middleware.AttemptMeter) { m.entries = append(m.entries, e) }

type callLogCollector struct {
	entries []middleware.CallLogEntry
}

func (l *callLogCollector) Record(e middleware.CallLogEntry) { l.entries = append(l.entries, e) }

type invocationRecord struct {
	Number    int
	Rung      structured.Rung
	ClientRef string
	Start     time.Time
	End       time.Time
	Usage     model.Usage
	HasUsage  bool
	Text      string
	Err       error
}

type chatBehavior func(ctx context.Context, emit func(model.Event)) (model.Result, error)

type scriptedChat struct {
	rt      *vRuntime
	plans   map[int]chatBehavior
	def     chatBehavior
	records []invocationRecord
}

func newScriptedChat(rt *vRuntime) *scriptedChat {
	return &scriptedChat{rt: rt, plans: map[int]chatBehavior{}}
}

func (p *scriptedChat) Attempt(ctx context.Context, a middleware.Attempt, emit func(model.Event)) (model.Result, error) {
	start := p.rt.Now()
	behavior := p.plans[a.Number]
	if behavior == nil {
		behavior = p.def
	}
	if behavior == nil {
		behavior = p.stream("Hello", nil, 1)
	}
	res, err := behavior(ctx, emit)
	usage := model.Usage{}
	hasUsage := res.Usage != nil
	if hasUsage {
		usage = *res.Usage
	}
	p.records = append(p.records, invocationRecord{
		Number: a.Number, Rung: a.Rung, ClientRef: a.ClientRef,
		Start: start, End: p.rt.Now(), Usage: usage, HasUsage: hasUsage,
		Text: textOf(res), Err: err,
	})
	return res, err
}

func (p *scriptedChat) set(attempt int, b chatBehavior) {
	if attempt <= 0 {
		p.def = b
		return
	}
	p.plans[attempt] = b
}

func (p *scriptedChat) stream(text string, usage *model.Usage, chunks int) chatBehavior {
	return func(ctx context.Context, emit func(model.Event)) (model.Result, error) {
		for _, part := range splitText(text, chunks) {
			emit(model.Event{Kind: model.EventText, Text: part})
		}
		return chatResult(text, usage), nil
	}
}

func (p *scriptedChat) fail(code core.Code, retryable bool, after time.Duration, usage *model.Usage) chatBehavior {
	return func(ctx context.Context, emit func(model.Event)) (model.Result, error) {
		return model.Result{Usage: usage}, &core.Error{Code: code, Message: string(code), Retryable: retryable, RetryAfter: after}
	}
}

func (p *scriptedChat) unacknowledgedStart(code core.Code, retryable bool) chatBehavior {
	return func(ctx context.Context, emit func(model.Event)) (model.Result, error) {
		return model.Result{}, &middleware.StartError{
			Err: &core.Error{Code: code, Message: string(code), Retryable: retryable},
		}
	}
}

func (p *scriptedChat) deltasThenFail(parts []string, code core.Code, retryable bool, usage *model.Usage) chatBehavior {
	return func(ctx context.Context, emit func(model.Event)) (model.Result, error) {
		for _, part := range parts {
			emit(model.Event{Kind: model.EventText, Text: part})
		}
		return model.Result{Usage: usage}, &core.Error{Code: code, Message: string(code), Retryable: retryable}
	}
}

func (p *scriptedChat) silent(ctx context.Context, _ func(model.Event)) (model.Result, error) {
	if err := p.rt.Sleep(ctx, 24*time.Hour); err != nil {
		return model.Result{}, err
	}
	return chatResult("Hello", nil), nil
}

func (p *scriptedChat) firstAt(times []time.Duration, text, rest string, usage *model.Usage) chatBehavior {
	return func(ctx context.Context, emit func(model.Event)) (model.Result, error) {
		elapsed := time.Duration(0)
		for i, at := range times {
			if d := at - elapsed; d > 0 {
				if err := p.rt.Sleep(ctx, d); err != nil {
					return model.Result{}, err
				}
				elapsed = at
			}
			chunk := text
			if i > 0 && rest != "" {
				chunk = rest
			}
			emit(model.Event{Kind: model.EventText, Text: chunk})
		}
		if err := p.rt.Sleep(ctx, 24*time.Hour); err != nil {
			return model.Result{}, err
		}
		return chatResult(strings.Repeat(text, len(times)), usage), nil
	}
}

func (cw *callWorld) ensure(clock *coretest.FakeClock) {
	if cw.rt != nil {
		return
	}
	cw.clock = clock
	cw.rt = newVRuntime(clock)
	cw.ctx, cw.stop = context.WithCancel(context.Background())
	cw.provider = newScriptedChat(cw.rt)
	cw.meter = &meterCollector{}
	cw.calls = &callLogCollector{}
}

func (cw *callWorld) close() {
	if cw.rt == nil {
		return
	}
	if cw.stop != nil {
		cw.stop()
	}
	cw.rt.close()
}

func (cw *callWorld) spec(structuredCall bool) (*structured.Spec, error) {
	if !structuredCall {
		return nil, nil
	}
	if len(cw.schema) == 0 {
		return nil, fmt.Errorf("the scenario set no output schema")
	}
	rungs := cw.rungs
	if !cw.rungsSet {
		rungs = structured.Ladder()
	}
	return structured.NewSpec(cw.schema, rungs)
}

func (cw *callWorld) config(clientRef string, structuredCall bool) (middleware.Config, error) {
	spec, err := cw.spec(structuredCall)
	if err != nil {
		return middleware.Config{}, err
	}
	content := cw.message
	if content == "" {
		content = "hi"
	}
	messages, err := json.Marshal([]map[string]string{{"role": "user", "content": content}})
	if err != nil {
		return middleware.Config{}, err
	}
	return middleware.Config{
		Model:         "sim-chat",
		ClientRef:     clientRef,
		Request:       model.Request{Model: "sim-chat", Op: model.CapChat, ClientRef: clientRef, Messages: messages},
		Structured:    spec,
		Attempts:      cw.budget,
		Provider:      cw.provider,
		Runtime:       cw.rt,
		Rand:          &scriptRand{draws: cw.jitter},
		Price:         cw.price,
		Meter:         cw.meter,
		CallLog:       cw.calls,
		CredentialRef: cw.credential,
		Timeouts:      cw.timeouts,
		Recorder:      middleware.NewRecorder(),
		Emit: func(ev model.Event) {
			if cw.current != nil {
				cw.current.events = append(cw.current.events, ev)
			}
		},
		OnAttempt: func(rec middleware.AttemptRecord) {
			if cw.current != nil {
				cw.current.attempts = append(cw.current.attempts, rec)
			}
		},
	}, nil
}

type scriptRand struct {
	draws []time.Duration
	next  int
}

func (r *scriptRand) Int64n(n int64) int64 {
	if n <= 0 {
		return 0
	}
	if r.next >= len(r.draws) {
		r.next++
		return 0
	}
	d := int64(r.draws[r.next])
	r.next++
	if d >= n {
		return n - 1
	}
	return d
}

func (cw *callWorld) start(ctx context.Context, structuredCall bool, clientRef string, record bool) error {
	cw.ensure(world(ctx).Core.clock)
	out := &callOutcome{done: make(chan struct{}), invStart: len(cw.provider.records), logStart: len(cw.calls.entries), meterStart: len(cw.meter.entries)}
	cw.current = out
	cw.soFar = append(cw.soFar, out)

	cfg, err := cw.config(clientRef, structuredCall)
	if err != nil {
		return err
	}
	go func() {
		defer close(out.done)
		res, callErr := middleware.Call(cw.ctx, cfg)
		out.result, out.err = res, callErr
		out.invEnd = len(cw.provider.records)
		out.logEnd = len(cw.calls.entries)
		out.meterEnd = len(cw.meter.entries)
		cw.mw.result = res.Result
		if record {
			cw.recorded = res.Recording
		}
		cw.rt.finish()
	}()
	return nil
}

// invoke runs a call to completion, advancing virtual time as its waits and
// stream demand.
func (cw *callWorld) invoke(ctx context.Context, structuredCall bool, clientRef string, record bool) error {
	if err := cw.start(ctx, structuredCall, clientRef, record); err != nil {
		return err
	}
	cw.rt.autoPump()
	<-cw.current.done
	return nil
}

// follow starts a call and returns once it has parked or finished, so the
// scenario's "N seconds pass" steps drive the rest.
func (cw *callWorld) follow(ctx context.Context, structuredCall bool, clientRef string) error {
	if err := cw.start(ctx, structuredCall, clientRef, false); err != nil {
		return err
	}
	cw.rt.waitReady()
	return nil
}

func (cw *callWorld) invocations() []invocationRecord {
	return cw.provider.records[cw.current.invStart:cw.current.invEnd]
}

func (cw *callWorld) logs() []middleware.CallLogEntry {
	return cw.calls.entries[cw.current.logStart:cw.current.logEnd]
}

func (cw *callWorld) meters() []middleware.AttemptMeter {
	return cw.meter.entries[cw.current.meterStart:cw.current.meterEnd]
}

func (cw *callWorld) attempt(n int) (middleware.AttemptRecord, bool) {
	for _, a := range cw.current.attempts {
		if a.Number == n {
			return a, true
		}
	}
	return middleware.AttemptRecord{}, false
}

func (cw *callWorld) textEvents(events []model.Event) []model.Event {
	var out []model.Event
	for _, e := range events {
		if e.Kind == model.EventText {
			out = append(out, e)
		}
	}
	return out
}

func chatResult(text string, usage *model.Usage) model.Result {
	message, _ := json.Marshal(map[string]any{"role": "assistant", "content": text})
	return model.Result{State: model.StateSucceeded, Message: message, Usage: usage}
}

func textOf(res model.Result) string {
	if len(res.Message) == 0 {
		return ""
	}
	var m struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(res.Message, &m); err != nil {
		return ""
	}
	return m.Content
}

func splitText(text string, chunks int) []string {
	if chunks <= 1 || text == "" {
		return []string{text}
	}
	runes := []rune(text)
	out := make([]string, 0, chunks)
	for i := 0; i < chunks; i++ {
		start := i * len(runes) / chunks
		end := (i + 1) * len(runes) / chunks
		if start == end {
			continue
		}
		out = append(out, string(runes[start:end]))
	}
	return out
}

func registerMiddleware(sc *godog.ScenarioContext) {
	sc.Step(`^(?:a )?chat model "([^"]*)" whose output schema requires an integer field "([^"]*)"$`, func(ctx context.Context, _, field string) error {
		schema, err := json.Marshal(map[string]any{
			"type":                 "object",
			"properties":           map[string]any{field: map[string]any{"type": "integer"}},
			"required":             []string{field},
			"additionalProperties": false,
		})
		if err != nil {
			return err
		}
		callOf(ctx).schema = schema
		return nil
	})
	sc.Step(`^chat model "([^"]*)" supports the rungs "([^"]*)"$`, func(ctx context.Context, _, rungs string) error {
		parsed, err := structured.ParseRungs(rungs)
		if err != nil {
			return err
		}
		cw := callOf(ctx)
		if cw == nil {
			return fmt.Errorf("no chat model was set up")
		}
		cw.rungs, cw.rungsSet = parsed, true
		return nil
	})
	sc.Step(`^the scripted model returns a JSON answer that misses the schema on every attempt$`, func(ctx context.Context) error {
		cw := callOf(ctx)
		cw.provider.set(0, cw.provider.stream(invalidAnswer, nil, 1))
		return nil
	})
	sc.Step(`^the scripted model returns satisfying output on every attempt$`, func(ctx context.Context) error {
		cw := callOf(ctx)
		cw.provider.set(0, cw.provider.stream(validAnswer, nil, 1))
		return nil
	})
	sc.Step(`^the scripted model returns a JSON answer that misses the schema on attempt (\d+) and then satisfying output$`, func(ctx context.Context, attempt int) error {
		cw := callOf(ctx)
		cw.provider.set(attempt, cw.provider.stream(invalidAnswer, nil, 1))
		cw.provider.set(0, cw.provider.stream(validAnswer, nil, 1))
		return nil
	})
	sc.Step(`^the scripted model fails attempt (\d+) with code "([^"]*)" marked retryable$`, func(ctx context.Context, attempt int, code string) error {
		cw := callOf(ctx)
		cw.provider.set(attempt, cw.provider.fail(core.Code(code), true, 0, nil))
		return nil
	})
	sc.Step(`^the call's seeded randomness draws (\d+) milliseconds of jitter$`, func(ctx context.Context, ms int) error {
		callOf(ctx).jitter = []time.Duration{time.Duration(ms) * time.Millisecond}
		return nil
	})
	sc.Step(`^the call's seeded randomness draws (\d+) milliseconds of jitter for retry (\d+) and (\d+) milliseconds for retry (\d+)$`, func(ctx context.Context, first, firstRetry, second, secondRetry int) error {
		cw := callOf(ctx)
		draws := make([]time.Duration, maxInt(firstRetry, secondRetry))
		draws[firstRetry-1] = time.Duration(first) * time.Millisecond
		draws[secondRetry-1] = time.Duration(second) * time.Millisecond
		cw.jitter = draws
		return nil
	})
	sc.Step(`^a call allows (\d+) attempts$`, func(ctx context.Context, n int) error {
		callOf(ctx).budget = n
		return nil
	})
	sc.Step(`^a call asks "([^"]*)" for output that satisfies the output schema$`, func(ctx context.Context, _ string) error {
		return callOf(ctx).invoke(ctx, true, "call-1", false)
	})
	sc.Step(`^a call with client ref "([^"]*)" asks "([^"]*)" for output that satisfies the output schema$`, func(ctx context.Context, ref, _ string) error {
		return callOf(ctx).invoke(ctx, true, ref, false)
	})
	sc.Step(`^another call with client ref "([^"]*)" asks "([^"]*)" for output that satisfies the output schema$`, func(ctx context.Context, ref, _ string) error {
		return callOf(ctx).invoke(ctx, true, ref, false)
	})
	sc.Step(`^the call made (\d+) attempts?$`, func(ctx context.Context, n int) error {
		cw := callOf(ctx)
		if got := len(cw.current.attempts); got != n {
			return fmt.Errorf("the call made %d attempts, want %d", got, n)
		}
		return nil
	})
	sc.Step(`^attempt (\d+) used the rung "([^"]*)"$`, func(ctx context.Context, n int, rung string) error {
		cw := callOf(ctx)
		rec, ok := cw.attempt(n)
		if !ok {
			return fmt.Errorf("attempt %d is missing", n)
		}
		if rec.Rung.Display() != rung {
			return fmt.Errorf("attempt %d used the rung %q, want %q", n, rec.Rung.Display(), rung)
		}
		return nil
	})
	sc.Step(`^invocation (\d+) used only the rung "([^"]*)"$`, func(ctx context.Context, n int, rung string) error {
		cw := callOf(ctx)
		inv := cw.invocations()
		if n < 1 || n > len(inv) {
			return fmt.Errorf("invocation %d is missing", n)
		}
		if inv[n-1].Rung.Display() != rung {
			return fmt.Errorf("invocation %d used the rung %q, want %q", n, inv[n-1].Rung.Display(), rung)
		}
		return nil
	})
	sc.Step(`^the call failed with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		cw := callOf(ctx)
		if cw.current.err == nil {
			return fmt.Errorf("the call succeeded, want code %s", code)
		}
		if got := string(core.CodeOf(cw.current.err)); got != code {
			return fmt.Errorf("the call failed with code %s, want %s: %w", got, code, cw.current.err)
		}
		return nil
	})
	sc.Step(`^the provider accepted (\d+) invocations?$`, func(ctx context.Context, n int) error {
		cw := callOf(ctx)
		if got := len(cw.invocations()); got != n {
			return fmt.Errorf("the provider accepted %d invocations, want %d", got, n)
		}
		return nil
	})
	sc.Step(`^the call's error says no attempt satisfied the output schema$`, func(ctx context.Context) error {
		cw := callOf(ctx)
		if cw.current.err == nil || !strings.Contains(cw.current.err.Error(), "no attempt satisfied the output schema") {
			return fmt.Errorf("the call's error = %w, want the schema message", cw.current.err)
		}
		return nil
	})
	sc.Step(`^for both calls invocation (\d+) carried the client ref "([^"]*)"$`, func(ctx context.Context, n int, ref string) error {
		cw := callOf(ctx)
		for i, call := range cw.soFar {
			inv := cw.provider.records[call.invStart:call.invEnd]
			if n < 1 || n > len(inv) {
				return fmt.Errorf("call %d has no invocation %d", i+1, n)
			}
			if inv[n-1].ClientRef != ref {
				return fmt.Errorf("call %d invocation %d carried %q, want %q", i+1, n, inv[n-1].ClientRef, ref)
			}
		}
		return nil
	})
	sc.Step(`^both calls' attempt (\d+) invocations carried the same client ref derived from "([^"]*)" and distinct from "([^"]*)"$`, func(ctx context.Context, n int, callRef, plainRef string) error {
		cw := callOf(ctx)
		var shared string
		for i, call := range cw.soFar {
			inv := cw.provider.records[call.invStart:call.invEnd]
			if n < 1 || n > len(inv) {
				return fmt.Errorf("call %d has no invocation %d", i+1, n)
			}
			ref := inv[n-1].ClientRef
			if ref == plainRef {
				return fmt.Errorf("call %d invocation %d reused %q", i+1, n, plainRef)
			}
			if i == 0 {
				shared = ref
			} else if ref != shared {
				return fmt.Errorf("calls derived different refs: %q and %q", shared, ref)
			}
		}
		if want := structured.ClientRef(callRef, n); shared != want {
			return fmt.Errorf("derived ref = %q, want %q", shared, want)
		}
		return nil
	})
	sc.Step(`^each call caused the provider to accept (\d+) invocations?$`, func(ctx context.Context, n int) error {
		cw := callOf(ctx)
		for i, call := range cw.soFar {
			if got := len(cw.provider.records[call.invStart:call.invEnd]); got != n {
				return fmt.Errorf("call %d caused %d invocations, want %d", i+1, got, n)
			}
		}
		return nil
	})
	sc.Step(`^the scripted model fails start (\d+) without acknowledging acceptance with code "([^"]*)" marked retryable and returns satisfying output on the next start$`, func(ctx context.Context, attempt int, code string) error {
		cw := callOf(ctx)
		cw.provider.set(attempt, cw.provider.unacknowledgedStart(core.Code(code), true))
		cw.provider.set(0, cw.provider.stream(validAnswer, nil, 1))
		return nil
	})
	sc.Step(`^the provider received (\d+) start requests both carrying the client ref "([^"]*)"$`, func(ctx context.Context, n int, ref string) error {
		cw := callOf(ctx)
		inv := cw.invocations()
		if len(inv) != n {
			return fmt.Errorf("the provider received %d start requests, want %d", len(inv), n)
		}
		for i, rec := range inv {
			if rec.ClientRef != ref {
				return fmt.Errorf("start request %d carried %q, want %q", i+1, rec.ClientRef, ref)
			}
		}
		return nil
	})
	sc.Step(`^attempts (\d+) and (\d+) used the rungs "([^"]*)" and "([^"]*)"$`, func(ctx context.Context, first, second int, firstRung, secondRung string) error {
		cw := callOf(ctx)
		for _, tc := range []struct {
			n    int
			rung string
		}{{first, firstRung}, {second, secondRung}} {
			rec, ok := cw.attempt(tc.n)
			if !ok {
				return fmt.Errorf("attempt %d is missing", tc.n)
			}
			if rec.Rung.Display() != tc.rung {
				return fmt.Errorf("attempt %d used the rung %q, want %q", tc.n, rec.Rung.Display(), tc.rung)
			}
		}
		return nil
	})
	sc.Step(`^attempt (\d+) repaired the JSON locally without a model invocation$`, func(ctx context.Context, n int) error {
		cw := callOf(ctx)
		rec, ok := cw.attempt(n)
		if !ok {
			return fmt.Errorf("attempt %d is missing", n)
		}
		if rec.Rung != structured.RungLocalRepair || !rec.LocalRepair {
			return fmt.Errorf("attempt %d was not the local repair: %+v", n, rec)
		}
		return nil
	})
	sc.Step(`^the scripted model returns the answer$`, func(ctx context.Context, answer string) error {
		cw := callOf(ctx)
		cw.provider.set(0, cw.provider.stream(strings.TrimSpace(answer), nil, 1))
		return nil
	})
	sc.Step(`^the call returns the JSON (.+)$`, func(ctx context.Context, want string) error {
		cw := callOf(ctx)
		content := textOf(cw.current.result.Result)
		var gotValue, wantValue any
		if err := json.Unmarshal([]byte(content), &gotValue); err != nil {
			return fmt.Errorf("the call returned %q, not JSON: %w", content, err)
		}
		if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
			return fmt.Errorf("the scenario's JSON is malformed: %w", err)
		}
		if !reflect.DeepEqual(gotValue, wantValue) {
			return fmt.Errorf("the call returned %s, want %s", content, want)
		}
		return nil
	})
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// callOf returns the scenario's call world, creating it on first use so a
// middleware scenario needs no setup step before its first Given.
func callOf(ctx context.Context) *callWorld {
	w := world(ctx).Model
	if w.call == nil {
		w.call = &callWorld{mw: w}
	}
	w.call.ensure(world(ctx).Core.clock)
	return w.call
}
