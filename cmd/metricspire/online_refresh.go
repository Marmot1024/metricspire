package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/servingrefresh"
)

func runOnlineRefresh(command string, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	source := flags.String("source", "", "reviewed semantic source")
	binding := flags.String("binding", "", "reviewed PostgreSQL serving binding")
	input := flags.String("input", "", "local typed NDJSON source or OnlineBatch artifact")
	output := flags.String("output", "", "new local OnlineBatch artifact")
	tenant := flags.String("tenant", "", "explicit tenant to replace")
	batch := flags.String("batch-id", "", "unique upstream batch ID")
	watermark := flags.String("data-as-of", "", "explicit UTC source coverage timestamp, never export time")
	reconcile := flags.String("reconcile", "", "upstream JSON count and numeric totals (independent source controls)")
	previous := flags.String("expected-batch", "", "reviewed current batch ID; empty for initial publication")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *source == "" || *binding == "" {
		return errors.New("--source and --binding required")
	}
	var s model.SemanticSource
	var b model.SourceBinding
	if err := contractio.ReadFile(*source, &s); err != nil {
		return err
	}
	if err := contractio.ReadFile(*binding, &b); err != nil {
		return err
	}
	m, err := compiler.Compile(s)
	if err != nil {
		return err
	}
	c, err := servingrefresh.NewContract(m, b)
	if err != nil {
		return err
	}
	if command == "export-online" {
		if *input == "" || *output == "" || *reconcile == "" {
			return errors.New("--input, --output and independent --reconcile required")
		}
		var e servingrefresh.Expectations
		controls, err := os.Open(*reconcile)
		if err != nil {
			return errors.New("cannot open upstream reconciliation file")
		}
		defer controls.Close()
		decoder := json.NewDecoder(io.LimitReader(controls, 65537))
		decoder.DisallowUnknownFields()
		var extra any
		if decoder.Decode(&e) != nil || decoder.Decode(&extra) != io.EOF {
			return errors.New("invalid upstream reconciliation JSON")
		}
		asof, err := time.Parse(time.RFC3339Nano, *watermark)
		if err != nil {
			return errors.New("--data-as-of requires explicit UTC source coverage")
		}
		in, err := os.Open(*input)
		if err != nil {
			return errors.New("cannot open source rows")
		}
		defer in.Close()
		// A private temporary file is published only after complete validation. Link
		// refuses accidental replacement of an already issued immutable artifact.
		tmp, err := os.CreateTemp(filepath.Dir(*output), ".metricspire-online-*")
		if err != nil {
			return errors.New("cannot create private artifact")
		}
		defer os.Remove(tmp.Name())
		defer tmp.Close()
		if err = servingrefresh.Export(in, tmp, c, servingrefresh.Header{Tenant: *tenant, BatchID: *batch, DataAsOf: asof}, e); err != nil {
			return err
		}
		if err = tmp.Sync(); err != nil {
			return errors.New("artifact flush failed")
		}
		if err = tmp.Close(); err != nil {
			return errors.New("artifact close failed")
		}
		if err = os.Link(tmp.Name(), *output); err != nil {
			return errors.New("artifact publication failed; output must not exist")
		}
		return json.NewEncoder(stdout).Encode(map[string]string{"status": "exported", "data_contract": c.Fingerprint})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	conn, err := servingrefresh.Connect(ctx, os.Getenv("METRICSPIRE_REFRESH_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	if command == "init-online-refresh" {
		if err = servingrefresh.Init(ctx, conn, c); err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(map[string]string{"status": "initialized"})
	}
	if command != "refresh-online" || *input == "" || *tenant == "" {
		return errors.New("refresh-online requires --input and --tenant")
	}
	// Explicit presence distinguishes initial empty CAS from an omitted decision.
	expectedProvided := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "expected-batch" {
			expectedProvided = true
		}
	})
	if !expectedProvided {
		return errors.New("--expected-batch required (empty for first publication)")
	}
	in, err := os.Open(*input)
	if err != nil {
		return errors.New("cannot open batch artifact")
	}
	defer in.Close()
	receipt, err := servingrefresh.Refresh(ctx, conn, c, in, *tenant, *previous)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(receipt)
}
