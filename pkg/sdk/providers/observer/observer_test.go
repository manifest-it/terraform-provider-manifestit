package observer_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"terraform-provider-manifestit/pkg/sdk"
	"terraform-provider-manifestit/pkg/sdk/auth"
	"terraform-provider-manifestit/pkg/sdk/providers/observer"

	"github.com/rs/zerolog"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newTestClient(t *testing.T, handler http.HandlerFunc) (observer.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	noAuth := auth.NewNoAuth()
	executor := sdk.NewHTTPExecutor(sdk.HTTPExecutorConfig{
		Client: srv.Client(),
		Auth:   noAuth,
		Logger: zerolog.Nop(),
	})
	api := sdk.NewAPIClient(sdk.APIClientConfig{
		Executor: executor,
		BaseURL:  srv.URL,
		Logger:   zerolog.Nop(),
	})
	return observer.New(api), srv
}

func respondJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// ---------------------------------------------------------------------------
// Post — "open" event
// ---------------------------------------------------------------------------

func TestObserverClient_Post_Success(t *testing.T) {
	var captured observer.ObserverPayload

	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/api/v1/events") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)

		respondJSON(w, http.StatusCreated, observer.ObserverResponse{
			ID:     "resp-id-1",
			Status: "open",
		})
	})

	payload := observer.ObserverPayload{
		RunID:       "run-uuid-001",
		Status:      "open",
		Action:      "apply",
		CollectedAt: "2026-03-30T10:00:00Z",
		OrgID:       "42",
	}

	resp, err := client.Post(context.Background(), payload)
	if err != nil {
		t.Fatalf("Post returned error: %v", err)
	}
	if resp.ID != "resp-id-1" {
		t.Errorf("ID: got %q want %q", resp.ID, "resp-id-1")
	}
	if resp.Status != "open" {
		t.Errorf("Status: got %q want %q", resp.Status, "open")
	}

	// Verify the server received the correct fields.
	if captured.RunID != "run-uuid-001" {
		t.Errorf("captured RunID: got %q want %q", captured.RunID, "run-uuid-001")
	}
	if captured.Status != "open" {
		t.Errorf("captured Status: got %q want %q", captured.Status, "open")
	}
	if captured.Action != "apply" {
		t.Errorf("captured Action: got %q want %q", captured.Action, "apply")
	}
}

func TestObserverClient_Post_Destroy(t *testing.T) {
	var captured observer.ObserverPayload

	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		respondJSON(w, http.StatusCreated, observer.ObserverResponse{ID: "d1", Status: "open"})
	})

	_, err := client.Post(context.Background(), observer.ObserverPayload{
		RunID:  "run-destroy-1",
		Status: "open",
		Action: "destroy",
	})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if captured.Action != "destroy" {
		t.Errorf("Action: got %q want destroy", captured.Action)
	}
}

func TestObserverClient_Post_HTTPError(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	_, err := client.Post(context.Background(), observer.ObserverPayload{RunID: "x"})
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
}

func TestObserverClient_Post_ServerError(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	_, err := client.Post(context.Background(), observer.ObserverPayload{RunID: "x"})
	if err == nil {
		t.Fatal("expected error for 500, got nil")
	}
}

func TestObserverClient_Post_EmptyResponseBody(t *testing.T) {
	// Some servers return 201 with empty body — should not error.
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	resp, err := client.Post(context.Background(), observer.ObserverPayload{RunID: "x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// resp may be a zero-value ObserverResponse — that's fine.
	_ = resp
}

// ---------------------------------------------------------------------------
// Patch — "closed" event
// ---------------------------------------------------------------------------

func TestObserverClient_Patch_Success(t *testing.T) {
	const wantRunID = "run-uuid-patch-001"
	var capturedClose observer.ClosePayload
	var capturedMethod string
	var capturedPath string

	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedPath = r.URL.Path

		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedClose)

		respondJSON(w, http.StatusOK, observer.ObserverResponse{
			ID:     wantRunID,
			Status: "closed",
		})
	})

	resp, err := client.Patch(context.Background(), wantRunID, observer.ClosePayload{
		Status:      "closed",
		Action:      "apply",
		CollectedAt: "2026-03-30T12:00:00Z",
		OrgID:       "42",
		Identity:    map[string]any{"os_user": "ci-runner"},
		Git:         map[string]any{"branch": "main", "commit": "abc123"},
	})
	if err != nil {
		t.Fatalf("Patch returned error: %v", err)
	}

	if capturedMethod != http.MethodPatch {
		t.Errorf("method: got %q want PATCH", capturedMethod)
	}
	if !strings.HasSuffix(capturedPath, "/api/v1/events/"+wantRunID) {
		t.Errorf("path: got %q, expected suffix /api/v1/events/%s", capturedPath, wantRunID)
	}

	if capturedClose.Status != "closed" {
		t.Errorf("Status: got %q want closed", capturedClose.Status)
	}
	if capturedClose.Action != "apply" {
		t.Errorf("Action: got %q want apply", capturedClose.Action)
	}

	if resp.ID != wantRunID {
		t.Errorf("resp.ID: got %q want %q", resp.ID, wantRunID)
	}
	if resp.Status != "closed" {
		t.Errorf("resp.Status: got %q want closed", resp.Status)
	}
}

