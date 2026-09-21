"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const fs = require("node:fs");
const vm = require("node:vm");
const {catalogStatusCounts, catalogStatusLabel, catalogStatusShortLabel, compactDimensions, draftCatalogEntries, errorMessage, filterCatalogEntries, governanceCatalogEntries, groupPublicationIssues, humanOwner, humanTag, matchingQueryMetrics, matchesCatalogSearch, mergeCatalogEntries, metricCalculationNote, metricDetailQuery, metricExpressionLabel, metricFilterLabel, planSummaryRows, preferredTimeGranularity, resolvePresetRange, sharedTimeMetadata, verificationLabel, zonedMidnightISO, shellQuote} = require("./app.js");
const {execFileSync} = require("node:child_process");

test("copied request preserves apostrophes and shell characters as literal JSON", () => {
  const payload = JSON.stringify({values: ["O'Reilly", "$(echo unintended)", "`echo unintended`", "$PATH", "a\nb"]});
  assert.equal(execFileSync("/bin/sh", ["-c", "printf %s " + shellQuote(payload)], {encoding: "utf8"}), payload);
});

test("maintainer catalog exposes unverified drafts without making them published", () => {
  const draft = {source: {spec: {
    dimensions: [{name: "date", time_granularities: ["day", "month"]}],
    metrics: [{name: "iap_amount", display_name: "付费额", owner: "土拨鼠", tags: ["governance_unverified"],
      value_type: "decimal", unit: "usd", allowed_dimensions: ["date"], time_dimension: "date",
      verification: {status: "unverified"}}],
  }}};
  const entries = draftCatalogEntries({namespace: "mm", model_name: "player_daily_behavior"}, draft);
  assert.equal(entries.length, 1);
  assert.equal(entries[0].catalog_status, "draft");
  assert.equal(entries[0].release_id, "");
  assert.deepEqual(entries[0].time_granularities, ["day", "month"]);
  assert.equal(matchesCatalogSearch(entries[0], "付费"), true);
  assert.equal(matchesCatalogSearch(entries[0], "收入"), false);
});

test("published metric wins over a same-code draft in the catalog view", () => {
  const published = [{name: "iap_amount", release_id: "rel_1"}];
  const drafts = [{name: "iap_amount", catalog_status: "draft", formula_summary: "SUM(amount)",
    authoritative_source: {resource: "payments", field: "amount"}, fact_grain: "player + date"},
  {name: "level_count", catalog_status: "draft"}];
  const view = mergeCatalogEntries(published, drafts);
  assert.equal(view.length, 2);
  assert.equal(view[0].catalog_status, "published");
  assert.equal(view[0].release_id, "rel_1");
  assert.equal(view[0].formula_summary, "SUM(amount)");
  assert.equal(view[0].authoritative_source.resource, "payments");
  assert.equal(view[0].fact_grain, "player + date");
  assert.equal(view[1].name, "level_count");
});

test("catalog status counts do not double count drafts shadowed by a published metric", () => {
  const view = mergeCatalogEntries(
    [{name: "iap_amount", release_id: "rel_1"}],
    [{name: "iap_amount", catalog_status: "draft"}, {name: "level_count", catalog_status: "governance"}],
  );
  assert.deepEqual(catalogStatusCounts(view), {published: 1, draft: 0, governance: 1});
});

test("catalog defaults to queryable entries and keeps governance records explicitly reachable", () => {
  const metrics = [
    {name: "published", catalog_status: "published"},
    {name: "draft", catalog_status: "draft"},
    {name: "pending", catalog_status: "governance"},
  ];
  assert.deepEqual(filterCatalogEntries(metrics).map((metric) => metric.name), ["published", "draft"]);
  assert.deepEqual(filterCatalogEntries(metrics, "governance").map((metric) => metric.name), ["pending"]);
  assert.equal(filterCatalogEntries(metrics, "all").length, 3);
});

