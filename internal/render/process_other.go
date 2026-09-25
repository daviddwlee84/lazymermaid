//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd

package render

import (
	"os"
	"os/exec"
)

func configureProcess(command *exec.Cmd)       {}
func terminateProcess(command *exec.Cmd) error { return killProcess(command) }
func killProcess(command *exec.Cmd) error {
	if command.Process == nil {
		return os.ErrProcessDone
	}
	return command.Process.Kill()
}
