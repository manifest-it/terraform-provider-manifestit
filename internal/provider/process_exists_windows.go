//go:build windows

package provider

import (
	"fmt"
	"os/exec"
	"strings"
)

func processExistsPlatform(pid int) bool {
	cmd := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return !strings.Contains(string(output), "No tasks")
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
