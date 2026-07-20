// kraken web — kanban over /api/board with two legal drag moves:
//   draft → queued   (flips ready: true in the job file)
//   blocked → done    (completes the job, outcome auto-filled from success)
// Un-stalling a blocked cell (answering a question, ▶ send, messaging zeus)
// happens via card buttons, not drag — those are actions on the cell, not a
// board-state assertion the browser is making. Everything else is read-only;
// the daemon drives working/blocked itself from live pod activity.

const COLUMNS = [
  { key: "draft", title: "DRAFT", states: ["draft"] },
  { key: "queued", title: "QUEUED", states: ["queued"] },
  { key: "working", title: "WORKING", states: ["working"] },
  { key: "blocked", title: "BLOCKED", states: ["blocked"] },
  { key: "done", title: "DONE", states: ["done"] },
];

const MOVES = { draft: ["queued"], blocked: ["done"] };

const STATE_COLOR = {
  working: "var(--solved)",
  blocked: "var(--counter)",
  queued: "var(--accent)",
  done: "var(--text-2)",
  draft: "var(--text-2)",
  archived: "var(--text-2)",
};

let jobsBySlug = {};
let dragging = null; // { slug, from }

function columnFor(state) {
  for (const c of COLUMNS) {
    if (c.states.includes(state)) return c.key;
  }
  return "draft";
}

function stateColor(state) {
  return STATE_COLOR[state] || "var(--text-2)";
}

const esc = (s) =>
  String(s ?? "").replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

function age(unix) {
  if (!unix) return "";
  const s = Math.max(0, Math.floor(Date.now() / 1000 - unix));
  if (s < 60) return s + "s";
  if (s < 3600) return Math.floor(s / 60) + "m";
  if (s < 86400) return Math.floor(s / 3600) + "h";
  return Math.floor(s / 86400) + "d";
}

// ---- render ----

// questionHTML renders a card's pending AskUserQuestion. Option buttons only
// make sense for a single, non-multi-select question — that's the one shape
// where "post a single digit" (webAnswerQuestion's whole contract) can answer
// it. Anything richer (more than one question, or a multi-select) can't be
// expressed as one digit, so it renders as read-only header(s) plus a note
// pointing at the pane instead of buttons that would silently mis-answer it.
function questionHTML(j) {
  const questions = j.question.questions || [];
  const single = questions.length === 1 && !questions[0].multi_select;
  if (!single) {
    const headers = questions
      .map((q) => `<div class="q-header">⚑ ${esc(q.header || q.question || "question")}</div>`)
      .join("");
    return `<div class="question">
      ${headers}
      <div class="q-text">answer in the pane (k o ${esc(j.cell)})</div>
    </div>`;
  }
  const q = questions[0];
  const opts = (q.options || [])
    .map(
      (o, i) => `<button class="q-opt" data-slug="${esc(j.slug)}" data-idx="${i + 1}" title="${esc(o.description || "")}">${i + 1}. ${esc(o.label)}</button>`,
    )
    .join("");
  return `<div class="question">
    <div class="q-header">⚑ ${esc(q.header || q.question || "question")}</div>
    ${q.header && q.question ? `<div class="q-text">${esc(q.question)}</div>` : ""}
    <div class="q-opts">${opts}</div>
  </div>`;
}

