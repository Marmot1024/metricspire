package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/marmot1024/metricspire/internal/adapter/databricks"
	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/auth/oidcauth"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/contractio"
	"github.com/marmot1024/metricspire/internal/httpapi"
	"github.com/marmot1024/metricspire/internal/model"
	"github.com/marmot1024/metricspire/internal/repository/postgres"
	"github.com/marmot1024/metricspire/internal/runtimeconfig"
)

const (
	defaultQueryJobCapacity = 100
	startupTimeout          = 15 * time.Second
	shutdownTimeout         = 10 * time.Second
)

type serveEnvironment struct {
	databaseURL      string
	sessionKey       []byte
	oidcClientSecret string
	databricksHost   string
	warehouseID      string
}

func runServe(parent context.Context, arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "runtime configuration (.json/.yaml)")
	httpAddress := flags.String("http-address", "", "trusted listen-address override (host:port)")
	if err := parseNoPositionals(flags, arguments); err != nil {
		return err
	}
	if strings.TrimSpace(*configPath) == "" {
		return errors.New("--config is required")
	}
	config, err := runtimeconfig.Load(*configPath)
	if err != nil {
		return err
	}
	listenAddress, err := resolveHTTPAddress(config.HTTP.Address, *httpAddress)
	if err != nil {
		return err
	}
	environment, err := loadServeEnvironment(os.Getenv)
	if err != nil {
		return err
	}
	controlTimeout, err := runtimeconfig.ParseDuration(config.HTTP.ControlTimeout, httpapi.DefaultControlTimeout)
	if err != nil {
		return fmt.Errorf("parse control timeout: %w", err)
	}
	queryTimeout, err := runtimeconfig.ParseDuration(config.HTTP.QueryTimeout, httpapi.DefaultQueryTimeout)
	if err != nil {
		return fmt.Errorf("parse query timeout: %w", err)
	}
	sessionTTL, err := runtimeconfig.ParseDuration(config.OIDC.SessionTTL, 8*time.Hour)
	if err != nil {
		return fmt.Errorf("parse OIDC session TTL: %w", err)
	}
	policies, bindings, err := loadTrustedRoutes(filepath.Dir(*configPath), config)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	initializationContext, cancelInitialization := context.WithTimeout(ctx, startupTimeout)
	defer cancelInitialization()

	store, err := postgres.Open(initializationContext, environment.databaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	management, err := catalog.NewService(store)
	if err != nil {
		return err
	}
	policyResolver, err := application.NewConfiguredPolicyResolver(policies)
	if err != nil {
		return err
	}
	bindingResolver, err := application.NewConfiguredBindingResolver(bindings)
	if err != nil {
		return err
	}
	catalogSearch, err := application.NewCatalogService(store, policyResolver)
	if err != nil {
		return err
	}
	tokenSource, err := databricksTokenSource(environment.databricksHost)
	if err != nil {
		return err
	}
	databricksClient, err := databricks.NewClient(databricks.ClientConfig{
		Host: environment.databricksHost, WarehouseID: environment.warehouseID, TokenSource: tokenSource,
	})
	if err != nil {
		return err
	}
	engine, err := databricks.NewQueryEngine(databricksClient)
	if err != nil {
		return err
	}
	queries, err := application.NewQueryService(store, policyResolver, bindingResolver, engine, store)
	if err != nil {
		return err
	}
	jobs, err := application.NewJobManager(ctx, queries, queryTimeout, defaultQueryJobCapacity, nil)
	if err != nil {
		return err
	}
	defer jobs.Close()

	oidcConfig := oidcauth.Config{
		IssuerURL: config.OIDC.IssuerURL, ClientID: config.OIDC.ClientID, BearerAudience: config.OIDC.BearerAudience,
		TenantClaim: config.OIDC.TenantClaim, RolesClaim: config.OIDC.RolesClaim,
		PermissionsClaim: config.OIDC.PermissionsClaim,
		AllowHTTP:        config.OIDC.DevelopmentAllowInsecureHTTP,
	}
	publicURL, err := url.Parse(config.HTTP.PublicURL)
	if err != nil {
		return err
	}
	authenticator, err := oidcauth.NewWeb(initializationContext, oidcauth.WebConfig{
		OIDC: oidcConfig, ClientSecret: environment.oidcClientSecret,
		RedirectURL: strings.TrimSuffix(config.HTTP.PublicURL, "/") + "/auth/callback",
		SessionKey:  environment.sessionKey, SessionTTL: sessionTTL, InsecureCookies: publicURL.Scheme == "http",
	})
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	handler, err := httpapi.NewServer(httpapi.Config{
		MaxBodyBytes: config.HTTP.MaxBodyBytes, ControlTimeout: controlTimeout,
		QueryTimeout: queryTimeout, AllowedOrigin: config.HTTP.PublicURL,
	}, httpapi.Dependencies{
		Authenticator: authenticator, AuthEndpoints: authenticator,
		Readiness:  store,
		Management: management, Catalog: store, CatalogSearch: catalogSearch,
		Queries: queries, Jobs: jobs,
	}, logger)
	if err != nil {
		return err
	}
	server, err := httpapi.NewRuntimeServer(listenAddress, handler)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", server.Addr, err)
	}
	defer listener.Close()
	if err := writeServeStatus(stdout, "listening", listener.Addr().String(), config.HTTP.PublicURL); err != nil {
		return err
	}

	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.Serve(listener)
	}()
	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancelShutdown()
		if err := server.Shutdown(shutdownContext); err != nil {
			return fmt.Errorf("shut down HTTP server: %w", err)
		}
		if err := <-serveErrors; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return writeServeStatus(stdout, "stopped", listener.Addr().String(), config.HTTP.PublicURL)
	}
}

