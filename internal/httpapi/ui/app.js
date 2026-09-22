"use strict";

const byID = (id) => document.getElementById(id);
const state = {
  context: null,
  namespace: "",
  catalog: [],
  catalogView: [],
  catalogLoadSequence: 0,
  catalogAbortController: null,
  catalogCursor: "",
  catalogCounts: null,
  catalogIncomplete: false,
  catalogLoading: false,
  queryCatalog: new Map(),
  metricDetailSequence: 0,
  metricPlanCache: new Map(),
  selectedCatalogIndex: -1,
  queryMetricKeys: new Set(),
  filters: [],
  activeJobID: null,
  queryBusy: false,
  governanceRoute: null,
  draft: null,
  review: null,
  releases: [],
  metricIndex: -1,
  creatingMetric: false,
  editorDirty: false,
  metricExpressionFilters: [],
};

function setNotice(message, tone = "neutral") {
  const target = byID("notice");
  target.textContent = message;
  target.dataset.tone = tone;
}

function setStatus(id, message, tone = "neutral") {
  const target = byID(id);
  target.hidden = false;
  target.textContent = message;
  target.dataset.tone = tone;
}

function resetQueryFeedback() {
  byID("query-status").hidden = true;
  byID("query-status").textContent = "";
  byID("result-table").replaceChildren();
  byID("result-meta").textContent = "";
  byID("query-output").textContent = "";
  byID("result-panel").hidden = true;
}

function errorMessage(error) {
  const detail = error?.detail || (error instanceof Error ? error.message : "");
  const code = error?.code || "";
  let message = detail || "请求失败，请稍后重试。";
  if (code === "permission_denied" || error?.status === 403) {
    message = "当前账号没有执行此操作的权限。请联系平台管理员确认所属权限组。";
  } else if (code === "unauthorized" || error?.status === 401) {
    message = "登录状态已失效，请重新登录后再试。";
  } else if ((code === "invalid_time" && error?.path === "time_grouping") || /grouped time dimensions require explicit grouping semantics|group_by contains time dimension/i.test(detail)) {
    message = "选择时间维度后必须指定时间粒度，请选择按日、按周或按月。";
  }
  return `${message}${error?.path ? `（${error.path}）` : ""}${error?.request_id ? ` · 请求 ${error.request_id}` : ""}`;
}

async function requestJSON(path, options = {}) {
  const headers = {Accept: "application/json", ...(options.headers || {})};
  if (options.body !== undefined) headers["Content-Type"] = "application/json";
  const response = await fetch(path, {credentials: "same-origin", ...options, headers});
  const text = await response.text();
  let body = {};
  if (text) {
    try { body = JSON.parse(text); } catch (_) { body = {detail: text}; }
  }
  if (!response.ok) {
    body.status = body.status || response.status;
    throw body;
  }
  return body;
}

function hasPermission(permission) {
  return Boolean(state.context && state.context.permissions.includes(permission));
}

function makeOption(text, value) {
  const option = document.createElement("option");
  option.textContent = text;
  option.value = value;
  return option;
}

function escapePath(value) {
  return encodeURIComponent(value);
}

function routePath(route, operation) {
  if (!route) throw new Error("当前没有可用的维护对象。");
  return `/api/v1/namespaces/${escapePath(route.namespace)}/models/${escapePath(route.model_name)}/${operation}`;
}

function publicQueryPath(operation) {
  if (!state.namespace) throw new Error("当前没有可用的业务域。");
  return `/api/v1/namespaces/${escapePath(state.namespace)}/${operation}`;
}

function metricKey(metric) {
  return metric.name;
}

function draftCatalogEntries(route, draft) {
  const source = draft?.source;
  if (!source?.spec) return [];
  const dimensions = new Map((source.spec.dimensions || []).map((dimension) => [dimension.name, dimension]));
  return (source.spec.metrics || []).map((metric) => {
    const time = dimensions.get(metric.time_dimension);
    return {
      ...metric,
      namespace: route.namespace,
      semantic_model_name: route.model_name,
      catalog_status: "draft",
      release_id: "",
      time_granularities: time?.time_granularities || [],
    };
  });
}

function governanceCatalogEntries(records, drafts = []) {
  const draftsByCode = new Map(drafts.map((metric) => [`${metric.semantic_model_name || metric.model_name || ""}\u0000${metric.name}`, metric]));
  const fallbackDraftsByCode = new Map(drafts.map((metric) => [metric.name, metric]));
  return records.map((record) => {
    const definition = record.definition || {};
    const semanticDraft = draftsByCode.get(`${definition.semantic_model_name || ""}\u0000${definition.code}`) || fallbackDraftsByCode.get(definition.code) || {};
    const executable = definition.semantic_readiness === "executable_unverified" && Boolean(definition.semantic_model_name);
    return {
      ...semanticDraft,
      namespace: record.namespace,
      name: definition.code,
      display_name: definition.display_name,
      description: definition.description,
      owner: definition.owner,
      unit: definition.unit,
      value_type: definition.value_type,
      allowed_dimensions: definition.test_dimensions || semanticDraft.allowed_dimensions || [],
      time_dimension: definition.time_dimension || semanticDraft.time_dimension || "",
      time_granularities: semanticDraft.time_granularities || [],
      verification: definition.verification,
      tags: semanticDraft.tags || [definition.business_type].filter(Boolean),
      usage_examples: semanticDraft.usage_examples || [],
      catalog_status: executable ? "draft" : "governance",
      semantic_readiness: definition.semantic_readiness,
      semantic_model_name: definition.semantic_model_name || "",
      business_type: definition.business_type,
      origin: definition.origin || "",
      calculation_kind: definition.calculation_kind || "",
      formula_summary: definition.serving_formula || definition.documented_formula || "",
      documented_formula: definition.documented_formula || "",
      serving_formula: definition.serving_formula || "",
      authoritative_source: definition.authoritative_source || null,
      source_references: definition.source_references || [],
      fact_grain: definition.fact_grain || "",
      readiness_reason: definition.readiness_reason || "",
      owner_status: definition.owner_status || "",
      issues: definition.issues || [],
      governance_revision: record.revision,
      source_import_id: record.source_import_id,
      release_id: "",
    };
  });
}

function mergeCatalogEntries(published, drafts) {
  const draftsByCode = new Map(drafts.map((metric) => [metricKey(metric), metric]));
  const publishedCodes = new Set(published.map(metricKey));
  return [
    ...published.map((metric) => {
      const governed = draftsByCode.get(metricKey(metric)) || {};
      return {
        ...governed,
        ...metric,
        formula_summary: metric.formula_summary || governed.formula_summary || "",
        authoritative_source: metric.authoritative_source || governed.authoritative_source || null,
        source_references: metric.source_references || governed.source_references || [],
        fact_grain: metric.fact_grain || governed.fact_grain || "",
        calculation_kind: metric.calculation_kind || governed.calculation_kind || "",
        origin: metric.origin || governed.origin || "",
        readiness_reason: metric.readiness_reason || governed.readiness_reason || "",
        owner_status: metric.owner_status || governed.owner_status || "",
        catalog_status: "published",
      };
    }),
    ...drafts.filter((metric) => !publishedCodes.has(metricKey(metric))),
  ];
}

function catalogStatusCounts(metrics) {
  return metrics.reduce((counts, metric) => {
    if (metric.catalog_status === "published") counts.published += 1;
    if (metric.catalog_status === "draft") counts.draft += 1;
    if (metric.catalog_status === "governance") counts.governance += 1;
    return counts;
  }, {published: 0, draft: 0, governance: 0});
}

function catalogVisibleTotal(counts, status) {
  if (status === "governance") return counts.governance;
  if (status === "queryable") return counts.published + counts.draft;
  return counts.total ?? counts.published + counts.draft + counts.governance;
}

function filterCatalogEntries(metrics, status = "queryable") {
  if (status === "all") return metrics;
  if (status === "governance") return metrics.filter((metric) => metric.catalog_status === "governance");
  return metrics.filter((metric) => metric.catalog_status === "published" || metric.catalog_status === "draft");
}

function isBusinessUnverified(metric) {
  return metric?.verification_status === "unverified" || metric?.verification?.status === "unverified" || (metric?.tags || []).includes("governance_unverified");
}

function catalogStatusLabel(metric) {
  if (metric.catalog_status === "draft") return "未验证草稿";
  if (metric.catalog_status === "governance") return "待治理";
  if (metric.deprecated) return "已弃用";
  return isBusinessUnverified(metric) ? "已发布 · 业务待验证" : "已发布";
}

function catalogStatusShortLabel(metric) {
  if (metric.catalog_status === "draft") return "草稿";
  if (metric.catalog_status === "governance") return "待治理";
  if (metric.deprecated) return "已弃用";
  return isBusinessUnverified(metric) ? "试用" : "线上";
}

function humanOwner(owner) {
  return !owner || owner === "pending_assignment" ? "待分配" : owner;
}

function verificationLabel(metric) {
  return isBusinessUnverified(metric) ? "技术试查证据已登记" : "口径已经验证";
}

function humanTag(tag) {
  return ({
    business_type_atomic: "原子指标",
    business_type_derived: "派生指标",
    business_type_composite: "复合指标",
    governance_unverified: "业务待验证",
    feishu_documented: "有治理来源",
  })[tag] || tag;
}

function humanCalculationKind(metric) {
  const kind = metric.calculation_kind || metric.metric_kind || metric.kind || "";
  return ({aggregate: "聚合指标", ratio: "比率指标", derived: "派生指标"})[kind] || "结构化指标";
}

function matchesCatalogSearch(metric, search) {
  if (!search) return true;
  const text = [metric.external_code, metric.name, metric.display_name, metric.description, metric.owner,
    metric.semantic_model_name, metric.authoritative_source?.resource, metric.authoritative_source?.field,
    metric.formula_summary, ...(metric.tags || [])]
    .filter(Boolean).join(" ").toLocaleLowerCase();
  return text.includes(search.toLocaleLowerCase());
}

function matchingQueryMetrics(metrics, search, selectedKeys, limit = 12) {
  const selected = metrics.filter((metric) => selectedKeys.has(metricKey(metric)));
  if (!search) return selected;
  const matches = metrics.filter((metric) => matchesCatalogSearch(metric, search));
  const seen = new Set();
  const combined = [];
  for (const metric of [...selected, ...matches]) {
    const key = metricKey(metric);
    if (seen.has(key)) continue;
    seen.add(key);
    combined.push(metric);
    if (combined.length >= Math.max(limit, selected.length)) break;
  }
  return combined;
}

function selectedQueryMetrics() {
  return [...state.queryMetricKeys].map((key) => state.queryCatalog.get(key)).filter(Boolean);
}

function queryMode(metrics = selectedQueryMetrics()) {
  if (!metrics.length) return {kind: "published", modelName: ""};
  const drafts = metrics.filter((metric) => metric.catalog_status === "draft");
  if (!drafts.length) return {kind: "published", modelName: ""};
  if (drafts.length !== metrics.length) throw new Error("已发布指标和未发布草稿不能混在一次查询中。");
  const modelNames = uniqueValues(drafts.map((metric) => metric.semantic_model_name));
  if (modelNames.length !== 1) throw new Error("不同语义模型中的草稿请分开试查。");
  return {kind: "draft", modelName: modelNames[0]};
}

function uniqueValues(values) {
  return [...new Set(values.filter(Boolean))];
}