function cardHTML(j, col) {
  const color = stateColor(j.state);
  const movable = !!MOVES[col];
  const prereqs = (j.prereqs || [])
    .map((p) => {
      const cls = p.done ? "ok" : p.gated ? "gate" : "pend";
      const mark = p.done ? "✓" : p.gated ? "⚑" : "✗";
      return `<span class="${cls}">${esc(p.job)}${mark}</span>`;
    })
    .join(" ");
  const chips = (j.artifacts || [])
    .map(
      (a) =>
        `<button class="chip" data-url="${esc(a.url)}" data-name="${esc(j.slug)}/${esc(a.name)}">▤ ${esc(a.name)}</button>`,
    )
    .join("");
  const roster = (j.roster || [])
    .map((r) => `<div class="roster-row">${esc(r.name)} <span class="dim">(${esc(r.model)})</span></div>`)
    .join("");
  const killable = ["working", "blocked"].includes(col) && j.live;
  const editable = !j.live && ["draft", "queued", "done"].includes(col);
  return `<div class="card ${j.question || j.health ? "flagged" : ""} ${editable ? "editable" : ""}" style="--state:${color}"
      draggable="${movable}" data-slug="${esc(j.slug)}" data-col="${col}" data-editable="${editable}">
    <div class="card-top">
      <span class="slug">${esc(j.slug)}</span>
      <span class="state">● ${esc(j.state)}</span>
      ${killable ? `<button class="kill" data-slug="${esc(j.slug)}" title="kill cell → back to draft">✕</button>` : ""}
    </div>
    <div class="meta">
      <span>${esc(j.type)}</span>
      <span>${esc(j.repo)}</span>
      ${j.live ? `<span>${esc(j.cell)}</span>` : ""}
      <span>${age(j.mtime)}</span>
      ${prereqs ? `<span class="prereq">${prereqs}</span>` : ""}
    </div>
    ${j.err ? `<div class="err">✕ ${esc(j.err)}</div>` : ""}
    ${j.health ? `<div class="err">⚠ ${esc(j.health)}</div>` : ""}
    ${j.detail ? `<div class="detail">${esc(j.detail)}</div>` : ""}
    ${roster ? `<div class="roster">${roster}</div>` : ""}
    ${j.outcome ? `<div class="outcome">→ ${esc(j.outcome)}</div>` : ""}
    ${chips ? `<div class="chips">${chips}</div>` : ""}
    ${j.health && !/is gone|startup prompt/.test(j.health) ? `<button class="btn retry" data-slug="${esc(j.slug)}">↻ retry</button>` : ""}
    ${j.question ? questionHTML(j) : ""}
    ${col === "done" && j.reactivatable ? `<button class="btn reactivate" data-slug="${esc(j.slug)}">↺ reactivate</button>` : ""}
    ${col === "blocked" && j.live && j.type === "build" ? `<button class="btn send" data-slug="${esc(j.slug)}">▶ send</button>` : ""}
    ${col === "blocked" && j.live && j.type === "build" ? `<div class="review-row">
      <button class="btn review-open" data-slug="${esc(j.slug)}" data-editor="nvim">⌨ nvim</button>
      <button class="btn review-open" data-slug="${esc(j.slug)}" data-editor="code">⌨ code</button>
    </div>` : ""}
    ${killable ? `<button class="btn steer" data-slug="${esc(j.slug)}">➤ message zeus</button>` : ""}
  </div>`;
}

function render(board) {
  jobsBySlug = {};
  const byCol = Object.fromEntries(COLUMNS.map((c) => [c.key, []]));
  const archived = [];
  let flags = 0;
  for (const j of board.jobs || []) {
    jobsBySlug[j.slug] = j;
    if (j.archived) {
      archived.push(j);
      continue; // drawer, not a column
    }
    // an unhealthy cell is blocked regardless of its file state — it needs
    // your retry, and the daemon isn't going to fix it
    byCol[j.health ? "blocked" : columnFor(j.state)].push(j);
    if (j.question) flags++;
    if (j.health) flags++;
  }
  document.getElementById("board").innerHTML = COLUMNS.map(
    (c) => `<section class="col ${byCol[c.key].length ? "has-cards" : ""}" data-col="${c.key}">
      <div class="col-head"><b>${c.title}</b><span class="n">${byCol[c.key].length}</span></div>
      <div class="col-cards">
        ${c.key === "draft" ? '<button class="btn-new" id="new-job">+ NEW JOB</button>' : ""}
        ${byCol[c.key].map((j) => cardHTML(j, c.key)).join("") || (c.key === "draft" ? "" : '<div class="empty">·</div>')}
      </div>
      ${c.key === "done" ? archiveDrawerHTML(archived) : ""}
    </section>`,
  ).join("");

  const led = document.getElementById("daemon-led");
  const stat = document.getElementById("daemon-stat");
  if (board.daemon_ok) {
    led.className = "led ok";
    stat.textContent = `daemon pid ${board.daemon.pid} · tick ${age(board.daemon.last_tick)} ago`;
  } else {
    led.className = "led";
    stat.textContent = "daemon not running";
  }
  document.getElementById("cells-stat").textContent = `cells ${board.live_cells}/${board.cell_cap}`;
  document.getElementById("flag-stat").textContent = flags ? `⚑ ${flags}` : "";
  document.getElementById("tick-stat").textContent = new Date().toLocaleTimeString();
}

// ---- archived drawer (collapsible, lives under the DONE column) ----

let archiveOpen = localStorage.getItem("kraken-archive-open") === "1";

