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

// --------------------------------------------------------------------------
// Package-level lifecycle guards
// --------------------------------------------------------------------------

var providerRunOnce sync.Once
var providerCloseOnce sync.Once

// --------------------------------------------------------------------------
// providerLog — always-on file logger (no TF_LOG needed)
// --------------------------------------------------------------------------

// providerLog writes a timestamped line to $TMPDIR/manifestit-provider-{ppid}.log.
// This file is always created — you don't need TF_LOG=DEBUG to see provider activity.
// Check it after any terraform apply to see what the provider did.
func providerLog(format string, args ...any) {
	logPath := filepath.Join(os.TempDir(),
		fmt.Sprintf("manifestit-provider-%d.log", os.Getppid()))
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	ts := time.Now().UTC().Format("15:04:05.000")
	fmt.Fprintf(f, "[%s] "+format+"\n", append([]any{ts}, args...)...)
}

// --------------------------------------------------------------------------
// Timing constants
// --------------------------------------------------------------------------

const (
	HeartbeatInterval   = 30 * time.Second
	ppidPollInterval    = 2 * time.Second
	ServerTimeoutWindow = 60 * time.Second
)

// --------------------------------------------------------------------------
// ClosureState — serialised to disk for the watcher subprocess
// --------------------------------------------------------------------------

// ClosureState carries everything the watcher subprocess needs to fire the
// closed event. Written to a temp file by the provider process; read by
// the watcher subprocess (MIT_WATCHER_MODE=1).
type ClosureState struct {
	RunID    string `json:"run_id"`
	Action   string `json:"action"`
	APIKey   string `json:"api_key"`
	BaseURL  string `json:"base_url"`
	OrgID    string `json:"org_id"`
	OrgKey   string `json:"org_key"`
	LockPath string `json:"lock_path"`
	PPID     int    `json:"ppid"`

	Identity any `json:"identity,omitempty"`
	Git      any `json:"git,omitempty"`
}

func writeClosureState(state ClosureState) (string, error) {
	f, err := os.CreateTemp("", "manifestit-watcher-*.json")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(state); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func readClosureState(path string) (ClosureState, error) {
	var s ClosureState
	data, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(data, &s)
}

// --------------------------------------------------------------------------
// Cleanup registry — called from main.go after Serve() returns
// --------------------------------------------------------------------------

var (
	cleanupMu sync.Mutex
	cleanupFn func()
)

// RegisterCleanup stores fn to be called by RunCleanup().
// Only the first registration is kept.
func RegisterCleanup(fn func()) {
	cleanupMu.Lock()
	defer cleanupMu.Unlock()
	if cleanupFn == nil {
		cleanupFn = fn
	}
}

// RunCleanup is called from main.go after Serve() returns.
// By the time Serve() returns, go-plugin is about to SIGKILL the process.
// The watcher subprocess already handles PATCH /closed.
// This just cancels the heartbeat goroutine so it exits cleanly.
func RunCleanup() {
	cleanupMu.Lock()
	fn := cleanupFn
	cleanupMu.Unlock()
	if fn != nil {
		fn()
	}
}

// --------------------------------------------------------------------------
// startHeartbeat
// --------------------------------------------------------------------------

// startHeartbeat starts a goroutine that sends PATCH {status:heartbeat} every
// HeartbeatInterval. The goroutine exits cleanly when ctx is cancelled.
// Errors are non-fatal — a transient network error must not stop the loop.
func startHeartbeat(ctx context.Context, obs observer.Client, runID string) {
	go func() {
		ticker := time.NewTicker(HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := obs.Heartbeat(ctx, runID); err != nil {
					fmt.Fprintf(os.Stderr, "manifestit: heartbeat warning: %v\n", err)
				}
			}
		}
	}()
}

// --------------------------------------------------------------------------
// spawnWatcher — detached self-re-exec subprocess
// --------------------------------------------------------------------------

