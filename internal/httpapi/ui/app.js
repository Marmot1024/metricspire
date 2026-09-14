"use strict";

const byID = (id) => document.getElementById(id);
const state = {
  context: null,
  namespace: "",
  catalog: [],
  catalogView: [],
  catalogLoadSequence: 0,
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
  dirty: false,
  editorDirty: false,
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
      issues: definition.issues || [],
      governance_revision: record.revision,
      source_import_id: record.source_import_id,
      release_id: "",
    };
  });
}

function mergeCatalogEntries(published, drafts) {
  const publishedCodes = new Set(published.map(metricKey));
  return [
    ...published.map((metric) => ({...metric, catalog_status: "published"})),
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

function filterCatalogEntries(metrics, status = "queryable") {
  if (status === "all") return metrics;
  if (status === "governance") return metrics.filter((metric) => metric.catalog_status === "governance");
  return metrics.filter((metric) => metric.catalog_status === "published" || metric.catalog_status === "draft");
}

function isBusinessUnverified(metric) {
  return metric?.verification?.status === "unverified" || (metric?.tags || []).includes("governance_unverified");
}

function catalogStatusLabel(metric) {
  if (metric.catalog_status === "draft") return "未验证草稿";
  if (metric.catalog_status === "governance") return "待治理";
  if (metric.deprecated) return "已弃用";
  return isBusinessUnverified(metric) ? "已发布 · 业务待验证" : "已发布";
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

function matchesCatalogSearch(metric, search) {
  if (!search) return true;
  const text = [metric.name, metric.display_name, metric.description, metric.owner, ...(metric.tags || [])]
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
	return state.catalog.filter((metric) => state.queryMetricKeys.has(metricKey(metric)));
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
  state.selectedCatalogIndex = -1;
  state.queryMetricKeys.clear();
  byID("query-metric-search").value = "";
  state.filters = [];
  resetQueryFeedback();
  renderCatalog();
  updateQueryBuilder();
  state.draft = null;
  state.review = null;
  state.dirty = false;
  state.editorDirty = false;
  initializeGovernanceRoutes();
  await loadCatalog();
  if (!byID("governance-workspace").hidden && state.governanceRoute) await loadGovernance();
}

async function loadCatalog() {
  const loadSequence = ++state.catalogLoadSequence;
  const namespace = state.namespace;
  if (!state.namespace || (!hasPermission("query:execute") && !hasPermission("model:manage"))) {
    state.catalog = [];
    state.catalogView = [];
    renderCatalog();
    return;
  }
  const search = byID("catalog-search").value.trim();
  setNotice(hasPermission("model:manage") ? "正在读取已发布指标和待治理草稿…" : "正在读取当前业务域的已发布指标…");
  try {
    const publishedRequest = hasPermission("query:execute")
      ? requestJSON(`/api/v1/catalog/search?namespace=${escapePath(namespace)}&q=${encodeURIComponent(search)}&limit=100`)
      : Promise.resolve([]);
    const draftRequest = hasPermission("model:manage") ? loadDraftCatalog("", namespace) : Promise.resolve([]);
    const governanceRequest = hasPermission("model:manage")
      ? requestJSON(`/api/v1/namespaces/${escapePath(namespace)}/governance/metrics?q=${encodeURIComponent(search)}&limit=1000`)
      : Promise.resolve([]);
    const [published, drafts, governanceRecords] = await Promise.all([publishedRequest, draftRequest, governanceRequest]);
    if (loadSequence !== state.catalogLoadSequence || namespace !== state.namespace) return;
    const governed = governanceCatalogEntries(governanceRecords, drafts).filter((metric) => matchesCatalogSearch(metric, search));
    state.catalogView = mergeCatalogEntries(published, governed);
    state.catalog = state.catalogView.filter((metric) => metric.catalog_status !== "governance");
    state.queryMetricKeys = new Set([...state.queryMetricKeys].filter((key) => state.catalog.some((metric) => metricKey(metric) === key)));
    if (state.selectedCatalogIndex >= state.catalogView.length) state.selectedCatalogIndex = -1;
    renderCatalog();
    updateQueryBuilder();
    const counts = catalogStatusCounts(state.catalogView);
    setNotice(hasPermission("model:manage")
      ? `已载入 ${counts.published} 个已发布指标、${counts.draft} 个可试查草稿和 ${counts.governance} 个待治理指标。`
      : `已载入 ${published.length} 个已发布指标。选择指标查看口径与可用维度。`, "success");
  } catch (error) {
    if (loadSequence !== state.catalogLoadSequence || namespace !== state.namespace) return;
    state.catalog = [];
    state.catalogView = [];
    renderCatalog();
    updateQueryBuilder();
    setNotice(errorMessage(error), "error");
  }
}

async function loadDraftCatalog(search, namespace = state.namespace) {
  const routes = (state.context.models || []).filter((route) => route.namespace === namespace);
  const drafts = await Promise.all(routes.map(async (route) => {
    try {
      const draft = await requestJSON(routePath(route, "draft"));
      return draftCatalogEntries(route, draft);
    } catch (error) {
      if (error?.status === 404) return [];
      throw error;
    }
  }));
  return drafts.flat().filter((metric) => matchesCatalogSearch(metric, search));
}

function renderCatalog() {
  const counts = catalogStatusCounts(state.catalogView);
  const {published: publishedCount, draft: draftCount, governance: governanceCount} = counts;
  const statusFilter = byID("catalog-status-filter").value || "queryable";
  const visibleEntries = filterCatalogEntries(state.catalogView, statusFilter);
  byID("catalog-count").textContent = publishedCount || draftCount || governanceCount
    ? `显示 ${visibleEntries.length} / ${state.catalogView.length} · ${publishedCount} 已发布 · ${draftCount} 可试查 · ${governanceCount} 待治理`
    : `${state.catalogView.length} 个指标`;
  const list = byID("catalog-list");
  list.replaceChildren();
  if (!visibleEntries.length) {
    const empty = document.createElement("div");
    empty.className = "empty-state";
    empty.textContent = state.catalogView.length
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
    button.className = `catalog-item${index === state.selectedCatalogIndex ? " active" : ""}`;
    const heading = document.createElement("div");
    heading.className = "catalog-title";
    const title = document.createElement("strong");
    title.textContent = metric.display_name || metric.name;
    const code = document.createElement("code");
    code.textContent = metric.name;
    heading.append(title, code);
    const description = document.createElement("p");
    description.textContent = metric.description || "暂无业务定义";
    const meta = document.createElement("small");
    const status = catalogStatusLabel(metric);
    meta.textContent = [status, metric.owner && `负责人 ${metric.owner}`, humanType(metric.value_type, metric.unit)].filter(Boolean).join(" · ");
    button.append(heading, description, meta);
    button.addEventListener("click", () => {
      state.selectedCatalogIndex = index;
      renderCatalog();
      renderMetricDetail(metric);
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
  if (!metric) return;
  const draft = metric.catalog_status === "draft";
  const pending = metric.catalog_status === "governance";
  const businessUnverified = isBusinessUnverified(metric);
  byID("detail-status").textContent = catalogStatusLabel(metric);
  byID("detail-status").className = `badge${draft || pending || metric.deprecated || businessUnverified ? " warning" : ""}`;
  byID("detail-name").textContent = metric.display_name || metric.name;
  byID("detail-code").textContent = metric.name;
  byID("detail-description").textContent = metric.description || "暂无业务定义。";
  byID("detail-owner").textContent = metric.owner || "未指定";
  byID("detail-type").textContent = humanType(metric.value_type, metric.unit);
  byID("detail-release").textContent = draft ? "尚未发布 · 可试查" : pending ? "尚无安全查询定义" : metric.release_id;
  byID("detail-time").textContent = metric.time_dimension
    ? `${metric.time_dimension} · ${(metric.time_granularities || []).map(humanGrain).join("/") || "未配置粒度"}${metricTimeDetail(metric)?.calendar_timezone ? ` · ${metricTimeDetail(metric).calendar_timezone} 日历` : ""}`
    : "非时间限定指标";
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
  byID("detail-query-button").textContent = draft ? "草稿试查" : pending ? "暂不可查询" : "用于查询";
  byID("detail-query-button").disabled = pending || metric.deprecated || !hasPermission("query:execute") || (draft && !hasPermission("model:manage"));
  byID("detail-query-button").onclick = () => addMetricToQuery(metric);
}

function addMetricToQuery(metric) {
  if (metric.catalog_status === "governance") return;
  const selected = selectedQueryMetrics();
  const incompatible = selected.some((candidate) => candidate.catalog_status !== metric.catalog_status ||
    (metric.catalog_status === "draft" && candidate.semantic_model_name !== metric.semantic_model_name));
  if (incompatible) state.queryMetricKeys.clear();
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
    ? `已选择 ${selected.length} 个指标。输入名称或 code 可继续添加。`
    : "先从指标库带入指标，或在这里输入名称或 code 搜索。";
  if (!state.catalog.length) {
    const empty = document.createElement("div");
    empty.className = "empty-state";
    empty.textContent = "请先在指标库中找到可查询指标。";
    container.append(empty);
    return;
  }
  const visibleMetrics = matchingQueryMetrics(state.catalog, search, state.queryMetricKeys);
  if (!visibleMetrics.length) {
    const empty = document.createElement("div");
    empty.className = "empty-state";
    empty.textContent = search
      ? "没有找到匹配的可查询指标。请调整名称或 code。"
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
    return;
  }
  select.disabled = false;
  state.metricIndex = Math.min(Math.max(preferredIndex, 0), governanceMetrics().length - 1);
  select.value = String(state.metricIndex);
  state.creatingMetric = false;
  renderMetricEditor(governanceMetrics()[state.metricIndex]);
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
  for (const id of ["metric-code", "metric-display-name", "metric-description", "metric-owner", "metric-unit", "metric-tags", "metric-examples", "metric-evidence"]) byID(id).value = "";
  byID("metric-verified").checked = false;
  byID("metric-deprecated").checked = false;
}

function renderMetricEditor(metric) {
  state.editorDirty = false;
  byID("editor-status").hidden = true;
  const aggregate = !metric || metric.kind === "aggregate";
  byID("editor-mode-note").textContent = state.creatingMetric
    ? "新增基础指标：只接受结构化聚合口径，不接受 SQL 片段。保存草稿前不会影响线上版本。"
    : aggregate
      ? "此表单锁定已有指标的计算口径；可维护名称、说明、负责人、标签、验证证据与弃用状态。"
      : `这是 ${metric.kind} 指标。当前界面只维护元数据；复合公式仍由版本化契约管理。`;
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
    ? "当前勾选只表示 staging 技术试查证据已登记；业务口径仍待确认。测试发布不等于业务验证通过。"
    : "";
  const entities = state.draft.source.spec.entities.map((entity) => entity.name);
  fillSelect(byID("metric-entity"), entities, metric?.entity || "", "没有可用实体");
  byID("metric-operation").value = metric?.expression?.op || "sum";
  renderEntityFields(byID("metric-entity").value, metric?.expression?.field || "");
  byID("metric-value-type").value = metric?.value_type || "decimal";
  renderMetricDimensions(byID("metric-entity").value, metric?.allowed_dimensions || [], metric?.time_dimension || "");
  const executionLocked = !state.creatingMetric || !aggregate;
  for (const id of ["metric-code", "metric-entity", "metric-operation", "metric-field", "metric-value-type", "metric-unit"]) byID(id).disabled = executionLocked;
  for (const input of document.querySelectorAll("input[name='metric-dimension']")) input.disabled = executionLocked;
  byID("metric-time-dimension").disabled = executionLocked;
  byID("execution-lock-note").textContent = executionLocked
    ? "为防止同一 code 静默改义，执行实体、公式、类型、单位和维度契约只能通过新增指标或显式弃用演进。"
    : "系统将保存结构化表达式，并在发布前编译、检查绑定和影响范围。";
}

function newMetric() {
  if (!state.draft) return;
  if (state.editorDirty && !window.confirm("当前表单的编辑尚未应用。放弃这些编辑并新增指标？")) return;
  state.creatingMetric = true;
  state.metricIndex = -1;
  byID("metric-select").value = "";
  renderMetricEditor(null);
}

function metricFromEditor() {
  const editable = {
    name: byID("metric-code").value.trim(),
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
  if (!editable.name || !editable.display_name || !editable.description || !editable.owner) throw new Error("指标 code、展示名称、业务定义和负责人都是必填项。");
  if (!/^[a-z][a-z0-9_]*$/.test(editable.name)) throw new Error("指标 code 只能使用小写字母、数字和下划线，并以字母开头。");
  if (editable.usage_examples.length > 5) throw new Error("每个指标最多维护 5 条常见使用示例。");
  if (!state.creatingMetric) {
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
    expression: {op: byID("metric-operation").value, field},
    allowed_dimensions: [...document.querySelectorAll("input[name='metric-dimension']:checked")].map((input) => input.value),
    time_dimension: byID("metric-time-dimension").value,
  };
}

async function applyMetric(event) {
  event.preventDefault();
  try {
    const metric = metricFromEditor();
    let index = state.metricIndex;
    if (state.creatingMetric) {
      if (governanceMetrics().some((candidate) => candidate.name === metric.name)) throw new Error(`指标 code ${metric.name} 已存在。`);
      state.draft.source.spec.metrics.push(metric);
      index = governanceMetrics().length - 1;
    } else {
      state.draft.source.spec.metrics[index] = metric;
    }
    state.creatingMetric = false;
    state.editorDirty = false;
    state.dirty = true;
    renderMetricSelect(index);
    await reviewGovernance();
    setStatus("editor-status", state.review
      ? "编辑已应用。请查看检查结果，修正问题后保存草稿。"
      : "编辑已应用，但未取得检查结果。请重新检查后再保存。", state.review ? "success" : "warning");
    setStatus("governance-status", "变更已应用到浏览器中的当前草稿；点击“保存草稿”后才会写入控制面。", "warning");
  } catch (error) {
    setStatus("editor-status", errorMessage(error), "error");
    setStatus("governance-status", errorMessage(error), "error");
  }
}

function confirmDiscardDraft() {
  return (!state.dirty && !state.editorDirty) || window.confirm("存在尚未保存的修改。放弃本页修改并切换？已保存的草稿与线上版本不会改变。");
}

function requireAppliedEditor() {
  if (!state.editorDirty) return true;
  const message = "表单编辑尚未应用。请先点击“应用编辑并检查”，再保存草稿。";
  setStatus("editor-status", message, "warning");
  setStatus("governance-status", message, "warning");
  return false;
}

function renderReview() {
  const review = state.review;
  if (!review) return;
  const changes = review.metric_changes || [];
  const breaking = changes.filter((change) => change.breaking);
  const issues = review.publication_issues || [];
  byID("active-release-value").textContent = review.active_release?.id || "尚未发布";
  byID("active-release-meta").textContent = review.active_release
    ? `revision ${review.active_release.source_revision} · ${review.active_release.manifest_fingerprint.slice(0, 18)}…`
    : "首次发布候选";
  byID("binding-status-value").textContent = review.binding.status === "ready" ? "完整" : review.binding.status === "missing" ? "未配置" : "需处理";
  byID("binding-status-meta").textContent = review.binding.message;
  setStatus("review-status", issues.length
    ? `发布前还需修正 ${issues.length} 项指标信息。`
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
  for (const issue of issues) {
    const card = document.createElement("article");
    card.className = "change-card";
    const title = document.createElement("strong");
    title.textContent = "发布信息不完整";
    const meta = document.createElement("small");
    meta.textContent = issue;
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
  byID("publish-button").disabled = state.dirty || issues.length > 0 || review.structure_changed || breaking.length > 0 ||
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
    meta.textContent = [`revision ${release.source_revision}`, release.created_by, created, release.note].filter(Boolean).join(" · ");
    const rollback = document.createElement("button");
    rollback.type = "button";
    rollback.className = "button danger";
    rollback.textContent = "回滚到此版本";
    rollback.disabled = Boolean(release.active);
    rollback.addEventListener("click", () => rollbackRelease(release.id));
    card.append(heading, meta, rollback);
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
    state.dirty = false;
    byID("draft-revision-value").textContent = `revision ${draft.revision}`;
    byID("draft-updated-meta").textContent = [draft.updated_by, draft.updated_at && new Date(draft.updated_at).toLocaleString("zh-CN")].filter(Boolean).join(" · ");
    renderMetricSelect();
    renderReleases();
    await reviewGovernance();
    setStatus("governance-status", "维护上下文已载入。线上查询继续只读取当前已发布版本。", "success");
  } catch (error) {
    state.draft = null;
    setStatus("governance-status", errorMessage(error), "error");
  }
}

async function saveDraft() {
  if (!state.draft) return;
  if (!requireAppliedEditor()) return;
  try {
    setStatus("governance-status", "正在校验并保存草稿…");
    const saved = await requestJSON(routePath(state.governanceRoute, "draft"), {
      method: "PUT",
      body: JSON.stringify({expected_revision: state.draft.revision, source: state.draft.source}),
    });
    state.draft = saved;
    state.dirty = false;
    byID("draft-revision-value").textContent = `revision ${saved.revision}`;
    byID("draft-updated-meta").textContent = [saved.updated_by, saved.updated_at && new Date(saved.updated_at).toLocaleString("zh-CN")].filter(Boolean).join(" · ");
    await reviewGovernance();
    setStatus("governance-status", `草稿 revision ${saved.revision} 已保存；当前线上版本未改变。`, "success");
  } catch (error) {
    setStatus("governance-status", errorMessage(error), "error");
  }
}

async function publishDraft() {
  if (!state.draft) return;
  if (!requireAppliedEditor()) return;
  if (state.dirty) {
    setStatus("governance-status", "当前修改尚未保存。请先保存草稿，检查通过后再发布。", "warning");
    return;
  }
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
  byID("catalog-search-button").addEventListener("click", loadCatalog);
  byID("catalog-search").addEventListener("keydown", (event) => { if (event.key === "Enter") loadCatalog(); });
  byID("catalog-status-filter").addEventListener("change", () => {
    state.selectedCatalogIndex = -1;
    renderCatalog();
  });
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
  byID("new-metric-button").addEventListener("click", newMetric);
  byID("metric-entity").addEventListener("change", (event) => {
    renderEntityFields(event.target.value);
    renderMetricDimensions(event.target.value);
  });
  byID("metric-form").addEventListener("submit", applyMetric);
  byID("metric-form").addEventListener("input", () => {
    state.editorDirty = true;
    setStatus("editor-status", "有尚未应用的编辑，请先“应用编辑并检查”。", "warning");
  });
  if (typeof window !== "undefined" && window.addEventListener) window.addEventListener("beforeunload", (event) => {
    if (state.dirty || state.editorDirty) {
      event.preventDefault();
      event.returnValue = "";
    }
  });
  byID("review-draft-button").addEventListener("click", reviewGovernance);
  byID("save-draft-button").addEventListener("click", saveDraft);
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
  module.exports = {catalogStatusCounts, catalogStatusLabel, draftCatalogEntries, errorMessage, filterCatalogEntries, governanceCatalogEntries, humanTag, matchingQueryMetrics, matchesCatalogSearch, mergeCatalogEntries, metricTimeDetail, planSummaryRows, preferredTimeGranularity, querySummaryRows, resolvePresetRange, sharedTimeMetadata, shiftDate, timezoneParts, verificationLabel, zonedMidnightISO, shellQuote};
}
if (typeof document !== "undefined") initialize();
