//go:build windows

package bridge

func processAlive(pid int) bool {
	// A fresh lock is protected by the startup grace period. Older Windows
	// locks are treated as stale because opening arbitrary PIDs would require
	// platform-specific privileges and risks denying recovery after a crash.
	return false
}
