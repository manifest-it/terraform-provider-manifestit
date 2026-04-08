package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"terraform-provider-manifestit/pkg/sdk/providers/observer"
)

// ---------------------------------------------------------------------------
// mockObserver
// ---------------------------------------------------------------------------

type mockObserver struct {
	mu         sync.Mutex
	postCalls  int
	patchCalls int
	patchErr   error
	postErr    error
	patchFn    func(runID string, input observer.ClosePayload) error
}

func (m *mockObserver) Post(_ context.Context, _ observer.ObserverPayload) (*observer.ObserverResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.postCalls++
	if m.postErr != nil {
		return nil, m.postErr
	}
	return &observer.ObserverResponse{}, nil
}

func (m *mockObserver) Patch(_ context.Context, runID string, input observer.ClosePayload) (*observer.ObserverResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.patchCalls++
	if m.patchFn != nil {
		return nil, m.patchFn(runID, input)
	}
	if m.patchErr != nil {
		return nil, m.patchErr
	}
	return &observer.ObserverResponse{}, nil
}

func (m *mockObserver) getPatchCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.patchCalls
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func tmpLockPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "test.lock")
}

func resetGlobals() {
	providerRunOnce = sync.Once{}
	providerCloseOnce = sync.Once{}
}

// acquireRunLockAt is the testable variant — accepts an explicit lockPath
// instead of deriving it from PPID so tests remain isolated.
func acquireRunLockAt(lockPath string) (runID, gotPath string, alreadyPosted bool) {
	runID = generateRunID()
	content := fmt.Sprintf("%d:%s", os.Getpid(), runID)
	dir := filepath.Dir(lockPath)

	tmp, err := os.CreateTemp(dir, ".lock-tmp-")
	if err != nil {
		return "", lockPath, true
	}
	tmpPath := tmp.Name()
	fmt.Fprint(tmp, content)
	tmp.Close()
	defer os.Remove(tmpPath)

	if err := os.Link(tmpPath, lockPath); err == nil {
		return runID, lockPath, false
	}

	data, err := os.ReadFile(lockPath)
	if err != nil {
		return "", lockPath, true
	}
	parts := strings.SplitN(strings.TrimSpace(string(data)), ":", 2)
	if len(parts) < 1 {
		return "", lockPath, true
	}
	var ownerPID int
	if _, err := fmt.Sscan(parts[0], &ownerPID); err != nil {
		return "", lockPath, true
	}
	if processExists(ownerPID) {
		return "", lockPath, true
	}

	os.Remove(lockPath)
	if err := os.Link(tmpPath, lockPath); err != nil {
		return "", lockPath, true
	}
	check, err := os.ReadFile(lockPath)
	if err != nil || strings.TrimSpace(string(check)) != content {
		return "", lockPath, true
	}
	return runID, lockPath, false
}

// ---------------------------------------------------------------------------
// Lock tests
// ---------------------------------------------------------------------------

func TestAcquireRunLock_FirstCall(t *testing.T) {
	lockPath := tmpLockPath(t)
	runID, _, alreadyPosted := acquireRunLockAt(lockPath)
	defer os.Remove(lockPath)

	if alreadyPosted {
		t.Fatal("first call should not report alreadyPosted")
	}
	if runID == "" {
		t.Fatal("expected non-empty runID")
	}
}

func TestAcquireRunLock_SecondCallReturnsDuplicate(t *testing.T) {
	lockPath := tmpLockPath(t)
	defer os.Remove(lockPath)

	_, _, first := acquireRunLockAt(lockPath)
	if first {
		t.Fatal("first call should succeed")
	}
	_, _, second := acquireRunLockAt(lockPath)
	if !second {
		t.Fatal("second call should report alreadyPosted=true")
	}
}

