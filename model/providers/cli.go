package providers

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
)

// Command is one process to run for a command-line provider.
type Command struct {
	// Path is the executable.
	Path string
	// Args are the arguments after Path.
	Args []string
	// Dir is the working directory; empty means the provider's directory.
	Dir string
	// Env is the process environment. The caller includes the inherited
	// environment plus the manifest's variables.
	Env []string
}

// Process is a started command. Its stdout is read until EOF; Wait reports how
// it ended.
type Process interface {
	// Stdout is the command's standard output.
	Stdout() io.Reader
	// Wait waits for the command to exit.
	Wait() error
}

// Runner starts commands. The production implementation is ExecRunner; tests
// and simulators substitute their own, so the gate never runs a vendor
// command.
type Runner interface {
	Start(ctx context.Context, c Command) (Process, error)
}

// ExecRunner runs commands as subprocesses. The context kills the process when
// the invocation is cancelled.
type ExecRunner struct{}

// Start starts cmd.
func (ExecRunner) Start(ctx context.Context, c Command) (Process, error) {
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execProcess{cmd: cmd, stdout: stdout}, nil
}

type execProcess struct {
	cmd    *exec.Cmd
	stdout io.Reader
}

func (p *execProcess) Stdout() io.Reader { return p.stdout }
func (p *execProcess) Wait() error       { return p.cmd.Wait() }

// CLIOptions configures the generic command-line provider.
type CLIOptions struct {
	// Runner starts commands. Nil runs them as subprocesses.
	Runner Runner
	// Clock stamps the invocation registry's retention window. Required: the
	// registry fails closed without one.
	Clock core.Clock
}

// CLI is a provider driven by a manifest that runs a local command, parses
// progress from stdout and collects output files (docs/architecture/model-layer.md
// §3). It is machine-local and implements model.Invoker.
type CLI struct {
	manifest    *Manifest
	descriptors []model.Descriptor
	runner      Runner
	dir         string
	progress    *regexp.Regexp
	args        *exprMapping

	runs *model.Registry

	startMu sync.Mutex
}

// exprMapping is the small interface the CLI needs from a compiled mapping, so
// this file does not depend on expr's concrete type at its call sites.
type exprMapping struct{ eval func(any) (any, error) }

func (m *exprMapping) Eval(input any) (any, error) { return m.eval(input) }

var _ model.Invoker = (*CLI)(nil)

// NewCLI builds the generic command-line provider from a manifest.
func NewCLI(m *Manifest, opts CLIOptions) (*CLI, error) {
	if m == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "cli provider needs a manifest", nil)
	}
	if m.CLI == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "manifest has no cli driver", nil)
	}
	if err := m.Validate(); err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, err.Error(), nil)
	}
	if opts.Clock == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "cli provider has no clock", nil)
	}
	descs, err := m.Descriptors()
	if err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, err.Error(), nil)
	}
	runs, err := model.NewRegistry(m.Name, model.WithClock(opts.Clock))
	if err != nil {
		return nil, err
	}
	c := &CLI{
		manifest:    m,
		descriptors: descs,
		runner:      opts.Runner,
		dir:         m.CLI.Dir,
		runs:        runs,
	}
	if c.runner == nil {
		c.runner = ExecRunner{}
	}
	if c.dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, core.NewError(core.CodeInternal, "cli provider cannot read its working directory", nil)
		}
		c.dir = wd
	}
	if m.CLI.Progress != "" {
		re, err := regexp.Compile(m.CLI.Progress)
		if err != nil {
			return nil, core.NewError(core.CodeInvalidArgument, "cli progress expression is invalid", nil)
		}
		if re.NumSubexp() == 0 {
			return nil, core.NewError(core.CodeInvalidArgument, "cli progress expression has no number group", nil)
		}
		c.progress = re
	}
	if m.CLI.Args != "" {
		mapping, err := compileMapping(m.CLI.Args)
		if err != nil {
			return nil, core.NewError(core.CodeInvalidArgument, "cli args mapping is invalid", nil)
		}
		c.args = &exprMapping{eval: func(v any) (any, error) { return mapping.Eval(v, nil) }}
	}
	return c, nil
}

