package bus

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/epzero/basamento/core"
)

func TestMapConnErrNoCredentialLeak(t *testing.T) {
	raw := "nats://secret:token@evil.example:4222"
	wrapped := fmt.Errorf("dial failed: %s", raw)
	mapped := mapConnErr(wrapped)
	if mapped == nil {
		t.Fatal("expected error")
	}
	if containsAny(mapped.Error(), "secret", "token", raw, "evil.example") {
		t.Fatalf("leaked in message: %v", mapped)
	}
	var ce *core.Error
	if errors.As(mapped, &ce) && ce.Details != nil {
		for _, v := range ce.Details {
			if s, ok := v.(string); ok && containsAny(s, "secret", "token", raw) {
				t.Fatalf("leaked in detail %q", s)
			}
		}
	}
}

func TestMapConnErrJetStream10054(t *testing.T) {
	mapped := mapConnErr(&jetstream.APIError{ErrorCode: jetstream.ErrorCode(jsAPIStreamMessageExceedsMaximum)})
	if string(core.CodeOf(mapped)) != string(core.CodeInvalidArgument) {
		t.Fatalf("got %s", core.CodeOf(mapped))
	}
}

func TestOpenCloudPlaintextCredentialPolicy(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		raw    string
		reject bool
	}{
		{"nats://user:pass@127.0.0.1:4222", false},
		{"nats://user:pass@[::1]:4222", false},
		{"nats://user:pass@localhost:4222", false},
		{"nats://user:pass@10.0.0.1:4222", true},
		{"nats://user:pass@remote.example:4222", true},
		{"tls://user:pass@remote.example:4222", false},
	}
	for _, tc := range cases {
		_, err := OpenCloud(ctx, tc.raw, Options{})
		if tc.reject {
			if err == nil || string(core.CodeOf(err)) != string(core.CodeInvalidArgument) {
				t.Fatalf("%q: got %v", tc.raw, err)
			}
			if containsAny(err.Error(), "user", "pass", tc.raw) {
				t.Fatalf("leaked url: %v", err)
			}
		} else if tc.raw == "tls://user:pass@remote.example:4222" {
			continue
		} else if err != nil && string(core.CodeOf(err)) == string(core.CodeInvalidArgument) && err.Error() == plaintextCredential().Error() {
			t.Fatalf("unexpected reject %q: %v", tc.raw, err)
		}
	}
}

func TestInvalidURLMapping(t *testing.T) {
	_, err := OpenCloud(context.TODO(), "ftp://host", Options{})
	if err == nil {
		t.Fatal("expected error")
	}
	if string(core.CodeOf(err)) != "invalid_argument" {
		t.Fatalf("got %s", core.CodeOf(err))
	}
	if containsAny(err.Error(), "ftp://") {
		t.Fatal("echoes url")
	}
}

func TestOpenCloudRejectsBadSchemes(t *testing.T) {
	ctx := context.Background()
	for _, raw := range []string{
		"nats+tls://127.0.0.1:4222",
		"http://127.0.0.1:4222",
		"ftp://user:secret@127.0.0.1:4222",
	} {
		_, err := OpenCloud(ctx, raw, Options{})
		if err == nil {
			t.Fatalf("want error for %q", raw)
		}
		if string(core.CodeOf(err)) != string(core.CodeInvalidArgument) {
			t.Fatalf("%q code %s", raw, core.CodeOf(err))
		}
		if containsAny(err.Error(), "secret", "user", "4222", "nats+tls", "http://") {
			t.Fatalf("leaked url in %v", err)
		}
	}
}

func TestMapConnErrContext(t *testing.T) {
	if string(core.CodeOf(mapConnErr(context.Canceled))) != string(core.CodeCancelled) {
		t.Fatal("cancelled")
	}
	if string(core.CodeOf(mapConnErr(context.DeadlineExceeded))) != string(core.CodeTimeout) {
		t.Fatal("timeout")
	}
}

func TestMapConnErrInvalidKey(t *testing.T) {
	if string(core.CodeOf(mapConnErr(jetstream.ErrInvalidKey))) != string(core.CodeInvalidArgument) {
		t.Fatal("invalid key")
	}
	if string(core.CodeOf(mapConnErr(nats.ErrBadSubject))) != string(core.CodeInvalidArgument) {
		t.Fatal("bad subject")
	}
}

func TestOversizePublishInvalidArgument(t *testing.T) {
	st, _, _ := openTestEmbedded(t)
	ctx := context.Background()
	_, err := st.Publish(ctx, "run.r1.deltas", make([]byte, DefaultMaxMessageSize+1))
	if err == nil {
		t.Fatal("expected error")
	}
	if string(core.CodeOf(err)) != string(core.CodeInvalidArgument) {
		t.Fatalf("got %s", core.CodeOf(err))
	}
	if containsAny(err.Error(), "nats", "jetstream", "maximum payload") {
		t.Fatalf("leaked vendor text: %v", err)
	}
}

func containsAny(s string, parts ...string) bool {
	for _, p := range parts {
		if len(p) > 0 && (s == p || len(s) > len(p) && (contains(s, p))) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	return len(sub) <= len(s) && (s == sub || len(s) > 0 && stringIndex(s, sub) >= 0)
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