function archivedCardHTML(j) {
  const label = j.display_slug || j.slug;
  const renamed = j.display_slug && j.display_slug !== j.slug;
  const chips = (j.artifacts || [])
    .map(
      (a) =>
        `<button class="chip" data-url="${esc(a.url)}" data-name="${esc(label)}/${esc(a.name)}">▤ ${esc(a.name)}</button>`,
    )
    .join("");
  return `<div class="card archived" style="--state:var(--text-2)" data-slug="${esc(j.slug)}">
    <div class="card-top">
      <span class="slug">${esc(label)}</span>
      ${renamed ? `<span class="state">as ${esc(j.slug)}</span>` : ""}
      <span class="state">● archived</span>
    </div>
    <div class="meta">
      <span>${esc(j.type)}</span>
      <span>${esc(j.repo)}</span>
      <span>${age(j.mtime)}</span>
    </div>
    ${j.outcome ? `<div class="outcome">→ ${esc(j.outcome)}</div>` : ""}
    ${chips ? `<div class="chips">${chips}</div>` : ""}
    <div class="review-row">
      <button class="btn unarchive" data-slug="${esc(j.slug)}">⬆ unarchive</button>
      <button class="btn danger delete" data-slug="${esc(j.slug)}">✕ delete forever</button>
    </div>
  </div>`;
}

// archiveDrawerHTML renders the drawer inside the DONE column's section —
// re-created on every render, so its toggle is handled by the delegated
// document click listener below, never a direct element listener.
function archiveDrawerHTML(archived) {
  if (!archived.length) return "";
  return `<div class="archive-drawer">
    <button class="archive-head" id="archive-toggle">
      <span class="tri">${archiveOpen ? "▾" : "▸"}</span> ARCHIVED <span class="n">${archived.length}</span>
    </button>
    <div class="archive-cards" ${archiveOpen ? "" : "hidden"}>
      ${archived.map(archivedCardHTML).join("")}
    </div>
  </div>`;
}

// ---- drag & drop (legal moves only) ----

document.addEventListener("dragstart", (ev) => {
  const card = ev.target.closest(".card[draggable='true']");
  if (!card) return;
  dragging = { slug: card.dataset.slug, from: card.dataset.col };
  card.classList.add("dragging");
  ev.dataTransfer.effectAllowed = "move";
  ev.dataTransfer.setData("text/plain", dragging.slug);
  for (const to of MOVES[dragging.from] || []) {
    document.querySelector(`.col[data-col="${to}"]`)?.classList.add("droptarget");
  }
});

document.addEventListener("dragend", () => {
  dragging = null;
  document.querySelectorAll(".droptarget, .drophover").forEach((el) =>
    el.classList.remove("droptarget", "drophover"));
  document.querySelectorAll(".card.dragging").forEach((el) => el.classList.remove("dragging"));
});

document.addEventListener("dragover", (ev) => {
  const col = ev.target.closest(".col");
  if (!col || !dragging) return;
  if ((MOVES[dragging.from] || []).includes(col.dataset.col)) {
    ev.preventDefault();
    ev.dataTransfer.dropEffect = "move";
    col.classList.add("drophover");
  }
});

document.addEventListener("dragleave", (ev) => {
  ev.target.closest(".col")?.classList.remove("drophover");
});

document.addEventListener("drop", async (ev) => {
  const col = ev.target.closest(".col");
  if (!col || !dragging) return;
  ev.preventDefault();
  const { slug, from } = dragging;
  const to = col.dataset.col;
  if (!(MOVES[from] || []).includes(to)) return;
  if (from === "draft" && to === "queued") await post("/api/queue", { slug });
  else if (from === "blocked" && to === "done") await post("/api/done", { slug });
});

