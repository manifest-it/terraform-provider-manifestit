package main

import (
	"context"
	"flag"
	"log"
	"terraform-provider-manifestit/internal/provider"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

func main() {
	ctx := context.Background()

	var debugMode bool

	flag.BoolVar(&debugMode, "debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	var serveOpts providerserver.ServeOpts

	if debugMode {
		serveOpts.Debug = true
	}

	serveOpts.Address = "registry.terraform.io/manifest-it/manifestit"

	// providerserver.Serve blocks for the entire terraform run.
	// When it returns (gRPC connection closed by terraform — normal completion,
	// ctrl+c via go-plugin SIGINT handler, or plugin stall → SIGKILL by go-plugin),
	// we call RunCleanup() to fire the PATCH /closed event and stop the heartbeat.
	//
	// Note on SIGKILL: if go-plugin sends SIGKILL after a stall, Serve() never
	// returns and RunCleanup() is never called. The server will mark the event
	// "timed_out" after the ServerTimeoutWindow (60s) — this is acceptable.
	err := providerserver.Serve(
		ctx,
		provider.New,
		serveOpts,
	)

	// Fire the close event on normal/clean exit. This is a no-op if SIGTERM
	// already fired the close event (providerCloseOnce guards against double-fire).
	provider.RunCleanup()

	if err != nil {
		log.Fatal(err)
	}
}
