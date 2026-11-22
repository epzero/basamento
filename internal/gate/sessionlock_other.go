//go:build !unix || solaris || aix

package gate

import (
	"errors"
	"os"
)

// errLockUnsupported says the platform has no flock, so writers in this clone
// cannot be serialised: `gate agent new`, `gate agent status` and
// `gate agent release` all fail here instead of running unlocked. The lock is
// per clone, not per issue. It is not contention.
var errLockUnsupported = errors.New("the per-clone session lock needs flock, which this platform does not support: agent new, agent status and agent release cannot run here")

func tryLock(*os.File) (bool, error) {
	return false, errLockUnsupported
}

func unlockFile(*os.File) {}