async function post(url, body) {
  try {
    const r = await fetch(url, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    const data = await r.json();
    if (!r.ok) throw new Error(data.error || r.status);
    await refresh();
    return true;
  } catch (e) {
    document.getElementById("tick-stat").textContent = "✕ " + e.message;
    await refresh();
    return false;
  }
}

// ---- markdown (tiny subset) ----

function inlineMD(s) {
  return esc(s)
    .replace(/`([^`]+)`/g, "<code>$1</code>")
    .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
    .replace(/\*([^*]+)\*/g, "<em>$1</em>")
    .replace(/\[([^\]]+)\]\((https?:[^)\s]+)\)/g, '<a href="$2" target="_blank" rel="noreferrer">$1</a>');
}

function renderMD(src) {
  const out = [];
  const lines = src.split("\n");
  let i = 0;
  let list = null;
  const closeList = () => { if (list) { out.push(`</${list}>`); list = null; } };
  while (i < lines.length) {
    const line = lines[i];
    if (line.startsWith("```")) {
      closeList();
      const buf = [];
      i++;
      while (i < lines.length && !lines[i].startsWith("```")) buf.push(lines[i++]);
      i++;
      out.push(`<pre><code>${esc(buf.join("\n"))}</code></pre>`);
      continue;
    }
    const h = line.match(/^(#{1,4})\s+(.*)/);
    if (h) { closeList(); out.push(`<h${h[1].length}>${inlineMD(h[2])}</h${h[1].length}>`); i++; continue; }
    const ul = line.match(/^\s*[-*]\s+(.*)/);
    const ol = line.match(/^\s*\d+[.)]\s+(.*)/);
    if (ul || ol) {
      const want = ul ? "ul" : "ol";
      if (list !== want) { closeList(); out.push(`<${want}>`); list = want; }
      out.push(`<li>${inlineMD((ul || ol)[1])}</li>`);
      i++;
      continue;
    }
    if (line.startsWith(">")) { closeList(); out.push(`<blockquote>${inlineMD(line.slice(1).trim())}</blockquote>`); i++; continue; }
    // GitHub-flavored pipe table: a line with | immediately followed by a
    // separator row (| --- | :--: | ---: |). A stray | in prose is left alone
    // because it requires that separator line to trigger.
    if (line.includes("|") && i + 1 < lines.length &&
        /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/.test(lines[i + 1])) {
      closeList();
      const splitRow = (r) => r.trim().replace(/^\|/, "").replace(/\|$/, "").split("|").map((c) => c.trim());
      const headers = splitRow(line);
      const aligns = splitRow(lines[i + 1]).map((c) => {
        const l = c.startsWith(":"), r = c.endsWith(":");
        return l && r ? "center" : r ? "right" : "left";
      });
      i += 2;
      const rows = [];
      while (i < lines.length && lines[i].trim() && lines[i].includes("|")) rows.push(splitRow(lines[i++]));
      const cell = (c, tag, k) => `<${tag} style="text-align:${aligns[k] || "left"}">${inlineMD(c || "")}</${tag}>`;
      let t = '<div class="md-table"><table><thead><tr>';
      headers.forEach((h, k) => { t += cell(h, "th", k); });
      t += "</tr></thead><tbody>";
      rows.forEach((row) => {
        t += "<tr>";
        for (let k = 0; k < headers.length; k++) t += cell(row[k], "td", k);
        t += "</tr>";
      });
      out.push(t + "</tbody></table></div>");
      continue;
    }
    if (line.trim() === "") { closeList(); i++; continue; }
    closeList();
    out.push(`<p>${inlineMD(line)}</p>`);
    i++;
  }
  closeList();
  return out.join("\n");
}

// ---- artifact overlay ----

const overlay = document.getElementById("overlay");
const panelBody = document.getElementById("panel-body");
let rawText = "";
let showRaw = false;
let artifactURL = "";

const isPage = (url) => /\.(html|svg|pdf)$/i.test(url);
const isImage = (url) => /\.(png|jpe?g|gif|webp)$/i.test(url);

function openArtifact(url, name) {
  document.getElementById("panel-title").textContent = name;
  artifactURL = url;
  rawText = "";
  showRaw = false;
  overlay.hidden = false;
  document.getElementById("panel-raw").hidden = isImage(url) || /\.pdf$/i.test(url);
  if (isImage(url)) {
    panelBody.className = "panel-body page";
    panelBody.innerHTML = `<img src="${esc(url)}" alt="artifact">`;
    return;
  }
  if (isPage(url)) {
    // rendered pages live in a same-origin iframe: full fidelity, and their
    // styles/scripts can't leak into the board
    panelBody.className = "panel-body page";
    panelBody.innerHTML = `<iframe src="${esc(url)}" title="artifact"></iframe>`;
    return;
  }
  panelBody.className = "panel-body";
  panelBody.textContent = "loading…";
  fetch(url)
    .then((r) => { if (!r.ok) throw new Error(r.status); return r.text(); })
    .then((text) => {
      rawText = text;
      panelBody.className = "panel-body md";
      panelBody.innerHTML = renderMD(text);
    })
    .catch((e) => { panelBody.textContent = "failed to load: " + e; });
}