// Provider is the manifest's provider name.
func (c *CLI) Provider() string { return c.manifest.Name }

// Describe lists the models the manifest serves.
func (c *CLI) Describe(context.Context) ([]model.Descriptor, error) {
	return append([]model.Descriptor(nil), c.descriptors...), nil
}

// Start launches the command and returns at once. A repeated start with the
// same client ref returns the original handle without running the command
// twice.
func (c *CLI) Start(ctx context.Context, r model.Request) (model.Handle, error) {
	if _, err := model.Select(c.descriptors, r.Model, r.Op); err != nil {
		return model.Handle{}, err
	}
	if r.ClientRef == "" {
		return model.Handle{}, core.NewError(core.CodeInvalidArgument, "request has no client ref", nil)
	}
	c.startMu.Lock()
	defer c.startMu.Unlock()
	if hd, err := c.runs.Lookup(ctx, r.ClientRef); err == nil && hd != nil {
		return *hd, nil
	}
	// Each invocation runs in its own subdirectory of the provider's
	// directory, so its output globs never pick up another invocation's files.
	dir := c.runDir(r.ClientRef)
	input, err := inputFor(r, "", dir)
	if err != nil {
		return model.Handle{}, err
	}
	args, err := c.arguments(input)
	if err != nil {
		return model.Handle{}, err
	}
	run, existed, err := c.runs.Begin(ctx, r.ClientRef)
	if err != nil {
		return model.Handle{}, err
	}
	if !existed {
		// The ref was evicted, so the directory still holds the earlier run's
		// files; a fresh invocation must not report them as its outputs.
		if err := os.RemoveAll(dir); err != nil {
			run.Fail(core.NewError(core.CodeVendor,
				"command-line provider could not clear the invocation directory", map[string]any{"cause": err.Error()}))
			return run.Handle(), nil
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		run.Fail(core.NewError(core.CodeVendor,
			"command-line provider could not create the invocation directory", map[string]any{"cause": err.Error()}))
		return run.Handle(), nil
	}
	cmd := Command{
		Path: c.manifest.CLI.Command[0],
		Args: append(append([]string(nil), c.manifest.CLI.Command[1:]...), args...),
		Dir:  dir,
		Env:  c.env(),
	}
	proc, err := c.runner.Start(run.Context(), cmd)
	if err != nil {
		run.Fail(core.NewError(core.CodeVendor,
			"command-line provider could not start the command", map[string]any{"cause": err.Error()}))
		return run.Handle(), nil
	}
	go c.execute(run, proc, dir)
	return run.Handle(), nil
}

// runDir is one invocation's working directory: a subdirectory of the
// provider's directory named after the client ref, with a short digest of the
// ref so two refs that sanitize alike stay apart.
func (c *CLI) runDir(clientRef string) string {
	sum := sha256.Sum256([]byte(clientRef))
	var name strings.Builder
	for _, r := range clientRef {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			name.WriteRune(r)
		default:
			name.WriteByte('_')
		}
		if name.Len() >= 48 {
			break
		}
	}
	return filepath.Join(c.dir, "runs", fmt.Sprintf("%s-%x", name.String(), sum[:4]))
}

// Events follows the command's progress events with Seq > after.
func (c *CLI) Events(ctx context.Context, h model.Handle, after int64) (<-chan model.Event, error) {
	return c.runs.Events(ctx, h, after)
}

// Result waits for the command and its output files.
func (c *CLI) Result(ctx context.Context, h model.Handle) (model.Result, error) {
	return c.runs.Result(ctx, h)
}

// Cancel ends the invocation and kills the command.
func (c *CLI) Cancel(ctx context.Context, h model.Handle) error {
	return c.runs.Cancel(ctx, h)
}

