// Sandbox UI. Runs the real acyclic-sdk-js agent loop in the browser against
// the mock vendor APIs; model calls go through the server's capped proxy.

import * as sdk from "/sdk/index.js";
import { snippets } from "/snippets.js";

const $ = (id) => document.getElementById(id);
const el = (tag, attrs = {}, ...kids) => {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") n.className = v;
    else if (k.startsWith("on")) n.addEventListener(k.slice(2), v);
    else if (v !== undefined && v !== false) n.setAttribute(k, v === true ? "" : v);
  }
  for (const kid of kids.flat()) if (kid !== undefined && kid !== null) n.append(kid.nodeType ? kid : String(kid));
  return n;
};
const store = {
  get(k, d) { try { return JSON.parse(localStorage.getItem(k)) ?? d; } catch { return d; } },
  set(k, v) { try { localStorage.setItem(k, JSON.stringify(v)); } catch {} },
};

const cfg = await (await fetch("/api/config")).json();
const replayId = location.pathname.match(/^\/r\/([\w-]+)/)?.[1];

const state = {
  vendor: store.get("acyclic.vendor", cfg.vendors[0].id),
  patient: undefined,
  patients: [],
  run: undefined,        // { id, messages, session, ehr, events, saved }
  busy: false,
  lang: "js",
  lastHttp: undefined,
};
const vendorCfg = () => cfg.vendors.find((v) => v.id === state.vendor);

// ---------- credentials: generated in this browser, registered with the mock ----------

async function credentials(vendor, { fresh = false } = {}) {
  const key = `acyclic.creds.${cfg.credsVersion}.${vendor.id}`;
  let c = fresh ? undefined : store.get(key);
  if (c) return c;
  const body = { vendor: vendor.id, name: "agentsdk browser" };
  let privateJwk, kid;
  if (vendor.auth === "private_key_jwt") {
    const pair = await sdk.generateKeyPair("RS384");
    privateJwk = await crypto.subtle.exportKey("jwk", pair.privateKey);
    kid = pair.kid;
    body.jwks = { keys: [pair.publicJwk] };
  }
  const res = await fetch(vendor.registerUrl, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(body) });
  if (!res.ok) throw new Error(`registration failed: ${res.status}`);
  const r = await res.json();
  c = { clientId: r.client_id, clientSecret: r.client_secret, privateJwk, kid };
  store.set(key, c);
  return c;
}

async function connect(allowWrites) {
  const v = vendorCfg();
  for (let attempt = 0; attempt < 2; attempt++) {
    const c = await credentials(v, { fresh: attempt > 0 });
    const ehr = await sdk.connect({
      vendor: v.id, baseUrl: v.baseUrl, tokenUrl: v.tokenUrl, clientId: c.clientId,
      clientSecret: c.clientSecret, privateKey: c.privateJwk, kid: c.kid, practiceId: v.practiceId,
      write: allowWrites, telemetry: uiTelemetry, inspect: onHttp,
    });
    try { await ehr.fhir.getToken(); return ehr; }
    catch (e) { if (e.code !== "invalid_client" || attempt) throw e; } // registration expired: make a new one
  }
}

// ---------- telemetry + http capture ----------

const telemetryLines = [];
const uiTelemetry = sdk.jsonTelemetry({
  level: "debug",
  write: (line) => {
    telemetryLines.push(line);
    record({ type: "telemetry", line });
    renderTelemetry();
  },
});

function onHttp(h) {
  const e = { type: "http", method: h.method, url: h.url, status: h.status, ms: h.ms, requestBody: h.requestBody, responseBody: truncate(h.responseBody, 60000) };
  state.lastHttp = e;
  record(e);
  traceHttp(e);
  touchChart(h);
}

const truncate = (s, n) => (s && s.length > n ? s.slice(0, n) + `\n… (${s.length - n} more characters)` : s);

// ---------- runs: persisted events ----------

let pending = [], flushTimer;
function record(e) {
  if (!state.run || replayId) return;
  e.t = Date.now();
  state.run.events.push(e);
  pending.push(e);
  clearTimeout(flushTimer);
  flushTimer = setTimeout(flush, 400);
}

