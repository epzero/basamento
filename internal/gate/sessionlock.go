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
// starting the same work item. The holder keeps the lock while it decides and
// creates the worktree, which can include remote git calls: a reused worktree
// may check the remote and then fetch, two calls each bounded by
// gitRemoteTimeout plus gitWaitDelay. The budget is two such calls plus 10s of
// local work, so a healthy but slow first call never makes the second fail.
const issueLockWait = 2*(gitRemoteTimeout+gitWaitDelay) + 10*time.Second

// errLockContended reports that the lock is held elsewhere. A lock failure for
// another reason (permissions, a read-only file system, no flock on this
// platform) is not contention and is reported as itself.
var errLockContended = errors.New("lock is held by another process")

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
		if errors.Is(err, errLockContended) {
			return nil, fmt.Errorf("another `gate agent new` for %s may still be running (lock %s): %w; wait for it, then retry", issue, p, err)
		}
		return nil, fmt.Errorf("cannot lock %s for %s: %w", p, issue, err)
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
			return nil, fmt.Errorf("%w: gave up waiting for %s: %w", errLockContended, path, ctx.Err())
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
