package main

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/marmot1024/metricspire/internal/audit"
	"github.com/marmot1024/metricspire/internal/catalog"
	"github.com/marmot1024/metricspire/internal/governance"
	"github.com/marmot1024/metricspire/internal/repository/mysql"
	"github.com/marmot1024/metricspire/internal/runtimeconfig"
)

type metadataStore interface {
	catalog.Repository
	governance.Repository
	audit.Recorder
	Ready(context.Context) error
	Close()
}

// CLI URLs identify the metadata driver. Runtime deployments additionally pin
// storage.driver, so a misconfigured URL never silently selects another store.
func openMetadataStore(ctx context.Context, raw string) (metadataStore, error) {
	if strings.HasPrefix(strings.TrimSpace(raw), "mysql:") {
		if strings.TrimSpace(os.Getenv("METRICSPIRE_LAKEBASE_ENDPOINT")) != "" {
			return nil, errors.New("MySQL metadata cannot use a Lakebase endpoint")
		}
		return mysql.Open(ctx, strings.TrimSpace(raw))
	}
	return openStore(ctx, raw)
}
func openServeMetadataStore(ctx context.Context, c runtimeconfig.Config, raw string) (metadataStore, error) {
	if (c.StorageDriver() == "mysql") != strings.HasPrefix(strings.TrimSpace(raw), "mysql:") {
		return nil, errors.New("metadata URL does not match storage.driver")
	}
	store, err := openMetadataStore(ctx, raw)
	if err != nil {
		return nil, err
	}
	if my, ok := store.(*mysql.Store); ok {
		if err = my.CheckSchema(ctx); err != nil {
			my.Close()
			return nil, err
		}
	}
	return store, nil
}
