//go:build unix

package gate

import (
	"io/fs"
	"syscall"
	"testing"
)

// TestDirSyncErrUnixUnsupportedErrnos covers the errnos that unix maps to
// errors.ErrUnsupported. The generic test cannot name them because plan9,
// windows, js and wasip1 do not define all of them.
func TestDirSyncErrUnixUnsupportedErrnos(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"ENOTSUP", syscall.ENOTSUP},
		{"EOPNOTSUPP", syscall.EOPNOTSUPP},
		{"ENOSYS", syscall.ENOSYS},
		{"wrapped ENOTSUP", &fs.PathError{Op: "sync", Path: "/d", Err: syscall.ENOTSUP}},
		{"wrapped EOPNOTSUPP", &fs.PathError{Op: "sync", Path: "/d", Err: syscall.EOPNOTSUPP}},
		{"wrapped ENOSYS", &fs.PathError{Op: "sync", Path: "/d", Err: syscall.ENOSYS}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dirSyncErr(tc.err); got != nil {
				t.Fatalf("dirSyncErr(%v) = %v, want nil", tc.err, got)
			}
		})
	}
}
