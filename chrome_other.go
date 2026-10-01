//go:build !linux

package main

import "os/exec"

// На платформах без Pdeathsig (Windows, macOS) просто ничего не делаем — это не влияет
// на поведение при разработке и редактировании, реально бот запускается только на Linux.
func setPdeathsig(cmd *exec.Cmd) {}
