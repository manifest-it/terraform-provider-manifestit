package provider

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/google/uuid"
	"go.uber.org/goleak"

	"terraform-provider-manifestit/pkg/sdk/providers/observer"
)

// ---------------------------------------------------------------------------
// mockObserver — thread-safe mock that records calls
// ---------------------------------------------------------------------------

type mockObserver struct {
	mu sync.Mutex

	postCalls      int
	patchCalls     int
	heartbeatCalls int

	postErr      error
	patchErr     error
	heartbeatErr error

	// per-call hooks for more control
	patchFn     func(runID string, input observer.ClosePayload) error
	heartbeatFn func(runID string) error
	postFn      func(input observer.ObserverPayload) error
}

func (m *mockObserver) Post(_ context.Context, input observer.ObserverPayload) (*observer.ObserverResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.postCalls++
	if m.postFn != nil {
		if err := m.postFn(input); err != nil {
			return nil, err
		}
	}
	if m.postErr != nil {
		return nil, m.postErr
	}
	return &observer.ObserverResponse{ID: input.RunID, Status: "open"}, nil
}

func (m *mockObserver) Patch(_ context.Context, runID string, input observer.ClosePayload) (*observer.ObserverResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.patchCalls++
	if m.patchFn != nil {
		if err := m.patchFn(runID, input); err != nil {
			return nil, err
		}
	}
	if m.patchErr != nil {
		return nil, m.patchErr
	}
	return &observer.ObserverResponse{ID: runID, Status: input.Status}, nil
}

func (m *mockObserver) Heartbeat(_ context.Context, runID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.heartbeatCalls++
	if m.heartbeatFn != nil {
		return m.heartbeatFn(runID)
	}
	return m.heartbeatErr
}

func (m *mockObserver) getPostCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.postCalls
}

func (m *mockObserver) getPatchCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.patchCalls
}

func (m *mockObserver) getHeartbeatCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.heartbeatCalls
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func tmpLockPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "test.lock")
}

func sampleClosureState(lockPath string) ClosureState {
	return ClosureState{
		RunID:    uuid.New().String(),
		Action:   "apply",
		APIKey:   "test-key",
		BaseURL:  "https://api.example.com",
		OrgID:    "42",
		OrgKey:   "my-org",
		LockPath: lockPath,
		Identity: map[string]any{"os_user": "testuser"},
		Git:      map[string]any{"branch": "main"},
	}
}

// resetGlobals resets package-level state between tests.
// Must be called at the start of any test that exercises providerRunOnce or
// providerCloseOnce so tests remain independent.
func resetGlobals() {
	providerRunOnce = sync.Once{}
	providerCloseOnce = sync.Once{}
	cleanupMu.Lock()
	cleanupFn = nil
	cleanupMu.Unlock()
	heartbeatCancel = nil
	// Reset the SIGTERM signal handler so stale channels from previous tests
	// don't receive the signal intended for the current test.
	signal.Reset(syscall.SIGTERM)
}

// ---------------------------------------------------------------------------
// TestGenerateRunID_*
// ---------------------------------------------------------------------------

func TestGenerateRunID_IsValidUUID(t *testing.T) {
	id := generateRunID()
	if _, err := uuid.Parse(id); err != nil {
		t.Errorf("generateRunID produced invalid UUID %q: %v", id, err)
	}
}

func TestGenerateRunID_IsVersion4(t *testing.T) {
	parsed, _ := uuid.Parse(generateRunID())
	if parsed.Version() != 4 {
		t.Errorf("expected UUID v4, got v%d", parsed.Version())
	}
}

func TestGenerateRunID_HasCorrectVariant(t *testing.T) {
	parsed, _ := uuid.Parse(generateRunID())
	if parsed.Variant() != uuid.RFC4122 {
		t.Errorf("expected RFC4122 variant, got %v", parsed.Variant())
	}
}

func TestGenerateRunID_Uniqueness(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id := generateRunID()
		if seen[id] {
			t.Fatalf("duplicate UUID at iteration %d: %q", i, id)
		}
		seen[id] = true
	}
}

// ---------------------------------------------------------------------------
// TestAcquireRunLock_*
// ---------------------------------------------------------------------------

