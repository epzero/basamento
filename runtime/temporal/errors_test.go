package temporal

import (
	"errors"
	"strings"
	"testing"

	"go.temporal.io/sdk/temporal"

	"github.com/epzero/basamento/core"
	commonpb "go.temporal.io/api/failure/v1"
)

func failureChainText(err error) string {
	fc := temporal.GetDefaultFailureConverter()
	f := fc.ErrorToFailure(err)
	return collectFailureText(f)
}

func collectFailureText(f *commonpb.Failure) string {
	if f == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(f.GetMessage())
	for c := f.GetCause(); c != nil; c = c.GetCause() {
		b.WriteByte('|')
		b.WriteString(c.GetMessage())
	}
	return b.String()
}

func TestActivityApplicationErrorOmitsCausePlain(t *testing.T) {
	err := activityApplicationError(errors.New("secret-token"))
	text := failureChainText(err)
	if strings.Contains(text, "secret-token") {
		t.Fatalf("failure chain leaks secret: %q", text)
	}
	app := extractAppFailure(err)
	if app == nil || app.GetType() != string(core.CodeInternal) || !app.GetNonRetryable() {
		t.Fatalf("app failure %+v", app)
	}
}

func TestActivityApplicationErrorCoreNonRetryable(t *testing.T) {
	pe := &core.Error{Code: "invalid_argument", Message: "bad", Retryable: false, Details: map[string]any{"marker": "hide-me"}}
	err := activityApplicationError(pe)
	text := failureChainText(err)
	if strings.Contains(text, "hide-me") {
		t.Fatal("details leaked")
	}
	app := extractAppFailure(err)
	if app == nil || app.GetType() != "invalid_argument" || !app.GetNonRetryable() {
		t.Fatal(app)
	}
	if f := fcFailure(err); f.GetCause() != nil {
		t.Fatal("nested cause present")
	}
}

func TestActivityApplicationErrorCoreRetryable(t *testing.T) {
	pe := &core.Error{Code: "vendor_error", Message: "retry", Retryable: true}
	err := activityApplicationError(pe)
	app := extractAppFailure(err)
	if app == nil || app.GetType() != "vendor_error" || app.GetNonRetryable() {
		t.Fatal(app)
	}
}

func TestWrapWorkflowErrorOmitsPlainCause(t *testing.T) {
	var r Runtime
	err := r.wrapWorkflowError("acme", "r1", errors.New("secret-flow"))
	text := failureChainText(err)
	if strings.Contains(text, "secret-flow") {
		t.Fatalf("leak %q", text)
	}
}

func fcFailure(err error) *commonpb.Failure {
	return temporal.GetDefaultFailureConverter().ErrorToFailure(err)
}

func extractAppFailure(err error) *commonpb.ApplicationFailureInfo {
	f := fcFailure(err)
	if f == nil {
		return nil
	}
	return f.GetApplicationFailureInfo()
}
