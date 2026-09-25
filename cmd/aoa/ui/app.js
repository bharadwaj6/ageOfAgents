// aoa ui — a view over `aoa status --json` and the Event Log (ADR 021).
//
// The browser never folds the log itself: the server's projection is the one
// authority. The event stream is only a signal that the log moved, plus the
// seq to resume from. Everything from the log — goal text, Gate output, task
// titles — is untrusted agent input, so it is only ever set as textContent.
"use strict";

const app = document.getElementById("app");
const live = document.getElementById("live");
const toastEl = document.getElementById("toast");

let info = { read_only: true };
let status = null;
let goalEventsCache = { id: null, events: [] };
let route = null;      // { name: "overview" } | { name: "goal", id }
let slots = {};        // the parts of the current view that refresh
const opened = new Set(); // <details> the user opened, kept open across refreshes
let showHeartbeats = false;

// ---- DOM ----

// h builds an element. Strings and numbers become text nodes, never markup.
function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "class") el.className = v;
    else el.setAttribute(k, v === true ? "" : String(v));
  }
  append(el, children);
  return el;
}

function append(el, children) {
  for (const c of children.flat(Infinity)) {
    if (c === undefined || c === null || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
}

function fill(el, ...children) {
  if (!el) return;
  el.replaceChildren();
  append(el, children);
}

// keepOpen makes a <details> remember being opened across re-renders.
function keepOpen(key, details) {
  if (opened.has(key)) details.open = true;
  details.addEventListener("toggle", () => {
    if (details.open) opened.add(key); else opened.delete(key);
  });
  return details;
}

let toastTimer = 0;
function toast(msg, bad) {
  toastEl.textContent = msg;
  toastEl.className = "toast" + (bad ? " bad" : "");
  toastEl.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { toastEl.hidden = true; }, bad ? 8000 : 3500);
}

// ---- formatting ----

const fmtInt = (n) => Number(n || 0).toLocaleString();
const fmtUSD = (n) => "$" + Number(n || 0).toFixed(n >= 100 ? 0 : 2);
const shortSha = (s) => (s || "").slice(0, 8);

function fmtTime(ts) {
  if (!ts || ts.startsWith("0001-")) return "";
  const d = new Date(ts);
  return isNaN(d) ? "" : d.toLocaleTimeString([], { hour12: false });
}

function ago(ts) {
  if (!ts || ts.startsWith("0001-")) return "";
  const s = Math.max(0, (Date.now() - new Date(ts).getTime()) / 1000);
  if (s < 60) return Math.floor(s) + "s ago";
  if (s < 3600) return Math.floor(s / 60) + "m ago";
  if (s < 86400) return Math.floor(s / 3600) + "h ago";
  return Math.floor(s / 86400) + "d ago";
}

function fmtDuration(sec) {
  sec = Math.round(sec || 0);
  if (sec < 60) return sec + "s";
  if (sec < 3600) return Math.floor(sec / 60) + "m " + (sec % 60) + "s";
  return Math.floor(sec / 3600) + "h " + Math.floor((sec % 3600) / 60) + "m";
}

// safeURL returns the URL only when it is http(s): a ref is whatever the
// submitter wrote, and a javascript: link must never be clickable.
function safeURL(s) {
  try {
    const u = new URL(s);
    return u.protocol === "http:" || u.protocol === "https:" ? u.href : null;
  } catch (_) {
    return null;
  }
}

const outcomeTone = {
  queued: "", running: "run", awaiting_approval: "warn",
  merged: "ok", delivered: "ok", failed: "bad", cancelled: "",
};
const ticketTone = {
  pending: "", ready: "", claimed: "run", running: "run", proposed: "run",
  awaiting: "warn", merged: "ok", failed: "bad", decomposed: "",
};

const badge = (text, tone) => h("span", { class: "badge " + (tone || "") }, text.replace(/_/g, " "));

function conditionChips(conds) {
  return h("span", { class: "chips" }, (conds || []).map((c) =>
    h("span", { class: "chip " + c.status, title: `${c.type}: ${c.status} (${c.reason})` }, c.type[0])));
}

// ---- API ----

async function getJSON(path) {
  const r = await fetch(path, { headers: { Accept: "application/json" } });
  const body = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(body.error || `${r.status} ${r.statusText}`);
  return body;
}

async function post(path, body) {
  const r = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: JSON.stringify(body || {}),
  });
  const res = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(res.error || `${r.status} ${r.statusText}`);
  return res;
}