func resolveHTTPAddress(configured, override string) (string, error) {
	address := strings.TrimSpace(configured)
	if strings.TrimSpace(override) != "" {
		address = strings.TrimSpace(override)
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		return "", fmt.Errorf("HTTP address must be host:port: %w", err)
	}
	return address, nil
}

func loadServeEnvironment(getenv func(string) string) (serveEnvironment, error) {
	databaseURL := strings.TrimSpace(getenv("METRICSPIRE_DATABASE_URL"))
	if databaseURL == "" {
		return serveEnvironment{}, errors.New("METRICSPIRE_DATABASE_URL is required")
	}
	sessionKey, err := decodeSessionKey(getenv("METRICSPIRE_SESSION_KEY"))
	if err != nil {
		return serveEnvironment{}, err
	}
	host := strings.TrimSpace(getenv("DATABRICKS_HOST"))
	warehouseID := strings.TrimSpace(getenv("DATABRICKS_SQL_WAREHOUSE_ID"))
	if host == "" || warehouseID == "" {
		return serveEnvironment{}, errors.New("DATABRICKS_HOST and DATABRICKS_SQL_WAREHOUSE_ID are required")
	}
	clientID := strings.TrimSpace(getenv("DATABRICKS_CLIENT_ID"))
	clientSecret := strings.TrimSpace(getenv("DATABRICKS_CLIENT_SECRET"))
	staticToken := strings.TrimSpace(getenv("DATABRICKS_TOKEN"))
	if (clientID == "") != (clientSecret == "") {
		return serveEnvironment{}, errors.New("DATABRICKS_CLIENT_ID and DATABRICKS_CLIENT_SECRET must both be set")
	}
	if clientID == "" && staticToken == "" {
		return serveEnvironment{}, errors.New("Databricks authentication requires OAuth M2M environment variables or DATABRICKS_TOKEN")
	}
	return serveEnvironment{
		databaseURL: databaseURL, sessionKey: sessionKey,
		oidcClientSecret: getenv("METRICSPIRE_OIDC_CLIENT_SECRET"),
		databricksHost:   host, warehouseID: warehouseID,
	}, nil
}

func decodeSessionKey(value string) ([]byte, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return nil, errors.New("METRICSPIRE_SESSION_KEY is required")
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		decoded, err := encoding.DecodeString(raw)
		if err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	return nil, errors.New("METRICSPIRE_SESSION_KEY must be base64 encoding of exactly 32 random bytes")
}

func loadTrustedRoutes(baseDirectory string, config runtimeconfig.Config) ([]application.PolicyConfiguration, []application.BindingConfiguration, error) {
	policies := make([]application.PolicyConfiguration, 0, len(config.Policies))
	for _, route := range config.Policies {
		var source model.PolicySource
		if err := contractio.ReadFile(resolveConfigPath(baseDirectory, route.Path), &source); err != nil {
			return nil, nil, err
		}
		policies = append(policies, application.PolicyConfiguration{
			Namespace: route.Namespace, ModelName: route.ModelName, Tenant: route.Tenant, Policy: source,
		})
	}
	bindings := make([]application.BindingConfiguration, 0, len(config.Bindings))
	for _, route := range config.Bindings {
		var source model.SourceBinding
		if err := contractio.ReadFile(resolveConfigPath(baseDirectory, route.Path), &source); err != nil {
			return nil, nil, err
		}
		if source.Engine != databricks.EngineName {
			return nil, nil, fmt.Errorf("binding route %s/%s uses unsupported engine %q", route.Namespace, route.ModelName, source.Engine)
		}
		bindings = append(bindings, application.BindingConfiguration{
			Namespace: route.Namespace, ModelName: route.ModelName, Binding: source,
		})
	}
	if _, err := application.NewConfiguredPolicyResolver(policies); err != nil {
		return nil, nil, err
	}
	if _, err := application.NewConfiguredBindingResolver(bindings); err != nil {
		return nil, nil, err
	}
	return policies, bindings, nil
}

func resolveConfigPath(baseDirectory, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(baseDirectory, path)
}

func writeServeStatus(writer io.Writer, status, address, publicURL string) error {
	return json.NewEncoder(writer).Encode(map[string]string{
		"status": status, "address": address, "public_url": publicURL,
	})
}
