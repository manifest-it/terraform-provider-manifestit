// mock-server is a local HTTP server that stands in for the ManifestIT API.
// It records every POST and PATCH to /api/v1/events and prints them to stdout
// so you can verify the open/closed lifecycle events during a real terraform apply.
//
// Usage:
//
//	go run ./localtest/mock-server
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

type event struct {
	ReceivedAt time.Time       `json:"received_at"`
	Method     string          `json:"method"`
	Path       string          `json:"path"`
	Body       json.RawMessage `json:"body"`
}

var (
	mu     sync.Mutex
	events []event
)

func handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	defer r.Body.Close()

	ev := event{
		ReceivedAt: time.Now().UTC(),
		Method:     r.Method,
		Path:       r.URL.Path,
		Body:       json.RawMessage(body),
	}

	mu.Lock()
	events = append(events, ev)
	n := len(events)
	mu.Unlock()

	// Decode status for display
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	status, _ := payload["status"].(string)
	runID, _ := payload["run_id"].(string)
	if runID == "" {
		// PATCH path has run_id in URL
		parts := strings.Split(r.URL.Path, "/")
		runID = parts[len(parts)-1]
	}

	// Colour-coded real-time output
	color := map[string]string{
		"open":      "\033[32m", // green
		"closed":    "\033[31m", // red
		"heartbeat": "\033[33m", // yellow
	}
	reset := "\033[0m"
	c := color[status]
	if c == "" {
		c = "\033[36m" // cyan for unknown
	}
	fmt.Printf("\n%s── event #%d  [%s]  %s  %s %s%s\n",
		c, n, strings.ToUpper(status), ev.ReceivedAt.Format("15:04:05"), r.Method, r.URL.Path, reset)
	fmt.Printf("   run_id: %s\n", runID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"id":     runID,
		"status": "ok",
	})
}

func dumpHandler(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(events)
}

func resetHandler(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	events = nil
	mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func main() {
	addr := ":8080"
	if v := os.Getenv("MOCK_ADDR"); v != "" {
		addr = v
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/events", handler)
	mux.HandleFunc("/api/v1/events/", handler)
	mux.HandleFunc("/dump", dumpHandler)
	// DELETE /reset — clears all stored events (used between test runs)
	mux.HandleFunc("/reset", resetHandler)

	log.Printf("mock ManifestIT API listening on http://localhost%s", addr)
	log.Printf("  POST  /api/v1/events        → open event")
	log.Printf("  PATCH /api/v1/events/{id}   → closed event")
	log.Printf("  GET   /dump                 → dump all received events as JSON")
	log.Printf("  POST  /reset                → clear all events")

	// Handle graceful shutdown on SIGTERM and SIGINT
	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		// Wait for interrupt signal to gracefully shutdown the server
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigChan

		log.Printf("received signal %s, shutting down gracefully...", sig)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Fatalf("could not gracefully shutdown the server: %v", err)
		}
		log.Println("server shut down gracefully")
	}()

	log.Fatal(srv.ListenAndServe())
}
