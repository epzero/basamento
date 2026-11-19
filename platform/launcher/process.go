package launcher

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

// ProcessConfig configures the process launcher: Command is started once per
// worker. It may contain {token}, {template}, {pool}, {idempotency_key} and
// {index} (the worker's position in the batch, from 0). The join token is also
// exported as BASAMENTO_JOIN_TOKEN, with BASAMENTO_POOL and
// BASAMENTO_TEMPLATE.
type ProcessConfig struct {
	Command []string
	Dir     string
	Env     []string
}

// Process starts workers as local processes. It is the embedded profile's
// launcher: a laptop that starts a local generation server only when it is
// needed. Only a deployment administrator may configure it; a tenant's own
// pools use an http launcher and the tenant's cloud credentials (ADR-0012
// §12).
type Process struct {
	cfg ProcessConfig

	mu    sync.Mutex
	procs []*exec.Cmd
}

var _ platform.Launcher = (*Process)(nil)

// NewProcess builds the process launcher. Command is required.
func NewProcess(cfg ProcessConfig) (*Process, error) {
	if len(cfg.Command) == 0 || strings.TrimSpace(cfg.Command[0]) == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "process launcher has no command", nil)
	}
	command := make([]string, len(cfg.Command))
	copy(command, cfg.Command)
	cfg.Command = command
	return &Process{cfg: cfg}, nil
}

// Launch starts one process per join token and returns once they have all
// started. A worker joining is still the only proof that it runs.
func (p *Process) Launch(_ context.Context, req platform.LaunchRequest) (platform.LaunchResult, error) {
	if err := validate(req); err != nil {
		return platform.LaunchResult{}, err
	}
	started := make([]*exec.Cmd, 0, req.Count)
	for i, token := range req.JoinTokens {
		args := make([]string, 0, len(p.cfg.Command)-1)
		for _, arg := range p.cfg.Command[1:] {
			args = append(args, processArg(arg, req, token, i))
		}
		cmd := exec.Command(p.cfg.Command[0], args...)
		cmd.Dir = p.cfg.Dir
		cmd.Env = append(os.Environ(), p.cfg.Env...)
		cmd.Env = append(cmd.Env,
			"BASAMENTO_JOIN_TOKEN="+token,
			"BASAMENTO_POOL="+req.Pool,
			"BASAMENTO_TEMPLATE="+req.Template,
			"BASAMENTO_IDEMPOTENCY_KEY="+req.IdempotencyKey,
		)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			p.stop(started)
			return platform.LaunchResult{}, core.NewError(core.CodeUnavailable,
				"process launcher could not start a worker", map[string]any{"error": err.Error()})
		}
		started = append(started, cmd)
	}
	p.mu.Lock()
	p.procs = append(p.procs, started...)
	p.mu.Unlock()
	for _, cmd := range started {
		go func(c *exec.Cmd) { _ = c.Wait() }(cmd)
	}
	return platform.LaunchResult{Accepted: true}, nil
}

func processArg(arg string, req platform.LaunchRequest, token string, index int) string {
	return strings.NewReplacer(
		"{token}", token,
		"{pool}", req.Pool,
		"{template}", req.Template,
		"{idempotency_key}", req.IdempotencyKey,
		"{index}", strconv.Itoa(index),
	).Replace(arg)
}

func (p *Process) stop(cmds []*exec.Cmd) {
	for _, cmd := range cmds {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	}
}
