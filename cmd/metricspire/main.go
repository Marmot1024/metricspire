package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/marmot1024/metricspire/internal/compiler"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/planner"
)

const version = "0.1.0-dev"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "metricspire:", err)
		os.Exit(1)
	}
}

func run(arguments []string, stdout, stderr io.Writer) error {
	return runContext(context.Background(), arguments, stdout, stderr)
}

func runContext(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	if len(arguments) == 0 {
		printUsage(stderr)
		return errors.New("a command is required")
	}
	switch arguments[0] {
	case "version":
		_, err := fmt.Fprintln(stdout, "metricspire", version)
		return err
	case "validate":
		return runValidate(arguments[1:], stdout, stderr)
	case "compile":
		return runCompile(arguments[1:], stderr)
	case "compile-policy":
		return runCompilePolicy(arguments[1:], stderr)
	case "plan":
		return runPlan(arguments[1:], stderr)
	case "serve":
		return runServe(ctx, arguments[1:], stdout, stderr)
	case "migrate", "healthcheck", "draft-put", "draft-get", "publish", "rollback", "release-list", "release-events", "query-active":
		return runPhase2(ctx, arguments[0], arguments[1:], stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	default:
		printUsage(stderr)
		return fmt.Errorf("unknown command %q", arguments[0])
	}
}

func runValidate(arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	sourcePath := flags.String("source", "", "semantic model (.json/.yaml)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *sourcePath == "" {
		return errors.New("--source is required")
	}
	var source model.SemanticSource
	if err := contractio.ReadFile(*sourcePath, &source); err != nil {
		return err
	}
	manifest, err := compiler.Compile(source)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "valid %s %s\n", manifest.Metadata.Name, manifest.Fingerprint)
	return err
}

func runCompile(arguments []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("compile", flag.ContinueOnError)
	flags.SetOutput(stderr)
	sourcePath := flags.String("source", "", "semantic model (.json/.yaml)")
	outPath := flags.String("out", "-", "manifest output path or -")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *sourcePath == "" {
		return errors.New("--source is required")
	}
	var source model.SemanticSource
	if err := contractio.ReadFile(*sourcePath, &source); err != nil {
		return err
	}
	manifest, err := compiler.Compile(source)
	if err != nil {
		return err
	}
	return contractio.WriteJSON(*outPath, manifest)
}

func runCompilePolicy(arguments []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("compile-policy", flag.ContinueOnError)
	flags.SetOutput(stderr)
	sourcePath := flags.String("source", "", "policy source (.json/.yaml)")
	manifestPath := flags.String("manifest", "", "compiled semantic manifest")
	outPath := flags.String("out", "-", "policy bundle output path or -")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *sourcePath == "" || *manifestPath == "" {
		return errors.New("--source and --manifest are required")
	}
	var source model.PolicySource
	var manifest model.SemanticManifest
	if err := contractio.ReadFile(*sourcePath, &source); err != nil {
		return err
	}
	if err := contractio.ReadFile(*manifestPath, &manifest); err != nil {
		return err
	}
	bundle, err := compiler.CompilePolicy(source, manifest)
	if err != nil {
		return err
	}
	return contractio.WriteJSON(*outPath, bundle)
}

func runPlan(arguments []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("plan", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "", "compiled semantic manifest")
	policyPath := flags.String("policy", "", "compiled policy bundle")
	contextPath := flags.String("context", "", "trusted local request context")
	queryPath := flags.String("query", "", "semantic query")
	bindingPath := flags.String("binding", "", "source binding")
	capabilitiesPath := flags.String("capabilities", "", "engine capability contract")
	logicalPath := flags.String("logical-out", "logical-plan.json", "logical plan output")
	physicalPath := flags.String("physical-out", "physical-plan.json", "physical plan output")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *manifestPath == "" || *policyPath == "" || *contextPath == "" || *queryPath == "" || *bindingPath == "" || *capabilitiesPath == "" {
		return errors.New("--manifest, --policy, --context, --query, --binding, and --capabilities are required")
	}
	var manifest model.SemanticManifest
	var bundle model.PolicyBundle
	var context model.RequestContext
	var query model.SemanticQuery
	var binding model.SourceBinding
	var capabilities model.EngineCapabilities
	inputs := []struct {
		path   string
		target any
	}{
		{*manifestPath, &manifest}, {*policyPath, &bundle}, {*contextPath, &context},
		{*queryPath, &query}, {*bindingPath, &binding}, {*capabilitiesPath, &capabilities},
	}
	for _, input := range inputs {
		if err := contractio.ReadFile(input.path, input.target); err != nil {
			return err
		}
	}
	logical, err := planner.BuildLogical(manifest, bundle, context, query)
	if err != nil {
		return err
	}
	physical, err := planner.BuildPhysical(manifest, logical, binding, capabilities)
	if err != nil {
		return err
	}
	if err := contractio.WriteJSON(*logicalPath, logical); err != nil {
		return err
	}
	return contractio.WriteJSON(*physicalPath, physical)
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, `MetricSpire compiles governed metric definitions into deterministic plans.

Usage:
  metricspire validate --source model.yaml
  metricspire compile --source model.yaml --out manifest.json
  metricspire compile-policy --source policy.yaml --manifest manifest.json --out policy.json
  metricspire plan --manifest manifest.json --policy policy.json --context context.json \
    --query query.json --binding binding.json --capabilities capabilities.json \
    --logical-out logical.json --physical-out physical.json

Product HTTP service:
  metricspire serve --config examples/runtime.example.yaml [--http-address host:port]

Catalog and query workflow:
  metricspire migrate
  metricspire draft-put --namespace demo --source model.yaml --actor alice --expected-revision 0
  metricspire publish --namespace demo --model commerce --revision 1 --actor alice
  metricspire query-active --namespace demo --model commerce --context context.json \
    --query query.json --policy policy.yaml --binding databricks-binding.json
  metricspire rollback --namespace demo --model commerce --release rel_... --actor alice

Database and Databricks credentials default to METRICSPIRE_DATABASE_URL,
DATABRICKS_HOST, DATABRICKS_SQL_WAREHOUSE_ID, and OAuth M2M environment variables.
The HTTP service also requires a base64 METRICSPIRE_SESSION_KEY; OIDC client
secrets, when required by the provider, use METRICSPIRE_OIDC_CLIENT_SECRET.`)
}