test("trial metrics remain visibly business-unverified", () => {
  const metric = {catalog_status: "published", tags: ["governance_unverified"]};
  assert.equal(catalogStatusLabel(metric), "已发布 · 业务待验证");
  assert.equal(catalogStatusLabel({catalog_status: "published", verification_status: "unverified"}), "已发布 · 业务待验证");
  assert.equal(verificationLabel(metric), "技术试查证据已登记");
  assert.equal(verificationLabel({verification: {status: "verified"}}), "口径已经验证");
  assert.equal(humanTag("business_type_atomic"), "原子指标");
  assert.equal(humanTag("governance_unverified"), "业务待验证");
  assert.equal(catalogStatusShortLabel(metric), "试用");
  assert.equal(humanOwner("pending_assignment"), "待分配");
});

test("query chooser shows selected metrics first and searches instead of listing the whole catalog", () => {
  const metrics = [
    {name: "iap_amount", display_name: "付费金额"},
    {name: "game_start_count", display_name: "游戏启动次数"},
    {name: "level_pass_count", display_name: "过关次数"},
  ];
  assert.deepEqual(matchingQueryMetrics(metrics, "", new Set(["iap_amount"])).map((metric) => metric.name), ["iap_amount"]);
  assert.deepEqual(matchingQueryMetrics(metrics, "次数", new Set(["iap_amount"]), 2).map((metric) => metric.name), ["iap_amount", "game_start_count"]);
  assert.deepEqual(matchingQueryMetrics(metrics, "missing", new Set()).map((metric) => metric.name), []);
});

test("metric registry searches external codes and presents structured formulas", () => {
  const metric = {external_code: "1024", name: "paid_users", display_name: "付费用户数", expression: {
    op: "count_distinct", field: "player.user_id", filters: [{field: "payment.amount", operator: "neq", values: ["0"]}],
  }};
  assert.equal(matchesCatalogSearch(metric, "1024"), true);
  assert.equal(metricExpressionLabel(metric.expression), "COUNT(DISTINCT CASE WHEN payment.amount <> '0' THEN player.user_id ELSE NULL END)");
  assert.equal(metricExpressionLabel({op: "divide", args: [{op: "metric", metric: "revenue"}, {op: "metric", metric: "buyers"}]}), "(revenue ÷ buyers)");
  assert.equal(metricFilterLabel({field: "order.status", operator: "in", values: ["paid", "refunded"]}), "order.status IN ('paid', 'refunded')");
  assert.equal(compactDimensions(["date", "country", "platform"]), "date · country · +1");
  assert.equal(metricCalculationNote(metric), "去重计数字段 player.user_id；仅统计满足 payment.amount <> '0' 的记录。");
});

test("metric detail plan query uses a bounded calendar range without executing SQL", () => {
  const query = metricDetailQuery({name: "daily_active_users", time_dimension: "date",
    dimension_details: [{name: "date", calendar_timezone: "UTC"}]}, new Date("2026-09-21T08:00:00Z"));
  assert.equal(query.api_version, "metricspire.io/v1alpha1");
  assert.equal(query.kind, "SemanticQuery");
  assert.deepEqual(query.metrics, ["daily_active_users"]);
  assert.deepEqual(query.time_range, {
    dimension: "date", start: "2026-09-20T00:00:00.000Z", end: "2026-09-21T00:00:00.000Z", timezone: "UTC",
  });
  assert.equal(query.limit, 1);
});

test("common authorization and time-grouping failures explain the recovery action", () => {
  assert.match(errorMessage({status: 403, request_id: "req_1"}), /权限组.*req_1/);
  assert.match(errorMessage({detail: "grouped time dimensions require explicit grouping semantics", request_id: "req_2"}), /选择按日、按周或按月.*req_2/);
});

