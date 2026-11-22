package tool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode/utf8"

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
	CredRefs  map[string]string // env var name -> cred:// name, resolved by RunSandbox before the run
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

	if err := validateSandboxSpec(spec); err != nil {
		return Result{}, err
	}
	resolved, err := l.resolveSandboxCredentials(ctx, spec.CredRefs)
	if err != nil {
		return Result{}, err
	}
	spec.CredRefs = resolved

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type completed struct {
		result SandboxResult
		err    error
		panic  any
	}
	done := make(chan completed, 1)
	// Register the deadline before the driver can run: a driver that moves the
	// test clock as soon as it starts must not outrun the timer.
	clock := l.clock()
	deadline := clock.After(limit)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- completed{panic: p}
			}
		}()
		res, err := sb.Run(runCtx, spec, command)
		done <- completed{result: res, err: err}
	}()

	out, cause := awaitBounded(done, deadline, ctx)
	cancel()
	switch cause {
	case waitDeadline:
		// Acknowledge the stop before the outcome is recorded: a replay must
		// not find the command still running. A driver that ignores its
		// context is bounded by one heartbeat, on the layer's clock.
		awaitStop(clock, l.log(), done, "sandbox did not stop after its time limit")
		return Result{}, core.NewError(core.CodeTimeout,
			fmt.Sprintf("sandbox: command exceeded its time limit of %s", limit), nil)
	case waitCancelled:
		awaitStop(clock, l.log(), done, "sandbox did not stop after its call was cancelled")
		return Result{}, core.NewError(core.CodeCancelled, "sandbox: command cancelled", nil)
	}
	if out.panic != nil {
		l.log().Error("sandbox driver panicked", "panic", fmt.Sprint(out.panic))
		return Result{}, core.NewError(core.CodeInternal, "sandbox: the sandbox failed", nil)
	}
	if out.err != nil {
		// A driver that returns because the call was cancelled is not failing
		// on its own: the call is cancelled, however the stop reached the layer.
		if ctx.Err() != nil && errors.Is(out.err, ctx.Err()) {
			return Result{}, core.NewError(core.CodeCancelled, "sandbox: command cancelled", nil)
		}
		l.log().Error("sandbox driver failed", "error", out.err)
		return Result{}, core.NewError(core.CodeInternal, "sandbox: the sandbox failed", nil)
	}
	return sandboxResult(out.result, maxOut), nil
}

// sandboxResult applies the output limit: the first bytes up to max are kept,
// the rest discarded, and the output is marked truncated (REQ-TOOL-020).
// Stdout and stderr are carried separately; a stream that is not UTF-8 is
// base64-encoded so no byte is rewritten.
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
	return Result{
		Content:    sandboxContent(stdout, stderr),
		ExitStatus: res.ExitStatus,
		Truncated:  truncated,
	}
}

// sandboxContent keeps the single-stream shape scenarios and models expect (a
// JSON string) and switches to an object only when both streams carry output.
// A non-UTF-8 stream is base64, under "<stream>_base64".
func sandboxContent(stdout, stderr []byte) json.RawMessage {
	switch {
	case len(stdout) == 0 && len(stderr) == 0:
		content, _ := json.Marshal("")
		return content
	case len(stderr) == 0:
		return streamContent("stdout", stdout)
	case len(stdout) == 0:
		return streamContent("stderr", stderr)
	}
	doc := map[string]any{}
	addStream(doc, "stdout", stdout)
	addStream(doc, "stderr", stderr)
	content, _ := json.Marshal(doc)
	return content
}

func streamContent(name string, value []byte) json.RawMessage {
	if utf8.Valid(value) {
		content, _ := json.Marshal(string(value))
		return content
	}
	doc := map[string]any{}
	addStream(doc, name, value)
	content, _ := json.Marshal(doc)
	return content
}

func addStream(doc map[string]any, name string, value []byte) {
	if utf8.Valid(value) {
		doc[name] = string(value)
		return
	}
	doc[name+"_base64"] = base64.StdEncoding.EncodeToString(value)
}

// validateSandboxSpec rejects a spec whose files could escape the work
// directory or whose environment names would override the driver's own.
func validateSandboxSpec(spec SandboxSpec) error {
	for name := range spec.Files {
		if !sandboxFilePath(name) {
			return core.NewError(core.CodeInvalidArgument,
				fmt.Sprintf("sandbox: file %q is not a relative path inside the work directory", name), nil)
		}
	}
	for name := range spec.CredRefs {
		if !sandboxEnvName(name) {
			return core.NewError(core.CodeInvalidArgument,
				fmt.Sprintf("sandbox: environment variable %q is not allowed", name), nil)
		}
	}
	return nil
}

func sandboxFilePath(name string) bool {
	if name == "" || strings.ContainsRune(name, 0) || strings.Contains(name, `\`) || path.IsAbs(name) {
		return false
	}
	clean := path.Clean(name)
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

// reservedSandboxEnv are the names a command's own loader or shell reads
// before the command runs; a credential must not set them.
var reservedSandboxEnv = map[string]bool{
	"PATH":              true,
	"HOME":              true,
	"SHELL":             true,
	"IFS":               true,
	"ENV":               true,
	"BASH_ENV":          true,
	"PWD":               true,
	"TMPDIR":            true,
	"HOSTNAME":          true,
	"LD_PRELOAD":        true,
	"LD_LIBRARY_PATH":   true,
	"LD_AUDIT":          true,
	"LD_DEBUG":          true,
	"LD_PROFILE":        true,
	"GCONV_PATH":        true,
	"NLSPATH":           true,
	"PYTHONPATH":        true,
	"NODE_OPTIONS":      true,
	"PERL5LIB":          true,
	"RUBYLIB":           true,
	"JAVA_TOOL_OPTIONS": true,
	"_JAVA_OPTIONS":     true,
	"CLASSPATH":         true,
}

func sandboxEnvName(name string) bool {
	if name == "" || strings.ContainsAny(name, "=\x00") {
		return false
	}
	return !reservedSandboxEnv[strings.ToUpper(name)]
}

// resolveSandboxCredentials resolves every reference before the driver runs,
// so a driver never sees a reference or a store error. A failure names the
// environment variable, never the reference or its value.
func (l *Layer) resolveSandboxCredentials(ctx context.Context, refs map[string]string) (map[string]string, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if l.cfg.Credentials == nil {
		return nil, core.NewError(core.CodeUnauthenticated, "sandbox: no credential store", nil)
	}
	resolved := make(map[string]string, len(refs))
	for name, ref := range refs {
		secret, err := l.cfg.Credentials.Resolve(ctx, ref)
		if err != nil {
			code := core.CodeOf(err)
			if code != core.CodeUnavailable && code != core.CodeNotFound {
				code = core.CodeUnauthenticated
			}
			return nil, core.NewError(code,
				fmt.Sprintf("sandbox: the credential for %q could not be resolved", name), nil)
		}
		resolved[name] = string(secret)
	}
	return resolved, nil
}
