package tool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/schema"
	"github.com/epzero/basamento/platform"
)

// Defaults of the tool layer (design §3).
const (
	// DefaultTimeout bounds one call when the tool declares none.
	DefaultTimeout = 60 * time.Second

	// DefaultMaxResult is the number of bytes kept inline before a result is
	// stored in blob storage and returned by reference (REQ-TOOL-007).
	DefaultMaxResult = 256 * 1024

	// Heartbeat is the interval at which a running tool may report progress
	// and observe cancellation (REQ-TOOL-006).
	Heartbeat = time.Second
)

// Clock is the layer's only source of time. After must wake at its deadline;
// the fake clock in tests provides both, so a timeout never depends on the
// wall clock in the gate.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// Config configures a Layer. The zero value is usable: the system clock, the
// package defaults, and no blob store (an oversized result then fails the
// layer).
type Config struct {
	Clock            Clock
	Blob             platform.Blob
	Credentials      platform.Credentials
	Log              *slog.Logger
	DefaultTimeout   time.Duration
	DefaultMaxResult int
}

// Layer is the tool layer: the call step, allowlist checks, argument
// validation, result spilling and the sandbox runner.
type Layer struct {
	cfg Config
}

// New returns a layer over cfg.
func New(cfg Config) *Layer { return &Layer{cfg: cfg} }

// Step is the side effect of one call, run by a step named by the caller:
//
//	dc.Step("tool:"+call.Name, call, runtime.StepOptions{IdempotencyKey: tool.Key(...)})
//
// It validates, checks the allowlist, runs the tool under its limits and
// returns an Outcome. An Outcome, success or failure, is a normal step result:
// the runtime never retries it. A Go error means the layer itself failed.
func (l *Layer) Step(ctx context.Context, set *Set, call Call) (Outcome, error) {
	// Order (design §3): allowlist → schema → run → size.
	t, ok := set.Get(call.Name)
	if !ok {
		return Outcome{
			Failed:  true,
			Code:    core.CodePermissionDenied,
			Message: fmt.Sprintf("tool %q is not on the caller's allowlist", call.Name),
		}, nil
	}
	args := call.Args
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage(`{}`)
	}
	var value any
	if err := json.Unmarshal(args, &value); err != nil {
		return Outcome{
			Failed:  true,
			Code:    core.CodeInvalidArgument,
			Message: fmt.Sprintf("tool %q: arguments are not valid JSON: %v", t.Name, err),
		}, nil
	}
	if err := l.validate(t, args); err != nil {
		return Outcome{
			Failed:  true,
			Code:    core.CodeOf(err),
			Message: errorMessage(err),
		}, nil
	}

	timeout := t.Timeout
	if timeout <= 0 {
		timeout = l.cfg.DefaultTimeout
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	clock := l.clock()
	toolCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type completed struct {
		result Result
		err    error
		panic  any
	}
	done := make(chan completed, 1)
	// Register the deadline before the tool can run: a tool that moves the
	// test clock as soon as it starts must not outrun the timer.
	deadline := clock.After(timeout)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- completed{panic: p}
			}
		}()
		res, err := t.Run(toolCtx, args)
		done <- completed{result: res, err: err}
	}()

	select {
	case <-deadline:
		cancel()
		// Acknowledge the stop before the outcome is recorded: a replay must
		// not find the tool still running. A tool that ignores its context is
		// bounded by one heartbeat.
		select {
		case <-done:
		case <-time.After(Heartbeat):
			l.log().Warn("tool did not stop after its time limit", "tool", t.Name)
		}
		return Outcome{
			Failed:  true,
			Code:    core.CodeTimeout,
			Message: fmt.Sprintf("tool %q exceeded its time limit of %s", t.Name, timeout),
		}, nil
	case <-ctx.Done():
		cancel()
		select {
		case <-done:
		case <-time.After(Heartbeat):
			l.log().Warn("tool did not stop after its call was cancelled", "tool", t.Name)
		}
		return Outcome{}, core.NewError(core.CodeCancelled,
			fmt.Sprintf("tool %q: call cancelled", t.Name), nil)
	case out := <-done:
		cancel()
		if out.panic != nil {
			l.log().Error("tool panicked", "tool", t.Name, "panic", fmt.Sprint(out.panic))
			return Outcome{
				Failed:  true,
				Code:    core.CodeInternal,
				Message: fmt.Sprintf("tool %q panicked", t.Name),
			}, nil
		}
		if out.err != nil {
			return Outcome{
				Failed:  true,
				Code:    core.CodeOf(out.err),
				Message: errorMessage(out.err),
			}, nil
		}
		res, err := l.spill(ctx, t, out.result)
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{Result: res}, nil
	}
}

// RunStep keeps the package-level contract of design §2: run one call with
// now as the layer's clock. It has no blob store, so a tool that needs to
// spill an oversized result must be run through a Layer with a Blob.
func RunStep(ctx context.Context, set *Set, call Call, now func() time.Time) (Outcome, error) {
	return New(Config{Clock: wallClock{now: now}}).Step(ctx, set, call)
}

