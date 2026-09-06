"use strict";

const byID = (id) => document.getElementById(id);
const state = {
  context: null,
  namespace: "",
  catalog: [],
  selectedCatalogIndex: -1,
  queryMetricKeys: new Set(),
  filters: [],
  activeJobID: null,
  governanceRoute: null,
  draft: null,
  review: null,
  releases: [],
  metricIndex: -1,
  creatingMetric: false,
  dirty: false,
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

function errorMessage(error) {
  if (error && error.detail) {
    return `${error.detail}${error.path ? `（${error.path}）` : ""}${error.request_id ? ` · 请求 ${error.request_id}` : ""}`;
  }
  return error instanceof Error ? error.message : "请求失败，请稍后重试。";
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

function selectedQueryMetrics() {
  return state.catalog.filter((metric) => state.queryMetricKeys.has(metricKey(metric)));
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
  state.namespace = namespace;
  byID("current-namespace").textContent = namespace || "没有配置业务域";
  state.catalog = [];
  state.selectedCatalogIndex = -1;
  state.queryMetricKeys.clear();
  state.filters = [];
  state.draft = null;
  state.review = null;
  initializeGovernanceRoutes();
  await loadCatalog();
  if (!byID("governance-workspace").hidden && state.governanceRoute) await loadGovernance();
}

async function loadCatalog() {
  if (!state.namespace || !hasPermission("query:execute")) {
    state.catalog = [];
    renderCatalog();
    return;
  }
  const search = byID("catalog-search").value.trim();
  setNotice("正在读取当前业务域的已发布指标…");
  try {
    state.catalog = await requestJSON(`/api/v1/catalog/search?namespace=${escapePath(state.namespace)}&q=${encodeURIComponent(search)}&limit=100`);
    state.queryMetricKeys = new Set([...state.queryMetricKeys].filter((key) => state.catalog.some((metric) => metricKey(metric) === key)));
    if (state.selectedCatalogIndex >= state.catalog.length) state.selectedCatalogIndex = -1;
    renderCatalog();
    updateQueryBuilder();
    setNotice(`已载入 ${state.catalog.length} 个可查询指标。物理模型与数据位置由平台内部解析。`, "success");
  } catch (error) {
    state.catalog = [];
    renderCatalog();
    updateQueryBuilder();
    setNotice(errorMessage(error), "error");
  }
}

function renderCatalog() {
  byID("catalog-count").textContent = `${state.catalog.length} 个指标`;
  const list = byID("catalog-list");
  list.replaceChildren();
  if (!state.catalog.length) {
    const empty = document.createElement("div");
    empty.className = "empty-state";
    empty.textContent = "当前业务域没有匹配且有权查询的已发布指标。";
    list.append(empty);
    renderMetricDetail(null);
    return;
  }
  state.catalog.forEach((metric, index) => {
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
    meta.textContent = [metric.owner && `负责人 ${metric.owner}`, humanType(metric.value_type, metric.unit)].filter(Boolean).join(" · ");
    button.append(heading, description, meta);
    button.addEventListener("click", () => {
      state.selectedCatalogIndex = index;
      renderCatalog();
      renderMetricDetail(metric);
    });
    list.append(button);
  });
  if (state.selectedCatalogIndex < 0) {
    state.selectedCatalogIndex = 0;
    renderMetricDetail(state.catalog[0]);
    list.firstElementChild?.classList.add("active");
  } else {
    renderMetricDetail(state.catalog[state.selectedCatalogIndex]);
  }
}

function renderMetricDetail(metric) {
  byID("metric-detail-empty").hidden = Boolean(metric);
  byID("metric-detail-content").hidden = !metric;
  if (!metric) return;
  byID("detail-status").textContent = metric.deprecated ? "已弃用" : "已发布";
  byID("detail-status").className = `badge${metric.deprecated ? " warning" : ""}`;
  byID("detail-name").textContent = metric.display_name || metric.name;
  byID("detail-code").textContent = metric.name;
  byID("detail-description").textContent = metric.description || "暂无业务定义。";
  byID("detail-owner").textContent = metric.owner || "未指定";
  byID("detail-type").textContent = humanType(metric.value_type, metric.unit);
  byID("detail-release").textContent = metric.release_id;
  byID("detail-time").textContent = metric.time_dimension
    ? `${metric.time_dimension} · ${(metric.time_granularities || []).map(humanGrain).join("/") || "未配置粒度"}`
    : "非时间限定指标";
  const dimensions = byID("detail-dimensions");
  dimensions.replaceChildren();
  for (const dimension of metric.allowed_dimensions) appendChip(dimensions, dimension);
  if (!metric.allowed_dimensions.length) appendChip(dimensions, "无分组维度");
  const tags = byID("detail-tags");
  tags.replaceChildren();
  for (const tag of metric.tags || []) appendChip(tags, tag);
  if (!(metric.tags || []).length) appendChip(tags, "未设置标签");
  const examples = byID("detail-examples");
  examples.replaceChildren();
  for (const example of metric.usage_examples || []) {
    const item = document.createElement("p");
    item.textContent = example;
    examples.append(item);
  }
  if (!(metric.usage_examples || []).length) examples.textContent = "维护者尚未提供常见使用示例。";
  byID("detail-query-button").disabled = metric.deprecated || !hasPermission("query:execute");
  byID("detail-query-button").onclick = () => addMetricToQuery(metric);
}

function addMetricToQuery(metric) {
  state.queryMetricKeys.add(metricKey(metric));
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
  return {
    dimension,
    granularities: (timed[0].time_granularities || []).filter((grain) => timed.every((metric) => (metric.time_granularities || []).includes(grain))),
  };
}

function renderMetricOptions() {
  const container = byID("query-metric-options");
  container.replaceChildren();
  if (!state.catalog.length) {
    const empty = document.createElement("div");
    empty.className = "empty-state";
    empty.textContent = "请先在指标库中找到可查询指标。";
    container.append(empty);
    return;
  }
  for (const metric of state.catalog) {
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
    meta.textContent = metric.name;
    text.append(name, meta);
    label.append(input, text);
    container.append(label);
  }
}

function renderDimensionOptions(dimensions) {
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
    input.addEventListener("change", updateQueryPreview);
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
  preset.disabled = !enabled;
  grain.disabled = !enabled;
  if (!enabled) {
    preset.value = "none";
    grain.value = "";
  }
  for (const option of grain.options) option.disabled = Boolean(option.value && !time.granularities.includes(option.value));
  if (grain.value && !time.granularities.includes(grain.value)) grain.value = "";
  byID("custom-time-range").hidden = preset.value !== "custom";
}

function updateQueryBuilder() {
  const metrics = selectedQueryMetrics();
  const dimensions = sharedDimensions(metrics);
  renderMetricOptions();
  renderDimensionOptions(dimensions);
  renderFilterDimensionOptions(dimensions);
  configureTimeControls(sharedTimeMetadata(metrics));
  const examples = metrics.flatMap((metric) => (metric.usage_examples || []).map((example) => `${metric.display_name || metric.name}：${example}`));
  byID("query-examples").textContent = examples.length
    ? `常见使用：${examples.slice(0, 5).join("；")}`
    : "所选指标尚无维护者示例；可直接组合维度、筛选和明确时间范围。";
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
  const preset = byID("time-preset").value;
  const timezone = byID("business-timezone").value.trim();
  if (time.dimension && preset === "none") throw new Error("时间口径指标必须选择明确时间范围。");
  if (preset !== "none") {
    if (!time.dimension) throw new Error("所选指标没有共同的时间口径，不能应用时间预设。");
    const range = resolvePresetRange(preset, timezone);
    query.time_range = {dimension: time.dimension, start: range.start, end: range.end, timezone};
  }
  const grain = byID("time-grain").value;
  if (grain) {
    if (!query.time_range) throw new Error("按时间分组前必须选择时间范围。");
    if (!query.group_by.includes(time.dimension)) query.group_by.push(time.dimension);
    query.time_grouping = {dimension: time.dimension, timezone, granularity: grain};
    if (grain === "week") query.time_grouping.week_start = "monday";
  }
  return {query};
}

function updateQueryPreview() {
  try {
    const built = buildQuery();
    byID("query-request-preview").textContent = JSON.stringify(built.query, null, 2);
    byID("resolved-time").textContent = built.query.time_range
      ? `实际请求范围：[${built.query.time_range.start}, ${built.query.time_range.end})`
      : selectedQueryMetrics().some((metric) => metric.time_dimension)
        ? "请选择一个明确时间范围；时间口径指标不允许隐式全量查询。"
        : "所选指标不要求时间范围。";
  } catch (error) {
    byID("query-request-preview").textContent = errorMessage(error);
    byID("resolved-time").textContent = errorMessage(error);
  }
}

function shellQuote(value) {
  return "'" + value.replaceAll("'", "'\"'\"'") + "'";
}

async function copyQueryRequest() {
  try {
    const {query} = buildQuery();
    const payload = [
      `curl --request POST ${shellQuote(window.location.origin + publicQueryPath("query"))}`,
      '--header "Authorization: Bearer ${METRICSPIRE_TOKEN}"',
      "--header 'Content-Type: application/json'",
      `--data ${shellQuote(JSON.stringify(query))}`,
    ].join(" \\\n  ");
    if (navigator.clipboard?.writeText) await navigator.clipboard.writeText(payload);
    else window.prompt("复制下面的 API 请求", payload);
    setStatus("query-status", "已复制规范 API 请求。运行身份仍由平台登录态提供，不写入正文。", "success");
  } catch (error) {
    setStatus("query-status", errorMessage(error), "error");
  }
}

function renderQueryResult(snapshot) {
  byID("query-output").textContent = JSON.stringify(snapshot, null, 2);
  byID("result-panel").hidden = false;
  const job = snapshot.job || {};
  const result = snapshot.result;
  if (!result) {
    setStatus("query-status", job.error?.message || (job.status ? `查询状态：${job.status}` : "查询已提交。"), job.status === "failed" ? "error" : "neutral");
    return;
  }
  const columns = result.columns || [];
  const rows = result.rows || [];
  const range = snapshot.resolved_time_range;
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
  try {
    const {query} = buildQuery();
    setStatus("query-status", operation === "query" ? "正在提交受控查询…" : "正在生成开发者检查结果…");
    const result = await requestJSON(publicQueryPath(operation), {method: "POST", body: JSON.stringify(query)});
    if (operation !== "query") {
      byID("query-output").textContent = JSON.stringify(result, null, 2);
      byID("result-panel").hidden = false;
      byID("result-table").replaceChildren();
      byID("result-meta").textContent = operation === "explain" ? "语义解释" : "执行计划";
      setStatus("query-status", `${operation === "explain" ? "语义解释" : "执行计划"}已生成。`, "success");
      return;
    }
    renderQueryResult(result);
    if (result.job?.id) {
      state.activeJobID = result.job.id;
      byID("cancel-job-button").disabled = false;
      await pollJob(result.job.id);
    }
  } catch (error) {
    byID("query-output").textContent = JSON.stringify(error, null, 2);
    byID("result-panel").hidden = false;
    setStatus("query-status", errorMessage(error), "error");
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
  const aggregate = !metric || metric.kind === "aggregate";
  byID("editor-mode-note").textContent = state.creatingMetric
    ? "新增基础指标：只接受结构化聚合口径，不接受 SQL 片段。保存草稿前不会影响线上版本。"
    : aggregate
      ? "已发布指标的执行口径已锁定；可安全维护名称、说明、负责人、标签、验证证据与弃用状态。"
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
  byID("metric-deprecated").checked = Boolean(metric?.deprecated);
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
    state.dirty = true;
    renderMetricSelect(index);
    await reviewGovernance();
    setStatus("governance-status", "变更已应用到浏览器中的当前草稿；点击“保存草稿”后才会写入控制面。", "warning");
  } catch (error) {
    setStatus("governance-status", errorMessage(error), "error");
  }
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
    state.governanceRoute = (state.context.models || []).filter((route) => route.namespace === state.namespace)[Number(event.target.value)] || null;
    state.draft = null;
    await loadGovernance();
  });
  byID("catalog-search-button").addEventListener("click", loadCatalog);
  byID("catalog-search").addEventListener("keydown", (event) => { if (event.key === "Enter") loadCatalog(); });
  for (const button of document.querySelectorAll("[data-tab]")) button.addEventListener("click", () => activateTab(button.dataset.tab));
  byID("metric-select").addEventListener("change", (event) => {
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
  module.exports = {resolvePresetRange, shiftDate, timezoneParts, zonedMidnightISO, shellQuote};
}
if (typeof document !== "undefined") initialize();
