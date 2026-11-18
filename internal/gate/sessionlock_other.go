//go:build !unix

package gate

import (
	"errors"
	"os"
)

func tryLock(*os.File) (bool, error) {
	return false, errors.New("per-issue session locks need a Unix-like system with flock")
}

func unlockFile(*os.File) {}