document.addEventListener("click", (ev) => {
  const chip = ev.target.closest(".chip");
  if (chip) {
    openArtifact(chip.dataset.url, chip.dataset.name);
    return;
  }
  const qopt = ev.target.closest(".q-opt");
  if (qopt) {
    document.querySelectorAll(`.q-opt[data-slug="${qopt.dataset.slug}"]`).forEach((b) => (b.disabled = true));
    post("/api/question", { slug: qopt.dataset.slug, text: qopt.dataset.idx });
    return;
  }
  const send = ev.target.closest(".send");
  if (send) {
    send.disabled = true;
    post("/api/send", { slug: send.dataset.slug });
    return;
  }
  const retry = ev.target.closest(".retry");
  if (retry) {
    retry.disabled = true;
    post("/api/retry", { slug: retry.dataset.slug });
    return;
  }
  const archiveToggle = ev.target.closest("#archive-toggle");
  if (archiveToggle) {
    archiveOpen = !archiveOpen;
    localStorage.setItem("kraken-archive-open", archiveOpen ? "1" : "0");
    refresh();
    return;
  }
  const unarchive = ev.target.closest(".unarchive");
  if (unarchive) {
    post("/api/unarchive", { slug: unarchive.dataset.slug });
    return;
  }
  const del = ev.target.closest("button.delete");
  if (del) {
    const slug = del.dataset.slug;
    if (confirm(`delete ${slug} forever? the archived job file and its artifact copies are permanently removed.`)) {
      post("/api/delete", { slug });
    }
    return;
  }
  const reactivate = ev.target.closest(".reactivate");
  if (reactivate) {
    openSteer(reactivate.dataset.slug, "reactivate");
    return;
  }
  const kill = ev.target.closest(".kill");
  if (kill) {
    const slug = kill.dataset.slug;
    if (confirm(`kill ${slug}? the cell is torn down (artifacts preserved) and the job returns to draft.`)) {
      post("/api/kill", { slug });
    }
    return;
  }
  const steer = ev.target.closest(".steer");
  if (steer) {
    openSteer(steer.dataset.slug);
    return;
  }
  const review = ev.target.closest(".review-open");
  if (review) {
    post("/api/review", { slug: review.dataset.slug, text: review.dataset.editor });
    return;
  }
  if (ev.target.closest("#new-job")) {
    openEditor(null);
    return;
  }
  // editable cards open their spec on plain click (buttons/chips above
  // already returned first)
  const card = ev.target.closest(".card[data-editable='true']");
  if (card) openEditor(card.dataset.slug);
});

// ---- steer: mid-run instructions ----

const steerOverlay = document.getElementById("steer-overlay");
let steerSlug = null;
let steerMode = "steer"; // "steer" | "reactivate" — same modal, different endpoint

function openSteer(slug, mode = "steer") {
  steerSlug = slug;
  steerMode = mode;
  document.getElementById("steer-title").textContent =
    mode === "reactivate" ? slug + " — reactivate (same zeus session)" : slug + " — message zeus";
  const text = document.getElementById("steer-text");
  text.placeholder =
    mode === "reactivate"
      ? "optional: what to adjust — delivered as the resume prompt to the job's original zeus session (leave empty to just wake it)"
      : "message zeus directly — steering / pivot instruction, delivered straight to its pane";
  text.value = "";
  document.getElementById("steer-note").textContent = "";
  document.getElementById("steer-send").textContent = mode === "reactivate" ? "reactivate ⌘⏎" : "send ⌘⏎";
  steerOverlay.hidden = false;
  text.focus();
}

document.getElementById("steer-send").addEventListener("click", async () => {
  const text = document.getElementById("steer-text").value.trim();
  // a reactivation message is optional; a steer without text is a mistake
  if (!text && steerMode !== "reactivate") {
    document.getElementById("steer-note").textContent = "message is empty";
    return;
  }
  const btn = document.getElementById("steer-send");
  btn.disabled = true;
  const ok = await post(steerMode === "reactivate" ? "/api/reactivate" : "/api/steer", { slug: steerSlug, text });
  btn.disabled = false;
  if (ok) steerOverlay.hidden = true;
  else document.getElementById("steer-note").textContent = "send failed — see status bar";
});
document.getElementById("steer-cancel").addEventListener("click", () => (steerOverlay.hidden = true));
steerOverlay.addEventListener("click", (ev) => { if (ev.target === steerOverlay) steerOverlay.hidden = true; });
document.getElementById("steer-text").addEventListener("keydown", (ev) => {
  if (ev.key === "Enter" && (ev.metaKey || ev.ctrlKey)) {
    ev.preventDefault();
    document.getElementById("steer-send").click();
  }
});

// ---- job editor (draft + spec in the browser) ----

const editorOverlay = document.getElementById("editor-overlay");
const editorText = document.getElementById("editor-text");
const editorNote = document.getElementById("editor-note");
let editorSlug = null;
let editorMTime = "";

