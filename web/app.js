"use strict";
const byId = id => document.getElementById(id);
let selected = null, refreshing = false, pending = false;
let currentView = "inspector", viewChosen = false, chatAvailable = false;
let runsSnapshot = "", detailSnapshot = "", chatSnapshot = "";
let chatSending = false, chatReady = false;
const terminalRuns = new Map();
const timeLabel = value => new Date(value).toLocaleTimeString([], {hour: "2-digit", minute: "2-digit"});
const statusLabel = status => ({running: "Running", completed: "Completed", failed: "Failed"}[status] || status);
function element(tag, text, className) {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = text;
  if (className) node.className = className;
  return node;
}
function setView(view, user = true) {
  if (user) viewChosen = true;
  currentView = view;
  for (const [name, id] of [["chat", "chat"], ["inspector", "inspector"], ["agent", "agent-panel"]]) {
    byId(id).hidden = name !== view;
    byId("view-" + name).setAttribute("aria-pressed", String(name === view));
  }
}
for (const button of document.querySelectorAll("[data-view]")) button.addEventListener("click", () => setView(button.dataset.view));
byId("run-filter").addEventListener("change", () => refresh());
async function get(path) {
  const response = await fetch(path);
  if (!response.ok) throw new Error("Could not load execution history. Check that the local server is running.");
  return response.json();
}
function selectRun(id) {
  selected = id; setView("inspector"); refresh();
  if (window.matchMedia("(max-width:680px)").matches) byId("workspace").scrollIntoView();
}
function statusNode(status) { return element("span", statusLabel(status), "status " + status); }
async function refresh() {
  if (refreshing) { pending = true; return; }
  refreshing = true;
  try {
    const [active, recent] = await Promise.all([get("/api/active-runs"), get("/api/runs")]);
    const runs = [...new Map([...active, ...recent].map(run => [run.id, run])).values()];
    runs.sort((a, b) => Number(b.status === "running") - Number(a.status === "running") || new Date(b.started_at) - new Date(a.started_at));
    byId("run-count").textContent = runs.length;
    const filter = byId("run-filter").value;
    const visible = runs.filter(run => filter === "all" || run.status === filter);
    const snapshot = JSON.stringify([visible, selected]);
    if (snapshot !== runsSnapshot) {
      const list = byId("runs");
      list.replaceChildren();
      if (!visible.length) list.append(element("p", runs.length ? "No runs match this filter." : "Waiting for the first event.", "empty-small"));
      for (const run of visible) {
        const button = element("button", undefined, "run-button");
        button.setAttribute("aria-pressed", String(selected === run.id));
        const top = element("span", undefined, "run-top");
        top.append(statusNode(run.status), element("span", timeLabel(run.started_at), "run-time"));
        const preview = typeof run.event?.data?.text === "string" ? run.event.data.text : run.source;
        button.append(top, element("span", preview, "run-preview"), element("span", new Date(run.started_at).toLocaleDateString() + " / " + run.id.slice(0, 8), "run-source"));
        button.title = run.source + " / " + run.id;
        button.onclick = () => selectRun(run.id);
        list.append(button);
      }
      runsSnapshot = snapshot;
    }
    if (selected) {
      const id = selected;
      const history = await get("/api/runs/" + encodeURIComponent(id));
      if (selected === id) render(history);
    }
    byId("error").textContent = "";
  } catch (error) { byId("error").textContent = error.message; }
  finally { refreshing = false; if (pending) { pending = false; refresh(); } }
}
function render(history) {
  const snapshot = JSON.stringify(history);
  if (snapshot === detailSnapshot) return;
  detailSnapshot = snapshot;
  const detail = byId("detail");
  const open = new Set([...detail.querySelectorAll("details[open]")].map(node => node.dataset.sequence));
  const run = history.run;
  const summary = element("div", undefined, "run-summary");
  summary.append(statusNode(run.status), element("span", run.source), element("span", new Date(run.started_at).toLocaleString()));
  if (run.finished_at) summary.append(element("span", ((new Date(run.finished_at) - new Date(run.started_at)) / 1000).toFixed(1) + " seconds"));
  detail.replaceChildren(summary);
  if (typeof run.event?.data?.text === "string") detail.append(element("p", run.event.data.text, "run-prompt"));
  const event = element("details", undefined, "raw-event"); event.dataset.sequence = "event-" + run.id; event.open = open.has(event.dataset.sequence);
  event.append(element("summary", "Triggering event and run ID"), element("pre", JSON.stringify({id: run.id, event: run.event}, null, 2)));
  detail.append(event);
  const timeline = element("div", undefined, "timeline");
  const names = new Map();
  for (const a of history.activity) {
    if (a.kind === "tool.started") names.set(a.data.call.id, a.data.call.name);
    const d = a.data;
    const labels = {"run.started": "Run started", "model.started": "Model thinking", "model.finished": "Model responded", "tool.started": "Calling " + (d.call?.name || "tool"), "tool.finished": (names.get(d.call_id) || "Tool") + " returned", "run.completed": "Run completed", "run.failed": "Run failed"};
    let preview = "";
    if (a.kind === "model.finished") preview = d.error || (d.response?.ToolCalls || d.response?.tool_calls || []).map(c => c.name).join(", ") || d.response?.text || "No tool calls requested";
    if (a.kind === "tool.started") preview = d.call?.arguments?.command || (d.call?.arguments?.path ? "File: " + d.call.arguments.path : "");
    if (a.kind === "tool.finished") preview = d.error || (d.result?.exit_code !== undefined ? "Exit code " + d.result.exit_code + (d.result.stderr ? "\n" + d.result.stderr : "") : d.result?.delivered ? "Reply delivered to the chat" : "Result recorded");
    const item = element("details", undefined, "activity"); item.dataset.sequence = String(a.sequence); item.open = open.has(item.dataset.sequence);
    const head = element("summary");
    head.append(element("span", labels[a.kind] || a.kind, "activity-label"), element("span", timeLabel(a.time), "activity-time"));
    item.append(head);
    if (preview) item.append(element("p", preview, "activity-preview"));
    item.append(element("pre", JSON.stringify(a.data, null, 2)));
    timeline.append(item);
  }
  detail.append(timeline);
  if (run.output || run.error) {
    const output = element("div", undefined, "saved-output" + (run.error ? " failure" : ""));
    output.append(element("h3", run.error ? "Run failure" : "Saved final output"), element("p", run.error || run.output));
    if (!run.error) output.append(element("p", "Recorded for inspection. This is not an automatically delivered chat reply."));
    detail.append(output);
  }
}
async function start() {
  try {
    const agent = await get("/api/agent");
    byId("agent-name").textContent = agent.name;
    byId("model").textContent = "Model: " + agent.model;
    for (const [label, items] of [["Instructions", agent.instructions], ["Event sources", agent.events], ["Tools", agent.tools]]) {
      const group = element("section", undefined, "capability-group");
      const heading = element("h2", label); heading.append(element("span", String(items.length), "count")); group.append(heading);
      if (!items.length) group.append(element("p", "None declared.", "empty-small"));
      for (const item of items) {
        if (typeof item === "string") group.append(element("p", item, "capability"));
        else {
          const entry = element("details", undefined, "capability");
          entry.append(element("summary", item.name), element("p", item.description || "No description provided."));
          if (item.parameters) entry.append(element("pre", JSON.stringify(item.parameters, null, 2)));
          group.append(entry);
        }
      }
      byId("capabilities").append(group);
    }
    await refresh();
    const stream = new EventSource("/api/stream");
    stream.onopen = () => { byId("connection").textContent = "Connected"; byId("connection").dataset.live = "true"; };
    stream.onmessage = () => refresh();
    stream.onerror = () => { byId("connection").textContent = "Reconnecting…"; byId("connection").dataset.live = "false"; };
  } catch (error) { byId("error").textContent = error.message; byId("connection").textContent = "Unavailable"; }
}
function emptyChat() {
  const empty = element("div", undefined, "empty-state");
  empty.append(element("span", "⌁", "empty-symbol"), element("h2", "What should this run do?"), element("p", "Ask a question or give the agent a task. Its reply and execution trace will appear here."));
  return empty;
}
function messageHeading(label, initial, agent = false) {
  const row = element("div", undefined, "message-heading");
  row.append(element("span", initial, "avatar" + (agent ? " agent" : "")), element("strong", label));
  return row;
}
async function refreshChat() {
  const response = await fetch("/api/chat");
  if (response.status === 404) return false;
  if (!response.ok) throw new Error("Chat could not connect. Check the local server, then reload.");
  const data = await response.json();
  if (!chatAvailable) {
    chatAvailable = true; byId("view-chat").hidden = false;
    if (!viewChosen) setView("chat", false);
  }
  chatReady = data.ready;
  byId("chat-send").disabled = chatSending || !chatReady;
  const messages = data.messages || [];
  const ids = new Set(messages.map(m => m.run_id));
  for (const id of terminalRuns.keys()) if (!ids.has(id)) terminalRuns.delete(id);
  const states = await Promise.all(messages.map(async m => {
    if (!m.run_id) return "Accepting message…";
    let run = terminalRuns.get(m.run_id);
    if (!run) {
      run = (await get("/api/runs/" + encodeURIComponent(m.run_id))).run;
      if (run.status !== "running") terminalRuns.set(m.run_id, run);
    }
    if (run.status === "failed") return "Run failed: " + run.error;
    if (run.status === "completed") return m.reply ? "Completed" : "Completed without a reply. Open the run to inspect its output.";
    return "Working…";
  }));
  const snapshot = JSON.stringify([messages, states]);
  if (snapshot !== chatSnapshot) {
    const list = byId("chat-messages");
    const nearBottom = list.scrollHeight - list.scrollTop - list.clientHeight < 80;
    list.replaceChildren();
    messages.forEach((m, i) => {
      const item = element("article", undefined, "chat-message");
      item.append(messageHeading("You", "Y"), element("p", m.text, "message-text"));
      if (m.reply) {
        const reply = element("div", undefined, "reply-block");
        reply.append(messageHeading("Agent", "w", true), element("p", m.reply, "message-text"));
        item.append(reply);
      }
      const meta = element("div", undefined, "message-meta");
      meta.append(element("span", m.error || states[i], m.error || states[i].startsWith("Run failed") ? "error" : ""));
      if (m.run_id) {
        const inspect = element("button", "Inspect run", "text-button"); inspect.onclick = () => selectRun(m.run_id); meta.append(inspect);
      }
      item.append(meta); list.append(item);
    });
    if (!messages.length) list.append(emptyChat());
    if (nearBottom) list.scrollTop = list.scrollHeight;
    chatSnapshot = snapshot;
  }
  return true;
}
byId("chat-input").addEventListener("keydown", event => {
  if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) { event.preventDefault(); if (!byId("chat-send").disabled) byId("chat-form").requestSubmit(); }
});
byId("chat-form").addEventListener("submit", async event => {
  event.preventDefault();
  if (chatSending || !chatReady) return;
  const text = byId("chat-input").value;
  if (!text.trim()) return;
  chatSending = true; byId("chat-send").disabled = true; byId("chat-error").textContent = "";
  try {
    const response = await fetch("/api/chat/messages", {method: "POST", headers: {"Content-Type": "application/json"}, body: JSON.stringify({text})});
    if (!response.ok) throw new Error(await response.text());
    if (byId("chat-input").value === text) byId("chat-input").value = "";
  } catch (error) { byId("chat-error").textContent = error.message + " No automatic retry was made."; }
  finally { chatSending = false; byId("chat-send").disabled = !chatReady; }
});
async function pollChat() {
  try { if (!await refreshChat()) return; }
  catch (error) { chatReady = false; byId("chat-send").disabled = true; byId("connection").textContent = "Chat disconnected"; }
  setTimeout(pollChat, 1000);
}
start();
pollChat();