test("publication issues are grouped by actionable cause instead of rendering an error wall", () => {
  const groups = groupPublicationIssues([
    'metric "revenue" is not verified', 'metric "orders" is not verified',
    'metric "revenue" has no owner', 'metric "orders" has no verification evidence',
  ]);
  assert.deepEqual(groups.map((group) => [group.key, group.issues.length]), [
    ["not_verified", 2], ["owner", 1], ["evidence", 1],
  ]);
  assert.deepEqual(groups[0].metrics, ["revenue", "orders"]);
});

test("governance catalog keeps all records but only marks executable definitions as draft previews", () => {
  const records = [
    {namespace: "mm", revision: 1, definition: {code: "ready", display_name: "可试查", description: "ready",
      semantic_readiness: "executable_unverified", semantic_model_name: "daily", test_dimensions: ["date"], verification: {status: "unverified"}}},
    {namespace: "mm", revision: 1, definition: {code: "pending", display_name: "待治理", description: "pending",
      semantic_readiness: "needs_semantic_remediation", test_dimensions: ["date"], verification: {status: "unverified"}}},
  ];
  const entries = governanceCatalogEntries(records, [{name: "ready", model_name: "daily", time_granularities: ["day"]}]);
  assert.deepEqual(entries.map((entry) => entry.catalog_status), ["draft", "governance"]);
  assert.equal(entries[0].semantic_model_name, "daily");
  assert.equal(entries[1].semantic_model_name, "");
  assert.equal(entries[0].authoritative_source, null);
});

test("time dimension has a deterministic safe grouping default", () => {
  assert.equal(preferredTimeGranularity(["month", "day", "week"]), "day");
  assert.equal(preferredTimeGranularity(["month"]), "month");
  assert.equal(preferredTimeGranularity([]), "");
});

test("date-backed metrics disclose and share one fixed calendar timezone", () => {
  const metric = (name, timezone) => ({name, time_dimension: "date", time_granularities: ["day", "month"],
    dimension_details: [{name: "date", type: "time", data_type: "date", calendar_timezone: timezone}]});
  assert.deepEqual(sharedTimeMetadata([metric("revenue", "UTC"), metric("orders", "UTC")]), {
    dimension: "date", granularities: ["day", "month"], calendarTimezone: "UTC",
  });
  assert.equal(sharedTimeMetadata([metric("revenue", "UTC"), metric("orders", "Asia/Shanghai")]).incompatible, true);
});

test("query form locks a date-backed metric to its declared calendar timezone", () => {
  const {context, run} = editorContext();
  context.document.getElementById("time-grain").options = [];
  context.document.getElementById("business-timezone").value = "Asia/Shanghai";
  run(`configureTimeControls({dimension: "date", granularities: ["day"], calendarTimezone: "UTC"})`);
  assert.equal(context.document.getElementById("business-timezone").value, "UTC");
  assert.equal(context.document.getElementById("business-timezone").readOnly, true);
  assert.match(context.document.getElementById("business-timezone").title, /UTC/);
});

test("explain and plan have a human-readable summary independent of raw JSON", () => {
  const rows = Object.fromEntries(planSummaryRows("plan", {
    release: {id: "rel_test"},
    logical_plan: {
      root_entity: "player_day",
      metrics: [{name: "game_start_count", output: true}],
      dimensions: [{name: "date", output: true}],
      time_range: {start: "2026-09-01T00:00:00Z", end: "2026-09-02T00:00:00Z", timezone: "Asia/Shanghai"},
      filters: [], limit: 10, lineage: {datasets: ["player_daily"]},
    },
    physical_plan: {engine: "databricks", root: {resource: {catalog: "main", schema: "gold", table: "player_daily"}}},
  }, [{name: "game_start_count", display_name: "游戏启动次数"}]));
  assert.equal(rows["指标"], "游戏启动次数 (game_start_count)");
  assert.equal(rows["执行引擎"], "databricks");
  assert.equal(rows["数据来源"], "main.gold.player_daily");
  assert.equal(rows["筛选"], "无（筛选条件可选）");
});