// Lookup resolves a client ref to its invocation. Command-line invocations are
// local to this process, so an unknown ref is simply not found.
func (c *CLI) Lookup(ctx context.Context, clientRef string) (*model.Handle, error) {
	return c.runs.Lookup(ctx, clientRef)
}

func (c *CLI) execute(run *model.Run, proc Process, dir string) {
	ctx := run.Context()
	// Drain stdout before Wait: a subprocess's Wait closes the pipe, so a
	// reader racing it would lose the progress lines still in the buffer.
	c.readProgress(run, proc.Stdout())
	err := proc.Wait()
	if ctx.Err() != nil {
		// Cancel ended the run and the process was killed.
		return
	}
	if err != nil {
		run.Fail(core.NewError(core.CodeVendor,
			"command-line provider command failed", map[string]any{"cause": err.Error()}))
		return
	}
	outputs, err := c.outputs(dir)
	if err != nil {
		run.Fail(err)
		return
	}
	run.Succeed(model.Result{Outputs: outputs})
}

func (c *CLI) readProgress(run *model.Run, stdout io.Reader) {
	if c.progress == nil || stdout == nil {
		_, _ = io.Copy(io.Discard, stdout)
		return
	}
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		match := c.progress.FindStringSubmatch(scanner.Text())
		if len(match) < 2 {
			continue
		}
		n, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			continue
		}
		p := n / 100
		if p < 0 {
			p = 0
		}
		if p > 1 {
			p = 1
		}
		run.Publish(model.Event{Kind: model.EventProgress, Progress: p})
	}
}

// arguments evaluates the manifest's args mapping into the command's
// arguments.
func (c *CLI) arguments(input map[string]any) ([]string, error) {
	if c.args == nil {
		return nil, nil
	}
	v, err := c.args.Eval(input)
	if err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "cli args mapping failed", nil)
	}
	list, ok := v.([]any)
	if !ok {
		return nil, core.NewError(core.CodeInvalidArgument, "cli args mapping did not produce an array", nil)
	}
	args := make([]string, 0, len(list))
	for _, item := range list {
		args = append(args, toString(item))
	}
	return args, nil
}

// outputs expands the manifest's globs into output files under the
// invocation's directory. Matches are sorted, so the same command yields the
// same result order.
func (c *CLI) outputs(dir string) ([]model.Output, error) {
	var out []model.Output
	seen := map[string]bool{}
	for _, pattern := range c.manifest.CLI.Outputs {
		glob := pattern
		if !filepath.IsAbs(glob) {
			glob = filepath.Join(dir, pattern)
		}
		matches, err := filepath.Glob(glob)
		if err != nil {
			return nil, core.NewError(core.CodeInvalidArgument, "cli output glob is invalid", map[string]any{"glob": pattern})
		}
		sort.Strings(matches)
		for _, path := range matches {
			if seen[path] {
				continue
			}
			seen[path] = true
			media, mime := mediaOf(path)
			out = append(out, model.Output{Path: path, Media: media, Mime: mime})
		}
	}
	return out, nil
}

func (c *CLI) env() []string {
	env := os.Environ()
	keys := make([]string, 0, len(c.manifest.CLI.Env))
	for k := range c.manifest.CLI.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+c.manifest.CLI.Env[k])
	}
	return env
}

// mediaOf names an output file's media and MIME type from its extension.
func mediaOf(path string) (media, mime string) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image", "image/png"
	case ".jpg", ".jpeg":
		return "image", "image/jpeg"
	case ".webp":
		return "image", "image/webp"
	case ".gif":
		return "image", "image/gif"
	case ".mp4":
		return "video", "video/mp4"
	case ".webm":
		return "video", "video/webm"
	case ".wav":
		return "audio", "audio/wav"
	case ".mp3":
		return "audio", "audio/mpeg"
	case ".json":
		return "file", "application/json"
	default:
		return "file", "application/octet-stream"
	}
}
