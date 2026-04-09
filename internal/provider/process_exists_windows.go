//go:build windows

package provider

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// processExistsPlatform checks whether a process is still alive using
// OpenProcess / GetExitCodeProcess — zero-overhead, no child process spawned.
func processExistsPlatform(pid int) bool {
	const processQueryLimitedInfo = 0x1000 // PROCESS_QUERY_LIMITED_INFORMATION
	h, err := syscall.OpenProcess(processQueryLimitedInfo, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	const stillActive = 259 // STILL_ACTIVE / STATUS_PENDING
	return code == stillActive
}

func getParentCommandLinePlatform(pid int) (string, error) {
	cmd := exec.Command("wmic", "process", "where",
		fmt.Sprintf("ProcessId=%d", pid), "get", "CommandLine", "/value")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "CommandLine=") {
			return strings.TrimPrefix(line, "CommandLine="), nil
		}
	}
	return "", fmt.Errorf("command line not found in wmic output")
}

// setSysProcAttr places the watcher subprocess in a new process group so that
// CTRL_C_EVENT sent to the parent's group does not reach it.
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x00000200, // CREATE_NEW_PROCESS_GROUP
	}
}

// reraiseSIGTERM performs the Windows equivalent of SIGTERM re-raise: exit(1).
// SIGTERM cannot be sent programmatically on Windows; exiting with a non-zero
// code is the conventional "terminated by signal" indicator.
func reraiseSIGTERM() {
	os.Exit(1)
}

// sendTestSIGTERM simulates a shutdown signal in the test suite on Windows.
// Windows cannot deliver SIGTERM; we send os.Interrupt (CTRL_C) instead, which
// is also registered in signal.Notify by registerSIGTERMHandler.
func sendTestSIGTERM(pid int) {
	p, err := os.FindProcess(pid)
	if err == nil {
		_ = p.Signal(os.Interrupt)
	}
}
