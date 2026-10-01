//go:build !linux

package ruwiki

import "os/exec"

func setPdeathsig(cmd *exec.Cmd) {}
