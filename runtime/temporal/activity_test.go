package temporal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestRunStepActivityUnregisteredIsCodedNonRetryable(t *testing.T) {
	rt, _ := New(Options{Payloads: testSecretsCodec(t), Blobs: testBlobStore(t)})
	_, err := rt.runStepActivity(context.Background(), stepActivityInput{Name: "nope"})
	if err == nil {
		t.Fatal("expected error")
	}
	if core.CodeOf(activityToCore(err)) != core.CodeUnsupported {
		t.Fatalf("code %v", core.CodeOf(err))
	}
	app := extractAppFailure(err)
	if app == nil || !app.GetNonRetryable() {
		t.Fatal("should be non-retryable")
	}
}

func TestRunStepActivityLogsPlainError(t *testing.T) {
	var buf bytes.Buffer
	rt, _ := New(Options{
		Payloads: testSecretsCodec(t),
		Blobs:    testBlobStore(t),
		Logger:   slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	_ = rt.RegisterStep("x", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return nil, errors.New("plain-secret")
	}, runtime.StepDefaults{})
	_, err := rt.runStepActivity(context.Background(), stepActivityInput{Name: "x"})
	if err == nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "plain-secret") {
		t.Fatalf("log %q", buf.String())
	}
}