function prereqRowHTML(p) {
  return `<div class="prereq-row">
    <input type="text" list="job-list" value="${esc(p.job || "")}" placeholder="job-slug" spellcheck="false">
    <label class="check"><input type="checkbox" ${p.gated ? "checked" : ""}> gated ⚑</label>
    <button class="rm" title="remove">✕</button>
  </div>`;
}

function fillForm(d) {
  document.getElementById("f-type").value = d.type || "build";
  document.getElementById("f-repo").value = d.repo || "";
  document.getElementById("f-base").value = d.base || "";
  document.getElementById("f-ready").checked = !!d.ready;
  document.getElementById("f-solo").checked = !!d.solo;
  document.getElementById("f-auto").checked = !!d.auto_approve;
  document.getElementById("f-success").value = d.success || "";
  document.getElementById("f-prereqs").innerHTML = (d.prereqs || []).map(prereqRowHTML).join("");
  editorText.value = d.spec || "";
  document.getElementById("repo-list").innerHTML = (d.repos || []).map((x) => `<option value="${esc(x)}">`).join("");
  document.getElementById("job-list").innerHTML = (d.slugs || []).map((x) => `<option value="${esc(x)}">`).join("");
}

function readForm() {
  const prereqs = [...document.querySelectorAll("#f-prereqs .prereq-row")]
    .map((row) => ({
      job: row.querySelector('input[type="text"]').value.trim(),
      gated: row.querySelector('input[type="checkbox"]').checked,
    }))
    .filter((p) => p.job);
  return {
    slug: editorSlug,
    type: document.getElementById("f-type").value,
    repo: document.getElementById("f-repo").value.trim(),
    base: document.getElementById("f-base").value.trim(),
    ready: document.getElementById("f-ready").checked,
    solo: document.getElementById("f-solo").checked,
    auto_approve: document.getElementById("f-auto").checked,
    success: document.getElementById("f-success").value,
    prereqs,
    spec: editorText.value,
    mtime: editorMTime,
  };
}

async function loadForm(slug) {
  const r = await fetch("/api/job/form?slug=" + encodeURIComponent(slug));
  const d = await r.json();
  if (d.error) throw new Error(d.error);
  if (!d.editable) throw new Error("job has a cell in flight — file belongs to the crew");
  editorSlug = slug;
  editorMTime = d.mtime;
  fillForm(d);
  document.getElementById("editor-title").textContent = slug + ".job.md";
  document.getElementById("editor-newrow").hidden = true;
  document.getElementById("editor-form").hidden = false;
  document.getElementById("editor-save").disabled = false;
  // archive lives here, inside the opened job, on purpose: no card-level
  // shortcut — the server still refuses jobs with a cell in flight
  document.getElementById("editor-archive").hidden = false;
  if (d.err) editorNote.textContent = "current file won't run: " + d.err;
}

function openEditor(slug) {
  editorSlug = slug;
  editorMTime = "";
  editorNote.textContent = "";
  editorNote.style.color = "";
  const isNew = !slug;
  document.getElementById("editor-title").textContent = isNew ? "new job" : slug + ".job.md";
  document.getElementById("editor-newrow").hidden = !isNew;
  document.getElementById("editor-form").hidden = true;
  document.getElementById("editor-save").disabled = true;
  document.getElementById("editor-force").hidden = true;
  document.getElementById("editor-archive").hidden = true;
  document.getElementById("editor-slug").value = "";
  editorOverlay.hidden = false;
  if (isNew) {
    document.getElementById("editor-slug").focus();
    return;
  }
  loadForm(slug).catch((e) => { editorNote.textContent = String(e.message || e); });
}

document.getElementById("editor-create").addEventListener("click", async () => {
  const slug = document.getElementById("editor-slug").value.trim();
  if (!slug) {
    editorNote.textContent = "give it a slug";
    return;
  }
  try {
    const r = await fetch("/api/job/create", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ slug }),
    });
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || r.status);
    await loadForm(d.slug);
    editorText.focus();
    refresh();
  } catch (e) {
    editorNote.textContent = String(e.message || e);
  }
});

document.getElementById("f-add-prereq").addEventListener("click", () => {
  document.getElementById("f-prereqs").insertAdjacentHTML("beforeend", prereqRowHTML({}));
  const rows = document.querySelectorAll("#f-prereqs .prereq-row");
  rows[rows.length - 1].querySelector("input").focus();
});
document.getElementById("f-prereqs").addEventListener("click", (ev) => {
  if (ev.target.closest(".rm")) ev.target.closest(".prereq-row").remove();
});

