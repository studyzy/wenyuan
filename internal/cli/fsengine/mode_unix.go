//go:build unix

package fsengine

import "os"

// osMode is the file-mode type the mode helpers accept.
type osMode = os.FileMode

// fsFileMode converts a permission value into the mode type.
func fsFileMode(perm int) osMode { return os.FileMode(perm) }

// applyMode asserts a file's permission bits. On Unix this is a real chmod,
// so a manual chmod +w is reverted on the next pass even with identical
// bytes.
func applyMode(path string, mode osMode) error {
	return os.Chmod(path, mode)
}
