//go:build windows

package pdf

import "os/exec"

func prepareProcessGroup(*exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