function humanType(value, unit = "") {
  const labels = {integer: "整数", decimal: "小数", string: "文本", boolean: "布尔", date: "日期", timestamp: "时间"};
  return [labels[value] || value || "未标注", unit].filter(Boolean).join(" · ");
}

function humanGrain(value) {
  return ({day: "日", week: "周", month: "月"})[value] || value;
}

function compactDimensions(dimensions = [], limit = 2) {
  const visible = dimensions.slice(0, limit);
  return [...visible, ...(dimensions.length > limit ? [`+${dimensions.length - limit}`] : [])].join(" · ") || "—";
}

function appendChip(container, label, tone = "") {
  const chip = document.createElement("span");
  chip.className = `chip${tone ? ` ${tone}` : ""}`;
  chip.textContent = label;
  container.append(chip);
}

function activateTab(name) {
  for (const button of document.querySelectorAll("[data-tab]")) {
    const active = button.dataset.tab === name;
    button.classList.toggle("active", active);
    button.setAttribute("aria-selected", String(active));
  }
  for (const workspace of document.querySelectorAll("[data-workspace]")) {
    workspace.hidden = workspace.dataset.workspace !== name;
  }
  if (typeof window !== "undefined" && window.scrollTo) window.scrollTo({top: 0, behavior: "auto"});
  if (name === "governance" && state.governanceRoute && !state.draft) loadGovernance();
  if (name === "query") updateQueryBuilder();
}

function initializeNamespaces() {
  const select = byID("namespace-select");
  select.replaceChildren();
  const namespaces = uniqueValues(state.context.namespaces || (state.context.models || []).map((route) => route.namespace));
  for (const namespace of namespaces) select.append(makeOption(namespace, namespace));
  select.disabled = namespaces.length < 2;
  state.namespace = namespaces[0] || "";
  select.value = state.namespace;
  byID("current-namespace").textContent = state.namespace || "没有配置业务域";
}

function initializeGovernanceRoutes() {
  const select = byID("governance-route");
  select.replaceChildren();
  const routes = (state.context.models || []).filter((route) => route.namespace === state.namespace);
  for (const [index, route] of routes.entries()) {
    select.append(makeOption(`${route.model_name} · ${route.namespace}`, String(index)));
  }
  state.governanceRoute = routes[0] || null;
  select.disabled = routes.length < 2;
  select.value = routes.length ? "0" : "";
}

async function switchNamespace(namespace) {
  if (state.queryBusy) {
    byID("namespace-select").value = state.namespace;
    setNotice("当前查询尚未结束，请等待结果或先取消查询，再切换业务域。", "warning");
    return;
  }
  if (!confirmDiscardDraft()) {
    byID("namespace-select").value = state.namespace;
    return;
  }
  state.namespace = namespace;
  byID("current-namespace").textContent = namespace || "没有配置业务域";
  state.catalog = [];
  state.catalogView = [];
  state.catalogCounts = null;
  state.catalogIncomplete = false;
  state.catalogCursor = "";
  state.catalogLoading = true;
  state.selectedCatalogIndex = -1;
  state.metricPlanCache.clear();
  state.queryMetricKeys.clear();
  state.queryCatalog.clear();
  byID("query-metric-search").value = "";
  state.filters = [];
  resetQueryFeedback();
  renderCatalog();
  updateQueryBuilder();
  state.draft = null;
  state.review = null;
  state.editorDirty = false;
  initializeGovernanceRoutes();
  await loadCatalog();
  if (!byID("governance-workspace").hidden && state.governanceRoute) await loadGovernance();
}

async function loadCatalog(more = false) {
  const loadSequence = ++state.catalogLoadSequence;
  state.catalogAbortController?.abort();
  const controller = new AbortController();
  state.catalogAbortController = controller;
  const namespace = state.namespace;
  const startedAt = typeof performance !== "undefined" && performance.now ? performance.now() : 0;
  if (!state.namespace || (!hasPermission("query:execute") && !hasPermission("model:manage"))) {
    state.catalogLoading = false;
    state.catalog = [];
    state.catalogView = [];
    renderCatalog();
    return;
  }
  const search = byID("catalog-search").value.trim();
  const status = byID("catalog-status-filter").value || "queryable";
  if (!more) {
    state.catalogLoading = true;
    state.catalogCursor = "";
    state.catalogCounts = null;
    state.catalogIncomplete = false;
    state.catalog = [];
    state.catalogView = [];
    state.selectedCatalogIndex = -1;
    renderCatalog();
  }
  setNotice(more ? "正在加载更多指标…" : "正在搜索指标目录…");
  byID("catalog-more").disabled = true;
  try {
    const params = new URLSearchParams({namespace, q: search, status, limit: "50"});
    if (more && state.catalogCursor) params.set("cursor", state.catalogCursor);
    const page = await requestJSON(`/api/v1/catalog/index?${params}`, {signal: controller.signal});
    if (loadSequence !== state.catalogLoadSequence || namespace !== state.namespace) return;
    state.catalogView = more ? [...state.catalogView, ...page.items] : page.items;
    state.catalogCounts = page.counts;
    state.catalogIncomplete = Boolean(page.incomplete);
    state.catalogCursor = page.next_cursor || "";
    state.catalogLoading = false;
    state.catalog = state.catalogView.filter((metric) => metric.catalog_status !== "governance");
    for (const metric of state.catalog) state.queryCatalog.set(metricKey(metric), metric);
    if (state.selectedCatalogIndex >= state.catalogView.length) state.selectedCatalogIndex = -1;
    renderCatalog();
    updateQueryBuilder();
    const elapsed = startedAt ? ` · ${Math.round(performance.now() - startedAt)}ms` : "";
    setNotice(`已显示 ${state.catalogView.length} / ${page.incomplete ? "至少 " : ""}${catalogVisibleTotal(page.counts, status)} 个指标${elapsed}${page.incomplete ? "；目录达到服务端扫描上限，请缩小搜索范围。" : "。"}`, page.incomplete ? "warning" : "success");
  } catch (error) {
    if (loadSequence !== state.catalogLoadSequence || namespace !== state.namespace) return;
    state.catalogLoading = false;
    if (!more) renderCatalog();
    if (error?.name !== "AbortError") setNotice(errorMessage(error), "error");
  } finally {
    if (loadSequence === state.catalogLoadSequence) byID("catalog-more").disabled = false;
  }
}

function renderCatalog() {
  const counts = state.catalogCounts || catalogStatusCounts(state.catalogView);
  const {published: publishedCount, draft: draftCount, governance: governanceCount} = counts;
  const statusFilter = byID("catalog-status-filter").value || "queryable";
  const visibleEntries = filterCatalogEntries(state.catalogView, statusFilter);
  const visibleTotal = catalogVisibleTotal(counts, statusFilter);
  byID("catalog-more").hidden = !state.catalogCursor;
  byID("catalog-count").textContent = publishedCount || draftCount || governanceCount
    ? `显示 ${visibleEntries.length} / ${state.catalogIncomplete ? "至少 " : ""}${visibleTotal} · ${publishedCount} 已发布 · ${draftCount} 可试查 · ${governanceCount} 待治理`
    : `${state.catalogView.length} 个指标`;
  const list = byID("catalog-list");
  list.replaceChildren();
  if (!visibleEntries.length) {
    const empty = document.createElement("div");
    empty.className = "empty-state";
    empty.textContent = state.catalogLoading ? "正在搜索指标目录…" : state.catalogView.length
      ? "当前状态筛选下没有匹配指标。请切换显示状态或调整搜索词。"
      : hasPermission("model:manage")
        ? "没有找到匹配的已发布指标或草稿。请确认业务域、搜索词和草稿录入情况。"
        : "没有找到匹配的已发布指标。试试其他名称或清空搜索；仍为空时，请联系指标负责人确认发布情况和访问权限。";
    list.append(empty);
    renderMetricDetail(null);
    return;
  }
  const selected = state.catalogView[state.selectedCatalogIndex];
  if (!visibleEntries.includes(selected)) state.selectedCatalogIndex = state.catalogView.indexOf(visibleEntries[0]);
  visibleEntries.forEach((metric) => {
    const index = state.catalogView.indexOf(metric);
    const button = document.createElement("button");
    button.type = "button";
    button.setAttribute("role", "row");
    button.className = `catalog-row${index === state.selectedCatalogIndex ? " active" : ""}`;
    const heading = document.createElement("span");
    heading.className = "catalog-identity";
    const title = document.createElement("strong");
    title.textContent = metric.display_name || metric.name;
    const code = document.createElement("code");
    code.textContent = [metric.external_code ? `#${metric.external_code}` : "", metric.name].filter(Boolean).join(" · ");
    heading.append(title, code);
    const formula = document.createElement("code");
    formula.className = "catalog-formula";
    formula.textContent = metricFormulaLabel(metric);
    formula.title = metricFormulaLabel(metric);
    const source = document.createElement("span");
    source.className = "catalog-source";
    source.textContent = metric.authoritative_source?.resource || metric.semantic_model_name || metric.entity || "来源待补充";
    source.title = [metric.semantic_model_name, metric.authoritative_source?.resource, metric.authoritative_source?.field].filter(Boolean).join(" · ");
    const status = document.createElement("span");
    const statusLabel = catalogStatusShortLabel(metric);
    status.className = `badge catalog-row-status${metric.catalog_status !== "published" || metric.deprecated || isBusinessUnverified(metric) ? " warning" : ""}`;
    status.textContent = statusLabel;
    status.title = catalogStatusLabel(metric);
    button.append(heading, formula, source, status);
    button.addEventListener("click", () => {
      state.selectedCatalogIndex = index;
      renderCatalog();
    });
    list.append(button);
  });
  if (state.selectedCatalogIndex < 0) {
    state.selectedCatalogIndex = state.catalogView.indexOf(visibleEntries[0]);
    renderMetricDetail(visibleEntries[0]);
    list.firstElementChild?.classList.add("active");
  } else {
    renderMetricDetail(state.catalogView[state.selectedCatalogIndex]);
  }
}

