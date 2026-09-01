//go:build windows

package runtime

import (
	"os"
	"os/exec"
	"syscall"
)

func configureDetached(command *exec.Cmd) {
	const detachedProcess = 0x00000008
	const createNewProcessGroup = 0x00000200
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess | createNewProcessGroup}
}

func requestProcessStop(pid int) error { return killWindowsProcess(pid) }
func forceProcessStop(pid int) error   { return killWindowsProcess(pid) }

func killWindowsProcess(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}

func processAlive(pid int) bool {
	const stillActive = 259
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)
	var exitCode uint32
	if err := syscall.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false
	}
	return exitCode == stillActive
}
