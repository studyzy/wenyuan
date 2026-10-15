//go:build !unix

package fsengine

import "os"

// osMode is the file-mode type the mode helpers accept.
type osMode = os.FileMode

// fsFileMode converts a permission value into the mode type.
func fsFileMode(perm int) osMode { return os.FileMode(perm) }

// applyMode is a no-op off Unix: the chmod step is skipped entirely.
func applyMode(path string, mode osMode) error { return nil }
