package steps

import (
	"context"
	"testing"

	"github.com/epzero/basamento/tool"
)

// TestSandboxCommandViewSeesNoHostSurface proves the simulated sandbox's
// command view is the surface it actually exposes, not its own spec: a driver
// that leaks the host's environment or files is observable and fails the
// scenario's assertions.
func TestSandboxCommandViewSeesNoHostSurface(t *testing.T) {
	sb := &bddSandbox{
		answers:   map[string]bddSandboxAnswer{},
		hostEnv:   map[string]string{"HOST_SECRET": "top-secret"},
		hostFiles: map[string]bool{"/home/kit/vault.db": true},
		startCh:   make(chan struct{}),
		stopCh:    make(chan struct{}),
	}
	if _, err := sb.Run(context.Background(), tool.SandboxSpec{}, []string{"env"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := sb.seenEnv["HOST_SECRET"]; ok {
		t.Fatal("the command saw a host environment variable")
	}
	if sb.seenFiles["/home/kit/vault.db"] {
		t.Fatal("the command saw a host file")
	}

	sb.leakHost = true
	if _, err := sb.Run(context.Background(), tool.SandboxSpec{}, []string{"env"}); err != nil {
		t.Fatalf("Run with a leaking driver: %v", err)
	}
	if _, ok := sb.seenEnv["HOST_SECRET"]; !ok {
		t.Fatal("a leaking driver is not observable")
	}
	if !sb.seenFiles["/home/kit/vault.db"] {
		t.Fatal("a leaking driver's host file is not observable")
	}
}