// act runs a write from a button, disabling it while in flight, then
// refreshes so the page shows what the log now says.
async function act(button, fn, done) {
  button.disabled = true;
  try {
    const res = await fn();
    if (done) toast(done(res));
    await refresh();
    return res;
  } catch (e) {
    toast(e.message, true);
    return null;
  } finally {
    button.disabled = false;
  }
}

// ---- live updates ----

let refreshTimer = 0;
let refreshing = null;

function scheduleRefresh() {
  clearTimeout(refreshTimer);
  refreshTimer = setTimeout(refresh, 250);
}

async function refresh() {
  if (refreshing) return refreshing;
  refreshing = (async () => {
    try {
      status = await getJSON("api/status");
      if (route && route.name === "goal") {
        goalEventsCache = { id: route.id, events: await getJSON(`api/goals/${encodeURIComponent(route.id)}/events`) };
      }
      update();
    } catch (e) {
      toast(e.message, true);
    } finally {
      refreshing = null;
    }
  })();
  return refreshing;
}

function connect(since) {
  // EventSource reconnects by itself and sends Last-Event-ID, so the server
  // resumes from the last event this page received.
  const es = new EventSource(`api/events?since=${since}`);
  es.onopen = () => { live.dataset.state = "live"; live.textContent = "live"; };
  es.onerror = () => { live.dataset.state = "down"; live.textContent = "reconnecting"; };
  es.onmessage = scheduleRefresh;
}

// ---- routing ----

