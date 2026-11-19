//go:build unix && !solaris && !aix

package gate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestOriginHasBranchKeepsTheLSRemoteExitCode proves the bounded helper still
// lets originHasBranch tell "no such ref" (exit 2) from a reachability error:
// the *exec.ExitError must survive the wrapping gitRemote adds.
func TestOriginHasBranchKeepsTheLSRemoteExitCode(t *testing.T) {
	bin := t.TempDir()
	gitPath := filepath.Join(bin, "git")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cases := []struct {
		name    string
		script  string
		want    bool
		wantErr string
	}{
		{"present", "#!/bin/sh\necho 0123456789012345678901234567890123456789\trefs/heads/topic\n", true, ""},
		{"absent", "#!/bin/sh\nexit 2\n", false, ""},
		{"unreachable", "#!/bin/sh\necho 'fatal: unable to access' >&2\nexit 128\n", false, "cannot reach origin"},
	}
	for _, c := range cases {
		if err := os.WriteFile(gitPath, []byte(c.script), 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := originHasBranch(t.TempDir(), "topic")
		if c.wantErr == "" {
			if err != nil || got != c.want {
				t.Fatalf("%s: originHasBranch = %v, %v; want %v, nil", c.name, got, err, c.want)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Fatalf("%s: want a reachability error, got %v", c.name, err)
		}
		for _, want := range []string{"git remote get-url origin", "network", "credentials"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the error must say what to check (%q): %v", c.name, want, err)
			}
		}
	}
}

// TestGitPipeHolderProcess is a helper, not a test: the fake git below starts
// it. It leaves git's process group so the kill at the deadline misses it, and
// then holds the stdout and stderr pipes inherited from git, the way
// git-remote-https or ssh would, until the test releases it through
// GATE_TEST_PIPE_RELEASE. The test keeps an O_RDWR descriptor on each fifo so
// when the test process exits the kernel closes those ends and this process's
// blocking read on release returns EOF instead of leaking forever.
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
	if pidPath := os.Getenv("GATE_TEST_PIPE_PID"); pidPath != "" {
		_ = os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
	readyPath := os.Getenv("GATE_TEST_PIPE_READY")
	donePath := os.Getenv("GATE_TEST_PIPE_DONE")
	releasePath := os.Getenv("GATE_TEST_PIPE_RELEASE")
	if readyPath == "" || donePath == "" || releasePath == "" {
		os.Exit(1)
	}
	signalDone := func(code int) {
		if done, err := os.OpenFile(donePath, os.O_RDWR, 0); err == nil {
			_, _ = done.Write([]byte{0})
			_ = done.Close()
		}
		os.Exit(code)
	}
	ready, err := os.OpenFile(readyPath, os.O_RDWR, 0)
	if err != nil {
		signalDone(1)
	}
	if _, err := ready.Write([]byte{1}); err != nil {
		_ = ready.Close()
		signalDone(1)
	}
	_ = ready.Close()
	release, err := os.OpenFile(releasePath, os.O_RDONLY, 0)
	if err != nil {
		signalDone(1)
	}
	if _, err := release.Read(make([]byte, 1)); err != nil && !errors.Is(err, io.EOF) {
		_ = release.Close()
		signalDone(1)
	}
	_ = release.Close()
	signalDone(0)
}

// TestRemoteGitCancelBoundsCallDespitePipeHolder proves a cancel (or deadline)
// bounds the call while a transport helper that survived the kill still holds
// the output pipes: the test cancels a parent context only after the helper
// blocks on the release fifo, so helper start time does not affect pass/fail.
func TestRemoteGitCancelBoundsCallDespitePipeHolder(t *testing.T) {
	bin := t.TempDir()
	repoDir := t.TempDir()
	mark := filepath.Join(t.TempDir(), "holder")
	holderPID := mark + ".pid"
	started := mark + ".script"
	readyFIFO := filepath.Join(t.TempDir(), "holder-ready.fifo")
	releaseFIFO := filepath.Join(t.TempDir(), "holder-release.fifo")
	doneFIFO := filepath.Join(t.TempDir(), "holder-done.fifo")
	for path, mode := range map[string]uint32{
		readyFIFO:   0o600,
		releaseFIFO: 0o600,
		doneFIFO:    0o600,
	} {
		if err := syscall.Mkfifo(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	readyFD, err := os.OpenFile(readyFIFO, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("opening holder-ready fifo: %v", err)
	}
	releaseFD, err := os.OpenFile(releaseFIFO, os.O_RDWR, 0)
	if err != nil {
		_ = readyFD.Close()
		t.Fatalf("opening holder-release fifo: %v", err)
	}
	doneFD, err := os.OpenFile(doneFIFO, os.O_RDWR, 0)
	if err != nil {
		_ = readyFD.Close()
		_ = releaseFD.Close()
		t.Fatalf("opening holder-done fifo: %v", err)
	}
	t.Cleanup(func() {
		_ = readyFD.Close()
		_ = releaseFD.Close()
		_ = doneFD.Close()
	})

	script := fmt.Sprintf("#!/bin/sh\necho started > %q\n%q -test.run='^TestGitPipeHolderProcess$' &\nwait\n",
		started, os.Args[0])
	gitPath := filepath.Join(bin, "git")
	if err := os.WriteFile(gitPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GATE_TEST_PIPE_HOLDER", "1")
	t.Setenv("GATE_TEST_PIPE_MARK", mark)
	t.Setenv("GATE_TEST_PIPE_PID", holderPID)
	t.Setenv("GATE_TEST_PIPE_READY", readyFIFO)
	t.Setenv("GATE_TEST_PIPE_RELEASE", releaseFIFO)
	t.Setenv("GATE_TEST_PIPE_DONE", doneFIFO)

	var releaseErr error
	var releaseOnce sync.Once
	releaseHolder := func() error {
		releaseOnce.Do(func() {
			if _, err := releaseFD.Write([]byte{0}); err != nil {
				releaseErr = fmt.Errorf("releasing the pipe holder: %w", err)
			}
		})
		return releaseErr
	}
	var waitOnce sync.Once
	var waitErr error
	waitHolderExited := func(fatal bool) {
		t.Helper()
		waitOnce.Do(func() {
			// The holder defers a done-byte write on every path it controls; if it
			// is killed from outside, this read blocks until go test's own timeout,
			// which is the accepted failure mode for that case.
			if _, err := doneFD.Read(make([]byte, 1)); err != nil && !errors.Is(err, io.EOF) {
				waitErr = fmt.Errorf("reading holder-done fifo: %w", err)
				if fatal {
					t.Fatal(waitErr)
				}
				t.Error(waitErr)
			}
		})
	}
	t.Cleanup(func() {
		if _, err := os.Stat(mark); err != nil {
			return
		}
		pidBytes, err := os.ReadFile(holderPID)
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
		if err != nil {
			return
		}
		proc, err := os.FindProcess(pid)
		if err != nil || proc.Signal(syscall.Signal(0)) != nil {
			return
		}
		if err := releaseHolder(); err != nil {
			t.Error(err)
		}
		waitHolderExited(false)
	})

	const waitDelay = 300 * time.Millisecond
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var remoteErr error
	go func() {
		// A long child timeout ensures the only bound is the cancel the test
		// fires after the helper has signalled readiness.
		_, remoteErr = gitRemoteWithin(parent, repoDir, time.Hour, waitDelay, "fetch", "origin")
		close(done)
	}()

	readyCh := make(chan error, 1)
	go func() {
		_, err := readyFD.Read(make([]byte, 1))
		readyCh <- err
	}()

	select {
	case err := <-readyCh:
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("reading holder-ready fifo: %v", err)
		}
	case <-done:
		if remoteErr != nil {
			t.Fatalf("gitRemoteWithin failed before the holder was ready: %v", remoteErr)
		}
		t.Fatal("gitRemoteWithin finished before the holder signalled readiness")
	}

	cancel()
	<-done

	if remoteErr == nil || !errors.Is(remoteErr, context.Canceled) {
		t.Fatalf("a cancelled remote call whose helper holds the pipes must report cancel: %v", remoteErr)
	}
	if b, err := os.ReadFile(mark); err != nil || string(b) != "setsid ok" {
		log, _ := os.ReadFile(started)
		t.Fatalf("the pipe holder did not leave git's process group (%q, %v); script log %q", b, err, log)
	}
	pidBytes, err := os.ReadFile(holderPID)
	if err != nil {
		t.Fatalf("the pipe holder never recorded its pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatalf("reading the pipe holder pid: %v", err)
	}
	if proc, err := os.FindProcess(pid); err != nil || proc.Signal(syscall.Signal(0)) != nil {
		t.Fatalf("the pipe holder must still be running while gitRemoteWithin returned (pid %d)", pid)
	}

	if err := releaseHolder(); err != nil {
		t.Fatal(err)
	}
	waitHolderExited(true)
}

// TestRemoteGitDeadlineErrorBeforeExec proves the deadline branch's message
// without waiting on the wall clock or on a pipe-holding helper.
func TestRemoteGitDeadlineErrorBeforeExec(t *testing.T) {
	bin := t.TempDir()
	ranMarker := filepath.Join(t.TempDir(), "git-ran")
	gitPath := filepath.Join(bin, "git")
	if err := os.WriteFile(gitPath, []byte(fmt.Sprintf("#!/bin/sh\necho ran > %q\n", ranMarker)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	past, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	_, err := gitRemoteWithin(past, t.TempDir(), time.Hour, time.Millisecond, "fetch", "origin")
	if err == nil || !strings.Contains(err.Error(), "timed out after") {
		t.Fatalf("want the deadline error before exec starts: %v", err)
	}
	for _, want := range []string{"git remote -v", "network", "credentials"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the timeout error must say what to check (%q): %v", want, err)
		}
	}
	if _, err := os.Stat(ranMarker); err == nil {
		t.Fatalf("the fake git must not run when the context is already expired")
	}
}