// spawnWatcher spawns a detached copy of this binary with MIT_WATCHER_MODE=1.
//
// Why a subprocess and not a goroutine:
//
//	Terraform's go-plugin calls client.Kill() on the provider after the gRPC
//	connection closes. Kill() sends SIGKILL to the plugin process — which cannot
//	be caught. A goroutine inside the plugin process dies with it. A subprocess
//	running in its own session (Setsid=true) is NOT in the plugin's process
//	group and survives the SIGKILL, giving it time to poll PPID and fire
//	PATCH /closed after ALL providers (AWS, GCP, etc.) have finished.
//
// The subprocess:
//   - reads ClosureState from the temp file at statePath
//   - polls PPID (terraform binary) every 2s
//   - fires PATCH /closed when PPID exits (= all providers done)
//   - removes the state file and lock file, then exits
func spawnWatcher(statePath string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find executable: %w", err)
	}

	// Resolve symlinks — on some systems os.Executable returns a symlink.
	// The subprocess must exec the real file.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	// Log file written to TempDir — readable even after the provider process
	// is SIGKILL'd by go-plugin, since the subprocess holds the fd open.
	logPath := filepath.Join(os.TempDir(), fmt.Sprintf("manifestit-watcher-%d.log", os.Getppid()))
	logFile, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)

	cmd := exec.Command(exe)
	cmd.Env = []string{
		"MIT_WATCHER_MODE=1",
		"MIT_WATCHER_STATE=" + statePath,
		"HOME=" + os.Getenv("HOME"),
		"PATH=" + os.Getenv("PATH"),
	}
	for _, key := range []string{
		"SSL_CERT_FILE", "SSL_CERT_DIR",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
		"http_proxy", "https_proxy", "no_proxy",
	} {
		if v := os.Getenv(key); v != "" {
			cmd.Env = append(cmd.Env, key+"="+v)
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil

	if startErr := cmd.Start(); startErr != nil {
		if logFile != nil {
			logFile.Close()
		}
		return startErr
	}
	// Close the parent's copy of the fd — the child has its own copy.
	if logFile != nil {
		logFile.Close()
	}
	return nil
}

// --------------------------------------------------------------------------
// WatcherMain — entry point when MIT_WATCHER_MODE=1
// --------------------------------------------------------------------------

// WatcherMain is called by main.go when MIT_WATCHER_MODE=1.
// It polls PPID (terraform) and fires PATCH /closed when it exits.
func WatcherMain() {
	statePath := os.Getenv("MIT_WATCHER_STATE")
	if statePath == "" {
		fmt.Fprintln(os.Stderr, "manifestit-watcher: MIT_WATCHER_STATE not set")
		os.Exit(1)
	}

	state, err := readClosureState(statePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "manifestit-watcher: cannot read state: %v\n", err)
		os.Exit(1)
	}
	// Remove the state file immediately — it contains credentials.
	_ = os.Remove(statePath)

	fmt.Fprintf(os.Stderr, "manifestit-watcher: started, run_id=%s ppid=%d base_url=%s\n",
		state.RunID, state.PPID, state.BaseURL)

	obs, err := buildObserverFromState(state)
	if err != nil {
		fmt.Fprintf(os.Stderr, "manifestit-watcher: cannot build observer: %v\n", err)
		os.Exit(1)
	}

	// Poll until terraform (PPID) exits.
	fmt.Fprintf(os.Stderr, "manifestit-watcher: polling ppid=%d every %s\n", state.PPID, ppidPollInterval)
	for {
		time.Sleep(ppidPollInterval)
		if !processExists(state.PPID) {
			break
		}
	}

	fmt.Fprintf(os.Stderr, "manifestit-watcher: ppid=%d exited, firing close event at %s\n",
		state.PPID, time.Now().UTC().Format(time.RFC3339))
	ctx, cancel := context.WithTimeout(context.Background(), observer.CloseDeadline)
	defer cancel()
	fireCloseEvent(ctx, obs, state.RunID, state)
	_ = os.Remove(state.LockPath)
	fmt.Fprintf(os.Stderr, "manifestit-watcher: done at %s\n", time.Now().UTC().Format(time.RFC3339))
}

