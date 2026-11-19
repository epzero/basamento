//go:build !unix || solaris || aix

package gate

import "os/exec"

// killWithProcessGroup is a no-op where syscall.Setpgid is unavailable; the
// command's WaitDelay still bounds the call if a helper outlives git.
func killWithProcessGroup(*exec.Cmd) {}
