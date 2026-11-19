package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
)

type credentialStruct struct {
	Name     string
	Password string
	Nested   struct {
		Token string
		Note  string
	}
}

type secretStringer struct{ secret string }

func (s secretStringer) String() string { return "token=" + s.secret }

// stringerCredential has an exported sensitive field and a value-receiver
// String method: field redaction must win over the text form.
type stringerCredential struct{ Password string }

func (s stringerCredential) String() string { return "password=" + s.Password }

// errorCredential is an error with an exported sensitive field.
type errorCredential struct{ Secret string }

func (e errorCredential) Error() string { return "secret=" + e.Secret }

// innerCreds is the unexported payload reached through an embedded or named
// unexported field.
type innerCreds struct{ Password string }

type embeddedCreds struct{ innerCreds }

func (e embeddedCreds) String() string { return "password=" + e.Password }

type namedCreds struct{ inner innerCreds }

func (n namedCreds) String() string { return "password=" + n.inner.Password }

// pointerHolder hides a sensitive payload behind a named unexported pointer to
// a struct and formats it in Error.
type pointerHolder struct{ inner *innerCreds }

func (p pointerHolder) Error() string { return "password=" + p.inner.Password }

// embeddedErrorHolder hides a sensitive payload behind an unexported embedded
// error interface, the idiomatic error wrapper.
type embeddedErrorHolder struct{ error }

// plainWrapper formats its cause but does not Unwrap, so the unwrap chain
// cannot see the payload.
type plainWrapper struct{ cause error }

func (w plainWrapper) Error() string { return w.cause.Error() }

// mapStringer is a Stringer backed by a map: its text must not be trusted when
// the same map would have its sensitive key redacted.
type mapStringer map[string]string

func (m mapStringer) String() string { return "password=" + m["password"] }

// sliceError is an error backed by a slice of structs with a sensitive field.
type sliceError []errorCredential

func (e sliceError) Error() string { return "secret=" + e[0].Secret }

// doublePointerHolder hides the payload behind a named unexported ** pointer.
type doublePointerHolder struct{ inner **innerCreds }

func (h doublePointerHolder) Error() string { return "password=" + (*h.inner).Password }

// structMapStringer hides its payload in an unexported map field. The map's
// keys are data, so the type must be canonicalised rather than rendered.
type structMapStringer struct{ m map[string]string }

func (s structMapStringer) String() string { return "password=" + s.m["password"] }

// intKeyMapStringer carries a non-string-keyed map, which reflection redacts
// whole; its text form must not bypass that.
type intKeyMapStringer struct{ m map[int]string }

func (s intKeyMapStringer) String() string { return "token=" + s.m[1] }

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

func TestRedactorCanonicalisesNonStringValues(t *testing.T) {
	r := redactor{sources: []SecretsSource{NewStaticSecrets("hunter2")}}
	got := r.Map(map[string]any{
		"error":    errors.New("hunter2"),
		"stringer": secretStringer{secret: "hunter2"},
		"bytes":    []byte("hunter2"),
		"creds": credentialStruct{
			Name:     "ana",
			Password: "hunter2",
			Nested: struct {
				Token string
				Note  string
			}{Token: "hunter2", Note: "kept"},
		},
	})

	if got["error"] != Redacted {
		t.Errorf("error = %v, want redacted", got["error"])
	}
	if got["stringer"] != Redacted {
		t.Errorf("Stringer = %v, want redacted", got["stringer"])
	}
	if got["bytes"] != Redacted {
		t.Errorf("[]byte = %v, want redacted", got["bytes"])
	}
	creds, ok := got["creds"].(map[string]any)
	if !ok {
		t.Fatalf("struct was not canonicalised: %T", got["creds"])
	}
	if creds["Password"] != Redacted {
		t.Errorf("Password = %v, want redacted", creds["Password"])
	}
	if creds["Name"] != "ana" {
		t.Errorf("Name = %v, want ana", creds["Name"])
	}
	nested, ok := creds["Nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested struct was not canonicalised: %T", creds["Nested"])
	}
	if nested["Token"] != Redacted || nested["Note"] != "kept" {
		t.Errorf("nested = %v, want Token redacted and Note kept", nested)
	}
	for key, value := range got {
		if text, ok := value.(string); ok && text == "hunter2" {
			t.Errorf("%s still carries the credential", key)
		}
	}
}

