//go:build linux

package main

import (
	"os/exec"
	"syscall"
)

// setPdeathsig просит ядро прислать Chrome SIGKILL, если процесс бота умрёт
// (авария, kill -9, OOM) — работает только на Linux, куда и идёт деплой.
func setPdeathsig(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