// Exercise the actual form handlers without adding a production DOM dependency.
function editorContext() {
  const context = vm.createContext({structuredClone});
  vm.runInContext(fs.readFileSync(require.resolve("./app.js"), "utf8"), context);
  const element = () => ({value: "", checked: false, disabled: false, dataset: {}, children: [], handlers: {},
    addEventListener(name, handler) { this.handlers[name] = handler; },
    setAttribute(name, value) { this[name] = value; },
    append(...children) { this.children.push(...children); },
    replaceChildren(...children) { this.children = children; }});
  const elements = new Map();
  context.document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, element());
      return elements.get(id);
    },
    createElement: element,
    createTextNode: (text) => ({textContent: text}),
    querySelectorAll: () => [],
  };
  vm.runInContext(`state.draft = {source: {spec: {
    entities: [{name: 'orders', dataset: 'order_source'}],
    datasets: [{name: 'order_source', fields: [{name: 'id'}, {name: 'amount'}]}],
    dimensions: [{name: 'status', entity: 'orders'}, {name: 'segment', entity: 'customers'}],
    metrics: []
  }}}; state.creatingMetric = true;`, context);
  return {context, elements, run: (code) => vm.runInContext(code, context)};
}

test("query builder automatically completes grouped time semantics and keeps filters optional", () => {
  const {context, run} = editorContext();
  run(`state.catalog = [{name: 'game_start_count', display_name: '游戏启动次数', catalog_status: 'published',
    allowed_dimensions: ['date'], time_dimension: 'date', time_granularities: ['day', 'month']}];
    state.queryMetricKeys = new Set(['game_start_count']); state.filters = [];`);
  context.document.querySelectorAll = (selector) => selector === "input[name='query-dimension']:checked" ? [{value: "date", checked: true}] : [];
  const values = {"row-limit": "10", "time-preset": "custom", "time-grain": "",
    "business-timezone": "Asia/Shanghai", "time-start": "2026-09-01", "time-end": "2026-09-02"};
  for (const [id, value] of Object.entries(values)) context.document.getElementById(id).value = value;
  const query = run("buildQuery().query");
  assert.deepEqual(JSON.parse(JSON.stringify(query.time_grouping)), {dimension: "date", timezone: "Asia/Shanghai", granularity: "day"});
  assert.equal(query.filters.length, 0);
  assert.deepEqual(Array.from(query.group_by), ["date"]);
});

test("new metric form submits an entity-qualified expression, not a physical dataset", () => {
  const {context, run} = editorContext();
  run("renderEntityFields('orders', 'orders.amount')");
  const values = {"metric-external-code": "1001", "metric-code": "revenue", "metric-display-name": "收入",
    "metric-description": "订单金额合计", "metric-owner": "data-team",
    "metric-entity": "orders", "metric-operation": "sum", "metric-value-type": "decimal"};
  for (const [id, value] of Object.entries(values)) context.document.getElementById(id).value = value;
  const metric = run("metricFromEditor()");
  assert.equal(metric.expression.field, "orders.amount");
  assert.equal(metric.entity, "orders");
  assert.equal(metric.expression.op, "sum");
  assert.equal(metric.external_code, "1001");
  assert.equal(context.document.getElementById("metric-field").value, "orders.amount");
});

test("metric editor preserves constrained CASE WHEN filters in the structured expression", () => {
  const {context, run} = editorContext();
  const values = {"metric-external-code": "1002", "metric-code": "paid_revenue", "metric-display-name": "付费收入",
    "metric-description": "只汇总成功订单收入", "metric-owner": "data-team", "metric-entity": "orders",
    "metric-operation": "sum", "metric-value-type": "decimal", "metric-field": "orders.amount",
    "metric-filter-field": "orders.id", "metric-filter-operator": "neq", "metric-filter-values": "0"};
  for (const [id, value] of Object.entries(values)) context.document.getElementById(id).value = value;
  run("addMetricExpressionFilter()");
  const metric = run("metricFromEditor()");
  assert.deepEqual(JSON.parse(JSON.stringify(metric.expression)), {
    op: "sum", field: "orders.amount", filters: [{field: "orders.id", operator: "neq", values: ["0"]}],
  });
  assert.equal(run("metricExpressionLabel(metricFromEditor().expression)"), "SUM(CASE WHEN orders.id <> '0' THEN orders.amount ELSE NULL END)");
});

