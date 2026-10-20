package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"

	"github.com/marmot1024/metricspire/internal/adapter/clickhousequery"
	"github.com/marmot1024/metricspire/internal/adapter/dorisquery"
	"github.com/marmot1024/metricspire/internal/adapter/postgresquery"
	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
)

// Calculate producer metadata offline; never connects, publishes or writes data.
func runOnlineContract(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("online-contract", flag.ContinueOnError)
	flags.SetOutput(stderr)
	sourcePath := flags.String("source", "", "reviewed semantic source")
	bindingPath := flags.String("binding", "", "reviewed online source binding")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *sourcePath == "" || *bindingPath == "" || flags.NArg() != 0 {
		return errors.New("--source and --binding are required")
	}
	var source model.SemanticSource
	var binding model.SourceBinding
	if err := contractio.ReadFile(*sourcePath, &source); err != nil {
		return err
	}
	if err := contractio.ReadFile(*bindingPath, &binding); err != nil {
		return err
	}
	manifest, err := compiler.Compile(source)
	if err != nil {
		return err
	}
	var fingerprint string
	switch binding.Engine {
	case postgresquery.EngineName:
		fingerprint, err = postgresquery.DataContractFingerprint(manifest, binding)
	case clickhousequery.EngineName:
		fingerprint, err = clickhousequery.DataContractFingerprint(manifest, binding)
	case dorisquery.EngineName:
		fingerprint, err = dorisquery.DataContractFingerprint(manifest, binding)
	default:
		return errors.New("online-contract requires a supported online engine")
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(map[string]string{"engine": binding.Engine, "data_contract": fingerprint, "manifest_fingerprint": manifest.Fingerprint})
}