func (l *Layer) clock() Clock {
	if l.cfg.Clock != nil {
		return l.cfg.Clock
	}
	return wallClock{now: func() time.Time { return time.Now().UTC() }}
}

func (l *Layer) log() *slog.Logger {
	if l.cfg.Log != nil {
		return l.cfg.Log
	}
	return slog.Default()
}

// validate compiles the tool's input schema and checks args against it. The
// message names the argument and what the schema requires of it (REQ-TOOL-002).
func (l *Layer) validate(t Tool, args json.RawMessage) error {
	if len(bytes.TrimSpace(t.Input)) == 0 {
		return nil
	}
	compiled, err := schema.Compile(t.Input)
	if err != nil {
		return core.NewError(core.CodeInternal,
			fmt.Sprintf("tool %q: its input schema does not compile", t.Name), nil)
	}
	if err := compiled.Validate(args); err != nil {
		text := violationText(t.Input, schema.Violations(err))
		if text == "" {
			text = err.Error()
		}
		return core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("tool %q: %s", t.Name, text), nil)
	}
	return nil
}

// violationText renders one violation as "argument \"x\" …", naming the
// argument and the constraint.
func violationText(input json.RawMessage, violations []schema.Violation) string {
	if len(violations) == 0 {
		return ""
	}
	parts := make([]string, 0, len(violations))
	for _, v := range violations {
		arg := strings.TrimPrefix(v.Location, "/")
		arg = strings.ReplaceAll(strings.ReplaceAll(arg, "~1", "/"), "~0", "~")
		switch v.Keyword {
		case "required":
			parts = append(parts, fmt.Sprintf("argument %q is required", arg))
		case "additionalProperties":
			parts = append(parts, fmt.Sprintf("argument %q is not allowed", arg))
		case "type":
			parts = append(parts, fmt.Sprintf("argument %q must be %s", arg, expectedType(input, v.Location)))
		default:
			if arg == "" {
				parts = append(parts, v.Message)
			} else {
				parts = append(parts, fmt.Sprintf("argument %q %s", arg, v.Message))
			}
		}
	}
	return strings.Join(parts, "; ")
}

// expectedType reads the declared type of the property at the violation's
// JSON pointer, for a message such as "must be a string".
func expectedType(input json.RawMessage, location string) string {
	var doc map[string]any
	if err := json.Unmarshal(input, &doc); err != nil {
		return "of the declared type"
	}
	arg := strings.TrimPrefix(location, "/")
	props, ok := doc["properties"].(map[string]any)
	if !ok {
		return "of the declared type"
	}
	prop, ok := props[arg].(map[string]any)
	if !ok {
		return "of the declared type"
	}
	switch typ := prop["type"].(type) {
	case string:
		return article(typ)
	case []any:
		names := make([]string, 0, len(typ))
		for _, item := range typ {
			if s, ok := item.(string); ok {
				names = append(names, s)
			}
		}
		if len(names) > 0 {
			return "of type " + strings.Join(names, " or ")
		}
	}
	return "of the declared type"
}

func article(typ string) string {
	if typ == "" {
		return "of the declared type"
	}
	first := typ[0]
	if strings.ContainsRune("aeiou", rune(first)) {
		return "an " + typ
	}
	return "a " + typ
}

// spill stores a result larger than the tool's limit in blob storage and
// returns its reference instead of its bytes (REQ-TOOL-007).
func (l *Layer) spill(ctx context.Context, t Tool, res Result) (Result, error) {
	limit := t.MaxResult
	if limit <= 0 {
		limit = l.cfg.DefaultMaxResult
	}
	if limit <= 0 {
		limit = DefaultMaxResult
	}
	if len(res.Content) <= limit {
		return res, nil
	}
	if l.cfg.Blob == nil {
		return Result{}, core.NewError(core.CodeInternal,
			fmt.Sprintf("tool %q: result exceeds %d bytes and no blob store is configured", t.Name, limit), nil)
	}
	sum := sha256.Sum256(res.Content)
	key := "sha256-" + hex.EncodeToString(sum[:])
	if err := l.cfg.Blob.Put(ctx, key, bytes.NewReader(res.Content), "application/octet-stream"); err != nil {
		return Result{}, core.NewError(core.CodeInternal,
			fmt.Sprintf("tool %q: result could not be stored", t.Name), map[string]any{"tool": t.Name})
	}
	res.BlobRef = key
	res.Content = nil
	return res, nil
}

// errorMessage is what a model sees for a tool's own error (REQ-TOOL-004,
// REQ-TOOL-011): the message, never the details.
func errorMessage(err error) string {
	var pe *core.Error
	if errors.As(err, &pe) {
		if pe.Message != "" {
			return pe.Message
		}
		return string(pe.Code)
	}
	return err.Error()
}

// wallClock is the design §2 clock: the caller supplies Now; After uses the
// operating system timer. Tests pass a fake Clock instead.
type wallClock struct {
	now func() time.Time
}

func (c wallClock) Now() time.Time {
	if c.now == nil {
		return time.Now().UTC()
	}
	return c.now()
}

func (wallClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