// --------------------------------------------------------------------------
// registerSIGTERMHandler — CI close path
// --------------------------------------------------------------------------

// sigTermReraiseHook replaces syscall.Kill in tests to prevent killing the
// test process. Nil in production.
var sigTermReraiseHook func()

// registerSIGTERMHandler installs a SIGTERM handler for the CI close path.
//
// In CI, the runner sends SIGTERM to the process group when tearing down a
// job step. The plugin shares terraform's PGID (go-plugin does not set
// Setsid), so it receives SIGTERM directly. go-plugin only intercepts SIGINT,
// so our handler fires first.
//
// On SIGTERM:
//  1. cancel() stops the heartbeat goroutine.
//  2. providerCloseOnce fires PATCH /closed immediately (no need to wait
//     for PPID — the CI job is being killed so terraform is also dying).
//  3. SIGTERM is re-raised so go-plugin and terraform exit normally.
//
// The goroutine also exits when ctx is cancelled (normal plugin exit path),
// preventing a goroutine leak in the local run case where SIGTERM never fires.
func registerSIGTERMHandler(ctx context.Context, cancel context.CancelFunc, obs observer.Client, runID string, state ClosureState) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)

	go func() {
		defer signal.Stop(ch)
		select {
		case <-ctx.Done():
			providerLog("SIGTERM handler exiting cleanly (context cancelled — normal exit)")
			return
		case <-ch:
		}

		providerLog("SIGTERM received — firing PATCH /closed immediately")
		fmt.Fprintf(os.Stderr, "manifestit: caught SIGTERM — firing close event\n")
		cancel()

		providerCloseOnce.Do(func() {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), observer.CloseDeadline)
			defer closeCancel()
			fireCloseEvent(closeCtx, obs, runID, state)
			_ = os.Remove(state.LockPath)
		})

		if sigTermReraiseHook != nil {
			sigTermReraiseHook()
		} else {
			_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		}
	}()
}

// --------------------------------------------------------------------------
// fireCloseEvent
// --------------------------------------------------------------------------

func fireCloseEvent(ctx context.Context, obs observer.Client, runID string, state ClosureState) {
	identity := state.Identity
	git := state.Git

	// Fallback re-collect only if pre-collection was skipped (should not
	// happen in normal flow — identity/git are collected in Configure()).
	if identity == nil || git == nil {
		c := collectors.NewCollector(collectors.DefaultCollectConfig())
		result := c.Collect(ctx)
		if identity == nil {
			identity = result.Identity
		}
		if git == nil {
			git = result.Git
		}
	}

	_, patchErr := obs.Patch(ctx, runID, observer.ClosePayload{
		Status:      "closed",
		Identity:    identity,
		Git:         git,
		CollectedAt: time.Now().UTC().Format(time.RFC3339),
		Action:      state.Action,
		OrgID:       state.OrgID,
	})
	if patchErr != nil {
		providerLog("PATCH /closed FAILED (run_id=%s base_url=%s): %v", runID, state.BaseURL, patchErr)
		fmt.Fprintf(os.Stderr, "manifestit: PATCH /closed FAILED (run_id=%s base_url=%s): %v\n",
			runID, state.BaseURL, patchErr)
	} else {
		providerLog("PATCH /closed OK (run_id=%s)", runID)
		fmt.Fprintf(os.Stderr, "manifestit: PATCH /closed OK (run_id=%s)\n", runID)
	}
}

// --------------------------------------------------------------------------
// watcherStatePath / buildObserverFromState
// --------------------------------------------------------------------------

func watcherStatePath(ppid int) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("manifestit-watcher-%d.json", ppid))
}

func buildObserverFromState(state ClosureState) (observer.Client, error) {
	return buildProviderClient(state.APIKey, state.BaseURL, state.OrgID, state.OrgKey)
}
