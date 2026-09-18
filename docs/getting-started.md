# Getting started

MetricSpire is a Go service backed by PostgreSQL for catalog state and an analytical engine for data queries. The steps below separate an **offline semantic-plan demo** from a **local catalog workflow**; neither is a production deployment.

## Prerequisites

- Go 1.26 or 1.27
- Docker with Compose, only for the PostgreSQL workflow

All commands run from the repository root. The `dist/` directory contains generated local artifacts.

## Compile and inspect a plan without a database

The example uses public, neutral order data. It compiles a semantic model and policy, then produces logical and physical plans without submitting an analytical query.

```bash
mkdir -p dist/demo

go run ./cmd/metricspire compile \
  --source examples/orders/model.yaml \
  --out dist/demo/manifest.json

go run ./cmd/metricspire compile-policy \
  --source examples/orders/policy.yaml \
  --manifest dist/demo/manifest.json \
  --out dist/demo/policy-bundle.json

go run ./cmd/metricspire plan \
  --manifest dist/demo/manifest.json \
  --policy dist/demo/policy-bundle.json \
  --context examples/orders/context.json \
  --query examples/orders/query.json \
  --binding examples/orders/binding.json \
  --capabilities examples/orders/capabilities.json \
  --logical-out dist/demo/logical-plan.json \
  --physical-out dist/demo/physical-plan.json
```

Open `dist/demo/logical-plan.json` to inspect semantic resolution and `dist/demo/physical-plan.json` to see the reviewed binding and capability checks. The example's `context.json` is trusted **only** in this offline CLI path. HTTP and MCP callers cannot submit their own tenant, identity, policy, binding, release, engine, or SQL.

## Try the catalog lifecycle locally

The Compose password is for local development only. Do not reuse it in a deployed environment.

```bash
docker compose up -d postgres
export METRICSPIRE_DATABASE_URL='postgres://metricspire:metricspire_dev_only@127.0.0.1:54329/metricspire?sslmode=disable'

go run ./cmd/metricspire migrate
go run ./cmd/metricspire draft-put \
  --namespace demo --source examples/orders/model.yaml \
  --actor local-user --expected-revision 0
go run ./cmd/metricspire publish \
  --namespace demo --model commerce --revision 1 --actor local-user
go run ./cmd/metricspire release-list \
  --namespace demo --model commerce
```

This demonstrates draft revisions, explicit publication, immutable releases, and the active pointer. PostgreSQL stores control metadata, not analytical facts or result rows. To execute `query-active`, additionally provide a reviewed source binding, a read-only Databricks SQL warehouse, and appropriate engine credentials. Real-engine tests are opt-in and require an explicitly configured environment.

## Run the HTTP service

`metricspire serve --config <runtime.yaml>` assembles the catalog, identity, query, audit, job, UI, and engine adapters. Start from the non-secret [portable OIDC example](../examples/runtime.example.yaml) or the [Databricks Apps example](../examples/runtime.databricks-apps.example.yaml); supply secrets through the environment or deployment platform, not the config file.

Run `metricspire migrate` as a separate operation before serving. The server never migrates PostgreSQL or creates analytical tables on startup. `GET /health/live` checks only the process; `GET /health/ready` checks only PostgreSQL. Neither proves a warehouse query succeeded.

The [Dockerfile](../Dockerfile) builds a non-root static-binary image. It is the self-hosted packaging path; the Databricks Apps staging package has a separate bootstrap and resource contract in [`deploy/databricks-apps`](../deploy/databricks-apps/README.md).

## Verify a change

```bash
node --test internal/httpapi/ui/app_test.js
go test ./...
go test -race ./...
go vet ./...
go mod verify
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' ./cmd/metricspire
```

The default suite uses local fixtures and mocks where appropriate. A passing build, unit test, or health probe is not a substitute for an authenticated deployment and a real, bounded analytical query.
