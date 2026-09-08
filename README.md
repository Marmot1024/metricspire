# MetricSpire

**Define metrics once. Serve every application and AI agent.**

MetricSpire is an open-source semantic metrics layer and governed data API. It compiles reviewed metric definitions into immutable releases, authorizes structured queries, and executes bounded plans through replaceable analytical-engine adapters.

> Development preview, not a production-ready release. This README describes product usage and stable boundaries, not the project's execution log.

## Version names

- `v0.1.0` is the planned first product release; development builds report `0.1.0-dev`.
- `metricspire.io/v1alpha1` is the machine-readable contract version. It can evolve independently of the product release.
- Internal architecture-review revisions are not product or API versions.

## What it does

- stores drafts, revisions, immutable releases, the active-release pointer, and release events in PostgreSQL;
- compiles a constrained expression tree (`sum`, `count`, `count_distinct`, `avg`, `min`, `max`, metric references, arithmetic, and governed filters), never caller-provided SQL;
- keeps portable metric definitions separate from environment-specific table and column bindings;
- evaluates metric policy and request budgets before engine execution;
- builds deterministic logical and physical plans with content fingerprints;
- executes parameterized Databricks SQL with timeout, cancellation, typed results, row limits, and cumulative response-byte limits;
- rejects unsupported engine capabilities before submitting a query.
- exposes separate management and query permissions through a strict HTTP API with Problem responses, body/deadline limits, security headers, asynchronous job status/cancellation, and fail-closed query audit;
- verifies generic OIDC issuer/signature/expiry claims, separates the browser client audience from the API JWT access-token audience, and supports Authorization Code + PKCE browser login with encrypted, HTTP-only sessions;
- supports a fail-closed Databricks Apps identity profile in which authenticated users receive query access, one stable publisher-group ID additionally grants management, and the forwarded user token exists only in the in-memory engine-execution context;
- serves a dependency-free product UI for indicator discovery, structured basic-indicator maintenance, binding and change-impact review, immutable publication/rollback, and developer query validation; every action calls the same HTTP API rather than duplicating application logic.
- offers an MCP stdio bridge for AI clients, using that same authenticated HTTP API without duplicating catalog or execution logic.

## Core flow

```text
SemanticModel ──compile/publish──> immutable SemanticManifest
       │                                  │
       └──── PostgreSQL draft/release lifecycle ──── active release

SemanticQuery + server-authenticated RequestContext + server-resolved PolicyBundle
                         └──> LogicalPlan
LogicalPlan + server-resolved SourceBinding + EngineCapabilities
                         └──> PhysicalPlan
PhysicalPlan ──> QueryEngine adapter ──> bounded TypedResult
```

PostgreSQL is the transactional control plane; it does not store analytical facts or query results. Databricks is the first analytical adapter, not a required control-plane dependency. Future engines implement the same `QueryEngine` boundary without changing the catalog lifecycle.

Warehouse-native permissions remain authoritative for catalogs, schemas, tables, views, rows, and columns. MetricSpire adds only product-level governance that a warehouse does not model directly: metric publication, metric/dimension grants, active releases, request budgets, and audit context.

## Local semantic workflow

Requirements: Go 1.26 or 1.27.

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

`context.json` is trusted only in this offline CLI demonstration. The HTTP transport creates `RequestContext` from its configured trusted identity adapter; callers cannot self-report tenant, principal, roles, policy, binding, engine, or manifest fingerprint in `SemanticQuery`. The portable OIDC profile selects the issuer, browser client ID, API bearer audience, and claim names without binding MetricSpire to Databricks identity. Browser sessions derive from verified ID tokens; API bearer authentication requires a locally verifiable JWT access token for the configured API audience. The Databricks Apps profile instead consumes only managed-ingress identity plus the current-user API, and has no separate MetricSpire login endpoint.

## Local PostgreSQL workflow

The Compose password below is deliberately local-development-only. Do not reuse it in a deployed environment.

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

`query-active` additionally requires a reviewed engine binding, a read-only Databricks SQL warehouse, and OAuth M2M credentials (or an explicit token for local development). Credentials are environment inputs and must never be committed. See the [Phase 2 acceptance record](docs/phase2-acceptance.md) for the opt-in real-engine tests.