test("editing an existing metric preserves its selected field and related dimensions", () => {
  const {context, run} = editorContext();
  run("renderEntityFields('orders', 'orders.amount'); renderMetricDimensions('orders', ['status', 'segment'])");
  assert.equal(context.document.getElementById("metric-field").value, "orders.amount");
  const inputs = context.document.getElementById("metric-dimension-options").children.map((label) => label.children[0]);
  assert.deepEqual(inputs.map((input) => [input.value, input.checked]), [["status", true], ["segment", true]]);
});

test("published derived metrics cannot be flattened through the aggregate copy form", () => {
  const {context, run} = editorContext();
  run(`state.review = {active_release: {id: 'rel_1'}, metric_changes: []};
    renderMetricEditor({name: 'conversion_rate', display_name: '转化率', entity: 'orders', kind: 'ratio',
      value_type: 'decimal', unit: '%', expression: {op: 'divide', args: [{op: 'metric', metric: 'buyers'}, {op: 'metric', metric: 'visitors'}]},
      allowed_dimensions: ['status'], verification: {status: 'verified'}})`);
  assert.equal(context.document.getElementById("duplicate-metric-button").hidden, true);
  assert.match(context.document.getElementById("metric-formula-preview").textContent, /buyers.*visitors/);
});

test("saving a metric writes one draft revision without a separate browser-only apply step", async () => {
  const {context, run} = editorContext();
  run("state.draft.revision = 1; state.governanceRoute = {namespace: 'matchingstory', model_name: 'daily'}");
  const values = {"metric-external-code": "1001", "metric-code": "daily_revenue", "metric-display-name": "日收入",
    "metric-description": "按自然日汇总订单收入", "metric-owner": "data-team", "metric-entity": "orders",
    "metric-operation": "sum", "metric-value-type": "decimal", "metric-field": "orders.amount"};
  for (const [id, value] of Object.entries(values)) context.document.getElementById(id).value = value;
  const calls = [];
  context.requestJSON = async (path, options) => {
    const body = JSON.parse(options.body);
    calls.push([path, options.method, body]);
    if (path.endsWith("/draft")) return {revision: 2, source: body.source, updated_by: "tester", updated_at: "2026-09-20T00:00:00Z"};
    return {active_release: null, metric_changes: [{code: "daily_revenue", kind: "added"}], publication_issues: [],
      structure_changed: false, binding: {status: "ready", message: "ready"}};
  };
  await run("applyMetric({preventDefault() {}})");
  assert.equal(calls.filter(([path]) => path.endsWith("/draft")).length, 1);
  assert.equal(calls[0][2].expected_revision, 1);
  assert.equal(calls[0][2].source.spec.metrics[0].external_code, "1001");
  assert.equal(run("state.draft.revision"), 2);
  assert.equal(run("state.editorDirty"), false);
  assert.match(context.document.getElementById("editor-status").textContent, /已保存到草稿/);
});