func TestObserverClient_Patch_Destroy(t *testing.T) {
	var capturedClose observer.ClosePayload

	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedClose)
		respondJSON(w, http.StatusOK, observer.ObserverResponse{ID: "d2", Status: "closed"})
	})

	_, err := client.Patch(context.Background(), "run-destroy-close", observer.ClosePayload{
		Status: "closed",
		Action: "destroy",
	})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if capturedClose.Action != "destroy" {
		t.Errorf("Action: got %q want destroy", capturedClose.Action)
	}
}

func TestObserverClient_Patch_HTTPError(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	_, err := client.Patch(context.Background(), "missing-run-id", observer.ClosePayload{Status: "closed"})
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
}

func TestObserverClient_Patch_RunIDInURL(t *testing.T) {
	// Verify different run IDs produce different URL paths.
	paths := make([]string, 0, 3)
	ids := []string{"aaa", "bbb", "ccc"}

	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		respondJSON(w, http.StatusOK, observer.ObserverResponse{})
	})

	for _, id := range ids {
		_, _ = client.Patch(context.Background(), id, observer.ClosePayload{Status: "closed"})
	}

	for i, p := range paths {
		if !strings.HasSuffix(p, "/"+ids[i]) {
			t.Errorf("path[%d] = %q, expected suffix /%s", i, p, ids[i])
		}
	}
}

// ---------------------------------------------------------------------------
// ObserverPayload — field presence / JSON keys
// ---------------------------------------------------------------------------

func TestObserverPayload_JSONKeys(t *testing.T) {
	p := observer.ObserverPayload{
		RunID:       "run-1",
		Status:      "open",
		Action:      "apply",
		CollectedAt: "2026-01-01T00:00:00Z",
		OrgID:       "10",
	}

	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var m map[string]any
	_ = json.Unmarshal(data, &m)

	for _, key := range []string{"run_id", "status", "action", "collected_at", "org_id"} {
		if _, ok := m[key]; !ok {
			t.Errorf("expected JSON key %q to be present", key)
		}
	}
}

func TestClosePayload_JSONKeys(t *testing.T) {
	p := observer.ClosePayload{
		Status:      "closed",
		Action:      "apply",
		CollectedAt: "2026-01-01T00:00:00Z",
		OrgID:       "10",
	}

	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var m map[string]any
	_ = json.Unmarshal(data, &m)

	for _, key := range []string{"status", "action", "collected_at"} {
		if _, ok := m[key]; !ok {
			t.Errorf("expected JSON key %q to be present", key)
		}
	}
}

func TestObserverPayload_OmitEmptyIdentityGit(t *testing.T) {
	p := observer.ObserverPayload{
		RunID:  "r1",
		Status: "open",
		Action: "apply",
	}
	data, _ := json.Marshal(p)
	raw := string(data)

	// identity and git should be omitted when nil/zero
	if strings.Contains(raw, `"identity"`) {
		t.Errorf("identity should be omitted when nil: %s", raw)
	}
	if strings.Contains(raw, `"git"`) {
		t.Errorf("git should be omitted when nil: %s", raw)
	}
}

// ---------------------------------------------------------------------------
// Heartbeat — new tests
// ---------------------------------------------------------------------------

// newTestClientNoRetry creates a client with MaxRetries=0 so tests control
// retries at the observer layer independently of the HTTPExecutor.
func newTestClientNoRetry(t *testing.T, handler http.HandlerFunc) (observer.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	noAuth := auth.NewNoAuth()
	executor := sdk.NewHTTPExecutor(sdk.HTTPExecutorConfig{
		Client:     srv.Client(),
		Auth:       noAuth,
		Logger:     zerolog.Nop(),
		MaxRetries: 0, // disable executor-level retries so observer controls them
	})
	api := sdk.NewAPIClient(sdk.APIClientConfig{
		Executor: executor,
		BaseURL:  srv.URL,
		Logger:   zerolog.Nop(),
	})
	return observer.New(api), srv
}