Databricks Apps may use Lakebase without a static database password. In that profile, set `METRICSPIRE_LAKEBASE_ENDPOINT` to the App database-resource endpoint and use the platform-injected `PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER`, `PGSSLMODE`, `DATABRICKS_HOST`, `DATABRICKS_CLIENT_ID`, and `DATABRICKS_CLIENT_SECRET`. MetricSpire obtains a one-hour database credential only when the PostgreSQL pool opens a physical connection. `METRICSPIRE_DATABASE_URL` and `METRICSPIRE_LAKEBASE_ENDPOINT` are mutually exclusive; `serve` still never runs migrations automatically.

## HTTP service

`metricspire serve --config <runtime.yaml>` composes the existing catalog, query, audit, identity, job, UI, and Databricks adapter boundaries. Runtime configuration selects exactly one authentication provider: portable OIDC or Databricks Apps. Files contain only non-secret routes and provider metadata; database URLs, OIDC session/client secrets, and engine credentials remain environment inputs. See [`examples/runtime.example.yaml`](examples/runtime.example.yaml), [`examples/runtime.databricks-apps.example.yaml`](examples/runtime.databricks-apps.example.yaml), and the [Phase 3 HTTP contract](docs/phase3-http-api.md).

Platforms that allocate a port at runtime can pass `--http-address 0.0.0.0:<port>` as a trusted process argument without rewriting the reviewed configuration. The server accepts HTTP/1.1, HTTP/2 over TLS, and unencrypted HTTP/2 (H2C) on the same listener; TLS normally terminates at the deployment ingress.

Run `metricspire migrate` explicitly before starting the service. `serve` never migrates PostgreSQL on startup and never creates analytical tables. The current Databricks adapter emits bounded, parameterized `SELECT` statements only.

The repository also provides a multi-stage OCI [`Dockerfile`](Dockerfile). The runtime image is a static binary plus CA certificates and license notices, runs as numeric non-root user `65532`, and contains no shell or credentials. Mount a reviewed runtime configuration and its policy/binding files read-only under `/etc/metricspire`; inject secret environment variables through the deployment platform. Run `metricspire migrate` as a separate one-shot deployment step before starting replicas. A successful local build is packaging evidence, not deployed-service acceptance.

Unauthenticated `GET /health/live` reports process liveness. `GET /health/ready` pings only the PostgreSQL control plane and returns a generic `503 not_ready` without connection details when unavailable; it never queries Databricks. These endpoints are intended for platform probes, not as acceptance evidence.

## 页面试用（既有 staging 订单样本）

这是 TPCH 样本预演，不是公司业务数据验收；普通试用者只执行前四步。

1. 打开维护者提供的 App 链接，用本人账号登录，选择业务域 `acceptance`。
2. 在“指标库”搜索 `gross_revenue`，阅读定义及可用维度；清空搜索词并重新搜索，恢复完整目录后点击“用于查询”。查询页选择 `gross_revenue`、`order_count`、`average_order_value`。
3. 分组选择 `customer_segment`、`order_status`；筛选选择 `order_id` → “属于其中” → `1,2,3,4,5`，点击“添加”；最多返回填 `10`，再运行查询。当前样本未声明时间口径，时间预设禁用是预期行为。
4. 预期返回 5 行、未截断；收入列合计 `693441.45`、订单数合计 `5`，行顺序不作要求。点击“复制 API 请求”查看接入方式；在终端运行须自行提供 API 令牌，不能把浏览器登录当成终端已登录。反馈截图、出错步骤及请求/任务 ID 即可，勿发送令牌或 Cookie。
5. **仅指定维护者**测试发布：先记录草稿和当前线上版本 → 修改一处说明 → 应用编辑并检查 → 保存草稿 → 填写说明并发布 → 在版本管理中恢复原线上版本。最后恢复并保存原草稿；回滚线上版本不会自动回退草稿。多人协作时先核对版本，避免覆盖他人修改。

## MCP client connection

Build the CLI with `go build -o dist/metricspire ./cmd/metricspire`. Configure a
stdio-capable MCP client to launch that binary with these arguments:

```json
{
  "mcpServers": {
    "metricspire": {
      "command": "/absolute/path/to/metricspire",
      "args": ["mcp", "--api-url", "https://metrics.example.com"]
    }
  }
}
```

