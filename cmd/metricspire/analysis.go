package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"

	"github.com/marmot1024/metricspire/internal/canonical"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/exploration"
)

func runValidateAnalysis(arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("validate-analysis", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("catalog", "", "reviewed external AnalysisCatalog (.json/.yaml)")
	if err := parseNoPositionals(flags, arguments); err != nil {
		return err
	}
	if *path == "" {
		return errors.New("--catalog is required")
	}
	var c exploration.Catalog
	if err := contractio.ReadFile(*path, &c); err != nil {
		return err
	}
	if err := exploration.ValidateCatalog(c); err != nil {
		return err
	}
	fp, err := canonical.Fingerprint(c)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(map[string]any{"valid": true, "name": c.Metadata.Name, "version": c.Metadata.Version, "fingerprint": fp, "sources": len(c.Sources)})
}
