//go:build !unix || solaris || aix

package gate

import (
	"errors"
	"os"
)

// errLockUnsupported says the platform has no flock, so two `gate agent new`
// calls for one issue cannot be serialised at all. It is not contention.
var errLockUnsupported = errors.New("per-issue session locks are not supported on this platform: it needs flock")

func tryLock(*os.File) (bool, error) {
	return false, errLockUnsupported
}

func unlockFile(*os.File) {}