async function flush() {
  if (!state.run || !pending.length) return;
  const run = state.run;
  const batch = pending;
  pending = [];
  const from = run.saved;
  run.saved += batch.length;
  try {
    await fetch(`/api/runs/${run.id}/events`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ from, events: batch }) });
  } catch { /* best effort; the live view is unaffected */ }
}

async function saveRun(title) {
  const r = state.run;
  await fetch(`/api/runs/${r.id}`, {
    method: "PUT", headers: { "content-type": "application/json" },
    body: JSON.stringify({ vendor: state.vendor, patient_id: state.patient ?? "", title: title ?? r.title ?? "" }),
  }).catch(() => {});
}

async function loadRuns() {
  const ul = $("runs");
  ul.replaceChildren();
  try {
    const runs = await (await fetch("/api/runs")).json();
    for (const r of runs) ul.append(el("li", {}, el("a", { href: `/r/${r.id}` }, r.title || r.id), el("span", { class: "muted" }, vendorLabel(r.vendor))));
    if (!runs.length) ul.append(el("li", { class: "muted" }, "None yet. Runs are kept for 30 days."));
  } catch { ul.append(el("li", { class: "muted" }, "Could not load runs.")); }
}

const vendorLabel = (id) => cfg.vendors.find((v) => v.id === id)?.label ?? id;

// ---------- chat ----------

