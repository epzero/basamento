package gate

import (
	"errors"
	"syscall"
)

// dirSyncErr returns nil when syncing the parent directory after a successful
// rename can be skipped safely: the new file is already visible, and some
// network or FUSE mounts refuse directory fsync or cannot open the directory
// for sync even though the rename succeeded.
func dirSyncErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EOPNOTSUPP) {
		return nil
	}
	return err
}
