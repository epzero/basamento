package bus

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/nats-io/nats.go"

	"github.com/epzero/basamento/core"
)

func TestMapConnErrNoCredentialLeak(t *testing.T) {
	raw := "nats://secret:token@127.0.0.1:4222"
	_, err := nats.Connect(raw, nats.Timeout(1))
	mapped := mapConnErr(err)
	if mapped == nil {
		t.Fatal("expected error")
	}
	if mapped.Error() != "" {
		_ = mapped
	}
	var ce *core.Error
	if !errors.As(mapped, &ce) {
		t.Fatal("not core error")
	}
	if ce.Details == nil {
		return
	}
	for _, v := range ce.Details {
		if s, ok := v.(string); ok {
			if containsAny(s, "secret", "token", raw) {
				t.Fatalf("leaked credential in detail %q", s)
			}
		}
	}
	if containsAny(mapped.Error(), "secret", "token") {
		t.Fatal("leaked in message")
	}
	_ = url.Parse
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