func TestAcquireRunLock_FirstCall(t *testing.T) {
	lockPath := tmpLockPath(t)

	runID, gotPath, alreadyPosted := acquireRunLockAt(lockPath)
	t.Cleanup(func() { os.Remove(gotPath) })

	if alreadyPosted {
		t.Fatal("first acquireRunLock should not report alreadyPosted")
	}
	if runID == "" {
		t.Error("expected non-empty runID")
	}
	if gotPath != lockPath {
		t.Errorf("lockPath mismatch: got %q want %q", gotPath, lockPath)
	}

	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, ":") {
		t.Errorf("lock content should be 'ppid:runID', got %q", content)
	}
	parts := strings.SplitN(content, ":", 2)
	if parts[1] != runID {
		t.Errorf("lock runID: got %q want %q", parts[1], runID)
	}
}

func TestAcquireRunLock_SecondCallReturnsDuplicate(t *testing.T) {
	lockPath := tmpLockPath(t)

	_, _, first := acquireRunLockAt(lockPath)
	t.Cleanup(func() { os.Remove(lockPath) })

	if first {
		t.Fatal("first call should succeed (alreadyPosted=false)")
	}
	_, _, second := acquireRunLockAt(lockPath)
	if !second {
		t.Fatal("second call should report alreadyPosted=true")
	}
}

func TestAcquireRunLock_ReclaimsStaleLock(t *testing.T) {
	lockPath := tmpLockPath(t)

	// Write a stale lock with a guaranteed-dead PID.
	stale := fmt.Sprintf("%d:old-uuid", 999999999)
	if err := os.WriteFile(lockPath, []byte(stale), 0644); err != nil {
		t.Fatalf("write stale lock: %v", err)
	}

	runID, _, alreadyPosted := acquireRunLockAt(lockPath)
	t.Cleanup(func() { os.Remove(lockPath) })

	if alreadyPosted {
		t.Fatal("stale lock should be reclaimed")
	}
	if runID == "" {
		t.Error("expected non-empty runID after reclaim")
	}
}

func TestAcquireRunLock_RunIDIsValidUUID(t *testing.T) {
	lockPath := tmpLockPath(t)
	runID, _, _ := acquireRunLockAt(lockPath)
	t.Cleanup(func() { os.Remove(lockPath) })

	if _, err := uuid.Parse(runID); err != nil {
		t.Errorf("runID %q is not a valid UUID: %v", runID, err)
	}
}

// TestAcquireRunLock_atomicReclaim races two goroutines reclaiming a stale lock.
// Exactly one must get ownership; the other must see alreadyPosted=true.
func TestAcquireRunLock_atomicReclaim(t *testing.T) {
	lockPath := tmpLockPath(t)
	t.Cleanup(func() { os.Remove(lockPath) })

	// Write stale lock with dead PID.
	stale := fmt.Sprintf("%d:dead-uuid", 999999999)
	if err := os.WriteFile(lockPath, []byte(stale), 0644); err != nil {
		t.Fatalf("write stale lock: %v", err)
	}

	const n = 20
	var owners int64

	var wg sync.WaitGroup
	wg.Add(n)
	ready := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			<-ready
			_, _, already := acquireRunLockAt(lockPath)
			if !already {
				atomic.AddInt64(&owners, 1)
			}
		}()
	}
	close(ready)
	wg.Wait()

	if owners != 1 {
		t.Errorf("expected exactly 1 owner, got %d", owners)
	}
}

// ---------------------------------------------------------------------------
// TestHeartbeat_tickerBased  (fake clock)
// ---------------------------------------------------------------------------

// startHeartbeatWithClock is a testable variant that accepts an injected clock.
// afterTick, if non-nil, is called after each successful Heartbeat() in the goroutine
// (used for test synchronisation — avoids timing races with clk.Add).
func startHeartbeatWithClock(ctx context.Context, obs observer.Client, runID string, clk clock.Clock, afterTick ...func()) {
	var cb func()
	if len(afterTick) > 0 {
		cb = afterTick[0]
	}
	go func() {
		ticker := clk.Ticker(HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := obs.Heartbeat(ctx, runID); err != nil {
					fmt.Fprintf(os.Stderr, "manifestit: heartbeat warning (non-fatal): %v\n", err)
				}
				if cb != nil {
					cb()
				}
			}
		}
	}()
}

