package gate

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	// gitRemoteTimeout bounds git commands that talk to a remote, so an
	// unreachable origin fails `gate agent new` instead of hanging it forever.
	gitRemoteTimeout = 30 * time.Second
	// gitWaitDelay is the grace period after the deadline before exec closes
	// git's output pipes. Killing git alone does not end a remote call: its
	// transport helper (git-remote-https, ssh, a credential helper) inherits
	// the pipes and can outlive the kill. Without this, CombinedOutput would
	// block until that helper exits, so the timeout would not bound the call.
	gitWaitDelay = 5 * time.Second
)

// gitRemote runs a remote-facing git command with gitRemoteTimeout.
// Commands that do not touch the network keep using git.
func gitRemote(dir string, args ...string) (string, error) {
	return gitRemoteContext(context.Background(), dir, args...)
}

func gitRemoteContext(parent context.Context, dir string, args ...string) (string, error) {
	return gitRemoteWithin(parent, dir, gitRemoteTimeout, gitWaitDelay, args...)
}

// gitRemoteWithin is gitRemoteContext with the bounds passed in, so a test can
// prove the wall-clock bound with a short timeout instead of waiting 30s.
func gitRemoteWithin(parent context.Context, dir string, timeout, waitDelay time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// A killed git can leave a transport helper holding the output pipes.
	// WaitDelay force-closes them after the grace period, which is what makes
	// the deadline a wall-clock bound.
	cmd.WaitDelay = waitDelay
	killWithProcessGroup(cmd)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "", fmt.Errorf("git %s timed out after %s: the remote is slow or unreachable; check `git remote -v`, your network and your credentials, then retry", strings.Join(args, " "), timeout)
	case errors.Is(ctx.Err(), context.Canceled):
		return "", fmt.Errorf("git %s was cancelled: %w", strings.Join(args, " "), ctx.Err())
	}
	return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
}
