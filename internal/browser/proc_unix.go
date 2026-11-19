//go:build !windows

package browser

import (
	"os/exec"
	"syscall"
)

// newSession starts the browser in its own session, so the whole process
// group (cage plus the browser) can be stopped together.
func newSession(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

func terminate(cmd *exec.Cmd) { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }

func kill(cmd *exec.Cmd) { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
