//go:build !windows

package panel

import "os/exec"

func hideLocalTerminalHelper(cmd *exec.Cmd) {}
