//go:build !windows

package provider

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func processExistsPlatform(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func getParentCommandLinePlatform(pid int) (string, error) {
	out, err := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// setSysProcAttr puts the watcher subprocess into its own session so that
// go-plugin's SIGKILL on the provider process does not cascade to the watcher.
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// reraiseSIGTERM re-raises SIGTERM on the current process after our handler
// finishes so the process exits with the correct signal status.
func reraiseSIGTERM() {
	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
}

// sendTestSIGTERM sends SIGTERM to the given PID. Used by tests only.
func sendTestSIGTERM(pid int) {
	_ = syscall.Kill(pid, syscall.SIGTERM)
}