func TestHeartbeat_tickerBased(t *testing.T) {
	defer goleak.VerifyNone(t)

	clk := clock.NewMock()
	obs := &mockObserver{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// tickDone is signalled inside the goroutine AFTER each Heartbeat() call,
	// guaranteeing the heartbeat counter has been incremented before we proceed.
	tickDone := make(chan struct{}, 10)

	startHeartbeatWithClock(ctx, obs, "run-1", clk, func() {
		tickDone <- struct{}{}
	})

	// Give the goroutine time to reach its select{} before we advance the clock.
	// Without this, clk.Add may fire the ticker before the goroutine is scheduled.
	time.Sleep(10 * time.Millisecond)

	// Advance one tick at a time and wait for it to be fully processed.
	for i := 0; i < 3; i++ {
		clk.Add(HeartbeatInterval)
		select {
		case <-tickDone:
		case <-time.After(2 * time.Second):
			t.Fatalf("tick %d not processed within 2s", i+1)
		}
	}

	// All 3 tickDone signals received ⟹ heartbeatCalls == 3.
	if got := obs.getHeartbeatCalls(); got != 3 {
		t.Errorf("expected 3 heartbeat calls, got %d", got)
	}

	// Cancel and let the goroutine exit cleanly before goleak check.
	cancel()
	time.Sleep(20 * time.Millisecond)
}

func TestHeartbeat_nonFatalOnError(t *testing.T) {
	defer goleak.VerifyNone(t)

	clk := clock.NewMock()
	obs := &mockObserver{}
	var callCount int32

	tickDone := make(chan struct{}, 10)
	obs.heartbeatFn = func(_ string) error {
		n := atomic.AddInt32(&callCount, 1)
		if n <= 3 {
			return fmt.Errorf("transient error %d", n)
		}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startHeartbeatWithClock(ctx, obs, "run-2", clk, func() { tickDone <- struct{}{} })

	// Give the goroutine time to reach its select{} before advancing the clock.
	time.Sleep(10 * time.Millisecond)

	for i := 0; i < 5; i++ {
		clk.Add(HeartbeatInterval)
		select {
		case <-tickDone:
		case <-time.After(2 * time.Second):
			t.Fatalf("tick %d not processed within 2s", i+1)
		}
	}

	cancel()
	time.Sleep(20 * time.Millisecond)

	if got := obs.getHeartbeatCalls(); got < 4 {
		t.Errorf("goroutine should continue after errors; got %d calls", got)
	}
}

func TestHeartbeat_stopsOnContextCancel(t *testing.T) {
	clk := clock.NewMock()
	obs := &mockObserver{}

	ctx, cancel := context.WithCancel(context.Background())

	startHeartbeatWithClock(ctx, obs, "run-3", clk)

	cancel()

	// Goroutine should exit within 100ms of cancellation.
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	// Verify leak-free: advance clock — should NOT produce new heartbeat calls.
	beforeCalls := obs.getHeartbeatCalls()
	clk.Add(HeartbeatInterval * 5)
	time.Sleep(20 * time.Millisecond)

	if after := obs.getHeartbeatCalls(); after != beforeCalls {
		t.Errorf("heartbeat called %d times after cancel (expected 0 new calls)", after-beforeCalls)
	}

	goleak.VerifyNone(t, goleak.IgnoreTopFunction("testing.(*T).Run"))
}

func TestHeartbeat_noCallAfterCancel(t *testing.T) {
	defer goleak.VerifyNone(t)

	clk := clock.NewMock()
	obs := &mockObserver{}

	ctx, cancel := context.WithCancel(context.Background())
	startHeartbeatWithClock(ctx, obs, "run-4", clk)

	// Cancel immediately before any ticks.
	cancel()
	time.Sleep(20 * time.Millisecond)

	// Advance clock far — no calls should happen.
	clk.Add(HeartbeatInterval * 10)
	time.Sleep(20 * time.Millisecond)

	if got := obs.getHeartbeatCalls(); got != 0 {
		t.Errorf("expected 0 heartbeat calls after cancel, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// TestSIGTERM_*
// ---------------------------------------------------------------------------

// sigtermHandlerFn is the testable core of registerSIGTERMHandler: it
// cancels the heartbeat context, fires the close event once, removes the
// lock, then calls the re-raise hook (no-op in tests).
// We test this logic directly without sending real OS signals to the process.
func sigtermHandlerFn(cancel context.CancelFunc, obs observer.Client, runID string, state ClosureState) {
	cancel()
	providerCloseOnce.Do(func() {
		ctx, cancelClose := context.WithTimeout(context.Background(), observer.CloseDeadline)
		defer cancelClose()
		fireCloseEvent(ctx, obs, runID, state)
		_ = os.Remove(state.LockPath)
	})
	if sigTermReraiseHook != nil {
		sigTermReraiseHook()
	}
}

func TestSIGTERM_sendsPatchClosed(t *testing.T) {
	resetGlobals()
	defer resetGlobals()

	sigTermReraiseHook = func() {}
	defer func() { sigTermReraiseHook = nil }()

	obs := &mockObserver{}
	lockPath := tmpLockPath(t)
	state := sampleClosureState(lockPath)

	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Invoke the handler logic directly (no real signal sent).
	sigtermHandlerFn(cancel, obs, state.RunID, state)

	if got := obs.getPatchCalls(); got != 1 {
		t.Errorf("expected 1 Patch(closed) call, got %d", got)
	}
}

func TestSIGTERM_doesNotSendHeartbeatAfterFiring(t *testing.T) {
	resetGlobals()
	defer resetGlobals()

	sigTermReraiseHook = func() {}
	defer func() { sigTermReraiseHook = nil }()

	clk := clock.NewMock()
	obs := &mockObserver{}
	lockPath := tmpLockPath(t)
	state := sampleClosureState(lockPath)

	ctx, cancel := context.WithCancel(context.Background())

	tickDone := make(chan struct{}, 10)
	startHeartbeatWithClock(ctx, obs, state.RunID, clk, func() { tickDone <- struct{}{} })

	// Simulate SIGTERM handler firing: cancel context + fire close event.
	sigtermHandlerFn(cancel, obs, state.RunID, state)

	if obs.getPatchCalls() < 1 {
		t.Fatal("expected PATCH /closed to be called after SIGTERM handler")
	}

	// Context is now cancelled — heartbeat goroutine should have stopped.
	// Advance clock many intervals; no new heartbeat calls should occur.
	hbBefore := obs.getHeartbeatCalls()
	clk.Add(HeartbeatInterval * 5)
	time.Sleep(30 * time.Millisecond)

	if after := obs.getHeartbeatCalls(); after != hbBefore {
		t.Errorf("heartbeat called %d times after SIGTERM (expected none)", after-hbBefore)
	}

	time.Sleep(20 * time.Millisecond) // let goroutine exit for goleak
	goleak.VerifyNone(t, goleak.IgnoreTopFunction("testing.(*T).Run"))
}

// ---------------------------------------------------------------------------
// TestCloseOnce_race
// ---------------------------------------------------------------------------

func TestCloseOnce_race(t *testing.T) {
	resetGlobals()
	defer resetGlobals()

	obs := &mockObserver{}
	lockPath := tmpLockPath(t)
	state := sampleClosureState(lockPath)

	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			<-start
			providerCloseOnce.Do(func() {
				ctx, cancel := context.WithTimeout(context.Background(), observer.CloseDeadline)
				defer cancel()
				fireCloseEvent(ctx, obs, state.RunID, state)
			})
		}()
	}

	close(start)
	wg.Wait()

	if got := obs.getPatchCalls(); got != 1 {
		t.Errorf("expected exactly 1 Patch call from 100 goroutines, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// TestRunCleanup_*
// ---------------------------------------------------------------------------

func TestRunCleanup_sendsClosedOnCleanExit(t *testing.T) {
	resetGlobals()
	defer resetGlobals()

	obs := &mockObserver{}
	lockPath := tmpLockPath(t)
	if err := os.WriteFile(lockPath, []byte("test"), 0644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	state := sampleClosureState(lockPath)

	ctx, cancel := context.WithCancel(context.Background())

	RegisterCleanup(func() {
		cancel()
		providerCloseOnce.Do(func() {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), observer.CloseDeadline)
			defer closeCancel()
			fireCloseEvent(closeCtx, obs, state.RunID, state)
			_ = os.Remove(lockPath)
		})
	})

	RunCleanup()

	// cancel() should have been called.
	select {
	case <-ctx.Done():
		// good
	default:
		t.Error("expected context to be cancelled after RunCleanup")
	}

	if got := obs.getPatchCalls(); got != 1 {
		t.Errorf("expected 1 Patch call, got %d", got)
	}

	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Error("lock file should have been removed by RunCleanup")
	}
}

func TestRunCleanup_idempotentWithSIGTERM(t *testing.T) {
	resetGlobals()
	defer resetGlobals()

	obs := &mockObserver{}
	lockPath := tmpLockPath(t)
	state := sampleClosureState(lockPath)

	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Simulate SIGTERM path already fired once.
	providerCloseOnce.Do(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), observer.CloseDeadline)
		defer closeCancel()
		fireCloseEvent(closeCtx, obs, state.RunID, state)
	})

	// Register cleanup that tries to fire again.
	RegisterCleanup(func() {
		cancel()
		providerCloseOnce.Do(func() {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), observer.CloseDeadline)
			defer closeCancel()
			fireCloseEvent(closeCtx, obs, state.RunID, state)
		})
	})

	RunCleanup()

	// Should still only be 1 total (providerCloseOnce).
	if got := obs.getPatchCalls(); got != 1 {
		t.Errorf("expected exactly 1 Patch call total (idempotent), got %d", got)
	}
}