function renderMetricDetail(metric) {
  byID("metric-detail-empty").hidden = Boolean(metric);
  byID("metric-detail-content").hidden = !metric;
  const sequence = ++state.metricDetailSequence;
  if (!metric) return;
  const draft = metric.catalog_status === "draft";
  const pending = metric.catalog_status === "governance";
  const businessUnverified = isBusinessUnverified(metric);
  byID("detail-status").textContent = catalogStatusLabel(metric);
  byID("detail-status").className = `badge${draft || pending || metric.deprecated || businessUnverified ? " warning" : ""}`;
  byID("detail-name").textContent = metric.display_name || metric.name;
  byID("detail-code").textContent = [metric.external_code ? `#${metric.external_code}` : "", metric.name].filter(Boolean).join(" · ");
  byID("detail-description").textContent = metric.description || "业务定义待补充：尚未说明统计对象、时间边界和排除条件。";
  const normalizedFormula = metricFormulaLabel(metric);
  byID("detail-formula").textContent = normalizedFormula;
  byID("detail-calculation-kind").textContent = humanCalculationKind(metric);
  byID("detail-calculation-note").textContent = metricCalculationNote(metric);
  const registeredFormula = metric.serving_formula || metric.documented_formula || metric.formula_summary || "";
  const showRegisteredFormula = Boolean(registeredFormula && registeredFormula !== normalizedFormula);
  byID("detail-registered-formula-wrap").hidden = !showRegisteredFormula;
  byID("detail-registered-formula").textContent = showRegisteredFormula ? registeredFormula : "";
  byID("detail-owner").textContent = humanOwner(metric.owner);
  byID("detail-type").textContent = humanType(metric.value_type, metric.unit);
  byID("detail-release").textContent = draft ? "尚未发布 · 可试查" : pending ? "尚无安全查询定义" : metric.release_id;
  byID("detail-time").textContent = metric.time_dimension
    ? `${metric.time_dimension} · ${(metric.time_granularities || []).map(humanGrain).join("/") || "未配置粒度"}${metricTimeDetail(metric)?.calendar_timezone ? ` · ${metricTimeDetail(metric).calendar_timezone} 日历` : ""}`
    : "非时间限定指标";
  byID("detail-verification").textContent = pending ? "尚未形成可执行定义" : verificationLabel(metric);
  byID("detail-dimension-count").textContent = `${(metric.allowed_dimensions || []).length} 个`;
  byID("detail-semantic-model").textContent = metric.semantic_model_name || "待补充";
  byID("detail-entity").textContent = metric.entity || "待补充";
  const authoritative = metric.authoritative_source || {};
  byID("detail-authoritative-source").textContent = [authoritative.resource, authoritative.field].filter(Boolean).join(" · ") || "待补充";
  byID("detail-physical-source").textContent = pending ? "尚未绑定" : "正在解析…";
  byID("detail-source-state").textContent = pending ? "待治理" : "正在解析";
  byID("detail-field-lineage").replaceChildren();
  byID("detail-fact-grain").textContent = metric.fact_grain ? `事实粒度：${metric.fact_grain}` : "事实粒度待补充。";
  renderSourceEvidence(metric.source_references || [], authoritative);
  const dimensions = byID("detail-dimensions");
  dimensions.replaceChildren();
  for (const dimension of metric.allowed_dimensions) appendChip(dimensions, dimension);
  if (!metric.allowed_dimensions.length) appendChip(dimensions, "无分组维度");
  const tags = byID("detail-tags");
  tags.replaceChildren();
  for (const tag of metric.tags || []) appendChip(tags, humanTag(tag));
  if (!(metric.tags || []).length) appendChip(tags, "未设置标签");
  const examples = byID("detail-examples");
  examples.replaceChildren();
  for (const example of metric.usage_examples || []) {
    const item = document.createElement("p");
    item.textContent = example;
    examples.append(item);
  }
  if (!(metric.usage_examples || []).length) examples.textContent = "维护者尚未提供常见使用示例。";
  byID("detail-query-button").textContent = draft ? "带入草稿试查" : pending ? "暂不可查询" : "带入查询验证";
  byID("detail-query-button").disabled = pending || metric.deprecated || !hasPermission("query:execute") || (draft && !hasPermission("model:manage"));
  byID("detail-query-button").onclick = () => addMetricToQuery(metric);
  if (!pending && (hasPermission("query:execute") || (draft && hasPermission("model:manage")))) loadMetricLineage(metric, sequence);
}

function metricCalculationNote(metric) {
  const expression = metric.expression || {};
  if (!expression.op) return metric.formula_summary ? "这是治理登记口径；形成可执行定义后会补充字段级解释。" : "计算逻辑待补充。";
  const aggregateLabels = {sum: "求和", count: "计数", count_distinct: "去重计数", avg: "求平均", min: "取最小值", max: "取最大值"};
  if (aggregateLabels[expression.op]) {
    const filters = (expression.filters || []).map(metricFilterLabel);
    return `${aggregateLabels[expression.op]}字段 ${expression.field || "*"}${filters.length ? `；仅统计满足 ${filters.join(" 且 ")} 的记录` : "；不附加指标级条件"}。`;
  }
  return `由结构化表达式 ${metricExpressionLabel(expression)} 计算；指标引用会在计划阶段解析为固定发布版本。`;
}

function renderSourceEvidence(references, authoritative = {}) {
  const container = byID("detail-source-evidence");
  container.replaceChildren();
  const evidence = [...references];
  if (authoritative.reference && !evidence.some((item) => item.reference === authoritative.reference)) evidence.unshift(authoritative);
  if (!evidence.length) {
    container.textContent = "尚未登记来源证据。";
    return;
  }
  const title = document.createElement("strong");
  title.textContent = "来源证据";
  container.append(title);
  for (const reference of evidence.slice(0, 4)) {
    const item = document.createElement("p");
    item.textContent = [reference.reference, reference.resource, reference.field].filter(Boolean).join(" · ");
    container.append(item);
  }
}

function metricDetailQuery(metric, now = new Date()) {
  const query = {api_version: "metricspire.io/v1alpha1", kind: "SemanticQuery", metrics: [metric.name], limit: 1};
  if (metric.time_dimension) {
    const timezone = metricTimeDetail(metric)?.calendar_timezone || "UTC";
    const range = resolvePresetRange("yesterday", timezone, now);
    query.time_range = {dimension: metric.time_dimension, start: range.start, end: range.end, timezone};
  }
  return query;
}

function metricDetailPlanPath(metric) {
  if (metric.catalog_status !== "draft") return publicQueryPath("plan");
  const route = (state.context.models || []).find((candidate) => candidate.namespace === metric.namespace && candidate.model_name === metric.semantic_model_name);
  if (!route) throw new Error("当前草稿没有可用的语义模型路由。");
  return routePath(route, "draft-plan");
}

function renderMetricLineage(result) {
  const physical = result.physical_plan || {};
  const root = physical.root || {};
  byID("detail-source-state").textContent = "执行计划已解析";
  byID("detail-physical-source").textContent = resourceLabel(root.resource || {}) || root.name || "未解析到物理数据集";
  const lineage = byID("detail-field-lineage");
  lineage.replaceChildren();
  const seen = new Set();
  for (const field of physical.metric_fields || []) {
    const logical = [field.entity, field.field].filter(Boolean).join(".");
    const physicalField = [resourceLabel(field.resource || {}), field.column].filter(Boolean).join(".");
    const key = `${logical}\u0000${physicalField}`;
    if (seen.has(key)) continue;
    seen.add(key);
    const row = document.createElement("div");
    const from = document.createElement("code");
    from.textContent = logical || "逻辑字段";
    const arrow = document.createElement("span");
    arrow.textContent = "→";
    const to = document.createElement("code");
    to.textContent = physicalField || "物理字段待解析";
    row.append(from, arrow, to);
    lineage.append(row);
  }
}

async function loadMetricLineage(metric, sequence) {
  const key = [metric.catalog_status, metric.release_id, metric.semantic_model_name, metric.name].join(":");
  try {
    let result = state.metricPlanCache.get(key);
    if (!result) {
      result = await requestJSON(metricDetailPlanPath(metric), {method: "POST", body: JSON.stringify(metricDetailQuery(metric))});
      state.metricPlanCache.set(key, result);
    }
    if (sequence !== state.metricDetailSequence) return;
    renderMetricLineage(result);
  } catch (error) {
    if (sequence !== state.metricDetailSequence) return;
    byID("detail-source-state").textContent = "物理来源暂不可用";
    byID("detail-physical-source").textContent = errorMessage(error);
  }
}

function addMetricToQuery(metric) {
  if (metric.catalog_status === "governance") return;
  const selected = selectedQueryMetrics();
  const incompatible = selected.some((candidate) => candidate.catalog_status !== metric.catalog_status ||
    (metric.catalog_status === "draft" && candidate.semantic_model_name !== metric.semantic_model_name));
  if (incompatible) state.queryMetricKeys.clear();
  state.queryCatalog.set(metricKey(metric), metric);
  state.queryMetricKeys.add(metricKey(metric));
  byID("query-metric-search").value = "";
  activateTab("query");
  updateQueryBuilder();
  setStatus("query-status", `已选择“${metric.display_name || metric.name}”，可继续选择维度和时间。`, "success");
}

function sharedDimensions(metrics) {
  if (!metrics.length) return [];
  return metrics[0].allowed_dimensions.filter((dimension) => metrics.every((metric) => metric.allowed_dimensions.includes(dimension)));
}

function sharedTimeMetadata(metrics) {
  const timed = metrics.filter((metric) => metric.time_dimension);
  if (!timed.length) return {dimension: "", granularities: []};
  const dimension = timed[0].time_dimension;
  if (timed.some((metric) => metric.time_dimension !== dimension)) return {dimension: "", granularities: [], incompatible: true};
  const fixedTimezones = [...new Set(timed.map((metric) => metricTimeDetail(metric)?.calendar_timezone || "").filter(Boolean))];
  if (fixedTimezones.length > 1) return {dimension: "", granularities: [], incompatible: true};
  return {
    dimension,
    granularities: (timed[0].time_granularities || []).filter((grain) => timed.every((metric) => (metric.time_granularities || []).includes(grain))),
    calendarTimezone: fixedTimezones[0] || "",
  };
}

function metricTimeDetail(metric) {
  return (metric.dimension_details || []).find((dimension) => dimension.name === metric.time_dimension);
}

function renderMetricOptions() {
  const container = byID("query-metric-options");
  container.replaceChildren();
  const selected = selectedQueryMetrics();
  const search = byID("query-metric-search").value.trim();
  byID("query-metric-selection-status").textContent = selected.length
    ? `已选择 ${selected.length} 个指标。可在目录搜索并带入更多指标。`
    : "先在指标目录搜索、确认口径并带入；这里可筛选当前已载入的指标。";
  if (!state.catalog.length && !selected.length) {
    const empty = document.createElement("div");
    empty.className = "empty-state";
    empty.textContent = "请先在指标库中找到可查询指标。";
    container.append(empty);
    return;
  }
  const available = [...new Map([...selected, ...state.catalog].map((metric) => [metricKey(metric), metric])).values()];
  const visibleMetrics = matchingQueryMetrics(available, search, state.queryMetricKeys);
  if (!visibleMetrics.length) {
    const empty = document.createElement("div");
    empty.className = "empty-state";
    empty.textContent = search
      ? "当前已载入指标中没有匹配项；请到指标目录搜索。"
      : "尚未选择指标。建议先在指标库确认口径，再点击“用于查询”。";
    container.append(empty);
    return;
  }
  for (const metric of visibleMetrics) {
    const label = document.createElement("label");
    label.className = "metric-option";
    const input = document.createElement("input");
    input.type = "checkbox";
    input.checked = state.queryMetricKeys.has(metricKey(metric));
    input.addEventListener("change", () => {
      if (input.checked) state.queryMetricKeys.add(metricKey(metric));
      else state.queryMetricKeys.delete(metricKey(metric));
      updateQueryBuilder();
    });
    const text = document.createElement("span");
    const name = document.createElement("strong");
    name.textContent = metric.display_name || metric.name;
    const meta = document.createElement("small");
    meta.textContent = `${metric.name}${isBusinessUnverified(metric) ? " · 业务待验证" : ""}`;
    text.append(name, meta);
    label.append(input, text);
    container.append(label);
  }
}

function preferredTimeGranularity(granularities = []) {
  return granularities.includes("day") ? "day" : (granularities[0] || "");
}

function renderDimensionOptions(dimensions, time = {dimension: "", granularities: []}) {
  const selected = new Set([...document.querySelectorAll("input[name='query-dimension']:checked")].map((input) => input.value));
  const container = byID("query-dimension-options");
  container.replaceChildren();
  if (!dimensions.length) {
    const empty = document.createElement("span");
    empty.className = "muted";
    empty.textContent = selectedQueryMetrics().length ? "所选指标没有共同可用维度。" : "选择指标后显示可用维度。";
    container.append(empty);
    return;
  }
  for (const dimension of dimensions) {
    const label = document.createElement("label");
    label.className = "check-chip";
    const input = document.createElement("input");
    input.type = "checkbox";
    input.name = "query-dimension";
    input.value = dimension;
    input.checked = selected.has(dimension);
    input.addEventListener("change", () => {
      if (dimension === time.dimension) {
        byID("time-grain").value = input.checked ? preferredTimeGranularity(time.granularities) : "";
      }
      updateQueryPreview();
    });
    label.append(input, document.createTextNode(dimension));
    container.append(label);
  }
}

