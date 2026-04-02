//go:build !windows

package provider

import (
	"errors"
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
