# Contributing to MetricSpire

MetricSpire keeps its semantic and authorization core deliberately small. Changes should preserve explicit business meaning, bounded execution, immutable history, and provider-neutral contracts.

## Development workflow

`main` is the releasable branch. Do not push feature work directly to it.

1. Update local `main`, then create a focused branch such as `feature/release-lifecycle`, `fix/query-timezone`, or `docs/mcp-onboarding`.
2. Keep one concern per pull request. Do not mix environment receipts, generated deployment packages, or unrelated formatting changes with product code.
3. Run the local quality gate below and review `git diff --check` plus the staged file list.
4. Open a pull request. Required CI must pass before a squash merge into `main`.
5. Deploy or run environment acceptance from the reviewed commit, and record private environment evidence outside the public repository.

Repository administrators should protect `main`: require a pull request, require the `test` CI job, dismiss stale approvals after new commits, block force pushes and deletion, and allow administrators to follow the same rules. A solo maintainer may approve their own PR only when GitHub policy permits it, but must not bypass required CI.

## Local quality gate

```bash
python3 tools/check_docs.py
node --test internal/httpapi/ui/app_test.js
python3 -m unittest tools/test_mcp_installer.py tools/test_deploy_staging.py
python3 -m unittest discover -s deploy/databricks-apps -p test_render.py
go mod verify
go test -race ./...
go vet ./...
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' ./cmd/metricspire
git diff --check
```

CI starts disposable PostgreSQL 17 and ClickHouse 25.8 services and runs catalog and online HTTP integration tests on every Go matrix entry. `METRICSPIRE_REQUIRE_POSTGRES_TESTS=1` makes missing database URLs a failure instead of a skip. Locally, these tests remain opt-in; use a disposable loopback database with permission to create schemas and roles:

```bash
export METRICSPIRE_TEST_DATABASE_URL='postgres://USER:PASSWORD@127.0.0.1:PORT/DISPOSABLE_DB?sslmode=disable'
export METRICSPIRE_ONLINE_TEST_DATABASE_URL="$METRICSPIRE_TEST_DATABASE_URL"
METRICSPIRE_REQUIRE_POSTGRES_TESTS=1 go test -race -count=1 -v ./internal/repository/postgres ./internal/httpapi
```

ClickHouse tests additionally require `METRICSPIRE_CLICKHOUSE_TEST_URL` with a disposable loopback administrator and `METRICSPIRE_REQUIRE_CLICKHOUSE_TESTS=1`. See [the isolated lab](deploy/testing/README.md) for setup and the optional large synthetic fixture.

Repository and online HTTP fixtures create and remove their own isolated schemas and synthetic roles. The full CI suite also exercises the CLI catalog workflow in the disposable service's default schema; those rows are discarded with the service. Coverage includes first install, upgrade from the original catalog schema, checksum drift rejection, migration and publication transaction rollback, lifecycle/concurrent writes, and online HTTP permissions/failure boundaries. Sustained load remains separately opt-in via `METRICSPIRE_ONLINE_SOAK_DURATION`; shared-runner timings are not production SLAs. No offline business data is synchronized by these tests.

Databricks, external OAuth, and deployed-App tests remain opt-in. They must use explicitly reviewed resources and must not be represented as passed when only mocks or fixtures ran.

## Product and data rules

- Public requests use structured metric codes, dimensions, filters, time bounds, and limits. Never add caller-provided SQL, identity, policy, physical bindings, or credentials.
- Published releases are immutable. A change creates a new revision and release; deactivation removes only the active pointer and preserves history.
- `trial` and `certified` are distinct release channels. Trial metrics remain visibly unverified and require an explicit deployment allowlist.
- PostgreSQL migrations are append-only. Never edit an applied migration; add a new numbered migration and test both first install and upgrade behavior.
- Any destructive environment operation starts with a read-only inventory, names the exact environment and rows/resources, and preserves an approved recovery path when the data has audit value.
- Keep credentials, private schemas, production identifiers, acceptance receipts, generated `dist/`, and local agent notes out of Git.

## Documentation

Update the closest stable guide when a public contract changes. Keep `README.md`, `README.zh-CN.md`, and `README.ja.md` aligned for top-level product claims. Historical phase logs and environment-specific evidence belong in ignored local notes, not the public documentation tree.

For documentation changes, run `python3 tools/check_docs.py` and execute any changed local example commands. Check that the three overview pages describe the same capability and maturity boundaries, and that examples distinguish stored fixture results from live queries. The checker covers local file/Markdown-heading links, JSON examples and repeated prose; it does not validate external URLs, translations, browser layout or deployment acceptance. A prose-only change does not require rerunning unrelated database or authentication acceptance.

Keep the three `docs/assets/architecture*.svg` diagrams aligned when integration roles change, and render them to check labels and arrows. Maintain the shipped-adapter matrix in `docs/architecture.md`; a database named as an extension target must not be advertised as an available runtime adapter.
