// promui single-page logic (vanilla JS, no build step).
// The file editor lives in a <dialog> modal instead of a separate page.
"use strict";

const $ = (sel) => document.querySelector(sel);

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") node.className = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else node.setAttribute(k, v);
  }
  for (const c of children) {
    if (c == null) continue;
    node.append(c);
  }
  return node;
}

async function getJSON(url) {
  const resp = await fetch(url);
  if (!resp.ok) throw new Error(`${url}: HTTP ${resp.status}`);
  return resp.json();
}

function showJobsError(msg) {
  $("#config-error").textContent = msg;
  $("#config-error").classList.remove("hidden");
}

// Every file link opens the editor modal (SPA, no per-file routes).
function fileLink(name) {
  return el("a", { href: "#", "data-file": name, onclick: (ev) => { ev.preventDefault(); openEditor(name); } }, name);
}

function renderJob(job) {
  const globs = (job.fileSdConfigs || [])
    .flatMap((c) => c.files || [])
    .join(", ") || "no file_sd_configs";
  const meta = [
    job.scrapeInterval ? `interval ${job.scrapeInterval}` : "",
    job.metricsPath ? `path ${job.metricsPath}` : "",
    job.scheme ? `scheme ${job.scheme}` : "",
  ].filter(Boolean).join(" · ");
  const ul = el("ul");
  const files = job.files || [];
  for (const f of files) {
    const li = el("li", {}, fileLink(f.name));
    if (f.jobs && f.jobs.length > 1) {
      li.append(el("span", { class: "shared-tag", title: `also read by: ${f.jobs.join(", ")}` }, `shared (${f.jobs.length} jobs)`));
    }
    ul.append(li);
  }
  if (!files.length) ul.append(el("li", {}, "(no matching files yet)"));
  return el("div", { class: "card" },
    el("h3", {}, job.name),
    el("p", { class: "meta" }, meta),
    ul,
    el("p", { class: "globs" }, `globs: ${globs}`));
}

async function loadConfig() {
  let cfg;
  try {
    cfg = await getJSON("/api/config");
  } catch (e) {
    showJobsError(`Failed to load config: ${e.message}`);
    return;
  }
  if (cfg.configError) {
    showJobsError(`Prometheus config unavailable: ${cfg.configError}`);
  }
  $("#files-sd-dir").textContent = cfg.filesSdDir;
  const jobs = $("#jobs");
  jobs.replaceChildren();
  const datalist = $("#job-names");
  datalist.replaceChildren();
  const jobSelect = $("#create-job-select");
  const prevJob = jobSelect.value;
  jobSelect.replaceChildren(
    el("option", { value: "" }, "— select a job —"),
    el("option", { value: "__custom__" }, "— new job name… —"),
  );
  for (const job of cfg.jobs || []) {
    jobs.append(renderJob(job));
    datalist.append(el("option", { value: job.name }));
    jobSelect.append(el("option", { value: job.name }, job.name));
  }
  if (prevJob && [...jobSelect.options].some((o) => o.value === prevJob)) {
    jobSelect.value = prevJob;
  }
  if (!cfg.jobs || !cfg.jobs.length) {
    jobs.append(el("p", { class: "hint" }, "No scrape jobs found in the Prometheus configuration."));
  }
  const others = $("#others");
  others.replaceChildren();
  if (cfg.others && cfg.others.length) {
    $("#others-section").classList.remove("hidden");
    for (const name of cfg.others) others.append(el("li", {}, fileLink(name)));
  } else {
    $("#others-section").classList.add("hidden");
  }
}

// File picker: filled from /api/files (the directory itself), so it works
// even when the Prometheus API is unreachable.
async function loadFiles() {
  const editSelect = $("#edit-file");
  let files = [];
  try {
    files = (await getJSON("/api/files")).files || [];
  } catch (e) {
    editSelect.replaceChildren(
      el("option", { value: "" }, `failed to list files: ${e.message}`));
    return;
  }
  const prev = editSelect.value;
  editSelect.replaceChildren(
    el("option", { value: "" }, files.length ? "— select a file —" : "— no files yet —"));
  for (const name of [...files].sort()) {
    editSelect.append(el("option", { value: name }, name));
  }
  if (prev && [...editSelect.options].some((o) => o.value === prev)) {
    editSelect.value = prev;
  }
}

function renderTarget(t) {
  const health = el("span", { class: `health-${t.health}` }, t.health);
  return el("tr", {},
    el("td", {}, el("a", { href: t.scrapeUrl, target: "_blank", rel: "noopener" }, t.scrapeUrl)),
    el("td", {}, t.labels ? (t.labels.job || t.scrapePool) : t.scrapePool),
    el("td", {}, health),
    el("td", { class: t.lastError ? "err" : "" }, t.lastError || ""),
    el("td", {}, t.lastScrape ? new Date(t.lastScrape).toLocaleString() : ""));
}