The launcher must inject `METRICSPIRE_API_TOKEN` through its secret/environment
mechanism. Use an API access token accepted by the configured HTTP identity
provider, not a browser ID token. Databricks Apps uses an authorized workspace
API token. Do not paste tokens into prompts, arguments or checked-in client
configuration. One process represents one principal; on token expiry, refresh
it through the existing identity provider and restart the bridge. No new login
system, App, database or MCP HTTP listener is required. Client configuration
syntax varies; the command, arguments and environment are the contract.

Codex CLI 的对应配置如下；令牌由启动进程的环境提供，不写入 TOML：

```toml
[mcp_servers.metricspire]
command = "/absolute/path/to/metricspire"
args = ["mcp", "--api-url", "https://metrics.example.com"]
env_vars = ["METRICSPIRE_API_TOKEN"]
```

交互使用时确认查询工具的执行请求。非交互 `codex exec` 无法弹出审批；仅对已明确授权的任务，可在本次进程配置 `mcp_servers.metricspire.tools.submit_query.approval_mode="approve"`。不要因此放开全局审批或沙盒。[Codex MCP 配置](https://learn.chatgpt.com/docs/extend/mcp?surface=cli)

| Tool | Purpose |
| --- | --- |
| `list_namespaces` / `search_metrics` | Find business domains, metric codes, definitions, dimensions and examples |
| `explain_query` | Validate and explain a `namespace` + `query` without running analytical SQL |
| `submit_query` | Submit that `SemanticQuery`; creates a job and audit record and may incur query cost |
| `get_query` / `cancel_query` | Read or cancel the returned `job_id` |

Discover → explain intent → submit → poll. The AI client composes a structured
candidate; MetricSpire does not host an LLM or guarantee natural-language
accuracy. Catalog text and result cells are data, not executable instructions.
The caller needs no internal model/table name or saved query template. Explain
does not pin a future submission: the job records the release actually used.
There are no SQL, publication, identity-override or management tools.

配置好客户端后，可用这句话做同一场景的中文试用：“在 acceptance 中查订单
1–5 的订单总金额、订单数和客单价，按客户类型与订单状态分组，上限 10 行。
请先从目录确认指标与维度，解释口径并展示查询参数，等我确认后再执行。”
找不到匹配项时应说明缺失，不猜指标 code。协议测试通过不代表自然语言理解已验收。

The bridge allows HTTPS origins (HTTP only on loopback for local development),
rejects redirects, and caps request/response bytes at 1/8 MiB with a 30-second
HTTP timeout. Existing service permissions, query limits and audit still apply.
After an uncertain submission failure, do not retry automatically. Canceling
an MCP call stops that HTTP request, not an already accepted query job; use
`cancel_query`. Decimal strings and integer JSON text are preserved.

Opt-in integration: with a staging token, set
`METRICSPIRE_RUN_MCP_ACCEPTANCE=staging-read-only`,
`METRICSPIRE_MCP_TEST_URL` and absolute `METRICSPIRE_MCP_TEST_BINARY`, then run
`go test ./cmd/metricspire -run '^TestMCPStagingAcceptance$' -v -count=1`.
This requires the existing `acceptance` TPCH fixture; it queries, but never
creates resources or modifies metric definitions or analytical tables.

## Distribution and deployment profiles

MetricSpire has one product core and two deployment profiles. They are release adapters, not separate products:

- **Open-source self-hosting**: the planned `v0.1.0` release will publish source, checksummed static Linux binaries, and versioned OCI images. The OCI image is the portable default for containers, Kubernetes, or a VM with a container runtime; the raw binary remains useful for minimal VM and local installations. PostgreSQL and analytical engines stay external. Release automation, an SBOM, provenance/signing, upgrade notes, and a clean-room/secret scan are separate public-release gates, not claims already satisfied by this development checkout or prerequisites for internal trials. A Helm chart is intentionally deferred until real operators need one.
- **Databricks Apps company profile**: the same Go service is intended to run behind Databricks Apps ingress and use a separately managed PostgreSQL-compatible control plane and a SQL warehouse resource. Databricks Apps deploys source with its own runtime and `app.yaml`; it does not consume this repository's OCI `Dockerfile`. The official development and dependency contract covers Python and Node.js, while custom commands leave packaged executables technically possible but do not make Go an officially documented runtime. On 2026-09-03, the acceptance-only [`deploy/databricks-apps-probe`](deploy/databricks-apps-probe) package ran in a resource-free staging custom App as an `amd64` Go 1.27.0 process. Authenticated `GET /health/live` returned 200, stop/start returned to 200, and the App was left stopped. Databricks ingress negotiated HTTP/2 externally but forwarded HTTP/1.1 to the process. This closes technical Go execution for that staging runtime; it does not establish official Go support or complete the full service identity/data acceptance.

On 2026-09-04, the dedicated staging Lakebase project was bound to the full App. An explicitly approved one-shot migration created only the dedicated `metricspire` schema and its seven control-plane tables, after which the migration switch was removed and the normal package was redeployed. Short-lived OAuth database credentials worked without being persisted; a forced read-only reconciliation confirmed both registered migrations, two immutable releases, the final v1 active pointer, and the publication/rollback/query audit records. No analytical table was created or modified.

The reviewable [`deploy/databricks-apps`](deploy/databricks-apps) package runs the full service as checksum-protected amd64 and arm64 gzip files. The staging App has one dedicated Lakebase resource, one read-only SQL warehouse resource, and one dedicated publisher group. Managed-ingress identity and publisher management passed with the available real publisher. The first attempt used `sql:restricted-query`, but Databricks returned 403 for the forwarded-user Statement API request even though the same user could query the same warehouse directly. With the App's staging scope changed to `sql`, the same governed query succeeded. This is observed staging compatibility evidence, not a claim about the undocumented cause of that difference.

The staging App definition, resources, releases, and audit evidence are retained. Its compute is currently running for the final UI walkthrough and may be stopped afterward to avoid idle cost.

Databricks Apps changes only the deployment authentication adapter, not the semantic or policy core. Every verified App user receives the configured query-policy role plus `query:execute`; one stable publisher group additionally receives `model:manage`. Interactive SQL uses the request's short-lived token, so Unity Catalog remains authoritative for data access. Staging has verified this split for both the publisher and a real non-publisher: the latter could query but received HTTP 403 from draft, publish, and rollback. The negative Unity Catalog case still needs a separately reviewed fixture and a genuinely least-privilege principal; the public sample and current broadly privileged users cannot prove that state. Generic OIDC remains the portable self-hosted adapter.

## Contracts and guarantees

The `v1alpha1` schema registry is [`contracts/metricspire.schema.json`](contracts/metricspire.schema.json). The Go implementation additionally checks references, cycles, types, time semantics, join cardinality, policy, budgets, fingerprints, and engine capabilities.

Important guarantees:

- time ranges are half-open `[start, end)`; grouping declares a business timezone and weekly queries declare their week start;
- v0.1 relationships are directed `many_to_one` joins, so grouping cannot silently multiply facts;
- draft or inactive releases cannot be selected by a caller;
- published metric codes cannot be silently removed or change execution semantics;
- policy evaluation fails closed and explicit deny wins;
- values are parameterized and physical identifiers come only from validated bindings;
- unknown fields, duplicate JSON/YAML keys, raw SQL, and identity fields in a query are rejected.

## Verification

```bash
node --test internal/httpapi/ui/app_test.js
go test ./...
go test -race ./...
go vet ./...
go mod verify
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' ./cmd/metricspire
```

Real PostgreSQL and Databricks acceptance is explicit and opt-in; skipped tests or mock responses are never reported as real integration proof.

Read the [architecture note](docs/architecture.md), [Phase 1 acceptance](docs/phase1-acceptance.md), [Phase 2 acceptance](docs/phase2-acceptance.md), and [Phase 3 HTTP contract](docs/phase3-http-api.md) for boundaries and evidence.

## Non-goals for v0.1

MetricSpire is not an ETL platform, BI dashboard, data warehouse, arbitrary SQL gateway, cross-engine execution engine, or large-result export system. The current scope excludes a hosted LLM, remote MCP HTTP transport, SDK/template systems, Redis, Kafka, complex approvals, and simultaneous support for multiple analytical engines.

## License

Apache License 2.0. This repository is an independent clean-room implementation using neutral public examples. It contains no legacy source, private business schema, credentials, or inherited Git history.