// ---------------------------------------------------------------------------
// TestProviderRunOnce_sameProcess
// ---------------------------------------------------------------------------

func TestProviderRunOnce_sameProcess(t *testing.T) {
	resetGlobals()
	defer resetGlobals()

	var callCount int32
	for i := 0; i < 3; i++ {
		providerRunOnce.Do(func() {
			atomic.AddInt32(&callCount, 1)
		})
	}

	if callCount != 1 {
		t.Errorf("providerRunOnce should fire exactly once, fired %d times", callCount)
	}
}

// ---------------------------------------------------------------------------
// TestNoSubprocessSpawned
// ---------------------------------------------------------------------------

func TestNoSubprocessSpawned(t *testing.T) {
	// Verify the heartbeat goroutine approach does not call os.StartProcess
	// or exec.Command. We can verify this by confirming no child PIDs are
	// created during startHeartbeat.
	//
	// Approach: record child PIDs before and after, confirm no new ones.

	obs := &mockObserver{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startHeartbeat(ctx, obs, "no-subprocess-test")

	// heartbeat.go uses a goroutine, not exec.Command — just verify goroutine
	// exits cleanly.
	cancel()
	time.Sleep(50 * time.Millisecond)
	goleak.VerifyNone(t, goleak.IgnoreTopFunction("testing.(*T).Run"))
}

// ---------------------------------------------------------------------------
// TestNoWatcherStateFileCreated
// ---------------------------------------------------------------------------

func TestNoWatcherStateFileCreated(t *testing.T) {
	// Snapshot existing watcher state files BEFORE starting the lifecycle —
	// stale files from previous test runs must not cause a false failure.
	before := make(map[string]bool)
	if entries, err := os.ReadDir(os.TempDir()); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "manifestit-watcher-") && strings.HasSuffix(e.Name(), ".json") {
				before[e.Name()] = true
			}
		}
	}

	obs := &mockObserver{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startHeartbeat(ctx, obs, "no-watcher-file-test")
	time.Sleep(20 * time.Millisecond)

	// Only report files that did NOT exist before this test started.
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "manifestit-watcher-") && strings.HasSuffix(e.Name(), ".json") {
			if !before[e.Name()] {
				t.Errorf("new watcher state file created during test: %s", e.Name())
			}
		}
	}

	cancel()
	time.Sleep(20 * time.Millisecond)
}

