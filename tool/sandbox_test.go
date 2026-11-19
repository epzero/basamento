package tool_test

import (
	"bytes"
	"context"
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
	started     chan struct{}
	stopped     chan struct{}
	startedOnce sync.Once
	stoppedOnce sync.Once
}

func (s *scriptedSandbox) Run(ctx context.Context, spec tool.SandboxSpec, command []string) (tool.SandboxResult, error) {
	s.mu.Lock()
	s.spec = spec
	s.command = command
	s.runs++
	s.mu.Unlock()
	if s.started != nil {
		s.startedOnce.Do(func() { close(s.started) })
	}
	if s.panic {
		panic("driver broke")
	}
	if s.err != nil {
		return tool.SandboxResult{}, s.err
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