function renderFilterDimensionOptions(dimensions) {
  state.filters = state.filters.filter((filter) => dimensions.includes(filter.dimension));
  const select = byID("filter-dimension");
  const previous = select.value;
  select.replaceChildren();
  if (!dimensions.length) select.append(makeOption("暂无可用维度", ""));
  for (const dimension of dimensions) select.append(makeOption(dimension, dimension));
  if (dimensions.includes(previous)) select.value = previous;
  renderFilters();
}

function configureTimeControls(time) {
  const preset = byID("time-preset");
  const grain = byID("time-grain");
  const enabled = Boolean(time.dimension);
  const timezone = byID("business-timezone");
  preset.disabled = !enabled;
  grain.disabled = !enabled;
  if (!enabled) {
    preset.value = "none";
    grain.value = "";
  } else {
    if (time.calendarTimezone) {
      timezone.value = time.calendarTimezone;
    } else if (!timezone.value.trim()) {
      timezone.value = Intl.DateTimeFormat().resolvedOptions().timeZone || "Asia/Shanghai";
    }
    if (preset.value === "none") preset.value = "yesterday";
  }
  timezone.readOnly = Boolean(time.calendarTimezone);
  timezone.title = time.calendarTimezone ? `该日期源固定使用 ${time.calendarTimezone} 日历，不能动态换算业务日。` : "使用 IANA 业务时区，例如 Asia/Shanghai";
  for (const option of grain.options) option.disabled = Boolean(option.value && !time.granularities.includes(option.value));
  if (grain.value && !time.granularities.includes(grain.value)) grain.value = "";
  const timeDimensionSelected = [...document.querySelectorAll("input[name='query-dimension']:checked")]
    .some((input) => input.value === time.dimension);
  if (timeDimensionSelected && !grain.value) grain.value = preferredTimeGranularity(time.granularities);
  byID("custom-time-range").hidden = preset.value !== "custom";
}

function updateQueryBuilder() {
  const metrics = selectedQueryMetrics();
  const dimensions = sharedDimensions(metrics);
  const time = sharedTimeMetadata(metrics);
  renderMetricOptions();
  renderDimensionOptions(dimensions, time);
  renderFilterDimensionOptions(dimensions);
  configureTimeControls(time);
  const examples = metrics.flatMap((metric) => (metric.usage_examples || []).map((example) => `${metric.display_name || metric.name}：${example}`));
  byID("query-examples").textContent = examples.length
    ? `常见使用：${examples.slice(0, 5).join("；")}`
    : "所选指标尚无维护者示例；可直接组合维度、筛选和明确时间范围。";
  let mode = {kind: "published"};
  try { mode = queryMode(metrics); } catch (_) {}
  byID("run-query-button").textContent = mode.kind === "draft" ? "草稿试查" : "运行查询";
  byID("copy-query-button").disabled = mode.kind === "draft";
  byID("cancel-job-button").disabled = mode.kind === "draft" || !state.activeJobID;
  updateQueryPreview();
}

function renderFilters() {
  const container = byID("active-filters");
  container.replaceChildren();
  for (const [index, filter] of state.filters.entries()) {
    const chip = document.createElement("span");
    chip.className = "filter-chip";
    chip.append(document.createTextNode(`${filter.dimension} ${filter.operator === "eq" ? "=" : "∈"} ${filter.values.join(", ")}`));
    const remove = document.createElement("button");
    remove.type = "button";
    remove.setAttribute("aria-label", `删除 ${filter.dimension} 筛选`);
    remove.textContent = "×";
    remove.addEventListener("click", () => {
      state.filters.splice(index, 1);
      renderFilters();
      updateQueryPreview();
    });
    chip.append(remove);
    container.append(chip);
  }
}

function timezoneParts(date, timezone) {
  const parts = new Intl.DateTimeFormat("en-CA", {
    timeZone: timezone, year: "numeric", month: "2-digit", day: "2-digit",
    hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23",
  }).formatToParts(date);
  return Object.fromEntries(parts.filter((part) => part.type !== "literal").map((part) => [part.type, Number(part.value)]));
}

function zonedMidnightISO(year, month, day, timezone) {
  let instant = Date.UTC(year, month - 1, day, 0, 0, 0);
  for (let attempt = 0; attempt < 3; attempt += 1) {
    const parts = timezoneParts(new Date(instant), timezone);
    const represented = Date.UTC(parts.year, parts.month - 1, parts.day, parts.hour, parts.minute, parts.second);
    instant += Date.UTC(year, month - 1, day, 0, 0, 0) - represented;
  }
  return new Date(instant).toISOString();
}

function shiftDate(parts, days = 0, months = 0) {
  const date = new Date(Date.UTC(parts.year, parts.month - 1 + months, parts.day + days));
  return {year: date.getUTCFullYear(), month: date.getUTCMonth() + 1, day: date.getUTCDate()};
}

function resolvePresetRange(preset, timezone, now = new Date(), custom = null) {
  if (preset === "none") return null;
  if (!timezone.trim()) throw new Error("请输入 IANA 业务时区，例如 Asia/Shanghai。");
  timezoneParts(new Date(), timezone);
  let start;
  let end;
  if (preset === "custom") {
    const startValue = custom?.start || byID("time-start").value;
    const endValue = custom?.end || byID("time-end").value;
    if (!startValue || !endValue) throw new Error("自定义时间必须填写开始日期和不含结束日期。");
    const parse = (value) => {
      const [year, month, day] = value.split("-").map(Number);
      return {year, month, day};
    };
    start = parse(startValue);
    end = parse(endValue);
  } else {
    const today = timezoneParts(now, timezone);
    const current = {year: today.year, month: today.month, day: today.day};
    if (preset === "yesterday") {
      end = current;
      start = shiftDate(current, -1);
    } else if (preset === "previous_week") {
      const weekday = new Date(Date.UTC(current.year, current.month - 1, current.day)).getUTCDay();
      const mondayOffset = (weekday + 6) % 7;
      end = shiftDate(current, -mondayOffset);
      start = shiftDate(end, -7);
    } else if (preset === "previous_month") {
      end = {year: current.year, month: current.month, day: 1};
      start = shiftDate(end, 0, -1);
    }
  }
  const result = {
    start: zonedMidnightISO(start.year, start.month, start.day, timezone),
    end: zonedMidnightISO(end.year, end.month, end.day, timezone),
  };
  if (new Date(result.start) >= new Date(result.end)) throw new Error("开始日期必须早于不含结束日期。");
  return result;
}

function buildQuery() {
  const metrics = selectedQueryMetrics();
  if (!metrics.length) throw new Error("请至少选择一个指标。");
  const limit = Number(byID("row-limit").value);
  if (!Number.isInteger(limit) || limit < 1 || limit > 1000) throw new Error("返回行数必须是 1 到 1000 之间的整数。");
  const query = {
    api_version: "metricspire.io/v1alpha1",
    kind: "SemanticQuery",
    metrics: metrics.map((metric) => metric.name),
    group_by: [...document.querySelectorAll("input[name='query-dimension']:checked")].map((input) => input.value),
    filters: state.filters,
    limit,
  };
  const time = sharedTimeMetadata(metrics);
  if (time.incompatible) throw new Error("所选时间指标使用不同时间口径，请拆成两次查询。");
  const selectedPreset = byID("time-preset").value;
  const preset = time.dimension && selectedPreset === "none" ? "yesterday" : selectedPreset;
  if (preset !== selectedPreset) byID("time-preset").value = preset;
  const timezone = byID("business-timezone").value.trim();
  if (preset !== "none") {
    if (!time.dimension) throw new Error("所选指标没有共同的时间口径，不能应用时间预设。");
    const range = resolvePresetRange(preset, timezone);
    query.time_range = {dimension: time.dimension, start: range.start, end: range.end, timezone};
  }
  const timeDimensionSelected = query.group_by.includes(time.dimension);
  const grain = byID("time-grain").value || (timeDimensionSelected ? preferredTimeGranularity(time.granularities) : "");
  if (grain && byID("time-grain").value !== grain) byID("time-grain").value = grain;
  if (grain) {
    if (!query.time_range) throw new Error("按时间分组前必须选择时间范围。");
    if (!query.group_by.includes(time.dimension)) query.group_by.push(time.dimension);
    query.time_grouping = {dimension: time.dimension, timezone, granularity: grain};
    if (grain === "week") query.time_grouping.week_start = "monday";
  }
  return {query};
}

function formatQueryFilters(filters = []) {
  if (!filters.length) return "无（筛选条件可选）";
  return filters.map((filter) => `${filter.dimension} ${filter.operator === "eq" ? "=" : "属于"} ${(filter.values || []).join("、")}`).join("；");
}

function formatTimeRange(range) {
  if (!range) return "不限制时间";
  return `[${range.start}, ${range.end}) · ${range.timezone || "未指定时区"}`;
}

function querySummaryRows(query, metrics = selectedQueryMetrics()) {
  const displayNames = new Map(metrics.map((metric) => [metric.name, metric.display_name || metric.name]));
  return [
    ["指标", query.metrics.map((name) => `${displayNames.get(name) || name} (${name})`).join("、")],
    ["分组", query.group_by?.length ? query.group_by.join("、") : "不分组，返回汇总值"],
    ["时间", formatTimeRange(query.time_range)],
    ["时间粒度", query.time_grouping ? `${humanGrain(query.time_grouping.granularity)} · ${query.time_grouping.dimension}` : "不按时间拆分"],
    ["筛选", formatQueryFilters(query.filters)],
    ["最多返回", `${query.limit} 行`],
  ];
}

function resourceLabel(resource = {}) {
  if (resource.uri) return resource.uri;
  return [resource.catalog, resource.schema, resource.table].filter(Boolean).join(".") || "由服务端绑定解析";
}

function planSummaryRows(operation, result, metrics = []) {
  const logical = result.logical_plan || {};
  const physical = result.physical_plan || {};
  const displayNames = new Map(metrics.map((metric) => [metric.name, metric.display_name || metric.name]));
  const outputMetrics = (logical.metrics || []).filter((metric) => metric.output)
    .map((metric) => `${displayNames.get(metric.name) || metric.name} (${metric.name})`);
  const outputDimensions = (logical.dimensions || []).filter((dimension) => dimension.output).map((dimension) => dimension.name);
  const rows = [
    ["使用版本", result.release?.id || "当前版本"],
    ["指标", outputMetrics.join("、") || "未识别"],
    ["业务实体", logical.root_entity || "未识别"],
    ["返回维度", outputDimensions.join("、") || "不分组"],
    ["时间", formatTimeRange(logical.time_range)],
    ["筛选", formatQueryFilters(logical.filters)],
    ["数据血缘", (logical.lineage?.datasets || []).join("、") || "未识别"],
  ];
  if (operation === "plan") {
    rows.push(
      ["执行引擎", physical.engine || "未识别"],
      ["数据来源", resourceLabel(physical.root?.resource)],
      ["返回上限", `${logical.limit || physical.limit || 0} 行`],
    );
  }
  return rows;
}

