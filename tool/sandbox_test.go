package tool_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/tool"
)

// scriptedSandbox is a deterministic driver: it answers the command it was
// told to answer and records exactly what the layer gave it.
type scriptedSandbox struct {
	mu          sync.Mutex
	spec        tool.SandboxSpec
	command     []string
	runs        int
	answer      tool.SandboxResult
	err         error
	panic       bool
	block       bool
	ignoreCtx   bool
	release     chan struct{}
	started     chan struct{}
	stopped     chan struct{}
	startedOnce sync.Once
	stoppedOnce sync.Once
	onRun       func()
}

func (s *scriptedSandbox) Run(ctx context.Context, spec tool.SandboxSpec, command []string) (tool.SandboxResult, error) {
	s.mu.Lock()
	s.spec = spec
	s.command = command
	s.runs++
	s.mu.Unlock()
	if s.onRun != nil {
		s.onRun()
	}
	if s.started != nil {
		s.startedOnce.Do(func() { close(s.started) })
	}
	if s.panic {
		panic("driver broke")
	}
	if s.err != nil {
		return tool.SandboxResult{}, s.err
	}
	if s.ignoreCtx {
		<-s.release
		return tool.SandboxResult{}, nil
	}
	if s.block {
		<-ctx.Done()
		if s.stopped != nil {
			s.stoppedOnce.Do(func() { close(s.stopped) })
		}
		return tool.SandboxResult{}, ctx.Err()
	}
	return s.answer, nil
}

func TestRunSandboxKeepsTheFirstBytesAndMarksTruncated(t *testing.T) {
	sb := &scriptedSandbox{answer: tool.SandboxResult{Stdout: bytes.Repeat([]byte("x"), 3000)}}
	res, err := layerWith(fixedClock(), newMemBlob(), nil).RunSandbox(context.Background(), sb,
		tool.SandboxSpec{MaxOutput: 1024}, []string{"seq", "1", "2000"})
	if err != nil {
		t.Fatalf("RunSandbox: %v", err)
	}
	var output string
	if err := json.Unmarshal(res.Content, &output); err != nil {
		t.Fatalf("content %s: %v", res.Content, err)
	}
	if len(output) != 1024 {
		t.Fatalf("output has %d bytes, want 1024", len(output))
	}
	if !res.Truncated {
		t.Fatal("output is not marked truncated")
	}
}

func TestRunSandboxReturnsNonZeroExitAsAResult(t *testing.T) {
	sb := &scriptedSandbox{answer: tool.SandboxResult{ExitStatus: 1, Stdout: []byte("FAIL example")}}
	res, err := layerWith(fixedClock(), newMemBlob(), nil).RunSandbox(context.Background(), sb,
		tool.SandboxSpec{}, []string{"go", "test"})
	if err != nil {
		t.Fatalf("RunSandbox: %v", err)
	}
	var output string
	_ = json.Unmarshal(res.Content, &output)
	if res.ExitStatus != 1 || output != "FAIL example" {
		t.Fatalf("result %+v output %q", res, output)
	}
}

func TestRunSandboxStopsAtItsTimeLimit(t *testing.T) {
	clock := fixedClock()
	sb := &scriptedSandbox{block: true, started: make(chan struct{}), stopped: make(chan struct{})}
	type done struct {
		res tool.Result
		err error
	}
	result := make(chan done, 1)
	go func() {
		res, err := layerWith(clock, newMemBlob(), nil).RunSandbox(context.Background(), sb,
			tool.SandboxSpec{TimeLimit: 60 * time.Second}, []string{"make", "test"})
		result <- done{res, err}
	}()
	<-sb.started
	clock.Advance(60 * time.Second)
	got := <-result
	if got.err == nil || core.CodeOf(got.err) != core.CodeTimeout {
		t.Fatalf("error %v, want timeout", got.err)
	}
	<-sb.stopped
}

func TestRunSandboxMapsADriverFailureToInternal(t *testing.T) {
	sb := &scriptedSandbox{err: errors.New("no runtime")}
	_, err := layerWith(fixedClock(), newMemBlob(), nil).RunSandbox(context.Background(), sb,
		tool.SandboxSpec{}, []string{"go", "test"})
	if err == nil || core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("error %v, want internal", err)
	}
	if sb.runs != 1 {
		t.Fatalf("driver ran %d times", sb.runs)
	}
}

func TestRunSandboxRecoversAPanic(t *testing.T) {
	sb := &scriptedSandbox{panic: true}
	_, err := layerWith(fixedClock(), newMemBlob(), nil).RunSandbox(context.Background(), sb,
		tool.SandboxSpec{}, []string{"go", "test"})
	if err == nil || core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("error %v, want internal", err)
	}
}

