//go:build unix && !solaris && !aix

package gate

import (
	"errors"
	"os/exec"
	"syscall"
)

// killWithProcessGroup makes a killed git take its transport helpers with it.
// git starts git-remote-https, ssh or a credential helper as children; killing
// only git can leave them running, and the caller in a different process group
// would then wait on pipes they still hold. WaitDelay still bounds that case.
func killWithProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return nil
	}
}
