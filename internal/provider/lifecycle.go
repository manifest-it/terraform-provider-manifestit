package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"terraform-provider-manifestit/internal/collectors"
	"terraform-provider-manifestit/pkg/sdk/providers/observer"
)

var (
	providerRunOnce   sync.Once
	providerCloseOnce sync.Once
)

// runState carries everything the watcher subprocess needs to fire PATCH /closed.
// Serialised to a temp file; the subprocess reads and deletes it on startup.
type runState struct {
	RunID    string `json:"run_id"`
	Action   string `json:"action"`
	APIKey   string `json:"api_key"`
	BaseURL  string `json:"base_url"`
	OrgID    string `json:"org_id"`
	OrgKey   string `json:"org_key"`
	LockPath string `json:"lock_path"`
	PPID     int    `json:"ppid"`
	Identity any    `json:"identity,omitempty"`
	Git      any    `json:"git,omitempty"`
}

// providerLog writes to $HOME/.manifestit/provider-{ppid}.log.
// Best-effort — failures are silently ignored (never block terraform).
func providerLog(format string, args ...any) {
	dir := logDir()
	if mkErr := os.MkdirAll(dir, 0700); mkErr != nil {
		return
	}
	path := filepath.Join(dir, fmt.Sprintf("provider-%d.log", os.Getppid()))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "[%s] "+format+"\n", append([]any{time.Now().UTC().Format("15:04:05.000")}, args...)...)
}

// logDir returns $HOME/.manifestit when home is available, else $TMPDIR/.manifestit.
// $TMPDIR (/tmp) is always writable on Linux, macOS, and EKS containers.
func logDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".manifestit")
	}
	return filepath.Join(os.TempDir(), ".manifestit")
}

// stateDir returns $TMPDIR/.manifestit — used for lock and watcher state files.
// Always $TMPDIR (not home) because these must be writable in all environments
// including containers with read-only home dirs.
func stateDir() string {
	return filepath.Join(os.TempDir(), ".manifestit")
}

// writeRunState serialises state to $TMPDIR/.manifestit/watcher-*.json.
func writeRunState(s runState) (string, error) {
	dir := stateDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "watcher-*.json")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(s); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func readRunState(path string) (runState, error) {
	var s runState
	data, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(data, &s)
}

// spawnWatcher launches a detached copy of this binary in watcher mode.
//
// The subprocess runs in its own session (Setsid) so go-plugin's SIGKILL on
// the provider process does not kill it. It polls terraform's PPID every 2s
// and fires PATCH /closed once terraform exits — after all providers finish.
func spawnWatcher(statePath string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot resolve executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	// watcher log: prefer $HOME/.manifestit, fall back to $TMPDIR/.manifestit
	dir := logDir()
	if mkErr := os.MkdirAll(dir, 0700); mkErr != nil {
		dir = stateDir()
		_ = os.MkdirAll(dir, 0700)
	}
	logPath := filepath.Join(dir, fmt.Sprintf("watcher-terraform-%d.log", os.Getppid()))
	logFile, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)

	cmd := exec.Command(exe)
	cmd.Env = append([]string{
		"MIT_WATCHER_MODE=1",
		"MIT_WATCHER_STATE=" + statePath,
		"HOME=" + os.Getenv("HOME"),
		"PATH=" + os.Getenv("PATH"),
	}, proxyEnv()...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		if logFile != nil {
			logFile.Close()
		}
		return err
	}
	if logFile != nil {
		logFile.Close()
	}
	return nil
}