func TestRedactorDoesNotLetStringerBypassFieldRedaction(t *testing.T) {
	r := redactor{sources: []SecretsSource{NewStaticSecrets()}}
	got := r.Map(map[string]any{
		"stringer": stringerCredential{Password: "S3cretField"},
		"error":    errorCredential{Secret: "S3cretField"},
	})

	stringer, ok := got["stringer"].(map[string]any)
	if !ok {
		t.Fatalf("Stringer struct was not canonicalised: %T", got["stringer"])
	}
	if stringer["Password"] != Redacted {
		t.Errorf("Stringer Password = %v, want redacted", stringer["Password"])
	}
	errValue, ok := got["error"].(map[string]any)
	if !ok {
		t.Fatalf("Error struct was not canonicalised: %T", got["error"])
	}
	if errValue["Secret"] != Redacted {
		t.Errorf("Error Secret = %v, want redacted", errValue["Secret"])
	}
	for key, value := range got {
		if text, ok := value.(string); ok && text == "S3cretField" {
			t.Errorf("%s still carries the field value", key)
		}
	}
}

func TestRedactorWalksWrappedErrors(t *testing.T) {
	r := redactor{sources: []SecretsSource{NewStaticSecrets()}}
	got := r.Map(map[string]any{
		"wrapped": fmt.Errorf("auth: %w", errorCredential{Secret: "S3cretField"}),
		"joined":  errors.Join(errors.New("plain"), errorCredential{Secret: "S3cretField"}),
		"slice":   []error{errorCredential{Secret: "S3cretField"}},
	})
	if got["wrapped"] != Redacted {
		t.Errorf("wrapped error = %v, want redacted", got["wrapped"])
	}
	if got["joined"] != Redacted {
		t.Errorf("joined error = %v, want redacted", got["joined"])
	}
	slice, ok := got["slice"].([]any)
	if !ok || len(slice) != 1 {
		t.Fatalf("[]error = %#v, want one canonicalised item", got["slice"])
	}
	item, ok := slice[0].(map[string]any)
	if !ok {
		t.Fatalf("[]error item = %T, want a canonicalised struct", slice[0])
	}
	if item["Secret"] != Redacted {
		t.Errorf("[]error Secret = %v, want redacted", item["Secret"])
	}
}

func TestRedactorReadsUnexportedStructPayload(t *testing.T) {
	r := redactor{sources: []SecretsSource{NewStaticSecrets()}}
	got := r.Map(map[string]any{
		"embeddedStringer": embeddedCreds{innerCreds{Password: "S3cretField"}},
		"namedStringer":    namedCreds{inner: innerCreds{Password: "S3cretField"}},
	})

	embedded, ok := got["embeddedStringer"].(map[string]any)
	if !ok {
		t.Fatalf("embedded struct = %T, want a canonicalised struct", got["embeddedStringer"])
	}
	inner, ok := embedded["innerCreds"].(map[string]any)
	if !ok || inner["Password"] != Redacted {
		t.Errorf("embedded Password = %#v, want redacted", embedded)
	}

	named, ok := got["namedStringer"].(map[string]any)
	if !ok {
		t.Fatalf("named struct = %T, want a canonicalised struct", got["namedStringer"])
	}
	inner, ok = named["inner"].(map[string]any)
	if !ok || inner["Password"] != Redacted {
		t.Errorf("named Password = %#v, want redacted", named)
	}
}

func TestRedactorDoesNotTrustTextAroundHiddenPayloads(t *testing.T) {
	r := redactor{sources: []SecretsSource{NewStaticSecrets()}}
	got := r.Map(map[string]any{
		"pointer":  pointerHolder{inner: &innerCreds{Password: "S3cretField"}},
		"embedded": embeddedErrorHolder{errorCredential{Secret: "S3cretField"}},
		"wrapper":  plainWrapper{cause: errorCredential{Secret: "S3cretField"}},
	})
	for key, value := range got {
		if text, ok := value.(string); ok {
			t.Errorf("%s kept the text form %q instead of reflecting the payload", key, text)
		}
	}
	pointer, ok := got["pointer"].(map[string]any)
	if !ok {
		t.Fatalf("pointer holder = %T, want a canonicalised struct", got["pointer"])
	}
	inner, ok := pointer["inner"].(map[string]any)
	if !ok || inner["Password"] != Redacted {
		t.Errorf("pointer holder = %#v, want its Password redacted", pointer)
	}
	embedded, ok := got["embedded"].(map[string]any)
	if !ok {
		t.Fatalf("embedded holder = %T, want a canonicalised struct", got["embedded"])
	}
	dynamic, ok := embedded["error"].(map[string]any)
	if !ok || dynamic["Secret"] != Redacted {
		t.Errorf("embedded holder = %#v, want its Secret redacted", embedded)
	}
	wrapper, ok := got["wrapper"].(map[string]any)
	if !ok {
		t.Fatalf("wrapper = %T, want a canonicalised struct", got["wrapper"])
	}
	cause, ok := wrapper["cause"].(map[string]any)
	if !ok || cause["Secret"] != Redacted {
		t.Errorf("wrapper = %#v, want its cause's Secret redacted", wrapper)
	}
}

