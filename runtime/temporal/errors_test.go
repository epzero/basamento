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

func TestWrapWorkflowErrorCoreCases(t *testing.T) {
	var r Runtime
	peNr := &core.Error{Code: "invalid_argument", Message: "bad", Retryable: false}
	errNr := r.wrapWorkflowError("acme", "r1", peNr)
	if app := extractAppFailure(errNr); app == nil || app.GetType() != "invalid_argument" || !app.GetNonRetryable() {
		t.Fatal(errNr)
	}
	if fcFailure(errNr).GetCause() != nil {
		t.Fatal("cause on non-retryable")
	}
	peR := &core.Error{Code: "vendor_error", Message: "retry", Retryable: true}
	errR := r.wrapWorkflowError("acme", "r1", peR)
	if app := extractAppFailure(errR); app == nil || app.GetType() != "vendor_error" || app.GetNonRetryable() {
		t.Fatal(errR)
	}
}

func TestActivityToCoreRoundTrip(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		code      core.Code
		message   string
		retryable bool
	}{
		{
			name:      "core_retryable",
			err:       activityApplicationError(&core.Error{Code: "vendor_error", Message: "retry me", Retryable: true}),
			code:      "vendor_error",
			message:   "retry me",
			retryable: true,
		},
		{
			name:      "core_non_retryable",
			err:       activityApplicationError(&core.Error{Code: "invalid_argument", Message: "nope", Retryable: false}),
			code:      core.CodeInvalidArgument,
			message:   "nope",
			retryable: false,
		},
		{
			name:      "plain",
			err:       activityApplicationError(errors.New("plain-secret")),
			code:      core.CodeInternal,
			message:   "temporal: step failed",
			retryable: false,
		},
		{
			name:      "unsupported",
			err:       activityApplicationError(core.NewError(core.CodeUnsupported, "no step", nil)),
			code:      core.CodeUnsupported,
			message:   "no step",
			retryable: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := activityToCore(tc.err)
			var pe *core.Error
			if !errors.As(got, &pe) {
				t.Fatalf("got %T", got)
			}
			if pe.Code != tc.code || pe.Message != tc.message {
				t.Fatalf("got code=%q msg=%q", pe.Code, pe.Message)
			}
			if core.IsRetryable(pe) != tc.retryable {
				t.Fatalf("retryable=%v want %v", core.IsRetryable(pe), tc.retryable)
			}
		})
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
