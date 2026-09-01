//go:build !windows

package control

import (
	"fmt"
	"os"
)

func validatePrivateFileMode(mode os.FileMode) error {
	if mode.Perm() != 0600 {
		return fmt.Errorf("control state file must be owner-only")
	}
	return nil
}
func validatePrivateDirMode(mode os.FileMode) error {
	if mode.Perm() != 0700 {
		return fmt.Errorf("control state directory must be owner-only")
	}
	return nil
}
