package middleware

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/structured"
)

const invalidAnswer = `{"answer": "seven"}`

func structuredProvider(t *testing.T, rt *virtualRuntime, answers ...string) (*scriptedProvider, *[]structured.Rung) {
	t.Helper()
	rungs := &[]structured.Rung{}
	p := &scriptedProvider{rt: rt}
	p.run = func(ctx context.Context, p *scriptedProvider, a Attempt, emit func(model.Event)) (model.Result, error) {
		*rungs = append(*rungs, a.Rung)
		answer := invalidAnswer
		if a.Number-1 < len(answers) {
			answer = answers[a.Number-1]
		}
		emit(model.Event{Kind: model.EventText, Text: answer})
		return succeeded(answer, nil), nil
	}
	return p, rungs
}

func TestLadderTriesEachSupportedRungOnOneBudget(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	p, _ := structuredProvider(t, rt)
	cfg := baseConfig(rt, p)
	cfg.Structured = answerSpec(t, structured.RungNativeSchema, structured.RungJSONMode)
	res, err := Call(context.Background(), cfg)
	if got := codeOf(t, err); got != core.CodeInvalidArgument {
		t.Fatalf("code = %s, want invalid_argument", got)
	}
	if !strings.Contains(err.Error(), "no attempt satisfied the output schema") {
		t.Fatalf("error = %v, want the schema message", err)
	}
	if len(res.Attempts) != 3 || p.invoked != 2 {
		t.Fatalf("attempts = %d, invocations = %d, want 3 and 2", len(res.Attempts), p.invoked)
	}
	want := []structured.Rung{structured.RungNativeSchema, structured.RungJSONMode, structured.RungLocalRepair}
	for i, r := range want {
		if res.Attempts[i].Rung != r {
			t.Fatalf("attempt %d rung = %s, want %s", i+1, res.Attempts[i].Rung, r)
		}
	}
	if !res.Attempts[2].LocalRepair {
		t.Fatal("attempt 3 is not the local repair")
	}
}

func TestSmallerBudgetStopsTheLadder(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	p, _ := structuredProvider(t, rt)
	cfg := baseConfig(rt, p)
	cfg.Structured = answerSpec(t, structured.RungNativeSchema, structured.RungJSONMode)
	cfg.Attempts = 2
	res, err := Call(context.Background(), cfg)
	if got := codeOf(t, err); got != core.CodeInvalidArgument {
		t.Fatalf("code = %s, want invalid_argument", got)
	}
	if len(res.Attempts) != 2 || p.invoked != 2 {
		t.Fatalf("attempts = %d, invocations = %d, want 2", len(res.Attempts), p.invoked)
	}
}