func TestRunSandboxPassesOnlyTheSpec(t *testing.T) {
	sb := &scriptedSandbox{answer: tool.SandboxResult{Stdout: []byte("ok")}}
	spec := tool.SandboxSpec{
		Network: []string{"api.example.com"},
		Files:   map[string][]byte{"main.go": []byte("package main")},
	}
	if _, err := layerWith(fixedClock(), newMemBlob(), nil).RunSandbox(context.Background(), sb,
		spec, []string{"curl", "https://api.example.com/health"}); err != nil {
		t.Fatalf("RunSandbox: %v", err)
	}
	if len(sb.spec.Files) != 1 || len(sb.spec.Network) != 1 || sb.spec.Network[0] != "api.example.com" {
		t.Fatalf("driver spec %+v", sb.spec)
	}
	if strings.Join(sb.command, " ") != "curl https://api.example.com/health" {
		t.Fatalf("command %v", sb.command)
	}
}

func TestRunSandboxWithoutADriverIsInternal(t *testing.T) {
	_, err := layerWith(fixedClock(), newMemBlob(), nil).RunSandbox(context.Background(), nil,
		tool.SandboxSpec{}, []string{"true"})
	if err == nil || core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("error %v, want internal", err)
	}
}

func TestRunSandboxRegistersTheDeadlineBeforeTheDriverRuns(t *testing.T) {
	clock := fixedClock()
	sb := &scriptedSandbox{
		block:   true,
		started: make(chan struct{}),
		stopped: make(chan struct{}),
		onRun:   func() { clock.Advance(60 * time.Second) },
	}
	type done struct {
		res tool.Result
		err error
	}
	result := make(chan done, 1)
	go func() {
		res, err := layerWith(clock, newMemBlob(), nil).RunSandbox(context.Background(), sb,
			tool.SandboxSpec{TimeLimit: 60 * time.Second}, []string{"make", "test"})
		result <- done{res, err}
	}()
	got := <-result
	if got.err == nil || core.CodeOf(got.err) != core.CodeTimeout {
		t.Fatalf("error %v, want timeout", got.err)
	}
}

func TestRunSandboxWaitsForTheDriverToStopAtItsTimeLimit(t *testing.T) {
	clock := fixedClock()
	sb := &scriptedSandbox{block: true, started: make(chan struct{}), stopped: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		_, err := layerWith(clock, newMemBlob(), nil).RunSandbox(context.Background(), sb,
			tool.SandboxSpec{TimeLimit: 60 * time.Second}, []string{"make", "test"})
		result <- err
	}()
	<-sb.started
	clock.Advance(60 * time.Second)
	if err := <-result; err == nil || core.CodeOf(err) != core.CodeTimeout {
		t.Fatalf("error %v, want timeout", err)
	}
	select {
	case <-sb.stopped:
	default:
		t.Fatal("RunSandbox returned before the driver acknowledged the stop")
	}
}

func TestRunSandboxCarriesBothStreams(t *testing.T) {
	sb := &scriptedSandbox{answer: tool.SandboxResult{Stdout: []byte("out"), Stderr: []byte("err")}}
	res, err := layerWith(fixedClock(), newMemBlob(), nil).RunSandbox(context.Background(), sb,
		tool.SandboxSpec{}, []string{"go", "test"})
	if err != nil {
		t.Fatalf("RunSandbox: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(res.Content, &got); err != nil {
		t.Fatalf("content %s: %v", res.Content, err)
	}
	if got["stdout"] != "out" || got["stderr"] != "err" {
		t.Fatalf("content %s does not carry both streams", res.Content)
	}
}

func TestRunSandboxBase64EncodesNonUTF8Output(t *testing.T) {
	raw := []byte{0xff, 0xfe, 'x'}
	sb := &scriptedSandbox{answer: tool.SandboxResult{Stdout: raw}}
	res, err := layerWith(fixedClock(), newMemBlob(), nil).RunSandbox(context.Background(), sb,
		tool.SandboxSpec{}, []string{"cat", "bin"})
	if err != nil {
		t.Fatalf("RunSandbox: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(res.Content, &got); err != nil {
		t.Fatalf("content %s: %v", res.Content, err)
	}
	if want := base64.StdEncoding.EncodeToString(raw); got["stdout_base64"] != want {
		t.Fatalf("content %s, want stdout_base64 %q", res.Content, want)
	}
}

func TestRunSandboxResolvesCredentialRefsBeforeTheRun(t *testing.T) {
	creds := &memCredentials{values: map[string][]byte{"cred://db": []byte("s3cret")}}
	sb := &scriptedSandbox{answer: tool.SandboxResult{Stdout: []byte("ok")}}
	layer := tool.New(tool.Config{Clock: fixedClock(), Credentials: creds})
	_, err := layer.RunSandbox(context.Background(), sb,
		tool.SandboxSpec{CredRefs: map[string]string{"DB_PASSWORD": "cred://db"}}, []string{"true"})
	if err != nil {
		t.Fatalf("RunSandbox: %v", err)
	}
	if got := sb.spec.CredRefs["DB_PASSWORD"]; got != "s3cret" {
		t.Fatalf("driver env %v, want the resolved credential", sb.spec.CredRefs)
	}
}

func TestRunSandboxCredentialFailureLeaksNothing(t *testing.T) {
	creds := &memCredentials{values: map[string][]byte{}}
	sb := &scriptedSandbox{}
	layer := tool.New(tool.Config{Clock: fixedClock(), Credentials: creds})
	_, err := layer.RunSandbox(context.Background(), sb,
		tool.SandboxSpec{CredRefs: map[string]string{"DB_PASSWORD": "cred://db"}}, []string{"true"})
	if err == nil {
		t.Fatal("RunSandbox succeeded without the credential")
	}
	if core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("error %v, want not_found", err)
	}
	for _, leak := range []string{"cred://db", "s3cret"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error %q leaks %q", err, leak)
		}
	}
	if sb.runs != 0 {
		t.Fatalf("the driver ran %d times", sb.runs)
	}
}

