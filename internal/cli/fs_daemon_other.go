//go:build !unix

package cli

// Non-Unix process primitives: parity with the reference, which refuses
// background daemons and reports nothing alive.

import (
	"errors"
	"os"
)

// fsProcessAlive always reports dead off Unix.
func fsProcessAlive(pid int) bool { return false }

// fsSignal is a no-op off Unix.
func fsSignal(pid int, sig os.Signal) error { return nil }

// fsSpawn refuses to fork off Unix.
func fsSpawn(exe string, args []string, stdin, stdout, stderr *os.File) (int, error) {
	return 0, errors.New("Background daemon mode is only supported on Unix platforms")
}
