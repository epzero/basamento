package temporal

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestLogStepFailureIncludesPlainErrorText(t *testing.T) {
	var buf bytes.Buffer
	rt := &Runtime{logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))}
	rt.logStepFailure("acme", "wf-1", "s", errors.New("secret-token"))
	if !strings.Contains(buf.String(), "secret-token") {
		t.Fatalf("log %q", buf.String())
	}
}

func TestLogStepFailureSkipsCoreError(t *testing.T) {
	var buf bytes.Buffer
	rt := &Runtime{logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))}
	rt.logStepFailure("acme", "wf-1", "s", &core.Error{Code: "vendor_error", Message: "visible", Retryable: true})
	if buf.Len() != 0 {
		t.Fatalf("unexpected log %q", buf.String())
	}
}

func TestTruncateLogTextBoundsLongErrors(t *testing.T) {
	long := strings.Repeat("x", maxPanicValue+10)
	got := truncateLogTextRunes(long)
	if len([]rune(got)) > maxPanicValue+20 {
		t.Fatal("not truncated")
	}
	if !strings.Contains(got, "truncated") {
		t.Fatal("missing truncation marker")
	}
}
