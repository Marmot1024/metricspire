package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/marmot1024/metricspire/internal/runtimeconfig"
)

func runValidateDomains(arguments []string, stdout, stderr io.Writer) error {
	f := flag.NewFlagSet("validate-domains", flag.ContinueOnError)
	f.SetOutput(stderr)
	path := f.String("config", "", "deployment-owned domain registry (.json/.yaml)")
	if err := parseNoPositionals(f, arguments); err != nil {
		return err
	}
	if *path == "" {
		return errors.New("--config is required")
	}
	r, err := runtimeconfig.LoadDomains(*path)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "valid domain registry: %d domains (no network or data queries)\n", len(r.Domains))
	return err
}