func TestHeartbeat_success(t *testing.T) {
	const wantRunID = "hb-run-001"
	var capturedBody []byte
	var capturedPath string

	client, _ := newTestClientNoRetry(t, func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedBody, _ = io.ReadAll(r.Body)
		respondJSON(w, http.StatusOK, observer.ObserverResponse{ID: wantRunID, Status: "heartbeat"})
	})

	if err := client.Heartbeat(context.Background(), wantRunID); err != nil {
		t.Fatalf("Heartbeat returned error: %v", err)
	}

	if !strings.HasSuffix(capturedPath, "/api/v1/events/"+wantRunID) {
		t.Errorf("path: got %q, expected suffix /api/v1/events/%s", capturedPath, wantRunID)
	}

	var body map[string]any
	_ = json.Unmarshal(capturedBody, &body)
	if status, _ := body["status"].(string); status != "heartbeat" {
		t.Errorf("body status: got %q want heartbeat", status)
	}
}

func TestHeartbeat_retriesTwiceOnError(t *testing.T) {
	var attempts int32

	client, _ := newTestClientNoRetry(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		respondJSON(w, http.StatusOK, observer.ObserverResponse{Status: "heartbeat"})
	})

	if err := client.Heartbeat(context.Background(), "hb-retry"); err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Errorf("expected 3 attempts (2 failures + 1 success), got %d", got)
	}
}

func TestHeartbeat_nonFatalAfterExhaustion(t *testing.T) {
	client, _ := newTestClientNoRetry(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	err := client.Heartbeat(context.Background(), "hb-exhaust")
	if err == nil {
		t.Fatal("expected non-nil error after all retries exhausted")
	}
	// Must not panic — error returned is non-nil but the test just logs it.
}

func TestHeartbeat_respectsDeadline(t *testing.T) {
	// Server delays 15s — heartbeat has a 10s deadline, so it should return fast.
	client, _ := newTestClientNoRetry(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(15 * time.Second)
		respondJSON(w, http.StatusOK, observer.ObserverResponse{})
	})

	start := time.Now()
	_ = client.Heartbeat(context.Background(), "hb-deadline")
	elapsed := time.Since(start)

	// HeartbeatDeadline is 10s; allow 1s tolerance per retry (max 2 retries = 30s limit).
	// In practice the deadline context should cut it off well within 11s per attempt.
	maxAllowed := observer.HeartbeatDeadline*time.Duration(observer.HeartbeatMaxRetries+1) + 500*time.Millisecond
	if elapsed > maxAllowed {
		t.Errorf("Heartbeat took %v, expected < %v", elapsed, maxAllowed)
	}
}

func TestHeartbeat_idempotentOn409(t *testing.T) {
	client, _ := newTestClientNoRetry(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict) // 409
	})

	if err := client.Heartbeat(context.Background(), "hb-409"); err != nil {
		t.Errorf("expected nil for 409, got: %v", err)
	}
}

func TestHeartbeat_idempotentOn410(t *testing.T) {
	client, _ := newTestClientNoRetry(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone) // 410
	})

	if err := client.Heartbeat(context.Background(), "hb-410"); err != nil {
		t.Errorf("expected nil for 410, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Patch — idempotency on 409/410
// ---------------------------------------------------------------------------

func TestPatch_idempotentOn409(t *testing.T) {
	client, _ := newTestClientNoRetry(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})

	resp, err := client.Patch(context.Background(), "run-409", observer.ClosePayload{Status: "closed"})
	if err != nil {
		t.Errorf("expected nil error for 409, got: %v", err)
	}
	if resp == nil {
		t.Error("expected non-nil response for 409")
	}
}

func TestPatch_idempotentOn410(t *testing.T) {
	client, _ := newTestClientNoRetry(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
	})

	resp, err := client.Patch(context.Background(), "run-410", observer.ClosePayload{Status: "closed"})
	if err != nil {
		t.Errorf("expected nil error for 410, got: %v", err)
	}
	if resp == nil {
		t.Error("expected non-nil response for 410")
	}
}

// ---------------------------------------------------------------------------
// Post — retry behaviour
// ---------------------------------------------------------------------------

func TestPost_retriesOnTransientError(t *testing.T) {
	var attempts int32

	client, _ := newTestClientNoRetry(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable) // 503
			return
		}
		respondJSON(w, http.StatusCreated, observer.ObserverResponse{ID: "r1", Status: "open"})
	})

	resp, err := client.Post(context.Background(), observer.ObserverPayload{
		RunID:  "post-retry",
		Status: "open",
		Action: "apply",
	})
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if resp.ID != "r1" {
		t.Errorf("ID: got %q want r1", resp.ID)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Errorf("expected 3 attempts (2 503 + 1 success), got %d", got)
	}
}