func TestFirstAttemptUsesTheFirstSupportedRung(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	p, rungs := structuredProvider(t, rt, `{"answer": 7}`)
	cfg := baseConfig(rt, p)
	cfg.Structured = answerSpec(t, structured.RungJSONMode, structured.RungForcedTool)
	res, err := Call(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(res.Attempts) != 1 || (*rungs)[0] != structured.RungJSONMode {
		t.Fatalf("rungs = %v, want JSON mode first", *rungs)
	}
}

func TestInvalidAnswerAdvancesToTheNextRung(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	p, rungs := structuredProvider(t, rt, invalidAnswer, `{"answer": 7}`)
	cfg := baseConfig(rt, p)
	cfg.Structured = answerSpec(t, structured.RungNativeSchema, structured.RungForcedTool)
	res, err := Call(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(res.Attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(res.Attempts))
	}
	if (*rungs)[0] != structured.RungNativeSchema || (*rungs)[1] != structured.RungForcedTool {
		t.Fatalf("rungs = %v, want native schema then forced tool", *rungs)
	}
}

func TestRetryableFailureRetriesOnTheSameRung(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	var rungs []structured.Rung
	p := &scriptedProvider{rt: rt}
	p.run = func(ctx context.Context, p *scriptedProvider, a Attempt, emit func(model.Event)) (model.Result, error) {
		rungs = append(rungs, a.Rung)
		switch a.Number {
		case 1:
			return model.Result{}, coreErr(core.CodeUnavailable, true, 0)
		case 2:
			emit(model.Event{Kind: model.EventText, Text: invalidAnswer})
			return succeeded(invalidAnswer, nil), nil
		default:
			emit(model.Event{Kind: model.EventText, Text: `{"answer": 7}`})
			return succeeded(`{"answer": 7}`, nil), nil
		}
	}
	cfg := baseConfig(rt, p)
	cfg.Structured = answerSpec(t, structured.RungNativeSchema, structured.RungJSONMode)
	res, err := Call(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	want := []structured.Rung{structured.RungNativeSchema, structured.RungNativeSchema, structured.RungJSONMode}
	if len(rungs) != len(want) {
		t.Fatalf("rungs = %v, want %v", rungs, want)
	}
	for i := range want {
		if rungs[i] != want[i] {
			t.Fatalf("rungs = %v, want %v", rungs, want)
		}
	}
	if len(res.Attempts) != 3 {
		t.Fatalf("attempts = %d, want 3", len(res.Attempts))
	}
}

func TestModelWithoutARungIsNotCalled(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	p, _ := structuredProvider(t, rt)
	cfg := baseConfig(rt, p)
	cfg.Structured = answerSpec(t)
	res, err := Call(context.Background(), cfg)
	if got := codeOf(t, err); got != core.CodeUnsupported {
		t.Fatalf("code = %s, want unsupported", got)
	}
	if len(res.Attempts) != 0 || p.invoked != 0 {
		t.Fatalf("attempts = %d, invocations = %d, want 0", len(res.Attempts), p.invoked)
	}
}

func TestSpentLadderRepairsOnceEvenWithBudgetLeft(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	p, _ := structuredProvider(t, rt)
	cfg := baseConfig(rt, p)
	cfg.Structured = answerSpec(t, structured.RungJSONMode)
	cfg.Attempts = 5
	res, err := Call(context.Background(), cfg)
	if got := codeOf(t, err); got != core.CodeInvalidArgument {
		t.Fatalf("code = %s, want invalid_argument", got)
	}
	if len(res.Attempts) != 2 || p.invoked != 1 {
		t.Fatalf("attempts = %d, invocations = %d, want 2 and 1", len(res.Attempts), p.invoked)
	}
}

func TestLocalRepairReturnsTheRepairedJSON(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	fenced := "```json\n{\"answer\": 7,}\n```\nHope this helps!"
	p, rungs := structuredProvider(t, rt, fenced)
	cfg := baseConfig(rt, p)
	cfg.Structured = answerSpec(t, structured.RungJSONMode)
	cfg.Attempts = 2
	res, err := Call(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(res.Attempts) != 2 || (*rungs)[0] != structured.RungJSONMode {
		t.Fatalf("attempts = %d, rungs = %v, want 2 and JSON mode", len(res.Attempts), *rungs)
	}
	if res.Attempts[1].Rung != structured.RungLocalRepair || !res.Attempts[1].LocalRepair {
		t.Fatalf("attempt 2 = %+v, want the local repair", res.Attempts[1])
	}
	var content struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(res.Result.Message, &content); err != nil {
		t.Fatal(err)
	}
	if content.Content != `{"answer":7}` {
		t.Fatalf("answer content = %q, want {\"answer\":7}", content.Content)
	}
}

func TestCallTotalTimeoutEndsTheLadder(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	p, _ := structuredProvider(t, rt)
	cfg := baseConfig(rt, p)
	cfg.Structured = answerSpec(t, structured.RungNativeSchema, structured.RungJSONMode)
	cfg.Timeouts.CallTotal = 3 * time.Second
	p.run = func(ctx context.Context, p *scriptedProvider, a Attempt, emit func(model.Event)) (model.Result, error) {
		return model.Result{}, coreErr(core.CodeTimeout, true, 4*time.Second)
	}
	res, err := Call(context.Background(), cfg)
	if got := codeOf(t, err); got != core.CodeTimeout {
		t.Fatalf("code = %s, want timeout", got)
	}
	if len(res.Attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(res.Attempts))
	}
}

func TestReplayUsesNoProviderAndNoMeter(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	p := &scriptedProvider{rt: rt}
	p.run = func(context.Context, *scriptedProvider, Attempt, func(model.Event)) (model.Result, error) {
		return model.Result{}, coreErr(core.CodeUnavailable, true, 0)
	}
	recorder := NewRecorder()
	meter := &meterCollector{}
	calls := &logCollector{}
	cfg := baseConfig(rt, p)
	cfg.Attempts = 2
	cfg.Recorder = recorder
	cfg.Meter = meter
	cfg.CallLog = calls
	first, err := Call(context.Background(), cfg)
	if got := codeOf(t, err); got != core.CodeUnavailable {
		t.Fatalf("first call code = %s, want unavailable", got)
	}
	if len(first.Attempts) != 2 || p.invoked != 2 {
		t.Fatalf("first call attempts = %d, invocations = %d, want 2", len(first.Attempts), p.invoked)
	}
	if len(meter.entries) != 2 || len(calls.entries) != 2 {
		t.Fatalf("first call meter = %d, log = %d, want 2", len(meter.entries), len(calls.entries))
	}
	if first.Recording == nil || len(first.Recording.Attempts) != 2 {
		t.Fatalf("recording = %+v, want 2 attempts", first.Recording)
	}

	replayCfg := baseConfig(rt, nil)
	replayCfg.Provider = nil
	replayCfg.Replay = first.Recording
	replayCfg.Meter = meter
	replayCfg.CallLog = calls
	second, err := Call(context.Background(), replayCfg)
	if got := codeOf(t, err); got != core.CodeUnavailable {
		t.Fatalf("replay code = %s, want unavailable", got)
	}
	if p.invoked != 2 {
		t.Fatalf("replay invoked the provider: %d total", p.invoked)
	}
	if len(meter.entries) != 2 {
		t.Fatalf("replay metered: %d entries total", len(meter.entries))
	}
	if len(calls.entries) != 3 {
		t.Fatalf("call log = %d entries total, want 3", len(calls.entries))
	}
	if second.Recording != first.Recording {
		t.Fatal("replay did not return its recording")
	}
}