async function saveForm(force) {
  if (!editorSlug) return;
  const forceBtn = document.getElementById("editor-force");
  try {
    const r = await fetch("/api/job/saveform", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ...readForm(), force: !!force }),
    });
    const d = await r.json();
    if (!r.ok) {
      if (d.conflict) {
        forceBtn.hidden = false;
        throw new Error(d.error + " — save again after reloading, or overwrite disk");
      }
      throw new Error(d.error || r.status);
    }
    forceBtn.hidden = true;
    editorMTime = d.mtime;
    editorNote.textContent = d.warn ? "saved — but won't run: " + d.warn : "saved ✓";
    editorNote.style.color = d.warn ? "" : "var(--solved)";
    refresh();
  } catch (e) {
    editorNote.style.color = "";
    editorNote.textContent = String(e.message || e);
  }
}

document.getElementById("editor-save").addEventListener("click", () => saveForm(false));
document.getElementById("editor-force").addEventListener("click", () => {
  if (confirm("overwrite " + editorSlug + ".job.md? whatever changed on disk since you opened the editor is lost.")) {
    saveForm(true);
  }
});

document.getElementById("editor-archive").addEventListener("click", async () => {
  if (!editorSlug) return;
  const ok = await post("/api/archive", { slug: editorSlug });
  if (ok) editorOverlay.hidden = true;
  else editorNote.textContent = "archive failed — see status bar";
});

document.getElementById("editor-cancel").addEventListener("click", () => (editorOverlay.hidden = true));
editorOverlay.addEventListener("click", (ev) => { if (ev.target === editorOverlay) editorOverlay.hidden = true; });
document.getElementById("editor-form").addEventListener("keydown", (ev) => {
  if (ev.key === "Enter" && (ev.metaKey || ev.ctrlKey)) {
    ev.preventDefault();
    document.getElementById("editor-save").click();
  }
});
document.getElementById("editor-slug").addEventListener("keydown", (ev) => {
  if (ev.key === "Enter") {
    ev.preventDefault();
    document.getElementById("editor-create").click();
  }
});

document.getElementById("panel-close").addEventListener("click", () => (overlay.hidden = true));
document.getElementById("panel-open").addEventListener("click", () => {
  if (artifactURL) window.open(artifactURL, "_blank");
});
document.getElementById("panel-raw").addEventListener("click", async () => {
  showRaw = !showRaw;
  if (showRaw) {
    if (!rawText) {
      // pages render via iframe without a fetch; raw needs the text
      try { rawText = await (await fetch(artifactURL)).text(); } catch { rawText = "failed to load"; }
    }
    panelBody.className = "panel-body raw";
    panelBody.textContent = rawText;
  } else if (isPage(artifactURL)) {
    panelBody.className = "panel-body page";
    panelBody.innerHTML = `<iframe src="${esc(artifactURL)}" title="artifact"></iframe>`;
  } else {
    panelBody.className = "panel-body md";
    panelBody.innerHTML = renderMD(rawText);
  }
});
overlay.addEventListener("click", (ev) => { if (ev.target === overlay) overlay.hidden = true; });
document.addEventListener("keydown", (ev) => {
  if (ev.key === "Escape") { overlay.hidden = true; editorOverlay.hidden = true; steerOverlay.hidden = true; }
});

// ---- dag view: jobs layered by dependency depth, edges = prereqs ----

let view = localStorage.getItem("kraken-view") || "board";

document.querySelectorAll(".viewtab").forEach((tab) =>
  tab.addEventListener("click", () => {
    view = tab.dataset.view;
    localStorage.setItem("kraken-view", view);
    document.querySelectorAll(".viewtab").forEach((t) =>
      t.classList.toggle("active", t.dataset.view === view));
    document.getElementById("board").hidden = view !== "board";
    document.getElementById("dag").hidden = view !== "dag";
    refresh();
  }));

function applyView() {
  document.querySelectorAll(".viewtab").forEach((t) =>
    t.classList.toggle("active", t.dataset.view === view));
  document.getElementById("board").hidden = view !== "board";
  document.getElementById("dag").hidden = view !== "dag";
}

const NODE_W = 190;
const NODE_H = 52;
const GAP_X = 90;
const GAP_Y = 22;

