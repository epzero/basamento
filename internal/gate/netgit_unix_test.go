//go:build unix && !aix

package gate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestGitPipeHolderProcess is a helper, not a test: the fake git below starts
// it. It leaves git's process group so the kill at the deadline misses it, and
// then holds the stdout and stderr pipes inherited from git, the way
// git-remote-https or ssh would.
func TestGitPipeHolderProcess(t *testing.T) {
	if os.Getenv("GATE_TEST_PIPE_HOLDER") != "1" {
		return
	}
	_, err := syscall.Setsid()
	if mark := os.Getenv("GATE_TEST_PIPE_MARK"); mark != "" {
		body := "setsid ok"
		if err != nil {
			body = "setsid failed: " + err.Error()
		}
		_ = os.WriteFile(mark, []byte(body), 0o600)
	}
	time.Sleep(5 * time.Second)
	os.Exit(0)
}

// TestRemoteGitTimeoutBoundsTheWallClock proves the deadline bounds the call,
// not only its message: git is killed at the deadline, and a transport helper
// that survived the kill and still holds the output pipes cannot extend the
// wait past gitWaitDelay.
func TestRemoteGitTimeoutBoundsTheWallClock(t *testing.T) {
	bin := t.TempDir()
	mark := filepath.Join(t.TempDir(), "holder")
	started := mark + ".script"
	// The warmup branch execs this exact file once before the timed call, so
	// the clock starts after the OS has the script cached; otherwise a cold
	// first exec on macOS can take longer than the timeout under test.
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = warmup ]; then exit 0; fi\necho started > %q\n%q -test.run='^TestGitPipeHolderProcess$' &\nwait\n",
		started, os.Args[0])
	gitPath := filepath.Join(bin, "git")
	if err := os.WriteFile(gitPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GATE_TEST_PIPE_HOLDER", "1")
	t.Setenv("GATE_TEST_PIPE_MARK", mark)
	if out, err := exec.Command(gitPath, "warmup").CombinedOutput(); err != nil {
		t.Fatalf("warming up the fake git: %v: %s", err, out)
	}

	const timeout, waitDelay = 200 * time.Millisecond, 300 * time.Millisecond
	start := time.Now()
	_, err := gitRemoteWithin(context.Background(), t.TempDir(), timeout, waitDelay, "fetch", "origin")
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "timed out after") {
		t.Fatalf("a remote call whose helper holds the pipes must time out: %v", err)
	}
	for _, want := range []string{"git remote -v", "network", "credentials"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the timeout error must say what to check (%q): %v", want, err)
		}
	}
	// The helper must really have survived the kill and kept the pipes open,
	// or the test would only be timing a plain kill.
	if b, err := os.ReadFile(mark); err != nil || string(b) != "setsid ok" {
		log, _ := os.ReadFile(started)
		t.Fatalf("the pipe holder did not leave git's process group (%q, %v); script log %q", b, err, log)
	}
	// A real clock is the subject here: the bound is what is under test. The
	// slack covers process start and the race detector; the helper would hold
	// the pipes for 5s without waitDelay.
	if limit := timeout + waitDelay + 2*time.Second; elapsed > limit {
		t.Fatalf("the call took %s; want it bounded by %s (git killed at the deadline, pipes closed after %s)", elapsed, limit, waitDelay)
	}
}
