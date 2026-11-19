package browsertest

import (
	"context"
	"testing"

	"github.com/epzero/basamento/browser/conformance"
)

func startedSession(t *testing.T) (*Runner, conformance.Handle) {
	t.Helper()
	app, err := conformance.Fixture(conformance.AppTodo)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	feature, err := conformance.Feature("todo/add-item.feature")
	if err != nil {
		t.Fatal(err)
	}
	runner := New()
	handle, err := runner.Start(context.Background(), conformance.StartRequest{
		Spec: conformance.SessionSpec{
			Mode:    conformance.ModeScripted,
			Target:  conformance.Target{BaseURL: app.URL()},
			Feature: feature,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner, handle
}

func TestStreamEventsResumesAfterASequenceNumber(t *testing.T) {
	runner, handle := startedSession(t)
	ctx := context.Background()

	all, err := runner.StreamEvents(ctx, handle, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 3 {
		t.Fatalf("events = %d, want navigation, step and finish events", len(all))
	}
	rest, err := runner.StreamEvents(ctx, handle, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != len(all)-1 {
		t.Fatalf("resumed events = %d, want %d", len(rest), len(all)-1)
	}
	if rest[0].Seq != 2 {
		t.Fatalf("first resumed seq = %d, want 2", rest[0].Seq)
	}
}

func TestUnknownSessionIsRefused(t *testing.T) {
	runner := New()
	ctx := context.Background()
	if _, err := runner.GetResult(ctx, conformance.Handle{ID: "nope"}); err == nil {
		t.Fatal("GetResult accepted an unknown session")
	}
	if err := runner.Cancel(ctx, conformance.Handle{ID: "nope"}); err == nil {
		t.Fatal("Cancel accepted an unknown session")
	}
	if _, err := runner.OpenArtifact(ctx, conformance.Handle{ID: "nope"}, "k"); err == nil {
		t.Fatal("OpenArtifact accepted an unknown session")
	}
}

func TestStartRequiresABaseURL(t *testing.T) {
	runner := New()
	if _, err := runner.Start(context.Background(), conformance.StartRequest{
		Spec: conformance.SessionSpec{Mode: conformance.ModeScripted, Feature: "Feature: x"},
	}); err == nil {
		t.Fatal("Start accepted a session without a target")
	}
}

func TestNavigationFailureEndsTheSession(t *testing.T) {
	app, err := conformance.Fixture(conformance.AppTodo)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	base := app.URL()
	app.Close()

	runner := New()
	handle, err := runner.Start(context.Background(), conformance.StartRequest{
		Spec: conformance.SessionSpec{
			Mode:   conformance.ModeScripted,
			Target: conformance.Target{BaseURL: base},
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	result, err := runner.GetResult(context.Background(), handle)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != conformance.StateFailed || result.Reason != conformance.ReasonError {
		t.Fatalf("result = %+v, want failed/error", result)
	}
}
