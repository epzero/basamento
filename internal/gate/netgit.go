package gate

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// gitRemoteTimeout bounds git commands that talk to a remote, so an
// unreachable origin fails `gate agent new` instead of hanging it forever.
const gitRemoteTimeout = 30 * time.Second

// gitRemote runs a remote-facing git command with gitRemoteTimeout.
// Commands that do not touch the network keep using git.
func gitRemote(dir string, args ...string) (string, error) {
	return gitRemoteContext(context.Background(), dir, args...)
}

func gitRemoteContext(parent context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, gitRemoteTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "", fmt.Errorf("git %s timed out after %s: the remote is slow or unreachable; check `git remote -v`, your network and your credentials, then retry", strings.Join(args, " "), gitRemoteTimeout)
	case errors.Is(ctx.Err(), context.Canceled):
		return "", fmt.Errorf("git %s was cancelled: %w", strings.Join(args, " "), ctx.Err())
	}
	return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
}