test("rollback uses the historical release and leaves an accurate success message after reload", async () => {
  const {context, run} = editorContext();
  let confirmation = "";
  let submitted;
  context.window = {confirm(message) { confirmation = message; return true; }};
  context.requestJSON = async (path, options) => {
    submitted = {path, body: JSON.parse(options.body)};
    return {id: "rel_original"};
  };
  context.loadGovernance = async () => { context.document.getElementById("governance-status").textContent = "已载入"; };
  context.loadCatalog = async () => {};
  context.document.getElementById("release-note").value = "UI acceptance rollback";
  run("state.governanceRoute = {namespace: 'acceptance', model_name: 'orders'}");
  await run("rollbackRelease('rel_original')");
  assert.match(confirmation, /重新启用该历史版本/);
  assert.equal(submitted.body.release_id, "rel_original");
  assert.equal(submitted.path, "/api/v1/namespaces/acceptance/models/orders/rollback");
  assert.equal(context.document.getElementById("governance-status").textContent, "已重新启用历史版本 rel_original，并记录回滚事件。");
});

test("cancel button calls the existing job endpoint for the active job only", async () => {
  const {context, run} = editorContext();
  const calls = [];
  context.requestJSON = async (path, options) => { calls.push([path, options.method]); return {job: {status: "cancelled"}}; };
  context.renderQueryResult = (snapshot) => { assert.equal(snapshot.job.status, "cancelled"); };
  run("bindEvents(); state.activeJobID = 'job_ui_check'");
  await context.document.getElementById("cancel-job-button").handlers.click();
  assert.deepEqual(calls, [["/api/v1/jobs/job_ui_check/cancel", "POST"]]);
  run("state.activeJobID = null");
  await context.document.getElementById("cancel-job-button").handlers.click();
  assert.equal(calls.length, 1);
});

test("unsaved form edits cannot silently publish the old draft", async () => {
  const {context, run} = editorContext();
  let requests = 0;
  context.requestJSON = async () => { requests++; };
  run("bindEvents()");
  context.document.getElementById("metric-form").handlers.input();
  await run("publishDraft()");
  assert.equal(requests, 0);
  assert.match(context.document.getElementById("editor-status").textContent, /尚未保存/);
});

test("cancelled navigation preserves unsaved draft and business domain", async () => {
  const {context, run} = editorContext();
  context.window = {confirm: () => false};
  run("state.namespace = 'original'; state.editorDirty = true");
  const original = run("state.draft");
  await run("switchNamespace('other')");
  assert.equal(run("state.draft"), original);
  assert.equal(run("state.namespace"), "original");
  assert.equal(context.document.getElementById("namespace-select").value, "original");
});

test("successful namespace switching clears the previous query metric search", async () => {
  const {context, run} = editorContext();
  context.window = {confirm: () => true};
  context.loadCatalog = async () => {};
  context.updateQueryBuilder = () => {};
  context.initializeGovernanceRoutes = () => {};
  context.document.getElementById("query-metric-search").value = "old-domain-metric";
  context.document.getElementById("query-status").hidden = false;
  context.document.getElementById("query-status").textContent = "old query succeeded";
  context.document.getElementById("result-panel").hidden = false;
  run("state.namespace = 'original'");
  await run("switchNamespace('other')");
  assert.equal(run("state.namespace"), "other");
  assert.equal(context.document.getElementById("query-metric-search").value, "");
  assert.equal(context.document.getElementById("query-status").hidden, true);
  assert.equal(context.document.getElementById("query-status").textContent, "");
  assert.equal(context.document.getElementById("result-panel").hidden, true);
  assert.equal(context.document.getElementById("catalog-list").children.length, 1);
  assert.match(context.document.getElementById("catalog-list").children[0].textContent, /没有找到匹配/);
});

test("a stale catalog response cannot overwrite a newer namespace", async () => {
  const {context, run} = editorContext();
  const pending = new Map();
  context.requestJSON = (path) => new Promise((resolve) => pending.set(path, resolve));
  context.renderCatalog = () => {};
  context.updateQueryBuilder = () => {};
  run("state.context = {permissions: ['query:execute'], models: []}; state.namespace = 'old'");
  const oldLoad = run("loadCatalog()");
  run("state.namespace = 'new'");
  const newLoad = run("loadCatalog()");
  const metric = (name) => ({name, display_name: name, description: name, owner: 'owner', value_type: 'integer', unit: 'times', allowed_dimensions: [], tags: [], examples: []});
  pending.get("/api/v1/catalog/search?namespace=new&q=&limit=100")([metric("new_metric")]);
  await newLoad;
  pending.get("/api/v1/catalog/search?namespace=old&q=&limit=100")([metric("old_metric")]);
  await oldLoad;
  assert.deepEqual(JSON.parse(JSON.stringify(run("state.catalog.map((entry) => entry.name)"))), ["new_metric"]);
});

