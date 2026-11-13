//go:build !unix

package authoring

import "os/exec"

func boundProcess(cmd *exec.Cmd) func() { return func() {} }
