//go:build unix

package cli

// Unix process primitives behind the fs daemon hooks: liveness via
// kill(pid, 0) and a setsid re-execution of this executable.

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// fsProcessAlive implements kill(pid, 0) semantics: success and EPERM both
// mean a live process, ESRCH means dead.
func fsProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// fsSignal sends one signal to a pid.
func fsSignal(pid int, sig os.Signal) error {
	return syscall.Kill(pid, sig.(syscall.Signal))
}

// fsSpawn re-executes exe in its own session so the child survives the
// shell. The starter exits without waiting; the child reparents.
func fsSpawn(exe string, args []string, stdin, stdout, stderr *os.File) (int, error) {
	cmd := exec.Command(exe, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}
