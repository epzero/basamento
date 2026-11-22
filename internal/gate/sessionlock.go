package gate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// cloneLockWait bounds how long `gate agent` commands wait for another writer
// in this clone. The longest healthy hold is a NewSession path that runs two
// sequential remote git calls (for example fastForwardBranch or
// reviewDetachSHA: originHasBranch via ls-remote, then fetch), each bounded by
// gitRemoteTimeout plus gitWaitDelay, plus local work. The budget is that
// worst-case hold plus 10s so a healthy but slow first call never makes a
// waiter on the second fail.
const cloneLockWait = 2*(gitRemoteTimeout+gitWaitDelay) + 10*time.Second

// errLockContended reports that the lock is held elsewhere. A lock failure for
// another reason (permissions, a read-only file system, no flock on this
// platform) is not contention and is reported as itself.
var errLockContended = errors.New("lock is held by another process")

type fileLock struct {
	f *os.File
}

// cloneLockPath returns the lock shared by every worktree of the repository.
// It lives in the common git directory, so main checkout, linked worktrees and
// their processes contend on the same file.
func cloneLockPath(dir string) (string, error) {
	common, err := git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return filepath.Join(common, "basamento-locks", "clone.lock"), nil
}

// acquireCloneLockWithContext takes the per-clone lock, giving up when ctx ends.
func acquireCloneLockWithContext(ctx context.Context, dir string) (func(), error) {
	p, err := cloneLockPath(dir)
	if err != nil {
		return nil, err
	}
	l, err := acquireFileLock(ctx, p)
	if err != nil {
		if errors.Is(err, errLockContended) {
			return nil, fmt.Errorf("another `gate agent` command in this clone may still be running (lock %s): %w; wait for it, then retry", p, err)
		}
		return nil, fmt.Errorf("cannot lock %s for this clone: %w", p, err)
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