function renderSummary(container, rows) {
  container.replaceChildren();
  for (const [label, value] of rows) {
    const item = document.createElement(container.id === "result-summary" ? "article" : "div");
    const heading = document.createElement(container.id === "result-summary" ? "small" : "strong");
    const content = document.createElement("span");
    heading.textContent = label;
    content.textContent = value;
    item.append(heading, content);
    container.append(item);
  }
}

function renderPlanResult(operation, result) {
  byID("result-title").textContent = operation === "explain" ? "指标口径解释" : "执行计划";
  byID("result-meta").textContent = operation === "explain" ? "只解释，不访问数据" : "只规划，不执行查询";
  byID("result-panel").hidden = false;
  byID("result-summary").hidden = false;
  byID("result-table-wrap").hidden = true;
  renderSummary(byID("result-summary"), planSummaryRows(operation, result, selectedQueryMetrics()));
  byID("query-output").textContent = JSON.stringify(result, null, 2);
}

function updateQueryPreview() {
  try {
    const built = buildQuery();
    byID("query-request-preview").textContent = JSON.stringify(built.query, null, 2);
    renderSummary(byID("query-summary"), querySummaryRows(built.query));
    byID("resolved-time").textContent = built.query.time_range
      ? `实际请求范围：[${built.query.time_range.start}, ${built.query.time_range.end})${sharedTimeMetadata(selectedQueryMetrics()).calendarTimezone ? ` · 日期源固定 ${sharedTimeMetadata(selectedQueryMetrics()).calendarTimezone} 日历` : ""}`
      : selectedQueryMetrics().some((metric) => metric.time_dimension)
        ? "请选择一个明确时间范围；时间口径指标不允许隐式全量查询。"
        : "所选指标不要求时间范围。";
  } catch (error) {
    byID("query-request-preview").textContent = errorMessage(error);
    byID("query-summary").textContent = errorMessage(error);
    byID("resolved-time").textContent = errorMessage(error);
  }
}

function shellQuote(value) {
  return "'" + value.replaceAll("'", "'\"'\"'") + "'";
}

async function copyQueryRequest() {
  try {
    const {query} = buildQuery();
    if (queryMode().kind === "draft") throw new Error("未发布草稿没有面向应用的稳定 API；维护者试查通过并正式发布后才能复制调用请求。");
    const payload = [
      `curl --request POST ${shellQuote(window.location.origin + publicQueryPath("query"))}`,
      '--header "Authorization: Bearer ${METRICSPIRE_TOKEN}"',
      "--header 'Content-Type: application/json'",
      `--data ${shellQuote(JSON.stringify(query))}`,
    ].join(" \\\n  ");
    if (navigator.clipboard?.writeText) await navigator.clipboard.writeText(payload);
    else window.prompt("复制下面的 API 请求", payload);
    setStatus("query-status", "已复制 API 请求。请在运行终端提供 METRICSPIRE_TOKEN；请求中不包含令牌，也不会自动使用浏览器登录态。", "success");
  } catch (error) {
    setStatus("query-status", errorMessage(error), "error");
  }
}

function renderQueryResult(snapshot) {
  byID("query-output").textContent = JSON.stringify(snapshot, null, 2);
  byID("result-title").textContent = "结果预览";
  byID("result-panel").hidden = false;
  byID("result-summary").hidden = true;
  const job = snapshot.job || {};
  const result = snapshot.result;
  if (!result) {
    byID("result-table-wrap").hidden = true;
    byID("result-table").replaceChildren();
    byID("result-meta").textContent = "";
    const status = {pending: "等待执行", running: "正在查询", failed: "查询失败", cancelled: "已取消", succeeded: "已完成"};
    setStatus("query-status", job.error?.message || (job.status ? `查询状态：${status[job.status] || job.status}` : "查询已提交。"), job.status === "failed" ? "error" : "neutral");
    return;
  }
  const columns = result.columns || [];
  const rows = result.rows || [];
  const range = snapshot.resolved_time_range;
  byID("result-table-wrap").hidden = false;
  byID("result-meta").textContent = `${rows.length} 行${result.truncated ? " · 已截断" : ""}${range ? ` · ${range.start} 至 ${range.end}${range.timezone ? ` · ${range.timezone}` : ""}` : ""}`;
  const table = byID("result-table");
  table.replaceChildren();
  const head = document.createElement("thead");
  const headRow = document.createElement("tr");
  for (const column of columns) {
    const cell = document.createElement("th");
    cell.textContent = column.name;
    cell.title = column.type_text || column.type_name || "";
    headRow.append(cell);
  }
  head.append(headRow);
  const body = document.createElement("tbody");
  for (const row of rows) {
    const tableRow = document.createElement("tr");
    for (const value of row) {
      const cell = document.createElement("td");
      cell.textContent = value === null ? "NULL" : String(value);
      if (value === null) cell.className = "null-value";
      tableRow.append(cell);
    }
    body.append(tableRow);
  }
  table.append(head, body);
  setStatus("query-status", `查询成功 · ${snapshot.release_id || "当前发布版本"}`, "success");
}

async function runQueryOperation(operation) {
  if (state.queryBusy) return;
  try {
    const {query} = buildQuery();
    state.queryBusy = true;
    byID("run-query-button").disabled = true;
    for (const button of document.querySelectorAll("[data-operation]")) button.disabled = true;
    byID("result-table").replaceChildren();
    byID("result-meta").textContent = "";
    byID("query-output").textContent = "";
    byID("result-panel").hidden = true;
    setStatus("query-status", ({
      query: "正在提交受控查询…",
      explain: "正在解释指标口径…",
      plan: "正在生成执行计划…",
    })[operation] || "正在处理请求…");
    const mode = queryMode();
    const path = mode.kind === "draft"
      ? routePath({namespace: state.namespace, model_name: mode.modelName}, `draft-${operation}`)
      : publicQueryPath(operation);
    const result = await requestJSON(path, {method: "POST", body: JSON.stringify(query)});
    if (operation !== "query") {
      renderPlanResult(operation, result);
      setStatus("query-status", `${operation === "explain" ? "语义解释" : "执行计划"}已生成。`, "success");
      return;
    }
    if (mode.kind === "draft") {
      renderQueryResult({...result.execution, release_id: result.release?.id || "未发布草稿"});
      return;
    }
    renderQueryResult(result);
    if (result.job?.id) {
      state.activeJobID = result.job.id;
      byID("cancel-job-button").disabled = false;
      await pollJob(result.job.id);
    }
  } catch (error) {
    byID("result-title").textContent = "请求未完成";
    byID("result-summary").hidden = false;
    renderSummary(byID("result-summary"), [["需要处理", errorMessage(error)]]);
    byID("result-table-wrap").hidden = true;
    byID("result-table").replaceChildren();
    byID("result-meta").textContent = "";
    byID("query-output").textContent = JSON.stringify(error instanceof Error ? {message: error.message} : error, null, 2);
    byID("result-panel").hidden = false;
    setStatus("query-status", errorMessage(error), "error");
  } finally {
    state.queryBusy = false;
    byID("run-query-button").disabled = false;
    for (const button of document.querySelectorAll("[data-operation]")) button.disabled = false;
  }
}

async function pollJob(id) {
  for (;;) {
    await new Promise((resolve) => window.setTimeout(resolve, 500));
    try {
      const snapshot = await requestJSON(`/api/v1/jobs/${escapePath(id)}`);
      renderQueryResult(snapshot);
      if (["succeeded", "failed", "cancelled"].includes(snapshot.job.status)) break;
    } catch (error) {
      setStatus("query-status", errorMessage(error), "error");
      break;
    }
  }
  if (state.activeJobID === id) {
    state.activeJobID = null;
    byID("cancel-job-button").disabled = true;
  }
}

function governanceMetrics() {
  return state.draft?.source?.spec?.metrics || [];
}

function metricFilterLabel(filter = {}) {
  const operators = {eq: "=", neq: "<>", in: "IN", not_in: "NOT IN", is_null: "IS NULL", is_not_null: "IS NOT NULL"};
  const operator = operators[filter.operator] || String(filter.operator || "").toUpperCase();
  if (filter.operator === "is_null" || filter.operator === "is_not_null") return `${filter.field || "?"} ${operator}`;
  const values = (filter.values || []).map((value) => `'${String(value).replaceAll("'", "''")}'`);
  return `${filter.field || "?"} ${operator} ${["in", "not_in"].includes(filter.operator) ? `(${values.join(", ")})` : (values[0] || "?")}`;
}

function metricExpressionLabel(expression = {}) {
  if (!expression.op) return "受治理口径 · 维护端可见";
  const op = String(expression.op || "").toUpperCase();
  if (expression.op === "metric") return expression.metric || "?";
  if (expression.op === "literal") return expression.value || "0";
  if (["add", "subtract", "multiply", "divide"].includes(expression.op)) {
    const symbols = {add: "+", subtract: "−", multiply: "×", divide: "÷"};
    return `(${(expression.args || []).map(metricExpressionLabel).join(` ${symbols[expression.op]} `)})`;
  }
  const field = expression.field || "*";
  const filters = (expression.filters || []).map(metricFilterLabel);
  const value = filters.length ? `CASE WHEN ${filters.join(" AND ")} THEN ${field} ELSE NULL END` : field;
  if (expression.op === "count_distinct") return `COUNT(DISTINCT ${value})`;
  return `${op || "FORMULA"}(${value})`;
}

function metricFormulaLabel(metric = {}) {
  return metric.expression?.op ? metricExpressionLabel(metric.expression) : (metric.formula_summary || "受治理口径 · 维护端可见");
}

function metricIsPublished(metric) {
  if (!metric || !state.review?.active_release) return false;
  const change = (state.review.metric_changes || []).find((candidate) => candidate.code === metric.name);
  return !change || change.kind !== "added";
}

function metricRegistryStatus(metric) {
  if (metric.deprecated) return "弃用";
  const change = (state.review?.metric_changes || []).find((candidate) => candidate.code === metric.name);
  if (!metricIsPublished(metric)) return "草稿";
  if (change) return "待发布";
  if (metric.verification?.status !== "verified") return "试用";
  return "线上";
}

function metricExecutionLocked(metric) {
  return Boolean(metric && (metric.kind !== "aggregate" || metricIsPublished(metric)));
}

function renderMetricInventory() {
  const container = byID("metric-inventory");
  const search = (byID("governance-metric-search")?.value || "").trim().toLowerCase();
  container.replaceChildren();
  const matches = governanceMetrics().map((metric, index) => ({metric, index})).filter(({metric}) =>
    !search || [metric.external_code, metric.name, metric.display_name].some((value) => String(value || "").toLowerCase().includes(search)));
  if (!matches.length) {
    const empty = document.createElement("div");
    empty.className = "empty-state";
    empty.textContent = governanceMetrics().length ? "没有匹配的指标。" : "当前草稿还没有指标。";
    container.append(empty);
    return;
  }
  for (const {metric, index} of matches) {
    const row = document.createElement("button");
    row.type = "button";
    row.className = `metric-row${!state.creatingMetric && index === state.metricIndex ? " active" : ""}`;
    const identity = document.createElement("span");
    identity.className = "metric-row-main";
    const number = document.createElement("span");
    number.textContent = metric.external_code ? `#${metric.external_code}` : "未编号";
    const title = document.createElement("strong");
    title.textContent = metric.display_name || metric.name;
    const name = document.createElement("code");
    name.textContent = metric.name;
    identity.append(number, title, name);
    const formula = document.createElement("span");
    formula.className = "metric-row-formula";
    formula.textContent = metricExpressionLabel(metric.expression);
    const status = document.createElement("span");
    const registryStatus = metricRegistryStatus(metric);
    status.className = `badge metric-row-status${registryStatus === "线上" ? "" : " warning"}`;
    status.textContent = registryStatus;
    row.append(identity, formula, status);
    row.addEventListener("click", () => selectGovernanceMetric(index));
    container.append(row);
  }
}