func proxyEnv() []string {
	var env []string
	for _, k := range []string{"SSL_CERT_FILE", "SSL_CERT_DIR", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// cleanStaleFiles removes observer lock files and watcher state files in
// stateDir() whose owner PPID is no longer alive. Called at apply start so
// files from crashed/killed previous runs don't accumulate.
func cleanStaleFiles() {
	dir := stateDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		// observer-{ppid}.lock
		if filepath.Ext(name) == ".lock" {
			var ppid int
			if _, err := fmt.Sscanf(name, "observer-%d.lock", &ppid); err == nil {
				if !processExists(ppid) {
					os.Remove(filepath.Join(dir, name))
				}
			}
			continue
		}
		// watcher-{random}.json — read PPID from content
		if filepath.Ext(name) == ".json" {
			path := filepath.Join(dir, name)
			if s, err := readRunState(path); err == nil && !processExists(s.PPID) {
				os.Remove(path)
			}
		}
	}
}

// WatcherMain is the entry point when MIT_WATCHER_MODE=1.
// Polls terraform PPID and fires PATCH /closed when it exits.
func WatcherMain() {
	statePath := os.Getenv("MIT_WATCHER_STATE")
	if statePath == "" {
		fmt.Fprintln(os.Stderr, "manifestit-watcher: MIT_WATCHER_STATE not set")
		os.Exit(1)
	}

	state, err := readRunState(statePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "manifestit-watcher: cannot read state: %v\n", err)
		os.Exit(1)
	}
	os.Remove(statePath) // contains credentials

	fmt.Fprintf(os.Stderr, "manifestit-watcher: run_id=%s ppid=%d base_url=%s\n", state.RunID, state.PPID, state.BaseURL)

	obs, err := buildProviderClient(state.APIKey, state.BaseURL, state.OrgID, state.OrgKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "manifestit-watcher: cannot build client: %v\n", err)
		os.Exit(1)
	}

	for {
		time.Sleep(2 * time.Second)
		if !processExists(state.PPID) {
			break
		}
	}

	fmt.Fprintf(os.Stderr, "manifestit-watcher: terraform exited, firing close\n")
	ctx, cancel := context.WithTimeout(context.Background(), observer.CloseDeadline)
	defer cancel()
	fireCloseEvent(ctx, obs, state.RunID, state)
	os.Remove(state.LockPath)
}

// sigTermReraiseHook is overridden in tests to prevent killing the test process.
var sigTermReraiseHook func()

// registerSIGTERMHandler handles CI job teardown.
// CI runners send SIGTERM to the process group; the plugin shares terraform's
// PGID so it receives it directly. go-plugin only intercepts SIGINT, not SIGTERM.
func registerSIGTERMHandler(cancel context.CancelFunc, obs observer.Client, runID string, state runState) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)

	go func() {
		defer signal.Stop(ch)
		select {
		case <-ch:
		}

		providerLog("SIGTERM — firing PATCH /closed")
		cancel()
		providerCloseOnce.Do(func() {
			ctx, done := context.WithTimeout(context.Background(), observer.CloseDeadline)
			defer done()
			fireCloseEvent(ctx, obs, runID, state)
			os.Remove(state.LockPath)
		})

		if sigTermReraiseHook != nil {
			sigTermReraiseHook()
		} else {
			syscall.Kill(os.Getpid(), syscall.SIGTERM)
		}
	}()
}

func fireCloseEvent(ctx context.Context, obs observer.Client, runID string, state runState) {
	identity, git := state.Identity, state.Git
	if identity == nil || git == nil {
		c := collectors.NewCollector(collectors.DefaultCollectConfig())
		r := c.Collect(ctx)
		if identity == nil {
			identity = r.Identity
		}
		if git == nil {
			git = r.Git
		}
	}

	_, err := obs.Patch(ctx, runID, observer.ClosePayload{
		Status:      "closed",
		Identity:    identity,
		Git:         git,
		CollectedAt: time.Now().UTC().Format(time.RFC3339),
		Action:      state.Action,
		OrgID:       state.OrgID,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "manifestit: PATCH /closed FAILED run_id=%s: %v\n", runID, err)
	} else {
		fmt.Fprintf(os.Stderr, "manifestit: PATCH /closed OK run_id=%s\n", runID)
	}
}
