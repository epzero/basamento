package telemetry

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
)

func TestRedactorReplacesCredentialValues(t *testing.T) {
	r := redactor{sources: []SecretsSource{NewStaticSecrets("sk-live-7f3a9c1b")}}
	cases := []string{
		"sk-live-7f3a9c1b",
		"key sk-live-7f3a9c1b in use",
	}
	for _, in := range cases {
		if got := r.String(in); got != Redacted {
			t.Errorf("String(%q) = %q, want %q", in, got, Redacted)
		}
	}
	if got := r.String("nothing sensitive"); got != "nothing sensitive" {
		t.Errorf("unrelated string changed: %q", got)
	}
	if got := r.String("sk-live-other"); got != "sk-live-other" {
		t.Errorf("partial credential changed: %q", got)
	}
}

func TestRedactorSensitiveKeysAtAnyDepth(t *testing.T) {
	r := redactor{sources: []SecretsSource{NewStaticSecrets()}}
	in := map[string]any{
		"password":      "hunter2",
		"Authorization": "Bearer abc",
		"Token":         "abc",
		"user": map[string]any{
			"secret": "hunter2",
			"name":   "ana",
		},
		"request.token": "abc",
		"token_count":   12,
		"note":          "step finished",
		"list":          []any{map[string]any{"secret": "hunter2"}},
	}
	got := r.Map(in)
	for _, path := range []string{"password", "Authorization", "Token", "request.token"} {
		if got[path] != Redacted {
			t.Errorf("%s = %v, want redacted", path, got[path])
		}
	}
	user := got["user"].(map[string]any)
	if user["secret"] != Redacted {
		t.Errorf("user.secret = %v, want redacted", user["secret"])
	}
	if user["name"] != "ana" {
		t.Errorf("user.name = %v, want ana", user["name"])
	}
	if got["token_count"] != 12 {
		t.Errorf("token_count = %v, want 12", got["token_count"])
	}
	if got["note"] != "step finished" {
		t.Errorf("note = %v, want unchanged", got["note"])
	}
	list := got["list"].([]any)
	if list[0].(map[string]any)["secret"] != Redacted {
		t.Error("nested slice secret was not redacted")
	}
}

func TestLogHandlerUsesClockAndCorrelation(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	setup := newEmbeddedTestSetup(t, clock)
	setup.SetCredential("openai", "sk-live-7f3a9c1b")

	ctx := telemetryContext("acme", "run_01")
	ctx, span := setup.Tracer().Start(ctx, "step a")
	setup.Logger().InfoContext(ctx, "key sk-live-7f3a9c1b in use", slog.String("note", "ok"))
	span.End()

	logs := setup.Logs()
	if len(logs) != 1 {
		t.Fatalf("kept %d logs, want 1", len(logs))
	}
	rec := logs[0]
	if !rec.Time.Equal(clock.Now()) {
		t.Errorf("log time = %v, want %v", rec.Time, clock.Now())
	}
	if rec.Message != Redacted {
		t.Errorf("message = %q, want redacted", rec.Message)
	}
	if rec.Attrs["run_id"] != "run_01" || rec.Attrs["tenant"] != "acme" {
		t.Errorf("correlation = %v", rec.Attrs)
	}
	if rec.Attrs["trace_id"] != span.SpanContext().TraceID().String() {
		t.Errorf("trace_id = %v, want %v", rec.Attrs["trace_id"], span.SpanContext().TraceID())
	}
	if rec.Attrs["span_id"] != span.SpanContext().SpanID().String() {
		t.Errorf("span_id = %v, want %v", rec.Attrs["span_id"], span.SpanContext().SpanID())
	}
}

func TestLogHandlerOutsideASpanNamesNoSpan(t *testing.T) {
	setup := newEmbeddedTestSetup(t, coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)))
	setup.Logger().InfoContext(telemetryContext("acme", "run_01"), "started")
	rec := setup.Logs()[0]
	if rec.Attrs["trace_id"] != "" || rec.Attrs["span_id"] != "" {
		t.Errorf("trace/span = %v/%v, want empty", rec.Attrs["trace_id"], rec.Attrs["span_id"])
	}
	if rec.Attrs["run_id"] != "run_01" || rec.Attrs["tenant"] != "acme" {
		t.Errorf("correlation = %v", rec.Attrs)
	}
}

func TestLogHandlerGroupRedaction(t *testing.T) {
	setup := newEmbeddedTestSetup(t, coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)))
	setup.Logger().Info("grouped", slog.Group("request", slog.String("token", "hunter2"), slog.String("path", "/runs")))
	rec := setup.Logs()[0]
	request := rec.Attrs["request"].(map[string]any)
	if request["token"] != Redacted {
		t.Errorf("grouped token = %v, want redacted", request["token"])
	}
	if request["path"] != "/runs" {
		t.Errorf("grouped path = %v, want /runs", request["path"])
	}
}

func TestShutdownStopsLogs(t *testing.T) {
	setup := newEmbeddedTestSetup(t, coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)))
	if err := setup.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	setup.Logger().Info("late")
	if got := len(setup.Logs()); got != 0 {
		t.Fatalf("kept %d logs after shutdown, want 0", got)
	}
}

func telemetryContext(tenant, run string) context.Context {
	ctx := core.WithTenant(context.Background(), core.TenantID(tenant))
	return WithRunID(ctx, run)
}

func TestKeptSignalsAreBounded(t *testing.T) {
	setup := newEmbeddedTestSetup(t, coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)))
	for i := 0; i < maxKeptSignals+5; i++ {
		setup.Logger().Info("bounded")
	}
	logs := setup.Logs()
	if len(logs) != maxKeptSignals {
		t.Fatalf("kept %d logs, want %d", len(logs), maxKeptSignals)
	}
}
