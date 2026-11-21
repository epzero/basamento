package local

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestNewRejectsClockWithoutAfter(t *testing.T) {
	_, err := New(Options{Clock: clockNowOnly{}})
	if codeOf(err) != "invalid_argument" {
		t.Fatalf("New = %v, want invalid_argument", err)
	}
	var platform *core.Error
	if !errors.As(err, &platform) {
		t.Fatalf("New = %v, want *core.Error", err)
	}
	if !strings.Contains(platform.Message, "cannot schedule timers") {
		t.Fatalf("message %q, want substring %q", platform.Message, "cannot schedule timers")
	}
}

func TestCapabilities(t *testing.T) {
	rt, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	caps := rt.Capabilities()
	want := runtime.Caps{SyncGateUpdates: true}
	if caps != want {
		t.Fatalf("caps %+v, want %+v", caps, want)
	}
}

func TestErrorsArePlatformErrors(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	_, err := f.rt.Describe(tenantCtx("acme"), runtime.RunRef{RunID: "missing"})
	var platform *core.Error
	if !errors.As(err, &platform) || platform.Code != core.CodeNotFound {
		t.Fatalf("Describe missing = %v, want a not_found *core.Error", err)
	}
	if msg := platform.Error(); !strings.Contains(msg, "missing") {
		t.Fatalf("error %q does not name the run", msg)
	}
}