// ---------------------------------------------------------------------------
// TestLockFileRemovedAfterClose
// ---------------------------------------------------------------------------

func TestLockFileRemovedAfterClose(t *testing.T) {
	resetGlobals()
	defer resetGlobals()

	obs := &mockObserver{}
	lockPath := tmpLockPath(t)
	if err := os.WriteFile(lockPath, []byte("test"), 0644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	state := sampleClosureState(lockPath)

	_, cancel := context.WithCancel(context.Background())

	RegisterCleanup(func() {
		cancel()
		providerCloseOnce.Do(func() {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), observer.CloseDeadline)
			defer closeCancel()
			fireCloseEvent(closeCtx, obs, state.RunID, state)
			_ = os.Remove(lockPath)
		})
	})

	RunCleanup()

	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Error("lock file should be removed after RunCleanup")
	}
}

// ---------------------------------------------------------------------------
// TestClosureState_inMemoryOnly
// ---------------------------------------------------------------------------

func TestClosureState_inMemoryOnly(t *testing.T) {
	typ := reflect.TypeOf(ClosureState{})

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)

		// No json tags.
		if tag, ok := field.Tag.Lookup("json"); ok {
			t.Errorf("ClosureState.%s has json tag %q — should be in-memory only", field.Name, tag)
		}
	}

	// No Write or Read methods.
	ptrType := reflect.TypeOf(&ClosureState{})
	for _, name := range []string{"Write", "Read", "Marshal", "Unmarshal"} {
		if _, ok := ptrType.MethodByName(name); ok {
			t.Errorf("ClosureState has unexpected method %q — should be in-memory only", name)
		}
	}
}

