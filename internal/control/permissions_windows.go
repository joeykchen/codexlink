//go:build windows

package control

import "os"

// Windows does not expose authoritative POSIX owner/group permission bits.
// Per-user state placement plus the regular-file, no-symlink and descriptor
// identity checks remain mandatory there.
func validatePrivateFileMode(os.FileMode) error { return nil }
func validatePrivateDirMode(os.FileMode) error  { return nil }
