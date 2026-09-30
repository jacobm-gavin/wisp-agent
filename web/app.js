"use strict";
const byId = id => document.getElementById(id);
let selected = null;
let refreshing = false;
let pending = false;
async function get(path) {
  const response = await fetch(path);
  if (!response.ok) throw new Error("History could not be loaded.");
  return response.json();
}
function element(tag, text) {
  const node = document.createElement(tag);
  node.textContent = text;
  return node;
}
async function refresh() {
  if (refreshing) { pending = true; return; }
  refreshing = true;
  try {
    const [active, recent] = await Promise.all([get("/api/active-runs"), get("/api/runs")]);
    // Recent records win if a run completed between the two snapshots.
    const runs = [...new Map([...active, ...recent].map(run => [run.id, run])).values()];
    runs.sort((a, b) => Number(b.status === "running") - Number(a.status === "running") || new Date(b.started_at) - new Date(a.started_at));
    const list = byId("runs");
    list.replaceChildren();
    if (!runs.length) list.textContent = "No runs yet.";
    for (const run of runs) {
      const button = element("button", `${run.source} · ${run.status}\n${new Date(run.started_at).toLocaleString()} · ${run.id.slice(0, 8)}`);
      button.setAttribute("aria-pressed", String(selected === run.id));
      button.onclick = () => { selected = run.id; refresh(); };
      list.append(button);
    }
    if (selected) {
      const id = selected;
      const history = await get(`/api/runs/${encodeURIComponent(id)}`);
      if (selected === id) render(history);
    }
    byId("error").textContent = "";
  } catch (error) { byId("error").textContent = error.message; }
  finally { refreshing = false; if (pending) { pending = false; refresh(); } }
}
function render(history) {
  const detail = byId("detail");
  const open = new Set([...detail.querySelectorAll("details[open]")].map(node => node.dataset.sequence));
  detail.replaceChildren(element("p", `${history.run.id} · ${history.run.status}`));
  detail.append(element("h3", "Triggering event"), element("pre", JSON.stringify(history.run.event, null, 2)));
  for (const activity of history.activity) {
    const item = document.createElement("details");
    item.dataset.sequence = String(activity.sequence);
    item.open = open.has(item.dataset.sequence);
    item.append(element("summary", `${new Date(activity.time).toLocaleTimeString()} · ${activity.kind}`));
    item.append(element("pre", JSON.stringify(activity.data, null, 2)));
    detail.append(item);
  }
  if (history.run.output) detail.append(element("h3", "Saved final output"), element("pre", history.run.output));
  if (history.run.error) detail.append(element("h3", "Failure"), element("pre", history.run.error));
}
async function start() {
  try {
    const agent = await get("/api/agent");
    byId("agent-name").textContent = agent.name;
    byId("model").textContent = `Model: ${agent.model}`;
    for (const [label, items] of [["Instructions", agent.instructions], ["Events", agent.events], ["Tools", agent.tools]]) {
      const list = document.createElement("ul");
      for (const item of items) list.append(element("li", typeof item === "string" ? item : `${item.name}${item.description ? " — " + item.description : ""}`));
      if (!items.length) list.append(element("li", "None declared"));
      byId("capabilities").append(element("h3", label), list);
    }
    await refresh();
    const stream = new EventSource("/api/stream");
    stream.onopen = () => { byId("connection").textContent = "Live history"; };
    stream.onmessage = () => refresh();
    stream.onerror = () => { byId("connection").textContent = "Reconnecting…"; };
  } catch (error) { byId("error").textContent = error.message; byId("connection").textContent = "Unavailable"; }
}
start();
