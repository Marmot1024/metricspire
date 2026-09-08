"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const fs = require("node:fs");
const vm = require("node:vm");
const {resolvePresetRange, zonedMidnightISO, shellQuote} = require("./app.js");
const {execFileSync} = require("node:child_process");

test("copied request preserves apostrophes and shell characters as literal JSON", () => {
  const payload = JSON.stringify({values: ["O'Reilly", "$(echo unintended)", "`echo unintended`", "$PATH", "a\nb"]});
  assert.equal(execFileSync("/bin/sh", ["-c", "printf %s " + shellQuote(payload)], {encoding: "utf8"}), payload);
});

// Exercise the actual form handlers without adding a production DOM dependency.
function editorContext() {
  const context = vm.createContext({});
  vm.runInContext(fs.readFileSync(require.resolve("./app.js"), "utf8"), context);
  const element = () => ({value: "", checked: false, dataset: {}, children: [], handlers: {},
    addEventListener(name, handler) { this.handlers[name] = handler; },
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

test("new metric form submits an entity-qualified expression, not a physical dataset", () => {
  const {context, run} = editorContext();
  run("renderEntityFields('orders', 'orders.amount')");
  const values = {"metric-code": "revenue", "metric-display-name": "收入",
    "metric-description": "订单金额合计", "metric-owner": "data-team",
    "metric-entity": "orders", "metric-operation": "sum", "metric-value-type": "decimal"};
  for (const [id, value] of Object.entries(values)) context.document.getElementById(id).value = value;
  const metric = run("metricFromEditor()");
  assert.equal(metric.expression.field, "orders.amount");
  assert.equal(metric.entity, "orders");
  assert.equal(metric.expression.op, "sum");
  assert.equal(context.document.getElementById("metric-field").value, "orders.amount");
});

test("editing an existing metric preserves its selected field and related dimensions", () => {
  const {context, run} = editorContext();
  run("renderEntityFields('orders', 'orders.amount'); renderMetricDimensions('orders', ['status', 'segment'])");
  assert.equal(context.document.getElementById("metric-field").value, "orders.amount");
  const inputs = context.document.getElementById("metric-dimension-options").children.map((label) => label.children[0]);
  assert.deepEqual(inputs.map((input) => [input.value, input.checked]), [["status", true], ["segment", true]]);
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

test("unapplied form edits cannot silently save or publish the old draft", async () => {
  const {context, run} = editorContext();
  let requests = 0;
  context.requestJSON = async () => { requests++; };
  run("bindEvents()");
  context.document.getElementById("metric-form").handlers.input();
  await run("saveDraft(); publishDraft()");
  assert.equal(requests, 0);
  assert.match(context.document.getElementById("editor-status").textContent, /尚未应用/);
  run("state.editorDirty = false; state.dirty = true");
  await run("publishDraft()");
  assert.equal(requests, 0);
  assert.match(context.document.getElementById("governance-status").textContent, /先保存草稿/);
});

test("cancelled navigation preserves unsaved draft and business domain", async () => {
  const {context, run} = editorContext();
  context.window = {confirm: () => false};
  run("state.namespace = 'original'; state.dirty = true");
  const original = run("state.draft");
  await run("switchNamespace('other')");
  assert.equal(run("state.draft"), original);
  assert.equal(run("state.namespace"), "original");
  assert.equal(context.document.getElementById("namespace-select").value, "original");
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
