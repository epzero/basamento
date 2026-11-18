package gate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// issueLockWait bounds how long `gate agent new` waits for another session
// starting the same work item. Worktree creation is quick; a longer wait means
// a stuck process, and the error tells the operator what to check.
const issueLockWait = 30 * time.Second

type fileLock struct {
	f *os.File
}

// issueLockPath returns the lock shared by every worktree of the repository.
// It lives in the common git directory, so main checkout, linked worktrees and
// their processes contend on the same file.
func issueLockPath(dir, issue string) (string, error) {
	common, err := git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return filepath.Join(common, "basamento-locks", issue+".lock"), nil
}

// acquireIssueLock takes the per-issue lock for the rest of the call. The file
// is not removed on release: waiters hold its inode, and a third process would
// otherwise create a second lock at the same path.
func acquireIssueLock(dir, issue string) (func(), error) {
	p, err := issueLockPath(dir, issue)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), issueLockWait)
	defer cancel()
	l, err := acquireFileLock(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("another `gate agent new` for %s may still be running (lock %s): %w; wait for it, then retry", issue, p, err)
	}
	return l.release, nil
}

// acquireFileLock waits for an exclusive lock, growing the interval between
// attempts so waiters do not spin on the file. It gives up when ctx ends.
func acquireFileLock(ctx context.Context, path string) (*fileLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	wait := 10 * time.Millisecond
	for {
		ok, err := tryLock(f)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		if ok {
			return &fileLock{f: f}, nil
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = f.Close()
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil, fmt.Errorf("lock wait cancelled: %w", ctx.Err())
			}
			return nil, fmt.Errorf("lock wait timed out: %w", ctx.Err())
		case <-timer.C:
		}
		if wait < 200*time.Millisecond {
			wait *= 2
		}
	}
}

func (l *fileLock) release() {
	unlockFile(l.f)
	_ = l.f.Close() // the lock is released above; a close error adds nothing here
}
