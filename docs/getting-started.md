# Getting started

Choose the path that matches your goal:

- **Use an existing team service:** start with the [Codex installer](mcp-installer.md#get-the-installer), [other MCP clients](mcp.md#connect), or your team's web metric catalog. You do not need Go, Docker or this repository.
- **Learn how metric definitions work:** follow the offline example below. You will generate a query plan without connecting to data.
- **Operate a service:** read [service setup](#run-the-http-service) for the required database, identity provider and warehouse configuration. The offline example alone does not set these up.

## Prerequisites for the local examples

- Go 1.26 or 1.27
- Docker with Compose, only for the PostgreSQL workflow

Clone the repository if you have not already done so:

```bash
git clone https://github.com/Marmot1024/metricspire.git
cd metricspire
```

Run the following commands from the repository root. The `dist/` directory contains generated local artifacts.

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

On success, the commands create four JSON files in `dist/demo/`: the compiled definition (`manifest.json`), access rules (`policy-bundle.json`), metric calculation plan (`logical-plan.json`) and data-access plan (`physical-plan.json`).

This example requests average order value and refund rate, grouped by day and customer segment, filtered to the east and south regions. Open `logical-plan.json` to inspect the selected metrics, grouping and time range, then `physical-plan.json` to inspect the approved table mappings. **There are no revenue rows or other data results:** the example plans the query but does not execute it. The example's `context.json` is trusted **only** in this offline CLI path. HTTP and MCP callers cannot submit their own tenant, identity, policy, binding, release, engine, or SQL.

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

## Configure your team

Use the [team configuration templates](../config/README.md) for a private runtime
file, business names/aliases and checksum-verified semantic exports. The
`validate-domains` command checks them offline, without database or Git access.

## Run the HTTP service

Before starting, prepare a PostgreSQL database for catalog state, an identity provider and an analytical engine with reviewed bindings and user access. Databricks SQL is the current analytical adapter. The [Databricks Apps package](../deploy/databricks-apps/README.md) is a dedicated staging path; portable OIDC hosting is configurable but still needs deployment acceptance.

`metricspire serve --config <runtime.yaml>` assembles the catalog, identity, query, audit, job, UI, and engine adapters. Start from the non-secret [portable OIDC example](../examples/runtime.example.yaml) or the [Databricks Apps example](../examples/runtime.databricks-apps.example.yaml); supply secrets through the environment or deployment platform, not the config file.

Run `metricspire migrate` as a separate operation before serving. The server never migrates PostgreSQL or creates analytical tables on startup. `GET /health/live` checks only the process; `GET /health/ready` checks the control PostgreSQL pool and, when online queries are enabled, their separate PostgreSQL pool. Neither proves warehouse access or snapshot freshness.

After starting, open the configured service URL and sign in. A query user starts in the metric catalog; a maintainer can manage definitions. Verify one published metric through a successful bounded query using the intended user before considering that environment usable.

The [Dockerfile](../Dockerfile) builds a non-root static-binary image. It is the self-hosted packaging path; the Databricks Apps staging package has a separate bootstrap and resource contract in [`deploy/databricks-apps`](../deploy/databricks-apps/README.md).

## Verify a change

```bash
python3 tools/check_docs.py
node --test internal/httpapi/ui/app_test.js
go test ./...
go test -race ./...
go vet ./...
go mod verify
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' ./cmd/metricspire
```

The default suite uses local fixtures and mocks where appropriate. A passing build, unit test, or health probe is not a substitute for an authenticated deployment and a real, bounded analytical query.