function parseRoute() {
  const m = location.hash.match(/^#\/goal\/(.+)$/);
  return m ? { name: "goal", id: decodeURIComponent(m[1]) } : { name: "overview" };
}

async function navigate() {
  route = parseRoute();
  slots = {};
  if (route.name === "goal") {
    goalEventsCache = { id: null, events: [] };
    renderGoalShell(route.id);
    await refresh();
  } else {
    renderOverviewShell();
    update();
  }
  window.scrollTo(0, 0);
}

function update() {
  if (!status) return;
  if (route.name === "goal") updateGoal(route.id);
  else updateOverview();
}

// ---- overview ----

function renderOverviewShell() {
  slots.hint = h("section");
  slots.stats = h("section");
  slots.budget = h("section");
  slots.goals = h("section");
  fill(app,
    slots.hint,
    slots.stats,
    slots.budget,
    info.read_only ? null : newGoalForm(),
    slots.goals,
  );
}

function newGoalForm() {
  const text = h("textarea", { name: "text", placeholder: "What should be done? e.g. add table-driven tests for parseUsage", required: true });
  const ref = h("input", { type: "text", name: "ref", placeholder: "ref (optional): an issue URL or tracker:ID" });
  const key = h("input", { type: "text", name: "key", placeholder: "idempotency key (optional)" });
  const submit = h("button", { class: "primary", type: "submit" }, "Submit goal");
  const form = h("form", {
    onsubmit: async (e) => {
      e.preventDefault();
      const res = await act(submit,
        () => post("api/goals", { text: text.value, ref: ref.value, key: key.value }),
        (r) => r.duplicate ? `Already submitted as ${r.goal_id}` : `Submitted ${r.goal_id}`);
      if (res) {
        text.value = ""; ref.value = ""; key.value = "";
        location.hash = `#/goal/${encodeURIComponent(res.goal_id)}`;
      }
    },
  }, text, h("div", { class: "row" }, ref, key, submit));
  return h("section", {}, h("h2", {}, "New goal"), h("div", { class: "panel" }, form,
    info.by ? h("p", { class: "muted" }, `Recorded as submitted by ${info.by}, source "ui".`) : null));
}

function updateOverview() {
  const t = status.totals;
  const q = status.merge_queue;
  fill(slots.stats, h("div", { class: "stats" },
    stat(fmtInt(t.goals), "goals"),
    stat(fmtInt(t.tickets), "tasks"),
    stat(fmtInt(t.merged), "merged"),
    stat(fmtInt(t.failed), "failed"),
    stat(fmtInt(t.awaiting), "awaiting approval"),
    stat(fmtInt(t.tokens), "tokens"),
    stat(fmtUSD(t.cost_usd), "cost"),
    stat(fmtDuration(t.wall_seconds), "wall time"),
    stat(fmtDuration(q.wait_mean_seconds), `mean Gate wait (max queue ${q.max_depth})`),
  ));

  const b = status.budget;
  fill(slots.budget, b ? h("div", { class: "panel" },
    h("h2", {}, `Day budget · ${b.day}`),
    h("div", { class: "meta" },
      limit("spend", fmtUSD(b.usd_spent), b.usd_limit ? fmtUSD(b.usd_limit) : null),
      limit("tokens", fmtInt(b.tokens_spent), b.tokens_limit ? fmtInt(b.tokens_limit) : null),
      limit("goals started", fmtInt(b.goals_started), b.goals_limit ? fmtInt(b.goals_limit) : null),
      b.exhausted ? badge("exhausted", "bad") : null)) : null);

  const waiting = status.goals.some((g) => g.outcome === "queued");
  fill(slots.hint, waiting ? h("div", { class: "hint" },
    "Queued goals wait for the Scheduler. This view never runs it — keep ",
    h("code", {}, `aoa run --path ${info.workspace || "."} --interval 5s`),
    " going beside it.") : null);

  const goals = [...status.goals].reverse(); // newest first
  fill(slots.goals, h("h2", {}, "Goals"), goals.length === 0
    ? h("p", { class: "muted" }, "No goals yet.")
    : h("div", { class: "scroll" }, h("table", {},
      h("thead", {}, h("tr", {},
        h("th", {}, "Goal"), h("th", {}, "Text"), h("th", {}, "Outcome"),
        h("th", { title: "Accepted · Verified · Delivered · Complete" }, "Conditions"),
        h("th", { class: "hide-sm" }, "Tasks"), h("th", { class: "hide-sm" }, "Tokens"),
        h("th", {}, "Cost"), h("th", { class: "hide-sm" }, "Submitted"))),
      h("tbody", {}, goals.map(goalRow)))));
}

function stat(value, label) {
  return h("div", { class: "stat" }, h("b", {}, value), h("span", {}, label));
}

function limit(label, spent, max) {
  return h("span", {}, `${label}: `, h("b", {}, spent), max ? ` / ${max}` : " (no limit)");
}

function goalRow(g) {
  const merged = g.tickets.filter((t) => t.status === "merged").length;
  const link = `#/goal/${encodeURIComponent(g.id)}`;
  return h("tr", {},
    h("td", { class: "mono nowrap" }, h("a", { href: link }, g.id)),
    h("td", { class: "text" }, h("a", { href: link }, g.text.length > 140 ? g.text.slice(0, 140) + "…" : g.text)),
    h("td", {}, badge(g.outcome, outcomeTone[g.outcome])),
    h("td", {}, conditionChips(g.conditions)),
    h("td", { class: "num hide-sm" }, `${merged}/${g.tickets.length}`),
    h("td", { class: "num hide-sm" }, fmtInt(g.tokens)),
    h("td", { class: "num" }, fmtUSD(g.cost_usd)),
    h("td", { class: "nowrap muted hide-sm", title: g.submitted_at }, ago(g.submitted_at)));
}

// ---- one goal ----

function renderGoalShell(id) {
  slots.head = h("section");
  slots.conditions = h("section");
  slots.controls = h("section");
  slots.tasks = h("section");
  slots.timeline = h("section");
  const heartbeat = h("input", {
    type: "checkbox",
    onchange: (e) => { showHeartbeats = e.target.checked; updateTimeline(); },
  });
  heartbeat.checked = showHeartbeats;
  slots.timelineBody = h("div");
  fill(slots.timeline,
    h("div", { class: "toolbar" }, h("h2", {}, "Event timeline"),
      h("label", { class: "check" }, heartbeat, "heartbeats")),
    slots.timelineBody);
  fill(app,
    h("p", {}, h("a", { href: "#/" }, "← all goals")),
    slots.head, slots.conditions, slots.controls, slots.tasks, slots.timeline);
  slots.goalID = id;
}

function updateGoal(id) {
  const g = status.goals.find((x) => x.id === id);
  if (!g) {
    fill(slots.head, h("h1", {}, "Unknown goal"), h("p", { class: "muted" }, `No goal ${id} on this workspace's Event Log.`));
    fill(slots.conditions); fill(slots.controls); fill(slots.tasks); fill(slots.timelineBody);
    return;
  }
  const ref = g.ref ? safeURL(g.ref) : null;
  const pr = g.pr_url ? safeURL(g.pr_url) : null;
  fill(slots.head,
    h("h1", {}, g.text),
    h("div", { class: "meta" },
      badge(g.outcome, outcomeTone[g.outcome]),
      g.budget_exceeded ? badge("budget exceeded", "bad") : null,
      h("span", { class: "mono" }, g.id),
      h("span", {}, `source: ${g.source}`),
      g.by ? h("span", {}, `by: ${g.by}`) : null,
      g.ref ? h("span", {}, "ref: ", ref ? h("a", { href: ref, target: "_blank", rel: "noopener noreferrer" }, g.ref) : g.ref) : null,
      h("span", { title: g.submitted_at }, `submitted ${ago(g.submitted_at)}`),
      h("span", {}, `${fmtInt(g.tokens)} tokens · ${fmtUSD(g.cost_usd)}`),
      g.branch ? h("span", { class: "mono" }, `branch: ${g.branch}`) : null,
      pr ? h("a", { href: pr, target: "_blank", rel: "noopener noreferrer" }, "pull request") : null),
    g.delivery_error ? h("p", { class: "failure" }, `Delivery failed: ${g.delivery_error}`) : null,
    g.amendments && g.amendments.length ? h("div", {}, h("h2", {}, "Amendments"),
      h("ol", {}, g.amendments.map((a) => h("li", {}, a)))) : null);

  fill(slots.conditions, h("h2", {}, "Conditions"), h("div", { class: "conditions" },
    (g.conditions || []).map((c) => h("div", { class: "cond " + c.status },
      h("div", {}, h("span", { class: "type" }, c.type), " ", badge(c.status, c.status === "True" ? "ok" : "")),
      h("div", { class: "reason" }, c.reason),
      c.message ? h("div", { class: "msg muted" }, c.message) : null,
      h("div", { class: "muted", title: c.last_transition_time }, `since ${fmtTime(c.last_transition_time)}`)))));

  updateControls(g);
  updateTasks(g);
  updateTimeline();
}

function updateControls(g) {
  const complete = (g.conditions || []).some((c) => c.type === "Complete" && c.status === "True");
  if (info.read_only || complete) {
    fill(slots.controls);
    return;
  }
  // Built once per goal page so a half-typed amendment survives live refreshes.
  if (slots.controls.dataset.goal === g.id) return;
  slots.controls.dataset.goal = g.id;

  const guidance = h("textarea", { placeholder: "Steering guidance for future attempts, e.g. keep the public API unchanged" });
  const amendBtn = h("button", { type: "submit" }, "Amend");
  const amend = h("form", {
    onsubmit: async (e) => {
      e.preventDefault();
      if (await act(amendBtn, () => post(`api/goals/${encodeURIComponent(g.id)}/amend`, { guidance: guidance.value }), () => "Amended")) {
        guidance.value = "";
      }
    },
  }, guidance, h("div", { class: "actions" }, amendBtn));

  const reason = h("input", { type: "text", placeholder: "reason (optional)" });
  const cancelBtn = h("button", { type: "submit", class: "danger" }, "Cancel goal");
  const cancel = h("form", {
    onsubmit: async (e) => {
      e.preventDefault();
      if (!confirm(`Cancel ${g.id}? None of its work will land from now on.`)) return;
      await act(cancelBtn, () => post(`api/goals/${encodeURIComponent(g.id)}/cancel`, { reason: reason.value }),
        (r) => r.already_cancelled ? "Already cancelled" : "Cancelled");
    },
  }, reason, h("div", { class: "actions" }, cancelBtn));

  fill(slots.controls, h("div", { class: "controls" },
    h("div", { class: "panel" }, h("h2", {}, "Amend"), amend),
    h("div", { class: "panel" }, h("h2", {}, "Withdraw"), cancel)));
}

// gateOutput finds the most recent Gate output recorded for a task, from its
// TicketFailed or VerificationFailed events.
function gateOutput(ticketID) {
  const evs = goalEventsCache.events;
  for (let i = evs.length - 1; i >= 0; i--) {
    const e = evs[i];
    if ((e.type === "TicketFailed" || e.type === "VerificationFailed") && e.payload &&
        e.payload.ticket_id === ticketID && e.payload.output) {
      return e.payload.output;
    }
  }
  return "";
}

function updateTasks(g) {
  if (!g.tickets.length) {
    fill(slots.tasks, h("h2", {}, "Tasks"), h("p", { class: "muted" }, "No tasks yet — the Scheduler has not picked this goal up."));
    return;
  }
  fill(slots.tasks, h("h2", {}, "Tasks"), h("div", { class: "scroll" }, h("table", {},
    h("thead", {}, h("tr", {},
      h("th", {}, "Task"), h("th", {}, "Status"), h("th", { class: "hide-sm" }, "Attempts"),
      h("th", { class: "hide-sm" }, "Tokens"), h("th", {}, "Commit"), h("th", {}, ""))),
    h("tbody", {}, g.tickets.map(taskRow)))));
}

function taskRow(t) {
  const detail = [];
  if (t.fail_reason) detail.push(h("div", { class: "failure" }, (t.rejected ? "Rejected: " : "Failed: ") + t.fail_reason));
  if (t.worktree) detail.push(h("div", { class: "muted" }, "Last attempt kept at ", h("code", {}, t.worktree)));
  const out = gateOutput(t.id);
  if (out) {
    detail.push(keepOpen("gate:" + t.id, h("details", {}, h("summary", {}, "Gate output"), h("pre", {}, out))));
  }
  let actions = null;
  if (t.status === "awaiting" && !info.read_only) {
    const approve = h("button", { class: "primary" }, "Approve");
    const reject = h("button", { class: "danger" }, "Reject");
    approve.addEventListener("click", () => act(approve,
      () => post(`api/tickets/${encodeURIComponent(t.id)}/approve`, {}), () => `Approved ${t.id} — the next run merges it`));
    reject.addEventListener("click", () => {
      const why = prompt(`Reject ${t.id}? Reason (optional):`, "");
      if (why === null) return;
      act(reject, () => post(`api/tickets/${encodeURIComponent(t.id)}/reject`, { reason: why }), () => `Rejected ${t.id}`);
    });
    actions = h("div", { class: "actions" }, approve, reject);
  }
  // Set through the CSSOM: the page's CSP forbids inline style attributes.
  const title = h("span", { class: "indent" }, t.depth ? "↳ " : "", t.title);
  title.style.paddingLeft = `${Math.min(t.depth, 8) * 16}px`;
  return h("tr", {},
    h("td", { class: "text" }, title,
      h("div", { class: "mono muted" }, t.id), detail),
    h("td", {}, badge(t.status, ticketTone[t.status])),
    h("td", { class: "num hide-sm" }, t.attempts),
    h("td", { class: "num hide-sm" }, fmtInt(t.tokens)),
    h("td", { class: "mono" }, shortSha(t.commit)),
    h("td", {}, actions));
}

const eventTone = {
  Merged: "ev-ok", VerificationPassed: "ev-ok", ApprovalGranted: "ev-ok", Delivered: "ev-ok",
  VerificationFailed: "ev-bad", TicketFailed: "ev-bad", ApprovalDenied: "ev-bad", DeliveryFailed: "ev-bad",
  GoalBudgetExceeded: "ev-bad", GoalCancelled: "ev-bad", RegressionEscaped: "ev-bad",
  ApprovalRequested: "ev-warn", WorkerStalled: "ev-warn", WorkerRestarted: "ev-warn", GoalAmended: "ev-warn",
  WorkStarted: "ev-run", ProposalSubmitted: "ev-run", TicketClaimed: "ev-run",
};

// summarize is a one-line reading of an event's payload.
function summarize(e) {
  const p = e.payload || {};
  switch (e.type) {
    case "GoalSubmitted": return p.text;
    case "TicketCreated": return p.title + (p.depends_on && p.depends_on.length ? ` (after ${p.depends_on.join(", ")})` : "");
    case "TicketDecomposed": return `split into ${(p.children || []).length} tasks`;
    case "TicketClaimed": return `claimed by ${p.worker}`;
    case "WorkStarted": return `${p.worker} in ${p.worktree}`;
    case "ProposalSubmitted":
      return [p.summary || `commit ${shortSha(p.commit)}`, p.tokens ? `${fmtInt(p.tokens)} tokens` : "", p.model].filter(Boolean).join(" · ");
    case "VerificationPassed": return "Gate passed" + (p.tree ? ` on tree ${shortSha(p.tree)}` : "");
    case "VerificationFailed": return p.reason;
    case "Merged": return `merged ${shortSha(p.commit)}` + (p.branch ? ` onto ${p.branch}` : "");
    case "TicketFailed": return p.reason;
    case "ApprovalRequested": return "passed the Gate; parked for a human decision";
    case "ApprovalGranted": case "ApprovalDenied": case "GoalCancelled":
      return [p.by && `by ${p.by}`, p.reason].filter(Boolean).join(": ");
    case "GoalAmended": return p.guidance;
    case "Delivered": return p.pr_url || "branch pushed";
    case "DeliveryFailed": return p.reason || p.error;
    default: return p.reason || p.summary || p.worker || "";
  }
}

function updateTimeline() {
  if (!slots.timelineBody) return;
  const evs = goalEventsCache.events.filter((e) => showHeartbeats || e.type !== "Heartbeat");
  if (!evs.length) {
    fill(slots.timelineBody, h("p", { class: "muted" }, "No events yet."));
    return;
  }
  fill(slots.timelineBody, h("div", { class: "scroll" }, h("table", {},
    h("thead", {}, h("tr", {}, h("th", {}, "#"), h("th", { class: "hide-sm" }, "Time"), h("th", {}, "Event"),
      h("th", { class: "hide-sm" }, "Task"), h("th", {}, "Detail"))),
    h("tbody", {}, evs.map((e) => h("tr", {},
      h("td", { class: "num muted" }, e.seq),
      h("td", { class: "nowrap mono hide-sm", title: e.ts }, fmtTime(e.ts)),
      h("td", { class: "ev-type " + (eventTone[e.type] || "") }, e.type),
      h("td", { class: "mono hide-sm" }, (e.payload && e.payload.ticket_id) || ""),
      h("td", { class: "text" }, summarize(e) || "",
        keepOpen("ev:" + e.seq, h("details", {}, h("summary", {}, "payload"),
          h("pre", {}, JSON.stringify(e.payload, null, 2)))))))))));
}

// ---- start ----

async function start() {
  try {
    info = await getJSON("api/info");
    document.getElementById("workspace").textContent = info.workspace + (info.read_only ? " · read-only" : "");
    status = await getJSON("api/status");
  } catch (e) {
    fill(app, h("p", { class: "failure" }, `Could not load the workspace: ${e.message}`));
    return;
  }
  window.addEventListener("hashchange", navigate);
  await navigate();
  connect(status.last_seq);
}

start();
