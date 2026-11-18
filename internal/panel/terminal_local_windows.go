package panel

import (
	"os/exec"
	"syscall"
)

func hideLocalTerminalHelper(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