// Small, safe markdown: escape first, then headings, bold, code, lists, tables.
function md(text) {
  const esc = (t) => t.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  const inline = (t) => esc(t).replace(/`([^`]+)`/g, "<code>$1</code>").replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
  const lines = text.split("\n");
  let html = "", i = 0;
  while (i < lines.length) {
    const l = lines[i];
    if (/^\s*\|.*\|\s*$/.test(l) && /^\s*\|[\s:|-]+\|\s*$/.test(lines[i + 1] ?? "")) {
      const cells = (r) => r.trim().replace(/^\||\|$/g, "").split("|").map((c) => c.trim());
      html += "<table><thead><tr>" + cells(l).map((c) => `<th>${inline(c)}</th>`).join("") + "</tr></thead><tbody>";
      i += 2;
      while (i < lines.length && /^\s*\|.*\|\s*$/.test(lines[i])) html += "<tr>" + cells(lines[i++]).map((c) => `<td>${inline(c)}</td>`).join("") + "</tr>";
      html += "</tbody></table>";
      continue;
    }
    if (/^\s*([-*]|\d+\.)\s+/.test(l)) {
      const ordered = /^\s*\d+\./.test(l);
      html += ordered ? "<ol>" : "<ul>";
      while (i < lines.length && /^\s*([-*]|\d+\.)\s+/.test(lines[i])) html += `<li>${inline(lines[i++].replace(/^\s*([-*]|\d+\.)\s+/, ""))}</li>`;
      html += ordered ? "</ol>" : "</ul>";
      continue;
    }
    const h = l.match(/^(#{1,4})\s+(.*)$/);
    if (h) html += `<h5>${inline(h[2])}</h5>`;
    else if (l.trim()) html += `<p>${inline(l)}</p>`;
    i++;
  }
  const d = document.createElement("div");
  d.innerHTML = html;
  return d;
}

function addMsg(kind, text) {
  const m = el("div", { class: `msg ${kind}` }, kind === "assistant" ? md(text) : text);
  $("messages").append(m);
  m.scrollIntoView({ block: "end" });
  return m;
}

function newRun() {
  flush();
  state.run = { id: "run_" + crypto.randomUUID().replace(/-/g, "").slice(0, 20), messages: undefined, session: undefined, ehr: undefined, events: [], saved: 0 };
  $("messages").replaceChildren();
  $("trace").replaceChildren(el("p", { class: "empty" }, "Tool calls and the FHIR requests behind them show up here."));
  telemetryLines.length = 0;
  renderTelemetry();
  $("share").disabled = true;
  $("traceStats").textContent = "";
  steps.clear();
  renderSuggest();
}

const SUGGEST = [
  "Summarize this patient's active problems and medications.",
  "What was the most recent HbA1c, and how has it trended?",
  "Any allergies I should know before prescribing an antibiotic?",
  "Read the latest progress note and list the follow-up items.",
  "What appointments are coming up, and what insurance is on file?",
  "Draft and save a short note summarizing today's chart review.",
];

function renderSuggest() {
  $("suggest").replaceChildren(...SUGGEST.map((s) => el("button", { type: "button", onclick: () => { $("prompt").value = s; if (/save/i.test(s)) $("writes").checked = true; $("prompt").focus(); } }, s)));
}

async function approve({ summary, args }) {
  record({ type: "approval_request", summary });
  $("approveSummary").textContent = summary;
  $("approveBody").textContent = args.text;
  const d = $("approve");
  d.returnValue = "";
  d.showModal();
  const answer = await new Promise((res) => d.addEventListener("close", () => res(d.returnValue === "yes"), { once: true }));
  record({ type: "approval", approved: answer });
  addMsg("note", answer ? "You approved the write." : "You declined the write.");
  return answer;
}

async function send(text) {
  if (state.busy || !state.patient) return;
  state.busy = true;
  $("send").disabled = true;
  $("suggest").replaceChildren();
  const allowWrites = $("writes").checked;
  const run = state.run;
  addMsg("user", text);
  record({ type: "user", text, vendor: state.vendor, patient: state.patient, allowWrites });
  if (run.saved === 0 && run.events.length === 1) await saveRun(text.slice(0, 80));
  const thinking = addMsg("note", "Working…");
  try {
    if (!run.ehr || run.allowWrites !== allowWrites) {
      run.ehr = await connect(allowWrites);
      run.allowWrites = allowWrites;
      run.session = sdk.createSession(run.ehr, { patient: state.patient, allowWrites, approve, runId: run.id });
    }
    const model = sdk.openAICompatible({ baseUrl: `${location.origin}/api/model`, apiKey: "sandbox", model: cfg.model });
    const result = await sdk.runAgent({
      ehr: run.ehr, session: run.session, model, dataPolicy: "synthetic",
      prompt: text, messages: run.messages, conversationId: run.id,
      maxSteps: 10, maxToolCalls: 20, maxOutputTokens: 2000,
      onEvent: onAgentEvent,
    });
    run.messages = result.messages;
    thinking.remove();
    addMsg("assistant", result.text || "(no answer)");
    if (result.stop !== "done") addMsg("note", `Stopped: ${result.stop.replace(/_/g, " ")}.`);
    $("share").disabled = false;
  } catch (e) {
    thinking.remove();
    addMsg("error", e.status === 429 || /429/.test(e.message) ? (e.detail || "The sandbox is rate limited right now. Try again in a few minutes.") : `Error: ${e.detail || e.message}`);
    record({ type: "error", message: e.detail || e.message });
  } finally {
    state.busy = false;
    $("send").disabled = false;
    flush();
    loadRuns();
    refreshChart();
  }
}

// ---------- trace ----------

const steps = new Map();
let currentCall;
let tokens = { input: 0, output: 0 };

function onAgentEvent(e) {
  if (e.type === "model_request") return; // large; the response carries what matters
  record(e);
  traceEvent(e);
}

function traceEvent(e) {
  const list = $("trace");
  list.querySelector(".empty")?.remove();
  if (e.type === "model_response") {
    tokens.input += e.usage?.input ?? 0; tokens.output += e.usage?.output ?? 0;
    $("traceStats").textContent = `${tokens.input + tokens.output} tokens`;
    const head = el("div", { class: "head" }, el("span", { class: "k" }, `Step ${e.step}`), e.toolCalls?.length ? `model asked for ${e.toolCalls.length} tool call${e.toolCalls.length > 1 ? "s" : ""}` : "model answered", el("span", { class: "r" }, `${e.usage?.input ?? "?"} in · ${e.usage?.output ?? "?"} out`));
    const box = el("div", { class: "step" }, head);
    steps.set(e.step, box);
    list.append(box);
  } else if (e.type === "tool_call") {
    const box = steps.get(e.step) ?? list;
    const http = el("div");
    const res = el("div");
    const call = el("div", { class: "call" },
      el("div", {}, el("span", { class: "name" }, e.name), " ", el("span", { class: "args" }, JSON.stringify(e.args))),
      el("div", { class: "copyrow" }, ...["js", "go", "rs"].map((l) => el("button", { type: "button", onclick: () => copy(snippets.toolCall(l, e.name, e.args, vendorCfg())) }, `Copy ${{ js: "JS", go: "Go", rs: "Rust" }[l]}`))),
      http, res);
    call.dataset.id = e.id;
    currentCall = { http, res };
    box.append(call);
    call.scrollIntoView({ block: "nearest" });
  } else if (e.type === "tool_result") {
    const target = currentCall?.res;
    if (!target) return;
    const status = e.ok ? el("span", { class: "status-ok" }, "ok") : el("span", { class: "status-err" }, `error: ${e.error}`);
    const summary = e.ok && Array.isArray(e.result?.items) ? ` · ${e.result.items.length} item${e.result.items.length === 1 ? "" : "s"}${e.result.truncated ? " (more available)" : ""}` : "";
    target.append(el("details", { class: "result" }, el("summary", {}, status, summary, " · what the model saw"), el("pre", { class: "code" }, JSON.stringify(e.ok ? e.result : { error: e.error, code: e.code }, null, 2))));
  } else if (e.type === "done") {
    // nothing extra; the chat shows the answer
  } else if (e.type === "user") {
    list.append(el("div", { class: "msg note" }, `You: ${e.text}`));
  } else if (e.type === "error") {
    list.append(el("div", { class: "msg error" }, e.message));
  }
}

function traceHttp(e) {
  const target = currentCall?.http ?? $("trace");
  const u = new URL(e.url);
  const cls = e.status < 300 ? "status-ok" : e.status < 500 ? "status-warn" : "status-err";
  let body = e.responseBody;
  try { body = JSON.stringify(JSON.parse(body), null, 2); } catch {}
  target.append(el("details", { class: "http" },
    el("summary", {}, el("span", { class: cls }, e.status), el("span", {}, e.method), el("span", { class: "url", title: e.url }, u.pathname.replace(/^.*?\/(?=[A-Z][a-zA-Z]+(\/|$))/, "…/") + u.search), el("span", { class: "ms" }, `${e.ms}ms`)),
    el("div", { class: "copyrow" }, el("button", { type: "button", onclick: () => copy(snippets.curl(e, vendorCfg())) }, "Copy curl")),
    e.requestBody ? el("pre", { class: "code" }, JSON.stringify(e.requestBody, null, 2)) : null,
    el("pre", { class: "code" }, body ?? "")));
}

function renderTelemetry() {
  $("telemetry").textContent = telemetryLines.map((l) => { try { return JSON.stringify(JSON.parse(l)); } catch { return l; } }).join("\n") || "Run a prompt to see telemetry.";
}

async function copy(text) {
  try { await navigator.clipboard.writeText(text); } catch { /* clipboard blocked: show it instead */ prompt("Copy:", text); }
}

// ---------- chart viewer: the mock's current state for this browser's app ----------

const touched = new Set();
let newIds = new Set();

function touchChart(h) {
  const m = new URL(h.url).pathname.match(/\/([A-Z][A-Za-z]+)(\/|$)/g);
  const type = m?.[m.length - 1]?.replace(/\//g, "");
  if (type) touched.add(type);
  if (h.method === "POST" && h.status < 300) refreshChart(true);
  else markTouched();
}

function markTouched() {
  for (const li of document.querySelectorAll("#tab-chart [data-type]")) li.classList.toggle("touched", touched.has(li.dataset.type));
}

let chartVersion = 0;
async function refreshChart(afterWrite) {
  const v = vendorCfg();
  if (!state.patient) return;
  const c = store.get(`acyclic.creds.${cfg.credsVersion}.${v.id}`);
  const my = ++chartVersion;
  const q = new URLSearchParams({ vendor: v.id, patient: state.patient });
  if (c?.clientId) q.set("client_id", c.clientId);
  const res = await fetch(`${cfg.mockBase}/api/chart?${q}`);
  if (!res.ok || my !== chartVersion) return;
  const chart = await res.json();
  if (afterWrite) newIds = new Set(chart.written ?? []);
  renderChart(chart);
}

function renderChart(c) {
  const P = sdk.projections;
  const p = P.patient(c.patient);
  const sec = (title, type, items, line) => [
    el("h4", {}, title),
    el("ul", {}, items.length ? items.map((r) => { const li = line(r); li.dataset.type = type; if (newIds.has(r.id)) li.classList.add("new"); return li; }) : [el("li", { class: "muted", "data-type": type }, "None")]),
  ];
  const obs = (rs) => rs.map((r) => ({ r, o: P.observation(r) }));
  const flag = (o) => o.interpretation ? el("span", { class: `flag ${o.interpretation[0]}` }, o.interpretation) : null;
  const box = el("div", { class: "chart" },
    el("div", { class: "banner" }, el("div", { class: "nm" }, p.name), el("div", { class: "muted" }, `${p.gender ?? ""} · born ${p.birth_date ?? "?"} · MRN ${p.mrn ?? "?"}`), el("div", { class: "muted small" }, `${vendorLabel(state.vendor)} id ${p.id}`)),
    sec("Problems", "Condition", c.conditions.map(P.condition), (x) => el("li", { "data-id": x.id }, x.display, el("span", { class: "d" }, x.onset ?? ""))),
    sec("Medications", "MedicationRequest", c.medications.map(P.medication), (x) => el("li", {}, el("span", {}, x.display, el("div", { class: "muted small" }, x.dosage ?? "")), el("span", { class: "d" }, x.status))),
    sec("Allergies", "AllergyIntolerance", c.allergies.map(P.allergy), (x) => el("li", {}, x.display, el("span", { class: "d" }, (x.reactions ?? []).join(", ")))),
    sec("Recent labs", "Observation", obs(c.labs).slice(0, 12).map((x) => x.o), (o) => el("li", {}, el("span", {}, o.display.replace(/ \[.*$/, ""), " ", flag(o)), el("span", { class: "d" }, `${o.value ?? ""} · ${(o.effective ?? "").slice(0, 10)}`))),
    sec("Recent vitals", "Observation", obs(c.vitals).slice(0, 8).map((x) => x.o), (o) => el("li", {}, o.display, el("span", { class: "d" }, `${o.value ?? ""} · ${(o.effective ?? "").slice(0, 10)}`))),
    sec("Notes", "DocumentReference", c.documents.map(P.document), (x) => el("li", {}, x.title, el("span", { class: "d" }, (x.date ?? "").slice(0, 10)))),
    sec("Appointments", "Appointment", c.appointments.map(P.appointment), (x) => el("li", {}, `${x.type ?? ""} (${x.status})`, el("span", { class: "d" }, (x.start ?? "").slice(0, 10)))),
    sec("Coverage", "Coverage", c.coverage.map(P.coverage), (x) => el("li", {}, `${x.payer} · ${x.plan ?? ""}`, el("span", { class: "d" }, x.member_id ?? ""))),
    el("p", { class: "muted small" }, "Live view of the mock server for your sandbox app. Highlighted sections were read by the agent in this run; bold rows were written."),
  );
  $("tab-chart").replaceChildren(box);
  markTouched();
}

// ---------- code + connect tabs ----------

function renderCode() {
  $("code").textContent = snippets.program(state.lang, vendorCfg(), state.patient);
}

function renderConnect() {
  const v = vendorCfg();
  const c = store.get(`acyclic.creds.${cfg.credsVersion}.${v.id}`) ?? {};
  $("tab-connect").replaceChildren(
    el("p", {}, `Point any FHIR client at this mock. Your browser registered a sandbox app for ${v.label}.`),
    el("pre", { class: "code" }, [
      `FHIR base   ${v.baseUrl}`,
      `Token URL   ${v.tokenUrl}`,
      `Auth        ${v.auth}`,
      `Client id   ${c.clientId ?? "(registers on first run)"}`,
      c.clientSecret ? `Secret      ${c.clientSecret}` : `Key         RS384 private key in this browser's storage`,
      v.practiceId ? `Practice    ${v.practiceId}` : "",
      `Register    POST ${v.registerUrl}  {"vendor":"${v.id}","jwks":{"keys":[<your public JWK>]}}`,
    ].filter(Boolean).join("\n")),
    el("p", { class: "muted small" }, v.fidelity),
    el("p", { class: "muted small" }, "Registrations and anything you write are removed after 7 days."),
  );
}

