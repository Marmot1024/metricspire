package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/marmot1024/metricspire/internal/adapter/clickhousequery"
	"github.com/marmot1024/metricspire/internal/adapter/postgresquery"
	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/runtimeconfig"
)

// Open only the selected result engine. Metadata storage remains independent.
func openOnlineEngine(ctx context.Context, urlValue string, config runtimeconfig.OnlineConfig) (application.QueryEngine, func(context.Context) error, func(), error) {
	if config.EngineDriver() == clickhousequery.EngineName {
		age, _ := runtimeconfig.ParseDuration(config.MaxDataAge, time.Minute)
		engine, err := clickhousequery.NewQueryEngine(ctx, urlValue, clickhousequery.Config{Tenant: config.Tenant, Resources: config.Resources, MaxDataAge: age, MaxBytes: 4 << 20})
		if err != nil {
			return nil, nil, nil, err
		}
		return engine, engine.Ready, engine.Close, nil
	}
	poolConfig, err := pgxpool.ParseConfig(urlValue)
	if err != nil {
		return nil, nil, nil, errors.New("invalid online PostgreSQL connection configuration")
	}
	poolConfig.MaxConns = 4
	poolConfig.ConnConfig.RuntimeParams["application_name"] = "metricspire-online"
	poolConfig.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	poolConfig.ConnConfig.RuntimeParams["timezone"] = "UTC"
	poolConfig.ConnConfig.RuntimeParams["statement_timeout"] = "2000"
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, nil, nil, errors.New("could not open online PostgreSQL pool")
	}
	ok := false
	defer func() {
		if !ok {
			pool.Close()
		}
	}()
	var privileged bool
	if err = pool.QueryRow(ctx, `SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname=current_user`).Scan(&privileged); err != nil || privileged {
		return nil, nil, nil, errors.New("online PostgreSQL requires a non-superuser, non-BYPASSRLS read role")
	}
	age, _ := runtimeconfig.ParseDuration(config.MaxDataAge, time.Minute)
	onlineEngine, err := postgresquery.NewQueryEngine(pool, postgresquery.Config{Tenant: config.Tenant, Resources: config.Resources, MaxDataAge: age, MaxBytes: 4 << 20})
	if err != nil {
		return nil, nil, nil, err
	}
	for _, resource := range config.Resources {
		qualified := pgx.Identifier{resource.Schema, resource.Table}.Sanitize()
		var readOnly bool
		// Table checks alone miss column grants. These inquiries also include
		// privileges inherited by the current role.
		if err := pool.QueryRow(ctx, `SELECT
				has_table_privilege(current_user,$1,'SELECT')
				AND NOT has_table_privilege(current_user,$1,'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
				AND NOT has_any_column_privilege(current_user,$1,'INSERT,UPDATE,REFERENCES')`, qualified).Scan(&readOnly); err != nil || !readOnly {
			return nil, nil, nil, errors.New("online credential must have SELECT and no write privileges on each enabled source")
		}
	}
	ok = true
	return onlineEngine, pool.Ping, pool.Close, nil
}
