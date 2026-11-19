package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/epzero/basamento/core"
)

// DefaultSandboxTimeout bounds a command whose spec declares no time limit.
const DefaultSandboxTimeout = 60 * time.Second

// DefaultSandboxOutput is the number of output bytes kept when the spec sets
// no limit.
const DefaultSandboxOutput = 1024 * 1024

// SandboxSpec describes one command's sandbox. It is the only state a driver
// may give the command: Files is its whole work directory, Network is its only
// network and CredRefs are resolved at the run (REQ-TOOL-018).
type SandboxSpec struct {
	Image     string            // or a driver-specific base
	TimeLimit time.Duration     // 0 = DefaultSandboxTimeout
	MaxOutput int               // bytes kept in total; 0 = DefaultSandboxOutput
	Network   []string          // hosts or CIDRs; empty = no network
	Files     map[string][]byte // the only files the command sees, under its work dir
	CredRefs  map[string]string // env var name -> cred:// name, resolved at the run
}

// Sandbox is the port for commands run outside the host. A driver gives the
// command no host files, environment, instance metadata or network beyond the
// spec, and stops it when ctx is cancelled.
type Sandbox interface {
	Run(ctx context.Context, spec SandboxSpec, command []string) (SandboxResult, error)
}

// SandboxResult is what a driver returns. A non-zero ExitStatus is a normal
// result (REQ-TOOL-021); an error means the sandbox itself failed.
type SandboxResult struct {
	ExitStatus int
	Stdout     []byte
	Stderr     []byte
	Truncated  bool
}

// RunSandbox runs command in sb within spec's limits. It enforces the time
// limit (REQ-TOOL-019) and the output limit (REQ-TOOL-020) around the driver,
// so every caller sees the same outcome whatever the driver does on its own.
// A driver that cannot start or fails is internal (REQ-TOOL-022).
func (l *Layer) RunSandbox(ctx context.Context, sb Sandbox, spec SandboxSpec, command []string) (Result, error) {
	if sb == nil {
		return Result{}, core.NewError(core.CodeInternal, "sandbox: no sandbox driver", nil)
	}
	limit := spec.TimeLimit
	if limit <= 0 {
		limit = DefaultSandboxTimeout
	}
	maxOut := spec.MaxOutput
	if maxOut <= 0 {
		maxOut = DefaultSandboxOutput
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type completed struct {
		result SandboxResult
		err    error
		panic  any
	}
	done := make(chan completed, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- completed{panic: p}
			}
		}()
		res, err := sb.Run(runCtx, spec, command)
		done <- completed{result: res, err: err}
	}()

	select {
	case <-l.clock().After(limit):
		cancel()
		return Result{}, core.NewError(core.CodeTimeout,
			fmt.Sprintf("sandbox: command exceeded its time limit of %s", limit), nil)
	case <-ctx.Done():
		cancel()
		return Result{}, core.NewError(core.CodeCancelled, "sandbox: command cancelled", nil)
	case out := <-done:
		cancel()
		if out.panic != nil {
			l.log().Error("sandbox driver panicked", "panic", fmt.Sprint(out.panic))
			return Result{}, core.NewError(core.CodeInternal, "sandbox: the sandbox failed", nil)
		}
		if out.err != nil {
			l.log().Error("sandbox driver failed", "error", out.err)
			return Result{}, core.NewError(core.CodeInternal, "sandbox: the sandbox failed", nil)
		}
		return sandboxResult(out.result, maxOut), nil
	}
}

// sandboxResult applies the output limit: the first bytes up to max are kept,
// the rest discarded, and the output is marked truncated (REQ-TOOL-020).
func sandboxResult(res SandboxResult, max int) Result {
	stdout, stderr := res.Stdout, res.Stderr
	truncated := res.Truncated
	total := len(stdout) + len(stderr)
	if total > max {
		truncated = true
	}
	if len(stdout) > max {
		stdout = stdout[:max]
	}
	remaining := max - len(stdout)
	if remaining < 0 {
		remaining = 0
	}
	if len(stderr) > remaining {
		stderr = stderr[:remaining]
	}
	output := stdout
	if len(output) == 0 && len(stderr) > 0 {
		output = stderr
	}
	content, _ := json.Marshal(string(output))
	return Result{
		Content:    content,
		ExitStatus: res.ExitStatus,
		Truncated:  truncated,
	}
}
