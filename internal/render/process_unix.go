//go:build darwin || linux || freebsd || openbsd || netbsd

package render

import (
	"os"
	"os/exec"
	"syscall"
)

func configureProcess(command *exec.Cmd) { command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func terminateProcess(command *exec.Cmd) error {
	if command.Process == nil {
		return os.ErrProcessDone
	}
	return syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
}
func killProcess(command *exec.Cmd) error {
	if command.Process == nil {
		return os.ErrProcessDone
	}
	return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
}
