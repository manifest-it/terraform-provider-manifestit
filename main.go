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

	serveOpts.Address = "registry.terraform.io/manifestit/manifestit"

	err := providerserver.Serve(
		ctx,
		provider.New,
		serveOpts,
	)
	if err != nil {
		log.Fatal(err)
	}
}