// ---------- wiring ----------

async function loadPatients() {
  const v = vendorCfg();
  const res = await fetch(`${cfg.mockBase}/api/patients?vendor=${v.id}`);
  state.patients = await res.json();
  const sel = $("patient");
  sel.replaceChildren(...state.patients.map((p) => el("option", { value: p.id }, `${p.name} · ${p.summary}`)));
  const remembered = store.get(`acyclic.patient.${v.id}`);
  state.patient = state.patients.some((p) => p.id === remembered) ? remembered : state.patients[0]?.id;
  sel.value = state.patient;
}

function renderVendors() {
  $("vendors").replaceChildren(...cfg.vendors.map((v) => el("button", { type: "button", class: v.id === state.vendor ? "on" : "", title: v.fidelity, onclick: () => switchVendor(v.id) }, v.label)));
}

async function switchVendor(id) {
  state.vendor = id;
  store.set("acyclic.vendor", id);
  renderVendors();
  await loadPatients();
  newRun();
  touched.clear();
  refreshChart();
  renderCode();
  renderConnect();
}

$("patient").addEventListener("change", (e) => {
  state.patient = e.target.value;
  store.set(`acyclic.patient.${state.vendor}`, state.patient);
  newRun();
  touched.clear();
  refreshChart();
  renderCode();
});
$("newRun").addEventListener("click", () => { newRun(); touched.clear(); markTouched(); });
$("ask").addEventListener("submit", (e) => { e.preventDefault(); const t = $("prompt").value.trim(); if (t) { $("prompt").value = ""; send(t); } });
$("prompt").addEventListener("keydown", (e) => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); $("ask").requestSubmit(); } });
$("share").addEventListener("click", async () => {
  const r = state.run;
  await fetch(`/api/runs/${r.id}/share`, { method: "POST" });
  const url = `${location.origin}/r/${r.id}`;
  copy(url);
  addMsg("note", `Read-only link copied: ${url}`);
});
for (const b of $("sideTabs").querySelectorAll("button")) b.addEventListener("click", () => {
  for (const x of $("sideTabs").querySelectorAll("button")) x.classList.toggle("on", x === b);
  for (const t of ["chart", "telemetry", "code", "connect"]) $(`tab-${t}`).hidden = t !== b.dataset.tab;
  if (b.dataset.tab === "connect") renderConnect();
});
for (const b of $("codeLang").querySelectorAll("button")) b.addEventListener("click", () => {
  state.lang = b.dataset.lang;
  for (const x of $("codeLang").querySelectorAll("button")) x.classList.toggle("on", x === b);
  renderCode();
});
$("copyCode").addEventListener("click", () => copy($("code").textContent));
for (const b of $("mobileTabs").querySelectorAll("button")) b.addEventListener("click", () => {
  for (const x of $("mobileTabs").querySelectorAll("button")) x.classList.toggle("on", x === b);
  for (const p of ["chat", "trace", "side"]) $(`pane-${p}`).classList.toggle("show", p === b.dataset.pane);
});
$("pane-chat").classList.add("show");