function selectGovernanceMetric(index) {
  if (state.editorDirty && !window.confirm("这条指标有尚未保存的修改。放弃修改并切换？")) return;
  state.metricIndex = index;
  state.creatingMetric = false;
  byID("metric-select").value = String(index);
  renderMetricEditor(governanceMetrics()[index]);
  renderMetricInventory();
}

function datasetForEntity(entityName) {
  const entity = state.draft.source.spec.entities.find((candidate) => candidate.name === entityName);
  return entity ? state.draft.source.spec.datasets.find((dataset) => dataset.name === entity.dataset) : null;
}

function dimensionsForEntity(entityName) {
  return state.draft.source.spec.dimensions.filter((dimension) => dimension.entity === entityName);
}

function renderMetricSelect(preferredIndex = 0) {
  const select = byID("metric-select");
  select.replaceChildren();
  for (const [index, metric] of governanceMetrics().entries()) {
    select.append(makeOption(`${metric.display_name || metric.name} · ${metric.name}`, String(index)));
  }
  if (!governanceMetrics().length) {
    select.append(makeOption("草稿中尚无指标", ""));
    select.disabled = true;
    state.metricIndex = -1;
    clearMetricEditor();
    renderMetricInventory();
    return;
  }
  select.disabled = false;
  state.metricIndex = Math.min(Math.max(preferredIndex, 0), governanceMetrics().length - 1);
  select.value = String(state.metricIndex);
  state.creatingMetric = false;
  renderMetricEditor(governanceMetrics()[state.metricIndex]);
  renderMetricInventory();
}

function fillSelect(select, values, selected, emptyLabel = "请选择") {
  select.replaceChildren();
  if (!values.length) select.append(makeOption(emptyLabel, ""));
  for (const value of values) select.append(makeOption(value, value));
  select.value = values.includes(selected) ? selected : (values[0] || "");
}

function renderEntityFields(entityName, selectedField = "") {
  const dataset = datasetForEntity(entityName);
  fillSelect(byID("metric-field"), dataset ? dataset.fields.map((field) => `${entityName}.${field.name}`) : [], selectedField, "没有可用字段");
  renderMetricFilterFields(entityName);
}

function renderMetricFilterFields(entityName) {
  const dataset = datasetForEntity(entityName);
  const fields = dataset ? dataset.fields.map((field) => `${entityName}.${field.name}`) : [];
  fillSelect(byID("metric-filter-field"), fields, byID("metric-filter-field").value, "没有可用字段");
}

function metricFilterNeedsValues(operator) {
  return operator !== "is_null" && operator !== "is_not_null";
}

function renderMetricExpressionFilters(locked = false) {
  const container = byID("metric-expression-filters");
  container.replaceChildren();
  if (!state.metricExpressionFilters.length) {
    const empty = document.createElement("span");
    empty.className = "muted";
    empty.textContent = "未设置条件，将聚合全部符合查询范围的记录。";
    container.append(empty);
  }
  state.metricExpressionFilters.forEach((filter, index) => {
    const chip = document.createElement("span");
    chip.className = "filter-chip expression-filter-chip";
    const label = document.createElement("span");
    label.textContent = metricFilterLabel(filter);
    chip.append(label);
    if (!locked) {
      const remove = document.createElement("button");
      remove.type = "button";
      remove.setAttribute("aria-label", `移除条件 ${label.textContent}`);
      remove.textContent = "×";
      remove.addEventListener("click", () => {
        state.metricExpressionFilters.splice(index, 1);
        state.editorDirty = true;
        renderMetricExpressionFilters(false);
        refreshFormulaPreview();
      });
      chip.append(remove);
    }
    container.append(chip);
  });
}

function addMetricExpressionFilter() {
  const field = byID("metric-filter-field").value;
  const operator = byID("metric-filter-operator").value;
  const values = metricFilterNeedsValues(operator)
    ? byID("metric-filter-values").value.split(",").map((value) => value.trim()).filter(Boolean)
    : [];
  if (!field || (metricFilterNeedsValues(operator) && !values.length)) {
    setStatus("editor-status", "请选择条件字段，并填写至少一个条件值。", "error");
    return;
  }
  if (["eq", "neq"].includes(operator) && values.length !== 1) {
    setStatus("editor-status", "“等于 / 不等于”只能填写一个值；多个值请使用“属于其中”。", "error");
    return;
  }
  if (state.metricExpressionFilters.some((filter) => filter.field === field && filter.operator === operator)) {
    setStatus("editor-status", "同一字段和判断方式不能重复；请修改已有条件。", "error");
    return;
  }
  state.metricExpressionFilters.push({field, operator, values});
  state.editorDirty = true;
  byID("metric-filter-values").value = "";
  renderMetricExpressionFilters(false);
  refreshFormulaPreview();
  setStatus("editor-status", "条件已加入计算口径；保存后只进入草稿。", "warning");
}

function renderMetricDimensions(entityName, selectedDimensions = [], timeDimension = "") {
  const dimensions = state.draft.source.spec.dimensions.filter((dimension) =>
    dimension.entity === entityName || selectedDimensions.includes(dimension.name));
  const container = byID("metric-dimension-options");
  container.replaceChildren();
  for (const dimension of dimensions) {
    const label = document.createElement("label");
    label.className = "check-chip";
    const input = document.createElement("input");
    input.type = "checkbox";
    input.name = "metric-dimension";
    input.value = dimension.name;
    input.checked = selectedDimensions.includes(dimension.name);
    label.append(input, document.createTextNode(dimension.name));
    container.append(label);
  }
  if (!dimensions.length) appendChip(container, "该实体没有维度", "warning");
  const timeDimensions = dimensions.filter((dimension) => dimension.type === "time").map((dimension) => dimension.name);
  const timeSelect = byID("metric-time-dimension");
  timeSelect.replaceChildren(makeOption("不限定时间", ""));
  for (const dimension of timeDimensions) timeSelect.append(makeOption(dimension, dimension));
  timeSelect.value = timeDimensions.includes(timeDimension) ? timeDimension : "";
}

function clearMetricEditor() {
  state.metricExpressionFilters = [];
  for (const id of ["metric-external-code", "metric-code", "metric-display-name", "metric-description", "metric-owner", "metric-unit", "metric-tags", "metric-examples", "metric-evidence"]) byID(id).value = "";
  byID("metric-verified").checked = false;
  byID("metric-deprecated").checked = false;
  byID("metric-editor-title").textContent = "新增基础指标";
  byID("metric-formula-preview").textContent = "—";
  renderMetricExpressionFilters(false);
}

function renderMetricEditor(metric) {
  state.editorDirty = false;
  byID("editor-status").hidden = true;
  const aggregate = !metric || metric.kind === "aggregate";
  const executionLocked = metricExecutionLocked(metric);
  byID("metric-editor-kicker").textContent = state.creatingMetric ? "新建草稿" : metricIsPublished(metric) ? "已发布指标" : "未发布草稿";
  byID("metric-editor-title").textContent = state.creatingMetric ? "新增基础指标" : (metric?.display_name || metric?.name || "选择一条指标");
  byID("duplicate-metric-button").hidden = !metric || metric.kind !== "aggregate" || !metricIsPublished(metric);
  byID("editor-mode-note").textContent = state.creatingMetric
    ? "这是一条新指标。保存后只进入草稿，发布前仍可修改口径。"
    : executionLocked && aggregate
      ? "这条指标已经发布。名称和说明可以继续维护；编号和计算口径已锁定。如需改口径，请复制为新指标。"
      : aggregate
        ? "这条指标还没有发布，编号和计算口径仍可修改。保存只更新草稿。"
      : `这是 ${metric.kind} 指标。当前界面只维护元数据；复合公式仍由版本化契约管理。`;
  byID("metric-external-code").value = metric?.external_code || "";
  byID("metric-code").value = metric?.name || "";
  byID("metric-display-name").value = metric?.display_name || "";
  byID("metric-description").value = metric?.description || "";
  byID("metric-owner").value = metric?.owner || "";
  byID("metric-unit").value = metric?.unit || "";
  byID("metric-tags").value = (metric?.tags || []).join(", ");
  byID("metric-examples").value = (metric?.usage_examples || []).join("\n");
  byID("metric-evidence").value = (metric?.verification?.evidence || []).join("；");
  byID("metric-verified").checked = metric?.verification?.status === "verified";
  byID("metric-verification-label").textContent = verificationLabel(metric);
  byID("metric-deprecated").checked = Boolean(metric?.deprecated);
  const verificationNote = byID("business-verification-note");
  verificationNote.hidden = !isBusinessUnverified(metric);
  verificationNote.textContent = isBusinessUnverified(metric)
    ? "当前记录仅表示技术试查证据已登记；业务口径仍待确认。试用版本不等于业务验证通过。"
    : "";
  const entities = state.draft.source.spec.entities.map((entity) => entity.name);
  fillSelect(byID("metric-entity"), entities, metric?.entity || "", "没有可用实体");
  byID("metric-operation").value = metric?.expression?.op || "sum";
  state.metricExpressionFilters = structuredClone(metric?.expression?.filters || []);
  renderEntityFields(byID("metric-entity").value, metric?.expression?.field || "");
  byID("metric-value-type").value = metric?.value_type || "decimal";
  renderMetricDimensions(byID("metric-entity").value, metric?.allowed_dimensions || [], metric?.time_dimension || "");
  for (const id of ["metric-code", "metric-entity", "metric-operation", "metric-field", "metric-value-type", "metric-unit"]) byID(id).disabled = executionLocked;
  byID("metric-external-code").disabled = executionLocked && Boolean(metric?.external_code);
  for (const input of document.querySelectorAll("input[name='metric-dimension']")) input.disabled = executionLocked;
  byID("metric-time-dimension").disabled = executionLocked;
  byID("metric-filter-field").disabled = executionLocked;
  byID("metric-filter-operator").disabled = executionLocked;
  byID("metric-filter-values").disabled = executionLocked;
  byID("add-metric-filter-button").disabled = executionLocked;
  renderMetricExpressionFilters(executionLocked);
  byID("execution-lock-note").textContent = executionLocked
    ? "发布后的英文名、公式、单位和维度契约不可原地改写；这样历史报表不会在同名指标下悄悄变义。"
    : "口径以结构化表达式保存，不接受任意 SQL；发布前系统会检查字段、维度和数据绑定。";
  byID("metric-formula-preview").textContent = metricExpressionLabel(metric?.expression || expressionFromEditor());
}

function newMetric() {
  if (!state.draft) return;
  if (state.editorDirty && !window.confirm("当前表单的编辑尚未应用。放弃这些编辑并新增指标？")) return;
  state.creatingMetric = true;
  state.metricIndex = -1;
  byID("metric-select").value = "";
  renderMetricEditor(null);
  renderMetricInventory();
}

