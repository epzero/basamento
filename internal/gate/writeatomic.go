package gate

import (
	"errors"
	"syscall"
)

// dirSyncErr returns nil when syncing the parent directory after a successful
// rename can be skipped safely: the new file is already visible, and some
// network or FUSE mounts refuse directory fsync or cannot open the directory
// for sync even though the rename succeeded. EINVAL is the usual refusal;
// ErrUnsupported covers ENOTSUP, EOPNOTSUPP and ENOSYS without naming errnos
// that some platforms (plan9, windows, js, wasip1) do not define.
func dirSyncErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, errors.ErrUnsupported) {
		return nil
	}
	return err
}