func TestAcquireRunLock_ReclaimsStaleLock(t *testing.T) {
	lockPath := tmpLockPath(t)
	_ = os.WriteFile(lockPath, []byte("999999999:old-uuid"), 0644)

	runID, _, alreadyPosted := acquireRunLockAt(lockPath)
	defer os.Remove(lockPath)

	if alreadyPosted {
		t.Fatal("stale lock should be reclaimed")
	}
	if runID == "" {
		t.Fatal("expected non-empty runID after reclaim")
	}
}

// TestAcquireRunLock_Race verifies exactly one winner across N concurrent goroutines.
func TestAcquireRunLock_Race(t *testing.T) {
	for iter := 0; iter < 30; iter++ {
		lockPath := filepath.Join(t.TempDir(), fmt.Sprintf("test-%d.lock", iter))
		var owners int64
		var wg sync.WaitGroup
		ready := make(chan struct{})

		for i := 0; i < 20; i++ {
			wg.Add(1)
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
		os.Remove(lockPath)

		if owners != 1 {
			t.Errorf("iter %d: expected 1 owner, got %d", iter, owners)
		}
	}
}

// ---------------------------------------------------------------------------
// SIGTERM handler
// ---------------------------------------------------------------------------

func TestSIGTERM_firesPatchClosed(t *testing.T) {
	resetGlobals()
	defer resetGlobals()

	sigTermReraiseHook = func() {}
	defer func() { sigTermReraiseHook = nil }()

	obs := &mockObserver{}
	lockPath := tmpLockPath(t)

	// Create the lock file so the cross-process guard in registerSIGTERMHandler
	// can atomically remove it (ErrNotExist means "already fired elsewhere").
	runID := generateRunID()
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("%d:%s", os.Getpid(), runID)), 0644); err != nil {
		t.Fatalf("create lock: %v", err)
	}

	state := runState{RunID: runID, Action: "apply", LockPath: lockPath}

	ctx, cancel := context.WithCancel(context.Background())

	registerSIGTERMHandler(ctx, cancel, obs, state.RunID, state)
	sendTestSIGTERM(os.Getpid())

	// Allow signal goroutine to fire.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if obs.getPatchCalls() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got := obs.getPatchCalls(); got != 1 {
		t.Errorf("expected 1 PATCH /closed call, got %d", got)
	}
}

func TestCloseOnce_race(t *testing.T) {
	resetGlobals()
	defer resetGlobals()

	obs := &mockObserver{}
	state := runState{RunID: generateRunID(), Action: "apply"}

	const n = 100
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		wg.Add(1)
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
		t.Errorf("expected exactly 1 PATCH from 100 goroutines, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// detectTerraformOperation
// ---------------------------------------------------------------------------

func TestDetectTerraformOperation_ReattachEnv(t *testing.T) {
	t.Setenv("TF_REATTACH_PROVIDERS", `{"registry.terraform.io/manifest-it/manifestit":{"Protocol":"grpc","Pid":1}}`)
	if op := detectTerraformOperation(); op != "apply" {
		t.Errorf("expected 'apply' when TF_REATTACH_PROVIDERS is set, got %q", op)
	}
}

// ---------------------------------------------------------------------------
// runState serialisation
// ---------------------------------------------------------------------------

func TestRunState_roundTrip(t *testing.T) {
	state := runState{
		RunID:    generateRunID(),
		Action:   "apply",
		APIKey:   "key",
		BaseURL:  "http://localhost",
		OrgID:    "1",
		OrgKey:   "org",
		LockPath: "/tmp/test.lock",
		PPID:     os.Getpid(),
	}

	path, err := writeRunState(state)
	if err != nil {
		t.Fatalf("writeRunState: %v", err)
	}
	defer os.Remove(path)

	got, err := readRunState(path)
	if err != nil {
		t.Fatalf("readRunState: %v", err)
	}
	if got.RunID != state.RunID {
		t.Errorf("RunID: got %q want %q", got.RunID, state.RunID)
	}
	if got.PPID != state.PPID {
		t.Errorf("PPID: got %d want %d", got.PPID, state.PPID)
	}
}