async function loadTargets() {
  let data;
  try {
    data = await getJSON("/api/targets");
  } catch (e) {
    data = { error: `Failed to load targets: ${e.message}` };
  }
  if (data.error) {
    $("#targets-error").textContent = data.error;
    $("#targets-error").classList.remove("hidden");
    return;
  }
  $("#targets-error").classList.add("hidden");
  const body = $("#targets-body");
  body.replaceChildren();
  for (const t of data.targets || []) body.append(renderTarget(t));
  if (!data.targets || !data.targets.length) {
    body.append(el("tr", {}, el("td", { colspan: "5" }, "No active targets.")));
  }
}

// ---- editor modal ----

// special label hints for the autocomplete list
const SPECIAL_LABELS = {
  "__metrics_path__": "Overrides the metrics path of the scrape (must start with /).",
  "__scheme__": "http or https.",
  "__scrape_interval__": "Per-group scrape interval, e.g. 30s, 5m, 1h30m.",
  "__scrape_timeout__": "Per-group scrape timeout; must not exceed the interval.",
};

function labelKeyList() {
  const dl = document.createElement("datalist");
  dl.id = "label-keys";
  for (const [k, desc] of Object.entries(SPECIAL_LABELS)) {
    dl.append(el("option", { value: k }, desc));
  }
  dl.append(el("option", { value: "__param_" }, "Sets URL query parameter <name>."));
  document.body.append(dl);
}

const editorModal = $("#editor-modal");
let editorFile = null; // name of the file being edited
let groups = [];

function renderGroups() {
  const box = $("#groups");
  box.replaceChildren();
  groups.forEach((g, i) => box.append(renderGroup(g, i)));
}

function renderGroup(group, idx) {
  const targetsArea = el("textarea", { placeholder: "host:port per line, e.g. node1.example.com:9100" });
  targetsArea.value = (group.targets || []).join("\n");
  targetsArea.addEventListener("input", () => {
    group.targets = targetsArea.value.split(/\r?\n/).map((s) => s.trim()).filter(Boolean);
  });

  const labelsBox = el("div");
  const renderLabels = () => {
    labelsBox.replaceChildren();
    const entries = Object.entries(group.labels || {});
    entries.forEach(([k, v]) => {
      const keyInput = el("input", { type: "text", list: "label-keys", title: SPECIAL_LABELS[k] || "", value: k });
      const valInput = el("input", { type: "text", value: v });
      const row = el("div", { class: "labels-row" }, keyInput, valInput,
        el("button", { class: "secondary", type: "button", onclick: () => { delete group.labels[k]; renderLabels(); } }, "✕"));
      keyInput.addEventListener("input", () => {
        const newKey = keyInput.value.trim();
        if (newKey && newKey !== k) {
          const labels = group.labels || {};
          delete labels[k];
          labels[newKey] = valInput.value;
          group.labels = labels;
          k = newKey;
        }
        keyInput.title = SPECIAL_LABELS[k] || "";
      });
      valInput.addEventListener("input", () => {
        if (group.labels) group.labels[k] = valInput.value;
      });
      labelsBox.append(row);
    });
    labelsBox.append(el("button", { class: "secondary", type: "button", onclick: () => {
      group.labels = group.labels || {};
      group.labels[""] = "";
      renderLabels();
    } }, "+ Add label"));
  };
  renderLabels();

  return el("div", { class: "group" },
    el("div", { class: "group-head" },
      el("h3", {}, `Group ${idx + 1}`),
      el("button", { class: "danger", type: "button", onclick: () => { groups.splice(idx, 1); renderGroups(); } }, "Remove group")),
    el("label", {}, "Targets (one per line)", targetsArea),
    el("label", {}, "Labels", labelsBox));
}

function showMessage(kind, text) {
  const box = $("#message");
  box.className = kind;
  box.textContent = text;
}

async function openEditor(name, { keepMessage = false } = {}) {
  editorFile = name;
  $("#editor-title").textContent = `Edit ${name}`;
  if (!keepMessage) showMessage("hidden", "");
  $("#groups").replaceChildren();
  editorModal.showModal();
  const resp = await fetch(`/api/files/${encodeURIComponent(name)}`);
  if (!resp.ok) {
    const data = await resp.json().catch(() => ({}));
    showMessage("error", data.error || `HTTP ${resp.status}`);
    return;
  }
  const data = await resp.json();
  groups = (data.groups || []).map((g) => ({ targets: g.targets || [], labels: g.labels || {} }));
  renderGroups();
}

