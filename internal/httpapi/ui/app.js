"use strict";

const byID = (id) => document.getElementById(id);
const show = (target, value) => { target.textContent = JSON.stringify(value, null, 2); };

byID("logout-button").addEventListener("click", async () => {
  await fetch("/auth/logout", {method: "POST", credentials: "same-origin"});
  window.location.assign("/");
});

async function requestJSON(path, options = {}) {
  const response = await fetch(path, {
    credentials: "same-origin",
    headers: {"Content-Type": "application/json"},
    ...options,
  });
  const body = await response.json();
  if (!response.ok) throw body;
  return body;
}

function modelPath(operation) {
  const namespace = encodeURIComponent(byID("namespace").value.trim());
  const model = encodeURIComponent(byID("model").value.trim());
  return `/api/v1/namespaces/${namespace}/models/${model}/${operation}`;
}

byID("search-button").addEventListener("click", async () => {
  const output = byID("catalog-output");
  try {
    const namespace = encodeURIComponent(byID("namespace").value.trim());
    const query = encodeURIComponent(byID("search").value.trim());
    show(output, await requestJSON(`/api/v1/catalog/search?namespace=${namespace}&q=${query}`));
  } catch (error) { show(output, error); }
});

for (const button of document.querySelectorAll("[data-operation]")) {
  button.addEventListener("click", async () => {
    const output = byID("query-output");
    try {
      const operation = button.dataset.operation;
      const body = JSON.parse(byID("query").value);
      const result = await requestJSON(modelPath(operation), {method: "POST", body: JSON.stringify(body)});
      show(output, result);
      if (operation === "query") pollJob(result.job.id, output);
    } catch (error) { show(output, error); }
  });
}

async function pollJob(id, output) {
  for (;;) {
    await new Promise((resolve) => setTimeout(resolve, 500));
    try {
      const result = await requestJSON(`/api/v1/jobs/${encodeURIComponent(id)}`);
      show(output, result);
      if (["succeeded", "failed", "cancelled"].includes(result.job.status)) return;
    } catch (error) { show(output, error); return; }
  }
}

for (const button of document.querySelectorAll("[data-management]")) {
  button.addEventListener("click", async () => {
    const output = byID("management-output");
    const operation = button.dataset.management;
    try {
      if (operation === "releases") {
        show(output, await requestJSON(modelPath("releases"), {headers: {}}));
        return;
      }
      const revision = Number(byID("revision").value);
      let body;
      let method = "POST";
      if (operation === "validate") body = {source: JSON.parse(byID("source").value)};
      if (operation === "draft") {
        method = "PUT";
        body = {expected_revision: revision, source: JSON.parse(byID("source").value)};
      }
      if (operation === "publish") body = {expected_revision: revision, note: "published from UI"};
      show(output, await requestJSON(modelPath(operation), {method, body: JSON.stringify(body)}));
    } catch (error) { show(output, error); }
  });
}