func TestRunSandboxWithoutCredentialStoreIsUnauthenticated(t *testing.T) {
	sb := &scriptedSandbox{}
	_, err := layerWith(fixedClock(), newMemBlob(), nil).RunSandbox(context.Background(), sb,
		tool.SandboxSpec{CredRefs: map[string]string{"DB_PASSWORD": "cred://db"}}, []string{"true"})
	if err == nil || core.CodeOf(err) != core.CodeUnauthenticated {
		t.Fatalf("error %v, want unauthenticated", err)
	}
	if sb.runs != 0 {
		t.Fatalf("the driver ran %d times", sb.runs)
	}
}

func TestRunSandboxRejectsUnsafeFiles(t *testing.T) {
	for _, name := range []string{"", "../escape", "/abs", "a/../../b", "a/../b", "a//b", "a/", ".", `a\b`, "a\x00b"} {
		t.Run(name, func(t *testing.T) {
			sb := &scriptedSandbox{}
			_, err := layerWith(fixedClock(), newMemBlob(), nil).RunSandbox(context.Background(), sb,
				tool.SandboxSpec{Files: map[string][]byte{name: []byte("x")}}, []string{"true"})
			if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
				t.Fatalf("error %v, want invalid_argument", err)
			}
			if sb.runs != 0 {
				t.Fatalf("the driver ran %d times", sb.runs)
			}
		})
	}
}

func TestRunSandboxRejectsReservedEnvironmentNames(t *testing.T) {
	for _, name := range []string{"", "A=B", "A\x00B", "PATH", "LD_PRELOAD", "ld_library_path", "HOME"} {
		t.Run(name, func(t *testing.T) {
			sb := &scriptedSandbox{}
			_, err := layerWith(fixedClock(), newMemBlob(), nil).RunSandbox(context.Background(), sb,
				tool.SandboxSpec{CredRefs: map[string]string{name: "cred://db"}}, []string{"true"})
			if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
				t.Fatalf("error %v, want invalid_argument", err)
			}
			if sb.runs != 0 {
				t.Fatalf("the driver ran %d times", sb.runs)
			}
		})
	}
}

// CLOCK-01: the sandbox's post-timeout and post-cancel grace waits must run on
// the layer's clock. With time.After they would run on wall time under a fake
// clock, so a replay and a test would diverge from production.
func TestRunSandboxGraceWaitsUseTheLayerClock(t *testing.T) {
	t.Run("after the time limit", func(t *testing.T) {
		clock := newRecordingClock()
		sb := &scriptedSandbox{block: true, started: make(chan struct{}), stopped: make(chan struct{})}
		result := make(chan error, 1)
		go func() {
			_, err := layerWith(clock, newMemBlob(), nil).RunSandbox(context.Background(), sb,
				tool.SandboxSpec{TimeLimit: 60 * time.Second}, []string{"make", "test"})
			result <- err
		}()
		<-sb.started
		clock.Advance(60 * time.Second)
		if err := <-result; err == nil || core.CodeOf(err) != core.CodeTimeout {
			t.Fatalf("error %v, want timeout", err)
		}
		if !clock.waited(tool.Heartbeat) {
			t.Fatal("the grace wait after the time limit did not use the layer's clock")
		}
	})

	t.Run("after cancellation", func(t *testing.T) {
		clock := newRecordingClock()
		ctx, cancel := context.WithCancel(context.Background())
		sb := &scriptedSandbox{ignoreCtx: true, release: make(chan struct{}), started: make(chan struct{})}
		result := make(chan error, 1)
		go func() {
			_, err := layerWith(clock, newMemBlob(), nil).RunSandbox(ctx, sb,
				tool.SandboxSpec{TimeLimit: 60 * time.Second}, []string{"make", "test"})
			result <- err
		}()
		<-sb.started
		cancel()
		if err := <-result; err == nil || core.CodeOf(err) != core.CodeCancelled {
			t.Fatalf("error %v, want cancelled", err)
		}
		close(sb.release)
		if !clock.waited(tool.Heartbeat) {
			t.Fatal("the grace wait after cancellation did not use the layer's clock")
		}
	})
}