function serverErrorsText(errors) {
  return errors
    .map((e) => {
      const msg = e.Message || e.message || String(e);
      const gi = e.GroupIndex;
      return gi === undefined ? msg : `Group ${gi + 1} (${e.Field || "group"}): ${msg}`;
    })
    .join("; ");
}

$("#add-group").addEventListener("click", () => {
  groups.push({ targets: [], labels: {} });
  renderGroups();
});

$("#editor-close").addEventListener("click", () => editorModal.close());
// The Esc key closes the dialog natively; a close also refreshes the lists,
// because the file may have been saved or deleted meanwhile.
editorModal.addEventListener("close", async () => {
  editorFile = null;
  await loadFiles();
  await loadConfig();
});

$("#save").addEventListener("click", async () => {
  if (!editorFile) return;
  // client-side sanity: drop fully empty groups silently
  const payload = groups
    .filter((g) => g.targets.length || Object.keys(g.labels || {}).length)
    .map((g) => ({ targets: g.targets, labels: g.labels }));
  const resp = await fetch(`/api/files/${encodeURIComponent(editorFile)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ groups: payload }),
  });
  const data = await resp.json().catch(() => ({}));
  if (resp.status === 422 && data.errors) {
    showMessage("error", `Validation failed — ${serverErrorsText(data.errors)}`);
    return;
  }
  if (!resp.ok) {
    showMessage("error", data.error || `HTTP ${resp.status}`);
    return;
  }
  showMessage("notice", `Saved. Backup created: ${data.backup}`);
  const savedFile = editorFile;
  // Reload the saved groups, keep the modal open and the notice visible.
  await openEditor(savedFile, { keepMessage: true });
});

$("#delete").addEventListener("click", async () => {
  if (!editorFile) return;
  if (!confirm(`Delete ${editorFile}? A backup is created next to it.`)) return;
  const resp = await fetch(`/api/files/${encodeURIComponent(editorFile)}`, { method: "DELETE" });
  const data = await resp.json().catch(() => ({}));
  if (!resp.ok) {
    showMessage("error", data.error || `HTTP ${resp.status}`);
    return;
  }
  editorModal.close();
});

// ---- page wiring ----

// Job picker: a select of existing jobs, with an escape hatch to type a
// new job name (kept in a plain input so it stays submittable).
const jobSelect = $("#create-job-select");
const jobInput = $("#create-job");
function syncJobPicker() {
  if (jobSelect.value === "__custom__") {
    jobInput.classList.remove("hidden");
    jobInput.value = "";
    jobInput.focus();
  } else {
    jobInput.classList.add("hidden");
    jobInput.value = jobSelect.value;
  }
}
jobSelect.addEventListener("change", syncJobPicker);
syncJobPicker();

// Edit-existing-file picker opens the modal in place.
const editSelect = $("#edit-file");
const editOpen = $("#edit-open");
editSelect.addEventListener("change", () => {
  editOpen.disabled = !editSelect.value;
});
$("#edit-form").addEventListener("submit", (ev) => {
  ev.preventDefault();
  if (editSelect.value) openEditor(editSelect.value);
});

$("#create-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const warningBox = $("#create-warning");
  warningBox.classList.add("hidden");
  const job = $("#create-job").value.trim();
  const id = $("#create-id").value.trim();
  const resp = await fetch("/api/files", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ job, id }),
  });
  const data = await resp.json();
  if (!resp.ok) {
    warningBox.querySelector("p").textContent = data.error || `HTTP ${resp.status}`;
    warningBox.querySelector("pre").replaceChildren();
    warningBox.classList.remove("hidden");
    return;
  }
  if (data.warning) {
    const p = warningBox.querySelector("p");
    p.replaceChildren();
    p.append(document.createTextNode(data.warning + " "));
    const a = el("a", { href: "#", onclick: (ev) => { ev.preventDefault(); openEditor(data.file); } },
      `Open ${data.file} anyway`);
    p.append(a);
    warningBox.querySelector("pre").textContent = data.snippet || "";
    warningBox.classList.remove("hidden");
    await loadConfig();
    await loadFiles();
    return; // stay on the page so the warning and snippet are readable
  }
  await loadConfig();
  await loadFiles();
  openEditor(data.file);
});

// Targets: loaded lazily — only when the collapsed section is opened, and
// refreshed periodically while it stays open.
const targetsDetails = $("#targets-section");
let targetsTimer = null;
targetsDetails.addEventListener("toggle", () => {
  if (targetsDetails.open) {
    loadTargets();
    targetsTimer = setInterval(loadTargets, 15000);
  } else if (targetsTimer) {
    clearInterval(targetsTimer);
    targetsTimer = null;
  }
});

labelKeyList();
loadConfig();
loadFiles();