func TestRedactorCanonicalisesContainerBackedText(t *testing.T) {
	r := redactor{sources: []SecretsSource{NewStaticSecrets()}}
	inner := &innerCreds{Password: "S3cretField"}
	got := r.Map(map[string]any{
		"mapStringer":   mapStringer{"password": "S3cretField", "note": "kept"},
		"sliceError":    sliceError{errorCredential{Secret: "S3cretField"}},
		"doublePointer": doublePointerHolder{inner: &inner},
	})
	for key, value := range got {
		if text, ok := value.(string); ok {
			t.Errorf("%s kept the text form %q instead of canonicalising", key, text)
		}
	}

	mapped, ok := got["mapStringer"].(map[string]any)
	if !ok {
		t.Fatalf("map-backed Stringer = %T, want a canonicalised map", got["mapStringer"])
	}
	if mapped["password"] != Redacted {
		t.Errorf("map password = %v, want redacted", mapped["password"])
	}
	if mapped["note"] != "kept" {
		t.Errorf("map note = %v, want kept", mapped["note"])
	}

	sliced, ok := got["sliceError"].([]any)
	if !ok || len(sliced) != 1 {
		t.Fatalf("slice-backed error = %#v, want one canonicalised item", got["sliceError"])
	}
	item, ok := sliced[0].(map[string]any)
	if !ok || item["Secret"] != Redacted {
		t.Errorf("slice item = %#v, want its Secret redacted", sliced[0])
	}

	double, ok := got["doublePointer"].(map[string]any)
	if !ok {
		t.Fatalf("double-pointer holder = %T, want a canonicalised struct", got["doublePointer"])
	}
	if !containsRedactedSecret(double) {
		t.Errorf("double-pointer holder = %#v, want its Password redacted", double)
	}
}

func TestRedactorCanonicalisesStructMapFields(t *testing.T) {
	r := redactor{sources: []SecretsSource{NewStaticSecrets()}}
	got := r.Map(map[string]any{
		"stringKey": structMapStringer{m: map[string]string{"password": "S3cretField"}},
		"intKey":    intKeyMapStringer{m: map[int]string{1: "S3cretField"}},
	})
	for key, value := range got {
		if text, ok := value.(string); ok {
			t.Errorf("%s kept the text form %q instead of canonicalising", key, text)
		}
	}

	stringKey, ok := got["stringKey"].(map[string]any)
	if !ok {
		t.Fatalf("string-key map holder = %T, want a canonicalised struct", got["stringKey"])
	}
	inner, ok := stringKey["m"].(map[string]any)
	if !ok || inner["password"] != Redacted {
		t.Errorf("string-key map holder = %#v, want its password key redacted", stringKey)
	}

	intKey, ok := got["intKey"].(map[string]any)
	if !ok {
		t.Fatalf("int-key map holder = %T, want a canonicalised struct", got["intKey"])
	}
	if intKey["m"] != Redacted {
		t.Errorf("int-key map holder = %#v, want the non-string-keyed map redacted whole", intKey)
	}
}

func containsRedactedSecret(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if sensitiveKey(key) && item == Redacted {
				return true
			}
			if containsRedactedSecret(item) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if containsRedactedSecret(item) {
				return true
			}
		}
	}
	return false
}

func TestRedactorKeepsOpaqueStringersAsText(t *testing.T) {
	r := redactor{sources: []SecretsSource{NewStaticSecrets()}}
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	if got := r.Value("at", at); got != at.String() {
		t.Errorf("time.Time = %v, want its text form %q", got, at.String())
	}
}