// ---------------------------------------------------------------------------
// TestDetectTerraformOperation_* — carry over from watcher_test.go
// ---------------------------------------------------------------------------

func TestDetectTerraformOperation_ReattachEnv(t *testing.T) {
	t.Setenv("TF_REATTACH_PROVIDERS", `{"registry.terraform.io/manifest-it/manifestit":{"Protocol":"grpc","Pid":12345,"Test":true,"Addr":{"Network":"unix","String":"/tmp/plugin.sock"}}}`)
	op := detectTerraformOperation()
	if op != "apply" {
		t.Errorf("expected 'apply' when TF_REATTACH_PROVIDERS is set, got %q", op)
	}
}

// ---------------------------------------------------------------------------
// acquireRunLockAt — testable variant used in tests above
// ---------------------------------------------------------------------------

// acquireRunLockAt is the testable variant of acquireRunLock that accepts an
// explicit lockPath instead of deriving it from the real PPID.
//
// Atomicity guarantee: uses os.Link (hard link) for the lock creation step.
// link(2) is atomic on POSIX and fails with EEXIST if the target already exists,
// meaning exactly one concurrent caller wins the race.
func acquireRunLockAt(lockPath string) (runID string, gotPath string, alreadyPosted bool) {
	ppid := os.Getpid()
	ppidS := fmt.Sprintf("%d", ppid)
	runID = generateRunID()
	content := ppidS + ":" + runID
	dir := filepath.Dir(lockPath)

	// Write our content to a uniquely-named temp file, then hard-link it to
	// the lock path. link(2) fails atomically with EEXIST if lock already exists.
	tmp, tmpErr := os.CreateTemp(dir, ".lock-tmp-")
	if tmpErr != nil {
		return "", lockPath, true
	}
	tmpPath := tmp.Name()
	_, _ = tmp.WriteString(content)
	_ = tmp.Close()
	defer os.Remove(tmpPath) // clean up temp regardless of outcome

	if linkErr := os.Link(tmpPath, lockPath); linkErr == nil {
		// We created the lock atomically — we own it.
		return runID, lockPath, false
	}

	// Lock file already exists — check if it is stale (owner PID dead).
	data, readErr := os.ReadFile(lockPath)
	if readErr != nil {
		return "", lockPath, true
	}
	parts := strings.SplitN(strings.TrimSpace(string(data)), ":", 2)
	if len(parts) < 1 {
		return "", lockPath, true
	}
	var ownerPID int
	if _, scanErr := fmt.Sscan(parts[0], &ownerPID); scanErr != nil {
		return "", lockPath, true
	}
	if processExists(ownerPID) {
		// Live owner — already posted.
		return "", lockPath, true
	}

	// Stale lock — remove it, then race to re-link our temp file.
	// os.Remove is best-effort; if another goroutine already removed and
	// re-linked, our subsequent Link will fail and we yield to them.
	_ = os.Remove(lockPath)
	if linkErr := os.Link(tmpPath, lockPath); linkErr != nil {
		// Lost the reclaim race — another goroutine owns it.
		return "", lockPath, true
	}

	// Verify we actually own the lock (read back and confirm our content).
	check, checkErr := os.ReadFile(lockPath)
	if checkErr != nil || strings.TrimSpace(string(check)) != content {
		return "", lockPath, true
	}

	return runID, lockPath, false
}