function duplicateMetric() {
  const metric = governanceMetrics()[state.metricIndex];
  if (!metric) return;
  if (state.editorDirty && !window.confirm("当前修改尚未保存。放弃修改并复制线上口径？")) return;
  state.creatingMetric = true;
  state.metricIndex = -1;
  const copy = structuredClone(metric);
  copy.name = "";
  copy.external_code = "";
  copy.display_name = `${metric.display_name || metric.name}（新口径）`;
  copy.deprecated = false;
  copy.verification = {status: "unverified", evidence: []};
  renderMetricEditor(copy);
  renderMetricInventory();
}

function expressionFromEditor() {
  return {op: byID("metric-operation").value, field: byID("metric-field").value,
    ...(state.metricExpressionFilters.length ? {filters: structuredClone(state.metricExpressionFilters)} : {})};
}

function refreshFormulaPreview() {
  if (!byID("metric-formula-preview")) return;
  byID("metric-formula-preview").textContent = metricExpressionLabel(expressionFromEditor());
}

function metricFromEditor() {
  const editable = {
    name: byID("metric-code").value.trim(),
    external_code: byID("metric-external-code").value.trim(),
    display_name: byID("metric-display-name").value.trim(),
    description: byID("metric-description").value.trim(),
    owner: byID("metric-owner").value.trim(),
    tags: byID("metric-tags").value.split(",").map((value) => value.trim()).filter(Boolean),
    usage_examples: byID("metric-examples").value.split("\n").map((value) => value.trim()).filter(Boolean),
    deprecated: byID("metric-deprecated").checked,
    verification: {
      status: byID("metric-verified").checked ? "verified" : "unverified",
      evidence: byID("metric-evidence").value.split("；").map((value) => value.trim()).filter(Boolean),
    },
  };
  if (!editable.name || !editable.display_name || !editable.description || !editable.owner) throw new Error("英文名、中文名、指标定义和负责人都是必填项。");
  if (!/^[a-z][a-z0-9_]*$/.test(editable.name)) throw new Error("英文名只能使用小写字母、数字和下划线，并以字母开头。");
  if (editable.external_code && !/^[0-9]+$/.test(editable.external_code)) throw new Error("业务编号只能包含数字。");
  if (editable.external_code && governanceMetrics().some((candidate, index) => candidate.external_code === editable.external_code && index !== state.metricIndex)) throw new Error(`业务编号 ${editable.external_code} 已被其他指标使用。`);
  if (editable.usage_examples.length > 5) throw new Error("每个指标最多维护 5 条常见使用示例。");
  const current = governanceMetrics()[state.metricIndex];
  if (!state.creatingMetric && metricExecutionLocked(current)) {
    return {...governanceMetrics()[state.metricIndex], ...editable};
  }
  const entity = byID("metric-entity").value;
  const field = byID("metric-field").value;
  if (!entity || !field) throw new Error("新增基础指标必须选择业务实体和计算字段。");
  return {
    ...editable,
    entity,
    kind: "aggregate",
    value_type: byID("metric-value-type").value,
    unit: byID("metric-unit").value.trim(),
    expression: expressionFromEditor(),
    allowed_dimensions: [...document.querySelectorAll("input[name='metric-dimension']:checked")].map((input) => input.value),
    time_dimension: byID("metric-time-dimension").value,
  };
}

async function applyMetric(event) {
  event.preventDefault();
  try {
    const metric = metricFromEditor();
    let index = state.metricIndex;
    const source = structuredClone(state.draft.source);
    if (state.creatingMetric) {
      if (governanceMetrics().some((candidate) => candidate.name === metric.name)) throw new Error(`英文名 ${metric.name} 已存在。`);
      source.spec.metrics.push(metric);
      index = source.spec.metrics.length - 1;
    } else {
      source.spec.metrics[index] = metric;
    }
    setStatus("editor-status", "正在校验并保存草稿…");
    const saved = await requestJSON(routePath(state.governanceRoute, "draft"), {
      method: "PUT",
      body: JSON.stringify({expected_revision: state.draft.revision, source}),
    });
    state.draft = saved;
    state.creatingMetric = false;
    state.editorDirty = false;
    byID("draft-revision-value").textContent = `revision ${saved.revision}`;
    byID("draft-updated-meta").textContent = [saved.updated_by, saved.updated_at && new Date(saved.updated_at).toLocaleString("zh-CN")].filter(Boolean).join(" · ");
    await reviewGovernance();
    renderMetricSelect(index);
    setStatus("editor-status", `已保存到草稿 revision ${saved.revision}，线上版本未改变。`, "success");
    setStatus("governance-status", "草稿已保存。确认发布检查和说明后，才会创建新的线上版本。", "success");
  } catch (error) {
    setStatus("editor-status", errorMessage(error), "error");
    setStatus("governance-status", errorMessage(error), "error");
  }
}

function confirmDiscardDraft() {
  return !state.editorDirty || window.confirm("存在尚未保存的修改。放弃本页修改并切换？已保存的草稿与线上版本不会改变。");
}

function requireAppliedEditor() {
  if (!state.editorDirty) return true;
  const message = "表单还有尚未保存的修改。请先保存到草稿，再进行发布。";
  setStatus("editor-status", message, "warning");
  setStatus("governance-status", message, "warning");
  return false;
}

function groupPublicationIssues(issues = []) {
  const rules = [
    {pattern: /^metric "([^"]+)" is not verified$/, key: "not_verified", label: "业务验证待完成"},
    {pattern: /^metric "([^"]+)" has no display name$/, key: "display_name", label: "缺少中文名称"},
    {pattern: /^metric "([^"]+)" has no business definition$/, key: "description", label: "缺少业务定义"},
    {pattern: /^metric "([^"]+)" has no owner$/, key: "owner", label: "缺少负责人"},
    {pattern: /^metric "([^"]+)" has no verification evidence$/, key: "evidence", label: "缺少验证证据"},
    {pattern: /^metric "([^"]+)" has empty verification evidence$/, key: "empty_evidence", label: "存在空验证证据"},
  ];
  const groups = new Map();
  for (const issue of issues) {
    const rule = rules.find((candidate) => candidate.pattern.test(issue));
    const match = rule?.pattern.exec(issue);
    const key = rule?.key || issue;
    if (!groups.has(key)) groups.set(key, {key, label: rule?.label || "其他发布问题", metrics: [], issues: []});
    const group = groups.get(key);
    if (match?.[1]) group.metrics.push(match[1]);
    group.issues.push(issue);
  }
  return [...groups.values()];
}

function renderReview() {
  const review = state.review;
  if (!review) return;
  const changes = review.metric_changes || [];
  const breaking = changes.filter((change) => change.breaking);
  const issues = review.publication_issues || [];
  const issueGroups = groupPublicationIssues(issues);
  byID("active-release-value").textContent = review.active_release?.id || "尚未发布";
  byID("active-release-meta").textContent = review.active_release
    ? `revision ${review.active_release.source_revision} · ${review.active_release.manifest_fingerprint.slice(0, 18)}…`
    : "首次发布候选";
  byID("binding-status-value").textContent = review.binding.status === "ready" ? "完整" : review.binding.status === "missing" ? "未配置" : "需处理";
  byID("binding-status-meta").textContent = review.binding.message;
  setStatus("review-status", issues.length
    ? `发布前还需处理 ${issues.length} 项问题，已归并为 ${issueGroups.length} 类。`
    : review.structure_changed
      ? "候选内容修改了内部模型结构，不能从简化页面直接发布。"
      : !changes.length
        ? "草稿与当前线上版本没有指标变化。"
    : breaking.length
      ? `发现 ${changes.length} 项指标变化，其中 ${breaking.length} 项会改变已发布契约，不能直接发布。`
      : `发现 ${changes.length} 项可审查变更；发布动作仍会重复执行完整校验。`, issues.length || review.structure_changed || breaking.length ? "error" : "success");
  const list = byID("change-list");
  list.replaceChildren();
  for (const change of changes) {
    const card = document.createElement("article");
    card.className = "change-card";
    const title = document.createElement("strong");
    title.textContent = `${({added: "新增", changed: "修改", removed: "删除"})[change.kind] || change.kind} · ${change.display_name || change.code}`;
    const meta = document.createElement("small");
    const details = [];
    if ((change.fields || []).length) details.push(`变化字段：${change.fields.join("、")}`);
    if ((change.dependents || []).length) details.push(`内部依赖：${change.dependents.join("、")}`);
    if (change.breaking) details.push("会改变已发布执行契约");
    meta.textContent = details.join("；") || "无下游内部依赖";
    card.append(title, meta);
    list.append(card);
  }
  for (const group of issueGroups) {
    const card = document.createElement("article");
    card.className = "change-card";
    const title = document.createElement("strong");
    title.textContent = `${group.label} · ${group.issues.length} 项`;
    const meta = document.createElement("small");
    const preview = group.metrics.slice(0, 8);
    meta.textContent = preview.length
      ? `影响指标：${preview.join("、")}${group.metrics.length > preview.length ? `，另有 ${group.metrics.length - preview.length} 项` : ""}`
      : group.issues[0];
    card.append(title, meta);
    list.append(card);
  }
  if (review.structure_changed) {
    const card = document.createElement("article");
    card.className = "change-card";
    const title = document.createElement("strong");
    title.textContent = "内部模型结构发生变化";
    const meta = document.createElement("small");
    meta.textContent = "当前简化界面不编辑数据集、实体或关系；请通过受审查的契约导入流程处理。";
    card.append(title, meta);
    list.append(card);
  }
  if (!changes.length && !issues.length && !review.structure_changed) {
    const empty = document.createElement("div");
    empty.className = "empty-state";
    empty.textContent = "没有待发布的指标变化。";
    list.append(empty);
  }
  renderBinding(review.binding);
  byID("publish-button").disabled = issues.length > 0 || review.structure_changed || breaking.length > 0 ||
    review.binding.status !== "ready" || (!changes.length && Boolean(review.active_release));
}

function renderBinding(bindingReview) {
  const container = byID("binding-summary");
  container.replaceChildren();
  const summary = document.createElement("p");
  summary.textContent = bindingReview.message;
  container.append(summary);
  for (const problem of bindingReview.problems || []) appendChip(container, problem, "warning");
  const binding = bindingReview.binding;
  if (!binding) return;
  const engine = document.createElement("p");
  engine.textContent = `执行引擎：${binding.engine}`;
  container.append(engine);
  for (const dataset of binding.datasets || []) {
    const item = document.createElement("div");
    item.className = "binding-dataset";
    const name = document.createElement("strong");
    name.textContent = dataset.name;
    const resource = document.createElement("code");
    const ref = dataset.resource || {};
    resource.textContent = ref.kind === "table" ? [ref.catalog, ref.schema, ref.table].filter(Boolean).join(".") : ref.uri || ref.kind;
    item.append(name, resource);
    container.append(item);
  }
}

function renderReleases() {
  const container = byID("release-list");
  container.replaceChildren();
  if (!state.releases.length) {
    const empty = document.createElement("div");
    empty.className = "empty-state";
    empty.textContent = "还没有不可变发布版本。";
    container.append(empty);
    return;
  }
  for (const release of state.releases) {
    const card = document.createElement("article");
    card.className = "release-card";
    const heading = document.createElement("div");
    const title = document.createElement("strong");
    title.textContent = release.active ? `${release.id} · 当前` : release.id;
    const fingerprint = document.createElement("code");
    fingerprint.textContent = release.manifest_fingerprint;
    heading.append(title, fingerprint);
    const meta = document.createElement("span");
    const created = release.created_at ? new Date(release.created_at).toLocaleString("zh-CN") : "";
    const channel = release.channel === "trial" ? "试用版本" : "认证版本";
    meta.textContent = [channel, `revision ${release.source_revision}`, release.created_by, created, release.note].filter(Boolean).join(" · ");
    const action = document.createElement("button");
    action.type = "button";
    action.className = "button danger";
    if (release.active) {
      action.textContent = "撤下当前版本";
      action.addEventListener("click", deactivateRelease);
    } else {
      action.textContent = "回滚到此版本";
      action.addEventListener("click", () => rollbackRelease(release.id));
    }
    card.append(heading, meta, action);
    container.append(card);
  }
}

