package provider

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"terraform-provider-manifestit/internal/collectors"
	"terraform-provider-manifestit/pkg/sdk/providers/observer"
)

// --------------------------------------------------------------------------
// Package-level lifecycle guards
// --------------------------------------------------------------------------

// providerRunOnce ensures the entire open/heartbeat/close lifecycle is started
// at most once per process, even if Configure() is called multiple times
// (e.g. aliased provider blocks in the same terraform run).
var providerRunOnce sync.Once

// providerCloseOnce guarantees exactly one PATCH /closed per process.
// It is shared between the SIGTERM handler (CI path) and RunCleanup()
// (local / normal-exit path) so whichever fires first wins.
var providerCloseOnce sync.Once

// --------------------------------------------------------------------------
// HeartbeatInterval and related timing constants
// --------------------------------------------------------------------------

const (
	// HeartbeatInterval is how often the plugin sends a heartbeat to the server.
	HeartbeatInterval = 30 * time.Second

	// ServerTimeoutWindow is the documented server-side inactivity window.
	// The server marks an event "timed_out" if no heartbeat is received for
	// this duration. Two consecutive heartbeat failures must occur before
	// the server times out (30s interval × 2 = 60s < ServerTimeoutWindow).
	ServerTimeoutWindow = 60 * time.Second
)

// --------------------------------------------------------------------------
// ClosureState — in-memory only, no serialisation, no disk I/O
// --------------------------------------------------------------------------

// ClosureState carries the information needed to fire the "closed" event.
// It is populated once in Configure() and passed by value to both the SIGTERM
// handler goroutine and the RunCleanup deferred function in main.go.
//
// Design: no JSON tags, no file I/O — this struct exists only in memory
// for the lifetime of the provider process.
type ClosureState struct {
	RunID    string
	Action   string
	APIKey   string
	BaseURL  string
	OrgID    string
	OrgKey   string
	LockPath string

	// Pre-collected during Configure() so CI env vars are captured at the
	// right moment. The SIGTERM handler and RunCleanup run later, potentially
	// after env vars have been cleared.
	Identity any
	Git      any
}

// --------------------------------------------------------------------------
// Cleanup registry (called from main.go after Serve() returns)
// --------------------------------------------------------------------------

var (
	cleanupMu sync.Mutex
	cleanupFn func()
)

// RegisterCleanup stores a cleanup function to be called by RunCleanup().
// It is called once from Configure() after the heartbeat goroutine is started.
// If called multiple times (should not happen due to providerRunOnce), only
// the first registration is kept.
func RegisterCleanup(fn func()) {
	cleanupMu.Lock()
	defer cleanupMu.Unlock()
	if cleanupFn == nil {
		cleanupFn = fn
	}
}

// RunCleanup executes the registered cleanup function exactly once.
// Called from main.go immediately after providerserver.Serve() returns.
// Serve() blocks for the entire terraform run, so this is the correct
// clean-exit path for normal apply completion and ctrl+c (SIGINT handled
// by go-plugin which lets Serve() return cleanly).
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

// startHeartbeat starts a goroutine that sends a heartbeat to the server every
// HeartbeatInterval, proving the provider process is still alive.
//
// Why we replaced the watcher-subprocess / PPID-polling approach:
//
//  1. PPID polling fails for remote execution backends (TFC, Atlantis) where
//     the PPID is a long-lived agent process, not the terraform command itself.
//  2. The detached subprocess ran in its own session (Setsid=true). On CI, the
//     cgroup teardown could kill it before it fired the close event.
//  3. sync.Once is per-process. The subprocess and the provider process each had
//     their own sync.Once, so duplicate PATCH /closed calls were possible (BUG-1).
//  4. The 4-hour safety cap fired "closed" unconditionally, closing still-running
//     applies on the server (BUG-2).
//  5. The heartbeat model moves the "is this run still alive?" decision to the
//     server. The plugin simply proves liveness every 30 seconds. The server
//     marks a run "timed_out" after 60 seconds of silence — catching SIGKILL,
//     network partition, and remote-execution scenarios correctly.
//
// The goroutine exits cleanly when ctx is cancelled. Heartbeat errors are
// logged as warnings but never stop the goroutine — a transient error must not
// interrupt the heartbeat loop.
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
					fmt.Fprintf(os.Stderr, "manifestit: heartbeat warning (non-fatal): %v\n", err)
				}
			}
		}
	}()
}

// sigTermReraiseHook is called after the close event fires instead of the real
// syscall.Kill re-raise. It is nil in production (real kill used) and set to a
// no-op in unit tests so the test process is not terminated.
var sigTermReraiseHook func()

// --------------------------------------------------------------------------
// registerSIGTERMHandler — CI-reliable close path
// --------------------------------------------------------------------------

// registerSIGTERMHandler installs a SIGTERM-only handler in the provider process.
//
// This is the CI close path. When a CI runner tears down a job step it sends
// SIGTERM to the entire process group. Because go-plugin does NOT set Setpgid,
// the plugin inherits terraform's PGID and receives SIGTERM directly.
//
// go-plugin v1.7.0 only registers signal.Notify for os.Interrupt (SIGINT) and
// deliberately ignores it. SIGTERM is not registered by go-plugin, so our
// handler below is the first code to receive it.
//
// SIGINT is intentionally NOT handled here:
//   - go-plugin already registers SIGINT and handles it by letting Serve() return.
//   - Multiple signal.Notify registrations on the same channel are non-deterministic.
//   - When Serve() returns (including after ctrl+c), RunCleanup() fires the close
//     event via the normal exit path — no signal handler needed for SIGINT.
func registerSIGTERMHandler(cancel context.CancelFunc, obs observer.Client, runID string, state ClosureState) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)

	go func() {
		<-ch
		signal.Stop(ch)

		fmt.Fprintf(os.Stderr, "manifestit: caught SIGTERM — firing close event\n")

		// Stop the heartbeat goroutine before sending the close event.
		cancel()

		providerCloseOnce.Do(func() {
			ctx, cancelClose := context.WithTimeout(context.Background(), observer.CloseDeadline)
			defer cancelClose()
			fireCloseEvent(ctx, obs, runID, state)
			_ = os.Remove(state.LockPath)
		})

		// Re-raise SIGTERM so go-plugin and terraform receive it and exit normally.
		// Without this re-raise the process would hang waiting for gRPC shutdown.
		// In unit tests sigTermReraiseHook is set to a no-op to prevent killing the
		// test binary.
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

// fireCloseEvent sends the PATCH /closed event using pre-collected context.
// Falls back to re-collecting identity/git only if not already populated.
func fireCloseEvent(ctx context.Context, obs observer.Client, runID string, state ClosureState) {
	identity := state.Identity
	git := state.Git

	// Fallback: re-collect only if not pre-populated (should not happen in normal flow).
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
		fmt.Fprintf(os.Stderr, "manifestit: PATCH /closed failed: %v\n", patchErr)
	}
}