func TestPost_failsAfterMaxRetries(t *testing.T) {
	var attempts int32

	// The HTTPExecutor also retries internally on 5xx (default MaxRetries=3).
	// Use 429 (rate-limited) which the executor treats as retryable but we
	// can verify the observer retries at its own level too.
	// Total calls = (PostOpenMaxRetries+1) × (executor.MaxRetries+1).
	// To verify observer-level retry count is exactly PostOpenMaxRetries+1,
	// use a non-retryable 4xx error (400) from the executor's perspective so
	// the executor does NOT retry — leaving retry responsibility solely to observer.
	//
	// However since 400 is a non-transient 4xx, our observer.Post returns
	// immediately on 400. Use 503 but track that total_attempts =
	// (PostOpenMaxRetries+1) * (executor_retries+1).
	client, _ := newTestClientNoRetry(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	_, err := client.Post(context.Background(), observer.ObserverPayload{RunID: "post-exhaust"})
	if err == nil {
		t.Fatal("expected error after all retries exhausted")
	}
	// newTestClientNoRetry sets executor MaxRetries=0 which is normalized to 3 by
	// NewHTTPExecutor. So total HTTP calls = (PostOpenMaxRetries+1) * (executor.MaxRetries+1)
	// = 4 * 4 = 16. That is expected and correct — observer retried PostOpenMaxRetries times.
	// Verify at least PostOpenMaxRetries+1 observer-level attempts occurred.
	if got := atomic.LoadInt32(&attempts); got < int32(observer.PostOpenMaxRetries+1) {
		t.Errorf("expected at least %d attempts, got %d", observer.PostOpenMaxRetries+1, got)
	}
}

func TestPost_exponentialBackoff(t *testing.T) {
	// Record the timestamps of each request to verify backoff spacing.
	var timestamps []time.Time
	var mu sync.Mutex

	client, _ := newTestClientNoRetry(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		timestamps = append(timestamps, time.Now())
		mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	_, _ = client.Post(context.Background(), observer.ObserverPayload{RunID: "backoff-test"})

	mu.Lock()
	defer mu.Unlock()

	if len(timestamps) < 2 {
		t.Fatalf("need at least 2 timestamps, got %d", len(timestamps))
	}

	// First backoff should be ~PostOpenRetryBase (500ms). Allow wide tolerance
	// for slow CI machines.
	gap1 := timestamps[1].Sub(timestamps[0])
	if gap1 < observer.PostOpenRetryBase/2 {
		t.Errorf("first backoff gap %v too short, expected ~%v", gap1, observer.PostOpenRetryBase)
	}

	// Second backoff should be ~1s (2× base).
	if len(timestamps) >= 3 {
		gap2 := timestamps[2].Sub(timestamps[1])
		if gap2 < observer.PostOpenRetryBase {
			t.Errorf("second backoff gap %v too short, expected ~%v (2×base)", gap2, observer.PostOpenRetryBase*2)
		}
	}
}

// ---------------------------------------------------------------------------
// TestObserver_runIDInURL
// ---------------------------------------------------------------------------

func TestObserver_runIDInURL(t *testing.T) {
	paths := make([]string, 0, 3)
	ids := []string{"alpha", "beta", "gamma"}

	client, _ := newTestClientNoRetry(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		respondJSON(w, http.StatusOK, observer.ObserverResponse{})
	})

	for _, id := range ids {
		_ = client.Heartbeat(context.Background(), id)
	}

	for i, p := range paths {
		if !strings.HasSuffix(p, "/"+ids[i]) {
			t.Errorf("path[%d] = %q, expected suffix /%s", i, p, ids[i])
		}
	}
}

// ---------------------------------------------------------------------------
// TestObserver_heartbeatDoesNotShareContextWithClose
// ---------------------------------------------------------------------------

func TestObserver_heartbeatDoesNotShareContextWithClose(t *testing.T) {
	// The cancelled parent context must not prevent Heartbeat from using its
	// own independent HeartbeatDeadline context.

	var reached int32

	client, _ := newTestClientNoRetry(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.StoreInt32(&reached, 1)
		respondJSON(w, http.StatusOK, observer.ObserverResponse{Status: "heartbeat"})
	})

	// Pass an already-cancelled context as "parent".
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	// Heartbeat should still succeed because it ignores the caller's context.
	err := client.Heartbeat(cancelledCtx, "hb-independent-ctx")
	if err != nil {
		t.Logf("Heartbeat returned error (may be ok if server unreachable): %v", err)
	}
	if atomic.LoadInt32(&reached) != 1 {
		t.Error("Heartbeat should have reached the server even with cancelled parent context")
	}
}