test("new query failure clears old results and unlocks controls without retry", async () => {
  const {context, run} = editorContext();
  const table = context.document.getElementById("result-table");
  table.children = ["stale-result"];
  context.document.getElementById("result-meta").textContent = "old release";
  context.buildQuery = () => ({query: {metrics: ["revenue"]}});
  let requests = 0;
  context.requestJSON = async () => { requests++; throw {detail: "test failure"}; };
  run("state.namespace = 'acceptance'");
  await run("runQueryOperation('query')");
  assert.equal(table.children.length, 0);
  assert.equal(context.document.getElementById("result-meta").textContent, "");
  assert.equal(context.document.getElementById("run-query-button").disabled, false);
  assert.equal(requests, 1);
  assert.match(context.document.getElementById("query-status").textContent, /test failure/);
});

test("one active query blocks duplicate submissions and namespace switching", async () => {
  const {context, run} = editorContext();
  context.buildQuery = () => ({query: {metrics: ["revenue"]}});
  let finish;
  let requests = 0;
  context.requestJSON = () => { requests++; return new Promise((resolve) => { finish = resolve; }); };
  run("state.namespace = 'acceptance'");
  const pending = run("runQueryOperation('query')");
  assert.equal(context.document.getElementById("run-query-button").disabled, true);
  await run("runQueryOperation('query'); switchNamespace('other')");
  assert.equal(requests, 1);
  assert.equal(run("state.namespace"), "acceptance");
  finish({job: {status: "failed", error: {message: "test stopped"}}});
  await pending;
  assert.equal(context.document.getElementById("run-query-button").disabled, false);
  assert.equal(run("state.queryBusy"), false);
});

test("pending and cancelled snapshots never retain an earlier result table", () => {
  const {context, run} = editorContext();
  for (const status of ["pending", "cancelled"]) {
    context.document.getElementById("result-table").children = ["old"];
    run(`renderQueryResult({job: {status: '${status}'}})`);
    assert.equal(context.document.getElementById("result-table").children.length, 0);
    assert.match(context.document.getElementById("query-status").textContent, /等待执行|已取消/);
  }
});

test("zoned midnight preserves the business timezone across DST", () => {
  assert.equal(zonedMidnightISO(2026, 9, 1, "Asia/Shanghai"), "2026-08-31T16:00:00.000Z");
  assert.equal(zonedMidnightISO(2026, 3, 8, "America/Los_Angeles"), "2026-03-08T08:00:00.000Z");
  assert.equal(zonedMidnightISO(2026, 3, 9, "America/Los_Angeles"), "2026-03-09T07:00:00.000Z");
});

test("relative presets become explicit half-open absolute ranges", () => {
  const now = new Date("2026-09-05T02:00:00Z");
  assert.deepEqual(resolvePresetRange("yesterday", "Asia/Shanghai", now), {
    start: "2026-09-03T16:00:00.000Z",
    end: "2026-09-04T16:00:00.000Z",
  });
  assert.deepEqual(resolvePresetRange("previous_month", "Asia/Shanghai", now), {
    start: "2026-07-31T16:00:00.000Z",
    end: "2026-08-31T16:00:00.000Z",
  });
  assert.deepEqual(resolvePresetRange("custom", "Asia/Shanghai", now, {start: "2026-08-01", end: "2026-09-01"}), {
    start: "2026-07-31T16:00:00.000Z",
    end: "2026-08-31T16:00:00.000Z",
  });
});