// dagLayers assigns each job a column: roots at 0, dependents one past their
// deepest prereq. Cycles (already erred by the parser) fall out of the
// resolution loop and go in a final column.
function dagLayers(jobs) {
  const bySlug = Object.fromEntries(jobs.map((j) => [j.slug, j]));
  const depth = {};
  let progress = true;
  while (progress) {
    progress = false;
    for (const j of jobs) {
      if (depth[j.slug] !== undefined) continue;
      const deps = (j.prereqs || []).filter((p) => bySlug[p.job]);
      if (deps.every((p) => depth[p.job] !== undefined)) {
        depth[j.slug] = deps.length ? 1 + Math.max(...deps.map((p) => depth[p.job])) : 0;
        progress = true;
      }
    }
  }
  const maxD = Math.max(0, ...Object.values(depth));
  for (const j of jobs) if (depth[j.slug] === undefined) depth[j.slug] = maxD + 1; // cycle members
  return depth;
}

function renderDag(board) {
  // archived jobs stay out of the dag: they're out of the active namespace,
  // and their prereq slugs may now point at unrelated newer jobs
  const jobs = (board.jobs || []).filter((j) => !j.archived);
  const nodesEl = document.getElementById("dag-nodes");
  const edgesEl = document.getElementById("dag-edges");
  if (!jobs.length) {
    nodesEl.innerHTML = '<div class="dag-empty">no jobs — + new on the board creates one</div>';
    edgesEl.innerHTML = "";
    return;
  }
  const depth = dagLayers(jobs);
  const layers = {};
  for (const j of jobs) (layers[depth[j.slug]] ||= []).push(j);
  const pos = {};
  let maxX = 0;
  let maxY = 0;
  for (const [d, js] of Object.entries(layers)) {
    js.sort((a, b) => a.slug.localeCompare(b.slug));
    js.forEach((j, i) => {
      const x = d * (NODE_W + GAP_X);
      const y = i * (NODE_H + GAP_Y);
      pos[j.slug] = { x, y };
      maxX = Math.max(maxX, x + NODE_W);
      maxY = Math.max(maxY, y + NODE_H);
    });
  }
  nodesEl.style.height = maxY + 20 + "px";
  nodesEl.innerHTML = jobs
    .map((j) => {
      const p = pos[j.slug];
      const editable = !j.live && ["draft", "queued", "done"].includes(columnFor(j.state));
      return `<div class="dag-node ${j.question || j.health ? "flagged" : ""} ${editable ? "editable" : ""}"
        style="left:${p.x}px;top:${p.y}px;--state:${stateColor(j.state)}"
        data-slug="${esc(j.slug)}" data-editable="${editable}">
        <span class="slug">${esc(j.slug)}</span>
        <span class="nstate">● ${esc(j.state)}</span><span class="ntype">${esc(j.type)}</span>
      </div>`;
    })
    .join("");
  const canvas = document.getElementById("dag-canvas");
  canvas.style.width = maxX + 20 + "px";
  canvas.style.height = maxY + 20 + "px";
  edgesEl.setAttribute("viewBox", `0 0 ${maxX + 20} ${maxY + 20}`);
  edgesEl.setAttribute("width", maxX + 20);
  edgesEl.setAttribute("height", maxY + 20);
  const bySlug = Object.fromEntries(jobs.map((j) => [j.slug, j]));
  let paths = "";
  for (const j of jobs) {
    for (const p of j.prereqs || []) {
      const from = pos[p.job];
      const to = pos[j.slug];
      if (!from || !to) continue;
      const x1 = from.x + NODE_W;
      const y1 = from.y + NODE_H / 2;
      const x2 = to.x;
      const y2 = to.y + NODE_H / 2;
      const mx = (x1 + x2) / 2;
      const cls = p.gated ? "gated" : p.done ? "done" : "";
      paths += `<path class="${cls}" d="M${x1},${y1} C${mx},${y1} ${mx},${y2} ${x2},${y2}"/>`;
      if (p.gated) paths += `<text x="${mx - 5}" y="${(y1 + y2) / 2 - 4}">⚑</text>`;
    }
  }
  edgesEl.innerHTML = paths;
}

document.getElementById("dag-nodes").addEventListener("click", (ev) => {
  const node = ev.target.closest(".dag-node");
  if (node && node.dataset.editable === "true") openEditor(node.dataset.slug);
});

// ---- poll (paused while dragging so the board doesn't rebuild mid-drag) ----

async function refresh() {
  if (dragging) return;
  try {
    const r = await fetch("/api/board");
    const board = await r.json();
    render(board);
    if (view === "dag") renderDag(board);
  } catch {
    document.getElementById("daemon-stat").textContent = "kraken web server unreachable";
    document.getElementById("daemon-led").className = "led";
  }
}
applyView();
refresh();
setInterval(refresh, 3000);
