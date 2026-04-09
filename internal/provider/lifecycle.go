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
	RunID                   string `json:"run_id"`
	Action                  string `json:"action"`
	APIKey                  string `json:"api_key"`
	BaseURL                 string `json:"base_url"`
	OrgID                   string `json:"org_id"`
	ProviderID              string `json:"provider_id"`
	ProviderConfigurationID string `json:"provider_configuration_id"`
	OrgKey                  string `json:"org_key"`
	LockPath                string `json:"lock_path"`
	PPID                    int    `json:"ppid"`
	Identity                any    `json:"identity,omitempty"`
	Git                     any    `json:"git,omitempty"`
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
// The subprocess runs in its own session/process-group (platform-specific via
// setSysProcAttr) so go-plugin's SIGKILL on the provider process does not kill
// it. It polls terraform's PPID every 2s and fires PATCH /closed once terraform
// exits — after all providers finish.
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
	env := []string{
		"MIT_WATCHER_MODE=1",
		"MIT_WATCHER_STATE=" + statePath,
		"PATH=" + os.Getenv("PATH"),
	}
	// Only pass HOME when it is actually set — avoids "HOME=" in containers.
	if h := os.Getenv("HOME"); h != "" {
		env = append(env, "HOME="+h)
	}
	cmd.Env = append(env, proxyEnv()...)

	// Platform-specific: Setsid (unix) or CREATE_NEW_PROCESS_GROUP (windows).
	setSysProcAttr(cmd)

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

// cleanStaleFiles removes observer lock files, watcher state files, and log
// files in stateDir()/logDir() whose owner PPID is no longer alive. Called at
// apply start so files from crashed/killed previous runs don't accumulate.
func cleanStaleFiles() {
	stDir := stateDir()
	cleanStateDir(stDir)

	lgDir := logDir()
	cleanStaleLogs(lgDir)
	if lgDir != stDir {
		cleanStaleLogs(stDir)
	}
}

func cleanStateDir(dir string) {
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

// cleanStaleLogs removes watcher-terraform-{ppid}.log and provider-{ppid}.log
// files whose PPID is no longer alive.
func cleanStaleLogs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".log" {
			continue
		}
		var ppid int
		if _, err := fmt.Sscanf(name, "watcher-terraform-%d.log", &ppid); err == nil {
			if !processExists(ppid) {
				os.Remove(filepath.Join(dir, name))
			}
			continue
		}
		if _, err := fmt.Sscanf(name, "provider-%d.log", &ppid); err == nil {
			if !processExists(ppid) {
				os.Remove(filepath.Join(dir, name))
			}
		}
	}
}

// WatcherMain is the entry point when MIT_WATCHER_MODE=1.
// Polls terraform PPID and fires PATCH /closed when it exits.
// Also handles SIGTERM/Interrupt in case the container or CI job is cancelled.
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

	obs, err := buildProviderClient(state.APIKey, state.BaseURL, state.OrgID, state.OrgKey, state.ProviderID, state.ProviderConfigurationID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "manifestit-watcher: cannot build client: %v\n", err)
		os.Exit(1)
	}

	// Intercept SIGTERM and os.Interrupt (covers Linux CI job cancel and Windows
	// CTRL_C) so the watcher can fire PATCH /closed before exiting.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, os.Interrupt)
	defer signal.Stop(sigCh)

	// Poll loop with a 4-hour safety cap to guard against PID reuse / stale
	// results in container PID namespaces.
	pollDone := make(chan struct{})
	go func() {
		deadline := time.Now().Add(4 * time.Hour)
		for time.Now().Before(deadline) {
			time.Sleep(2 * time.Second)
			if !processExists(state.PPID) {
				break
			}
		}
		close(pollDone)
	}()

	select {
	case <-pollDone:
		fmt.Fprintf(os.Stderr, "manifestit-watcher: terraform exited, firing close\n")
	case sig := <-sigCh:
		fmt.Fprintf(os.Stderr, "manifestit-watcher: received signal %v, firing close\n", sig)
	}

	// Atomically claim the "fire close" role by removing the lock first.
	// If the lock is already gone the provider's SIGTERM handler already fired —
	// exit without duplicating the PATCH /closed call.
	if err := os.Remove(state.LockPath); err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "manifestit-watcher: lock already removed — close event fired by provider process\n")
			return
		}
		// Other error (permissions, etc.) — still attempt the close call.
		fmt.Fprintf(os.Stderr, "manifestit-watcher: lock remove error: %v — firing close anyway\n", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), observer.CloseDeadline)
	defer cancel()
	fireCloseEvent(ctx, obs, state.RunID, state)
}

// sigTermReraiseHook is overridden in tests to prevent killing the test process.
var sigTermReraiseHook func()

// registerSIGTERMHandler handles CI job teardown.
//
// CI runners send SIGTERM to the process group on job cancellation; the plugin
// shares terraform's PGID so it receives it directly. go-plugin only intercepts
// SIGINT, not SIGTERM. On Windows, CTRL_C_EVENT is mapped to os.Interrupt.
//
// ctx should be cancelled when the provider is shutting down normally so the
// goroutine can be collected (prevents goroutine leaks under goleak).
func registerSIGTERMHandler(ctx context.Context, cancel context.CancelFunc, obs observer.Client, runID string, state runState) {
	ch := make(chan os.Signal, 1)
	// syscall.SIGTERM: Linux/macOS CI job cancel.
	// os.Interrupt:   Windows CTRL_C + Linux Ctrl+C (covers both CI and interactive).
	signal.Notify(ch, syscall.SIGTERM, os.Interrupt)

	go func() {
		defer signal.Stop(ch)
		select {
		case <-ch:
		case <-ctx.Done():
			return // provider shutting down normally — no signal received
		}

		providerLog("SIGTERM/Interrupt — firing PATCH /closed")
		cancel()
		providerCloseOnce.Do(func() {
			// Atomically claim the "fire close" role by removing the lock first.
			// If the lock is already gone the watcher subprocess already fired —
			// exit without duplicating the PATCH /closed call.
			if err := os.Remove(state.LockPath); err != nil {
				if os.IsNotExist(err) {
					providerLog("lock already removed — close event fired by watcher")
					return
				}
				// Other error — still attempt the close call (best effort).
			}
			closeCtx, done := context.WithTimeout(context.Background(), observer.CloseDeadline)
			defer done()
			fireCloseEvent(closeCtx, obs, runID, state)
		})

		if sigTermReraiseHook != nil {
			sigTermReraiseHook()
		} else {
			// Platform-specific re-raise: syscall.Kill on unix, os.Exit(1) on Windows.
			reraiseSIGTERM()
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
		Status:        "closed",
		Identity:      identity,
		Git:           git,
		CollectedAt:   time.Now().UTC().Format(time.RFC3339),
		Action:        state.Action,
		OrgID:         state.OrgID,
		ProviderCfgID: state.ProviderConfigurationID,
		RunID:         state.RunID,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "manifestit: PATCH /closed FAILED run_id=%s: %v\n", runID, err)
	} else {
		fmt.Fprintf(os.Stderr, "manifestit: PATCH /closed OK run_id=%s\n", runID)
	}
}
