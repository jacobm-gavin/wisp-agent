//go:build !linux && !darwin

package bash

import "os/exec"

const supported = false

func configureProcess(cmd *exec.Cmd) {}
func killGroup(cmd *exec.Cmd) error  { return cmd.Process.Kill() }