async function reviewGovernance() {
  if (!state.draft || !state.governanceRoute) return;
  try {
    state.review = await requestJSON(routePath(state.governanceRoute, "review"), {
      method: "POST", body: JSON.stringify({source: state.draft.source}),
    });
    renderReview();
  } catch (error) {
    state.review = null;
    setStatus("review-status", errorMessage(error), "error");
    byID("publish-button").disabled = true;
  }
}

async function loadGovernance() {
  if (!state.governanceRoute || !hasPermission("model:manage")) return;
  setStatus("governance-status", "正在读取草稿、当前发布版本和数据绑定…");
  try {
    const [draft, releases] = await Promise.all([
      requestJSON(routePath(state.governanceRoute, "draft")),
      requestJSON(routePath(state.governanceRoute, "releases")),
    ]);
    state.draft = draft;
    state.releases = releases;
    byID("governance-namespace-value").textContent = state.governanceRoute.namespace;
    byID("draft-revision-value").textContent = `revision ${draft.revision}`;
    byID("draft-updated-meta").textContent = [draft.updated_by, draft.updated_at && new Date(draft.updated_at).toLocaleString("zh-CN")].filter(Boolean).join(" · ");
    renderReleases();
    await reviewGovernance();
    renderMetricSelect();
    setStatus("governance-status", "维护上下文已载入。线上查询继续只读取当前已发布版本。", "success");
  } catch (error) {
    state.draft = null;
    setStatus("governance-status", errorMessage(error), "error");
  }
}

async function publishDraft() {
  if (!state.draft) return;
  if (!requireAppliedEditor()) return;
  const note = byID("release-note").value.trim();
  if (!note) {
    setStatus("governance-status", "发布说明必填：请说明本次为什么修改。", "error");
    return;
  }
  try {
    setStatus("governance-status", "正在发布不可变版本…");
    const release = await requestJSON(routePath(state.governanceRoute, "publish"), {
      method: "POST", body: JSON.stringify({expected_revision: state.draft.revision, note}),
    });
    byID("release-note").value = "";
    await loadGovernance();
    await loadCatalog();
    setStatus("governance-status", `版本 ${release.id} 已发布并成为当前版本。`, "success");
  } catch (error) {
    setStatus("governance-status", errorMessage(error), "error");
  }
}

async function rollbackRelease(releaseID) {
  const note = byID("release-note").value.trim();
  if (!note) {
    setStatus("governance-status", "回滚说明必填：请说明为什么恢复此版本。", "error");
    return;
  }
  if (!window.confirm(`确认将 ${state.governanceRoute.model_name} 恢复为 ${releaseID}？系统会重新启用该历史版本并记录回滚事件，不会创建新版本。`)) return;
  try {
    const release = await requestJSON(routePath(state.governanceRoute, "rollback"), {
      method: "POST", body: JSON.stringify({release_id: releaseID, note}),
    });
    byID("release-note").value = "";
    await loadGovernance();
    await loadCatalog();
    setStatus("governance-status", `已重新启用历史版本 ${release.id}，并记录回滚事件。`, "success");
  } catch (error) {
    setStatus("governance-status", errorMessage(error), "error");
  }
}

async function deactivateRelease() {
  const note = byID("release-note").value.trim();
  if (!note) {
    setStatus("governance-status", "撤下说明必填：请说明为什么停止对外提供当前版本。", "error");
    return;
  }
  if (!window.confirm(`确认撤下 ${state.governanceRoute.model_name} 的当前版本？历史版本和审计记录会保留。`)) return;
  try {
    const release = await requestJSON(routePath(state.governanceRoute, "deactivate"), {
      method: "POST", body: JSON.stringify({note}),
    });
    byID("release-note").value = "";
    await loadGovernance();
    await loadCatalog();
    setStatus("governance-status", `版本 ${release.id} 已撤下；历史记录仍保留。`, "success");
  } catch (error) {
    setStatus("governance-status", errorMessage(error), "error");
  }
}

function bindEvents() {
  byID("logout-button").addEventListener("click", async () => {
    await fetch("/auth/logout", {method: "POST", credentials: "same-origin"});
    window.location.assign("/");
  });
  byID("namespace-select").addEventListener("change", (event) => switchNamespace(event.target.value));
  byID("governance-route").addEventListener("change", async (event) => {
    if (!confirmDiscardDraft()) {
      event.target.value = String((state.context.models || []).filter((route) => route.namespace === state.namespace).indexOf(state.governanceRoute));
      return;
    }
    state.governanceRoute = (state.context.models || []).filter((route) => route.namespace === state.namespace)[Number(event.target.value)] || null;
    state.draft = null;
    await loadGovernance();
  });
  byID("catalog-search-button").addEventListener("click", () => loadCatalog());
  byID("catalog-search").addEventListener("keydown", (event) => { if (event.key === "Enter") loadCatalog(); });
  byID("catalog-status-filter").addEventListener("change", () => {
    loadCatalog();
  });
  byID("catalog-more").addEventListener("click", () => loadCatalog(true));
  byID("query-metric-search").addEventListener("input", renderMetricOptions);
  for (const button of document.querySelectorAll("[data-tab]")) button.addEventListener("click", () => activateTab(button.dataset.tab));
  byID("metric-select").addEventListener("change", (event) => {
    if (state.editorDirty && !window.confirm("当前表单的编辑尚未应用。放弃这些编辑并切换指标？")) {
      event.target.value = state.creatingMetric ? "" : String(state.metricIndex);
      return;
    }
    state.metricIndex = Number(event.target.value);
    state.creatingMetric = false;
    renderMetricEditor(governanceMetrics()[state.metricIndex]);
  });
  byID("governance-metric-search").addEventListener("input", renderMetricInventory);
  byID("new-metric-button").addEventListener("click", newMetric);
  byID("duplicate-metric-button").addEventListener("click", duplicateMetric);
  byID("discard-editor-button").addEventListener("click", () => {
    if (!state.editorDirty || window.confirm("放弃这次尚未保存的修改？")) {
      if (state.creatingMetric) renderMetricSelect();
      else renderMetricEditor(governanceMetrics()[state.metricIndex]);
      renderMetricInventory();
    }
  });
  byID("metric-entity").addEventListener("change", (event) => {
    state.metricExpressionFilters = [];
    renderEntityFields(event.target.value);
    renderMetricDimensions(event.target.value);
    renderMetricExpressionFilters(false);
    refreshFormulaPreview();
  });
  byID("add-metric-filter-button").addEventListener("click", addMetricExpressionFilter);
  byID("metric-filter-operator").addEventListener("change", (event) => {
    const needsValues = metricFilterNeedsValues(event.target.value);
    byID("metric-filter-values").disabled = !needsValues || byID("metric-filter-operator").disabled;
    byID("metric-filter-values").placeholder = needsValues ? "多个值用英文逗号分隔" : "此判断不需要填写值";
  });
  byID("metric-form").addEventListener("submit", applyMetric);
  byID("metric-form").addEventListener("input", () => {
    state.editorDirty = true;
    refreshFormulaPreview();
    setStatus("editor-status", "有尚未保存的修改；保存后只进入草稿，不会影响线上版本。", "warning");
  });
  if (typeof window !== "undefined" && window.addEventListener) window.addEventListener("beforeunload", (event) => {
    if (state.editorDirty) {
      event.preventDefault();
      event.returnValue = "";
    }
  });
  byID("review-draft-button").addEventListener("click", reviewGovernance);
  byID("publish-button").addEventListener("click", publishDraft);
  byID("add-filter-button").addEventListener("click", () => {
    const dimension = byID("filter-dimension").value;
    const values = byID("filter-values").value.split(",").map((value) => value.trim()).filter(Boolean);
    if (!dimension || !values.length) {
      setStatus("query-status", "请选择筛选维度并填写至少一个值。", "error");
      return;
    }
    const operator = byID("filter-operator").value;
    state.filters.push({dimension, operator, values: operator === "eq" ? values.slice(0, 1) : values});
    byID("filter-values").value = "";
    renderFilters();
    updateQueryPreview();
  });
  for (const id of ["time-preset", "time-grain", "business-timezone", "row-limit", "time-start", "time-end"]) {
    byID(id).addEventListener("change", () => {
      byID("custom-time-range").hidden = byID("time-preset").value !== "custom";
      updateQueryPreview();
    });
  }
  byID("copy-query-button").addEventListener("click", copyQueryRequest);
  byID("run-query-button").addEventListener("click", () => runQueryOperation("query"));
  for (const button of document.querySelectorAll("[data-operation]")) button.addEventListener("click", () => runQueryOperation(button.dataset.operation));
  byID("cancel-job-button").addEventListener("click", async () => {
    if (!state.activeJobID) return;
    try {
      const snapshot = await requestJSON(`/api/v1/jobs/${escapePath(state.activeJobID)}/cancel`, {method: "POST"});
      renderQueryResult(snapshot);
    } catch (error) {
      setStatus("query-status", errorMessage(error), "error");
    }
  });
}

async function initialize() {
  bindEvents();
  byID("business-timezone").value = Intl.DateTimeFormat().resolvedOptions().timeZone || "Asia/Shanghai";
  try {
    state.context = await requestJSON("/api/v1/ui/context");
    byID("workspace-tabs").hidden = false;
    byID("session-name").textContent = state.context.display_name;
    byID("environment-badge").textContent = state.context.authentication_profile === "databricks_apps" ? "Databricks Apps" : "OIDC";
    byID("logout-button").hidden = state.context.authentication_profile !== "oidc";
    byID("governance-tab").hidden = !hasPermission("model:manage");
    byID("query-tab").hidden = !hasPermission("query:execute");
    initializeNamespaces();
    initializeGovernanceRoutes();
    if (hasPermission("query:execute")) {
      activateTab("catalog");
      await loadCatalog();
    } else if (hasPermission("model:manage")) {
      activateTab("governance");
      await loadGovernance();
    } else {
      setNotice("当前身份没有可用的平台权限。", "warning");
    }
  } catch (error) {
    byID("session-name").textContent = "尚未登录";
    byID("environment-badge").textContent = "未认证";
    byID("login-link").hidden = false;
    setNotice(errorMessage(error), "error");
  }
}

if (typeof module !== "undefined") {
  module.exports = {catalogStatusCounts, catalogVisibleTotal, catalogStatusLabel, catalogStatusShortLabel, compactDimensions, draftCatalogEntries, errorMessage, filterCatalogEntries, governanceCatalogEntries, groupPublicationIssues, humanOwner, humanTag, matchingQueryMetrics, matchesCatalogSearch, mergeCatalogEntries, metricCalculationNote, metricDetailQuery, metricExpressionLabel, metricFilterLabel, metricFormulaLabel, metricTimeDetail, planSummaryRows, preferredTimeGranularity, querySummaryRows, resolvePresetRange, sharedTimeMetadata, shiftDate, timezoneParts, verificationLabel, zonedMidnightISO, shellQuote};
}
if (typeof document !== "undefined") initialize();
