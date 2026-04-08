package main

import (
	"context"
	"flag"
	"log"
	"os"
	"terraform-provider-manifestit/internal/provider"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

func main() {
	// When spawned as the watcher subprocess, run WatcherMain and exit.
	// The provider binary re-execs itself with MIT_WATCHER_MODE=1 after
	// POST /open so the watcher survives go-plugin's SIGKILL and can fire
	// PATCH /closed once the terraform binary (PPID) fully exits.
	if os.Getenv("MIT_WATCHER_MODE") == "1" {
		provider.WatcherMain()
		os.Exit(0)
	}

	var debugMode bool

	flag.BoolVar(&debugMode, "debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	var serveOpts providerserver.ServeOpts

	serveOpts.Address = "registry.terraform.io/manifest-it/manifestit"

	if debugMode {
		serveOpts.Debug = true
	}

	// providerserver.Serve blocks for the entire terraform run.
	// When it returns the gRPC connection has been closed by terraform (normal
	// completion, ctrl+c, or SIGTERM). The watcher subprocess — spawned during
	// Configure — fires PATCH /closed once the terraform binary (PPID) exits.
	// If go-plugin sends SIGKILL after a stall, Serve never returns; the watcher
	// detects this via its PPID poll loop and fires PATCH /closed regardless.
	err := providerserver.Serve(
		context.Background(),
		provider.New,
		serveOpts,
	)

	if err != nil {
		log.Fatal(err)
	}
}
