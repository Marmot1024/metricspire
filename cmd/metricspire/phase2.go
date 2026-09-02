package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/adapter/databricks"
	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/repository/postgres"
)

const defaultCommandTimeout = 30 * time.Second

func runPhase2(ctx context.Context, command string, arguments []string, stdout, stderr io.Writer) error {
	switch command {
	case "migrate":
		return runMigrate(ctx, arguments, stdout, stderr)
	case "healthcheck":
		return runHealthcheck(ctx, arguments, stdout, stderr)
	case "draft-put":
		return runDraftPut(ctx, arguments, stdout, stderr)
	case "draft-get":
		return runDraftGet(ctx, arguments, stdout, stderr)
	case "publish":
		return runPublish(ctx, arguments, stdout, stderr)
	case "rollback":
		return runRollback(ctx, arguments, stdout, stderr)
	case "release-list":
		return runReleaseList(ctx, arguments, stdout, stderr)
	case "release-events":
		return runReleaseEvents(ctx, arguments, stdout, stderr)
	case "query-active":
		return runQueryActive(ctx, arguments, stdout, stderr)
	default:
		return fmt.Errorf("unsupported Phase 2 command %q", command)
	}
}

func runMigrate(parent context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databaseURL := flags.String("database-url", os.Getenv("METRICSPIRE_DATABASE_URL"), "PostgreSQL URL (or METRICSPIRE_DATABASE_URL)")
	timeout := flags.Duration("timeout", defaultCommandTimeout, "operation timeout")
	if err := parseNoPositionals(flags, arguments); err != nil {
		return err
	}
	ctx, cancel, err := commandContext(parent, *timeout)
	if err != nil {
		return err
	}
	defer cancel()
	store, err := openStore(ctx, *databaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := postgres.Migrate(ctx, store.Pool()); err != nil {
		return err
	}
	return writeJSON(stdout, map[string]string{"status": "ok", "component": "postgres_migrations"})
}

func runHealthcheck(parent context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databaseURL := flags.String("database-url", os.Getenv("METRICSPIRE_DATABASE_URL"), "PostgreSQL URL (or METRICSPIRE_DATABASE_URL)")
	timeout := flags.Duration("timeout", 5*time.Second, "operation timeout")
	if err := parseNoPositionals(flags, arguments); err != nil {
		return err
	}
	ctx, cancel, err := commandContext(parent, *timeout)
	if err != nil {
		return err
	}
	defer cancel()
	store, err := openStore(ctx, *databaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	return writeJSON(stdout, map[string]string{"status": "ok", "component": "postgres"})
}

func runDraftPut(parent context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("draft-put", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databaseURL := flags.String("database-url", os.Getenv("METRICSPIRE_DATABASE_URL"), "PostgreSQL URL (or METRICSPIRE_DATABASE_URL)")
	namespace := flags.String("namespace", "", "catalog namespace")
	sourcePath := flags.String("source", "", "semantic source (.json/.yaml)")
	actor := flags.String("actor", "", "human or service actor")
	expectedRevision := flags.Int64("expected-revision", 0, "current revision; 0 creates a draft")
	timeout := flags.Duration("timeout", defaultCommandTimeout, "operation timeout")
	if err := parseNoPositionals(flags, arguments); err != nil {
		return err
	}
	if strings.TrimSpace(*namespace) == "" || strings.TrimSpace(*sourcePath) == "" || strings.TrimSpace(*actor) == "" {
		return errors.New("--namespace, --source, and --actor are required")
	}
	var source model.SemanticSource
	if err := contractio.ReadFile(*sourcePath, &source); err != nil {
		return err
	}
	ctx, cancel, err := commandContext(parent, *timeout)
	if err != nil {
		return err
	}
	defer cancel()
	store, err := openStore(ctx, *databaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	service, _ := catalog.NewService(store)
	draft, err := service.SaveDraft(ctx, catalog.SaveDraftInput{
		Namespace: *namespace, Source: source, Actor: *actor, ExpectedRevision: *expectedRevision,
	})
	if err != nil {
		return err
	}
	return writeJSON(stdout, draft)
}

func runDraftGet(parent context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("draft-get", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databaseURL, namespace, modelName, timeout := catalogReadFlags(flags)
	if err := parseNoPositionals(flags, arguments); err != nil {
		return err
	}
	if err := requireCatalogIdentity(*namespace, *modelName); err != nil {
		return err
	}
	ctx, cancel, err := commandContext(parent, *timeout)
	if err != nil {
		return err
	}
	defer cancel()
	store, err := openStore(ctx, *databaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	draft, err := store.GetDraft(ctx, *namespace, *modelName)
	if err != nil {
		return err
	}
	return writeJSON(stdout, draft)
}

func runPublish(parent context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("publish", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databaseURL, namespace, modelName, timeout := catalogReadFlags(flags)
	revision := flags.Int64("revision", 0, "exact draft revision to publish")
	actor := flags.String("actor", "", "human or service actor")
	note := flags.String("note", "", "release note")
	if err := parseNoPositionals(flags, arguments); err != nil {
		return err
	}
	if err := requireCatalogIdentity(*namespace, *modelName); err != nil || *revision < 1 || strings.TrimSpace(*actor) == "" {
		return errors.New("--namespace, --model, positive --revision, and --actor are required")
	}
	ctx, cancel, err := commandContext(parent, *timeout)
	if err != nil {
		return err
	}
	defer cancel()
	store, err := openStore(ctx, *databaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	service, _ := catalog.NewService(store)
	release, err := service.Publish(ctx, *namespace, *modelName, *revision, *actor, *note)
	if err != nil {
		return err
	}
	return writeJSON(stdout, release)
}

func runRollback(parent context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("rollback", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databaseURL, namespace, modelName, timeout := catalogReadFlags(flags)
	releaseID := flags.String("release", "", "existing immutable release ID")
	actor := flags.String("actor", "", "human or service actor")
	note := flags.String("note", "", "rollback reason")
	if err := parseNoPositionals(flags, arguments); err != nil {
		return err
	}
	if err := requireCatalogIdentity(*namespace, *modelName); err != nil || strings.TrimSpace(*releaseID) == "" || strings.TrimSpace(*actor) == "" {
		return errors.New("--namespace, --model, --release, and --actor are required")
	}
	ctx, cancel, err := commandContext(parent, *timeout)
	if err != nil {
		return err
	}
	defer cancel()
	store, err := openStore(ctx, *databaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	service, _ := catalog.NewService(store)
	release, err := service.Rollback(ctx, *namespace, *modelName, *releaseID, *actor, *note)
	if err != nil {
		return err
	}
	return writeJSON(stdout, release)
}

func runReleaseList(parent context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("release-list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databaseURL, namespace, modelName, timeout := catalogReadFlags(flags)
	if err := parseNoPositionals(flags, arguments); err != nil {
		return err
	}
	if err := requireCatalogIdentity(*namespace, *modelName); err != nil {
		return err
	}
	ctx, cancel, err := commandContext(parent, *timeout)
	if err != nil {
		return err
	}
	defer cancel()
	store, err := openStore(ctx, *databaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	releases, err := store.ListReleases(ctx, *namespace, *modelName)
	if err != nil {
		return err
	}
	activeID := ""
	if active, activeErr := store.GetActiveRelease(ctx, *namespace, *modelName); activeErr == nil {
		activeID = active.ID
	} else if !errors.Is(activeErr, catalog.ErrNotFound) {
		return activeErr
	}
	type releaseSummary struct {
		ID                  string    `json:"id"`
		SourceRevision      int64     `json:"source_revision"`
		ManifestFingerprint string    `json:"manifest_fingerprint"`
		Active              bool      `json:"active"`
		CreatedBy           string    `json:"created_by"`
		Note                string    `json:"note"`
		CreatedAt           time.Time `json:"created_at"`
	}
	result := make([]releaseSummary, 0, len(releases))
	for _, release := range releases {
		result = append(result, releaseSummary{
			ID: release.ID, SourceRevision: release.SourceRevision,
			ManifestFingerprint: release.ManifestFingerprint, Active: release.ID == activeID,
			CreatedBy: release.CreatedBy, Note: release.Note, CreatedAt: release.CreatedAt,
		})
	}
	return writeJSON(stdout, result)
}

func runReleaseEvents(parent context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("release-events", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databaseURL, namespace, modelName, timeout := catalogReadFlags(flags)
	if err := parseNoPositionals(flags, arguments); err != nil {
		return err
	}
	if err := requireCatalogIdentity(*namespace, *modelName); err != nil {
		return err
	}
	ctx, cancel, err := commandContext(parent, *timeout)
	if err != nil {
		return err
	}
	defer cancel()
	store, err := openStore(ctx, *databaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	events, err := store.ListEvents(ctx, *namespace, *modelName)
	if err != nil {
		return err
	}
	return writeJSON(stdout, events)
}

func runQueryActive(parent context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("query-active", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databaseURL := flags.String("database-url", os.Getenv("METRICSPIRE_DATABASE_URL"), "PostgreSQL URL (or METRICSPIRE_DATABASE_URL)")
	namespace := flags.String("namespace", "", "catalog namespace")
	modelName := flags.String("model", "", "semantic model name")
	contextPath := flags.String("context", "", "trusted request context (.json/.yaml)")
	queryPath := flags.String("query", "", "semantic query (.json/.yaml)")
	policyPath := flags.String("policy", "", "policy source (.json/.yaml)")
	bindingPath := flags.String("binding", "", "Databricks source binding (.json/.yaml)")
	host := flags.String("databricks-host", os.Getenv("DATABRICKS_HOST"), "workspace URL (or DATABRICKS_HOST)")
	warehouseID := flags.String("warehouse-id", os.Getenv("DATABRICKS_SQL_WAREHOUSE_ID"), "SQL warehouse ID (or DATABRICKS_SQL_WAREHOUSE_ID)")
	byteLimit := flags.Int64("byte-limit", 4<<20, "maximum inline result bytes")
	pollInterval := flags.Duration("poll-interval", 500*time.Millisecond, "status poll interval")
	timeout := flags.Duration("timeout", 2*time.Minute, "end-to-end query timeout")
	if err := parseNoPositionals(flags, arguments); err != nil {
		return err
	}
	if err := requireCatalogIdentity(*namespace, *modelName); err != nil || strings.TrimSpace(*contextPath) == "" || strings.TrimSpace(*queryPath) == "" || strings.TrimSpace(*policyPath) == "" || strings.TrimSpace(*bindingPath) == "" {
		return errors.New("--namespace, --model, --context, --query, --policy, and --binding are required")
	}
	var requestContext model.RequestContext
	var query model.SemanticQuery
	var policy model.PolicySource
	var binding model.SourceBinding
	inputs := []struct {
		path   string
		target any
	}{{*contextPath, &requestContext}, {*queryPath, &query}, {*policyPath, &policy}, {*bindingPath, &binding}}
	for _, input := range inputs {
		if err := contractio.ReadFile(input.path, input.target); err != nil {
			return err
		}
	}
	if binding.Engine != databricks.EngineName {
		return fmt.Errorf("binding engine must be %q", databricks.EngineName)
	}
	tokens, err := databricksTokenSource(*host)
	if err != nil {
		return err
	}
	client, err := databricks.NewClient(databricks.ClientConfig{
		Host: *host, WarehouseID: *warehouseID, TokenSource: tokens,
		ByteLimit: *byteLimit, PollInterval: *pollInterval,
	})
	if err != nil {
		return err
	}
	engine, _ := databricks.NewQueryEngine(client)
	ctx, cancel, err := commandContext(parent, *timeout)
	if err != nil {
		return err
	}
	defer cancel()
	store, err := openStore(ctx, *databaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	service, _ := application.NewQueryService(store, engine)
	output, executeErr := service.ExecuteActive(ctx, application.QueryInput{
		Namespace: *namespace, ModelName: *modelName, Context: requestContext,
		Query: query, Policy: policy, Binding: binding,
	})
	if output.Release.ID != "" {
		if err := writeJSON(stdout, output); err != nil {
			return err
		}
	}
	return executeErr
}

func catalogReadFlags(flags *flag.FlagSet) (*string, *string, *string, *time.Duration) {
	databaseURL := flags.String("database-url", os.Getenv("METRICSPIRE_DATABASE_URL"), "PostgreSQL URL (or METRICSPIRE_DATABASE_URL)")
	namespace := flags.String("namespace", "", "catalog namespace")
	modelName := flags.String("model", "", "semantic model name")
	timeout := flags.Duration("timeout", defaultCommandTimeout, "operation timeout")
	return databaseURL, namespace, modelName, timeout
}

func parseNoPositionals(flags *flag.FlagSet, arguments []string) error {
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	return nil
}

func commandContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc, error) {
	if timeout <= 0 {
		return nil, nil, errors.New("--timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	return ctx, cancel, nil
}

func openStore(ctx context.Context, databaseURL string) (*postgres.Store, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, errors.New("--database-url or METRICSPIRE_DATABASE_URL is required")
	}
	return postgres.Open(ctx, databaseURL)
}

func requireCatalogIdentity(namespace, modelName string) error {
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(modelName) == "" {
		return errors.New("--namespace and --model are required")
	}
	return nil
}

func databricksTokenSource(host string) (databricks.TokenSource, error) {
	clientID := os.Getenv("DATABRICKS_CLIENT_ID")
	clientSecret := os.Getenv("DATABRICKS_CLIENT_SECRET")
	if clientID != "" || clientSecret != "" {
		if clientID == "" || clientSecret == "" {
			return nil, errors.New("DATABRICKS_CLIENT_ID and DATABRICKS_CLIENT_SECRET must both be set")
		}
		return databricks.NewOAuthM2MTokenSource(databricks.OAuthM2MConfig{
			Host: host, ClientID: clientID, ClientSecret: clientSecret,
		})
	}
	if token := os.Getenv("DATABRICKS_TOKEN"); token != "" {
		return databricks.StaticTokenSource(token), nil
	}
	return nil, errors.New("Databricks authentication requires OAuth M2M environment variables or DATABRICKS_TOKEN")
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