// TestOpaqueFieldTypeIsPinnedByKind pins the rule at the kind level: only a
// struct that is itself opaque (time.Time) may be rendered through an owner's
// text method. An interface can hold any error or Stringer at runtime, and a
// map's keys are data rather than types, so neither is ever trusted. A slice
// or array is decided by its element type; a scalar, func or chan field holds
// no payload reflection could redact, so it is trusted.
func TestOpaqueFieldTypeIsPinnedByKind(t *testing.T) {
	type exposedStruct struct{ Password string }
	cases := []struct {
		name string
		typ  reflect.Type
		want bool
	}{
		{"map/string-key", reflect.TypeOf(map[string]string{}), false},
		{"map/int-key", reflect.TypeOf(map[int]string{}), false},
		{"slice/trusted-elem", reflect.TypeOf([]string{}), true},
		{"slice/payload-elem", reflect.TypeOf([]exposedStruct{}), false},
		{"array/trusted-elem", reflect.TypeOf([2]string{}), true},
		{"array/payload-elem", reflect.TypeOf([2]exposedStruct{}), false},
		{"struct/opaque", reflect.TypeOf(time.Time{}), true},
		{"struct/payload", reflect.TypeOf(exposedStruct{}), false},
		{"pointer/struct-opaque", reflect.TypeOf(&time.Time{}), true},
		{"pointer/struct-payload", reflect.TypeOf(&exposedStruct{}), false},
		{"pointer/scalar", reflect.TypeOf((*string)(nil)), false},
		{"interface", reflect.TypeOf((*any)(nil)).Elem(), false},
		{"func", reflect.TypeOf(func() {}), true},
		{"chan", reflect.TypeOf(make(chan string)), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := opaqueFieldType(tc.typ, 0, map[reflect.Type]bool{}); got != tc.want {
				t.Errorf("opaqueFieldType(%v) = %v, want %v", tc.typ, got, tc.want)
			}
		})
	}
}

func TestRedactorCanonicalisesDelegateValues(t *testing.T) {
	r := redactor{sources: []SecretsSource{NewStaticSecrets()}}
	got := r.Value("blob", []byte("plain bytes"))
	if got != "plain bytes" {
		t.Errorf("[]byte without a credential = %v, want the string form", got)
	}
	got = r.Value("metric", fmt.Errorf("boom"))
	if got != "boom" {
		t.Errorf("error = %v, want its message", got)
	}
}

func TestLogHandlerRedactsContainerBackedText(t *testing.T) {
	setup := newEmbeddedTestSetup(t, coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)))
	inner := &innerCreds{Password: "S3cretField"}
	setup.Logger().Info("container backed",
		slog.Any("mapStringer", mapStringer{"password": "S3cretField", "note": "kept"}),
		slog.Any("sliceError", sliceError{errorCredential{Secret: "S3cretField"}}),
		slog.Any("doublePointer", doublePointerHolder{inner: &inner}),
		slog.Any("structMap", structMapStringer{m: map[string]string{"password": "S3cretField"}}),
		slog.Any("intKeyMap", intKeyMapStringer{m: map[int]string{1: "S3cretField"}}),
	)
	rec := setup.Logs()[0]
	if containsText(rec.Attrs, "S3cretField") {
		t.Fatalf("kept log attributes still carry the hidden payload: %#v", rec.Attrs)
	}
	if mapped, ok := rec.Attrs["mapStringer"].(map[string]any); !ok || mapped["password"] != Redacted {
		t.Errorf("map-backed Stringer = %#v, want a canonicalised map", rec.Attrs["mapStringer"])
	}
	if sliced, ok := rec.Attrs["sliceError"].([]any); !ok || len(sliced) != 1 {
		t.Errorf("slice-backed error = %#v, want one canonicalised item", rec.Attrs["sliceError"])
	}
	structMap, ok := rec.Attrs["structMap"].(map[string]any)
	if !ok {
		t.Fatalf("map-field Stringer = %#v, want a canonicalised struct", rec.Attrs["structMap"])
	}
	if inner, ok := structMap["m"].(map[string]any); !ok || inner["password"] != Redacted {
		t.Errorf("map-field Stringer = %#v, want its map's password key redacted", structMap)
	}
	intKeyMap, ok := rec.Attrs["intKeyMap"].(map[string]any)
	if !ok || intKeyMap["m"] != Redacted {
		t.Errorf("int-key map holder = %#v, want the non-string-keyed map redacted whole", rec.Attrs["intKeyMap"])
	}
}

func containsText(value any, text string) bool {
	switch typed := value.(type) {
	case string:
		return typed == text || strings.Contains(typed, text)
	case map[string]any:
		for key, item := range typed {
			if strings.Contains(key, text) || containsText(item, text) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if containsText(item, text) {
				return true
			}
		}
	}
	return false
}

func TestLogHandlerUsesClockAndCorrelation(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	setup := newEmbeddedTestSetup(t, clock)
	setup.SetCredential("sk-live-7f3a9c1b")

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