// ---------- replay (/r/:id) ----------

async function replay(id) {
  const res = await fetch(`/api/runs/${id}/events`);
  if (!res.ok) { addMsg("error", "This run is not shared or no longer exists."); return; }
  const { run, events } = await res.json();
  state.vendor = run.vendor;
  renderVendors();
  for (const b of $("vendors").querySelectorAll("button")) b.disabled = true;
  $("ask").hidden = true;
  $("suggest").hidden = true;
  $("patient").replaceChildren(el("option", {}, `Replay · ${run.title}`));
  $("patient").disabled = true;
  $("newRun").textContent = "Start your own";
  $("newRun").onclick = () => { location.href = "/"; };
  addMsg("note", `Recorded run on the ${vendorLabel(run.vendor)} mock, ${new Date(run.created).toLocaleString()}.`);
  state.patient = run.patient_id;
  for (const { event: e } of events) {
    if (e.type === "user") addMsg("user", e.text);
    if (e.type === "model_response" && !e.toolCalls?.length && e.content) addMsg("assistant", e.content);
    if (e.type === "approval") addMsg("note", e.approved ? "The user approved the write." : "The user declined the write.");
    if (e.type === "error") addMsg("error", e.message);
    if (e.type === "telemetry") { telemetryLines.push(e.line); continue; }
    if (e.type === "http") { traceHttp(e); continue; }
    traceEvent(e);
  }
  renderTelemetry();
  $("tab-chart").replaceChildren(el("p", { class: "muted" }, "Chart view is live-only. Start your own run to see it."));
}

renderVendors();
if (replayId) {
  await replay(replayId);
} else {
  await loadPatients();
  newRun();
  refreshChart();
  renderCode();
  loadRuns();
}
