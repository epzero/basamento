package conformance

import (
	"context"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

// Run checks one launcher implementation. new returns a fresh launcher for
// each subtest.
func Run(t *testing.T, newLauncher func(t *testing.T) platform.Launcher) {
	t.Helper()
	t.Run("accepts a batch with one token per worker", func(t *testing.T) {
		l := newLauncher(t)
		res, err := l.Launch(context.Background(), request(2))
		if err != nil {
			t.Fatalf("launch: %v", err)
		}
		if !res.Accepted {
			t.Fatal("the launcher refused a valid batch")
		}
	})
	t.Run("needs an idempotency key", func(t *testing.T) {
		l := newLauncher(t)
		req := request(1)
		req.IdempotencyKey = ""
		assertCode(t, l, req, core.CodeInvalidArgument)
	})
	t.Run("needs one token per worker", func(t *testing.T) {
		l := newLauncher(t)
		req := request(2)
		req.JoinTokens = req.JoinTokens[:1]
		assertCode(t, l, req, core.CodeInvalidArgument)
	})
	t.Run("needs a positive count", func(t *testing.T) {
		l := newLauncher(t)
		req := request(1)
		req.Count = 0
		req.JoinTokens = nil
		assertCode(t, l, req, core.CodeInvalidArgument)
	})
	t.Run("needs a template and a pool", func(t *testing.T) {
		l := newLauncher(t)
		req := request(1)
		req.Template = ""
		assertCode(t, l, req, core.CodeInvalidArgument)
	})
}

func request(n int) platform.LaunchRequest {
	tokens := make([]string, n)
	for i := range tokens {
		tokens[i] = "join-token-" + string(rune('a'+i))
	}
	return platform.LaunchRequest{
		Pool:           "pool",
		Template:       "template",
		Count:          n,
		JoinTokens:     tokens,
		IdempotencyKey: "launch-1",
	}
}

func assertCode(t *testing.T, l platform.Launcher, req platform.LaunchRequest, want core.Code) {
	t.Helper()
	_, err := l.Launch(context.Background(), req)
	if err == nil {
		t.Fatalf("launch succeeded, want code %s", want)
	}
	if got := core.CodeOf(err); got != want {
		t.Fatalf("got code %s, want %s: %v", got, want, err)
	}
}
