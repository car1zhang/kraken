// kraken web — kanban over /api/board with two legal drag moves:
//   draft → queued   (flips ready: true in the job file)
//   blocked → done    (completes the job)
// Un-stalling a blocked cell (answering a question, messaging zeus, marking
// it done) happens via the card click → steer modal, not drag — those are
// actions on the cell, not a board-state assertion the browser is making.
// Everything else is read-only; the daemon drives working/blocked itself
// from live pod activity.

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

// serialized board.jobs of the last DOM rebuild — refresh() skips rebuilding
// #board while it still matches, so any client-side state change that
// affects markup (but doesn't itself change the jobs payload) must call
// invalidateBoard() before its next refresh().
let boardSnapshot = "";

function invalidateBoard() {
  boardSnapshot = "";
}

// per-slug detail-expanded state, persisted the same way archiveOpen is
// (see archiveOpen below) so it survives the 3s poll re-render, which
// rebuilds #board innerHTML and would otherwise wipe any .expanded class
let expandedDetails = new Set(JSON.parse(localStorage.getItem("kraken-expanded-details") || "[]"));

// per-slug artifact-chips-expanded state, same persistence pattern as
// expandedDetails above
let expandedArtifacts = new Set(JSON.parse(localStorage.getItem("kraken-expanded-artifacts") || "[]"));

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

// detailClampable heuristically decides whether a detail block is long
// enough to plausibly get clamped by the CSS line-clamp — only then does it
// make sense to show a pointer cursor / accept clicks to expand it.
function detailClampable(text) {
  return text.length > 240 || (text.match(/\n/g) || []).length >= 6;
}

// prChipHTML renders one linked chip for a job's PR: "#<N> <state>" parsed
// from the URL's trailing "/pull/<N>", falling back to the state alone if
// the URL doesn't match that shape (still a valid, clickable link).
function prChipHTML(pr) {
  const m = /\/pull\/(\d+)(?:[/?#]|$)/.exec(pr.url || "");
  const label = m ? `#${m[1]} ${pr.state}` : pr.state;
  const cls = pr.state === "merged" ? "pr-merged" : pr.state === "open" ? "pr-open" : "pr-closed";
  // job files are hand-editable, so only linkify http(s) urls — esc() stops
  // attribute breakout but not a javascript: scheme
  if (!/^https?:\/\//i.test(pr.url || "")) {
    return `<span class="pr-chip ${cls}">${esc(label)}</span>`;
  }
  return `<a class="pr-chip ${cls}" href="${esc(pr.url)}" target="_blank" rel="noreferrer">${esc(label)}</a>`;
}

// chipsHTML renders a job's artifact chips, collapsing to the first 3 (plus
// a "+N more" toggle) unless slug is in expandedArtifacts — slug keys the
// expanded state, label prefixes each chip's data-name (differs from slug
// for a renamed archived job)
function chipsHTML(artifacts, slug, label) {
  const list = artifacts || [];
  if (!list.length) return "";
  const expanded = expandedArtifacts.has(slug);
  const shown = expanded ? list : list.slice(0, 3);
  const chips = shown
    .map(
      (a) =>
        `<button class="chip" data-url="${esc(a.url)}" data-name="${esc(label)}/${esc(a.name)}">▤ ${esc(a.name)}</button>`,
    )
    .join("");
  const toggle =
    list.length > 3
      ? `<button class="chip chip-more" data-slug="${esc(slug)}">${expanded ? "less" : `+${list.length - 3} more`}</button>`
      : "";
  return chips + toggle;
}

function cardHTML(j, col) {
  const color = stateColor(j.state);
  const movable = !!MOVES[col];
  const prs = (j.prs || []).map(prChipHTML).join("");
  const prereqs = (j.prereqs || [])
    .map((p) => {
      const cls = p.done ? "ok" : p.gated ? "gate" : "pend";
      const mark = p.done ? "✓" : p.gated ? "⚑" : "✗";
      return `<span class="${cls}">${esc(p.job)}${mark}</span>`;
    })
    .join(" ");
  const chips = chipsHTML(j.artifacts, j.slug, j.slug);
  const roster = (j.roster || [])
    .map((r) => `<div class="roster-row">${esc(r.name)} <span class="dim">(${esc(r.model)})</span></div>`)
    .join("");
  const editable = !j.live && ["draft", "queued", "done"].includes(col);
  return `<div class="card editable ${j.question || j.health ? "flagged" : ""}" style="--state:${color}"
      draggable="${movable}" data-slug="${esc(j.slug)}" data-col="${col}" data-editable="${editable}">
    <div class="card-top">
      <span class="slug">${esc(j.slug)}</span>
      <span class="state">● ${esc(j.state)}</span>
    </div>
    ${prs ? `<div class="prs">${prs}</div>` : ""}
    <div class="meta">
      <span>${esc(j.type)}</span>
      <span>${esc(j.repo)}</span>
      ${j.live ? `<span>${esc(j.cell)}</span>` : ""}
      <span data-age="${j.mtime}">${age(j.mtime)}</span>
      ${prereqs ? `<span class="prereq">${prereqs}</span>` : ""}
    </div>
    ${j.live ? `<div class="review-row"><button class="btn tmux-open open-tmux" data-slug="${esc(j.slug)}" title="raise zeus' pane in Ghostty">⧉ tmux</button></div>` : ""}
    ${j.err ? `<div class="err">✕ ${esc(j.err)}</div>` : ""}
    ${j.health ? `<div class="err">⚠ ${esc(j.health)}</div>` : ""}
    ${j.last_sent ? `<div class="last-sent">me→zeus: ${esc(j.last_sent)}</div>` : ""}
    ${j.detail ? `<div class="detail${detailClampable(j.detail) ? " clampable" : ""}${expandedDetails.has(j.slug) ? " expanded" : ""}">${esc(j.detail)}</div>` : ""}
    ${roster ? `<div class="roster">${roster}</div>` : ""}
    ${j.outcome && col === "done" ? `<div class="outcome">→ ${esc(j.outcome)}</div>` : ""}
    ${chips ? `<div class="chips">${chips}</div>` : ""}
    ${j.health && !/is gone|startup prompt/.test(j.health) ? `<button class="btn retry" data-slug="${esc(j.slug)}">↻ retry</button>` : ""}
    ${j.question ? questionHTML(j) : ""}
    ${col === "done" && j.reactivatable ? `<button class="btn reactivate" data-slug="${esc(j.slug)}">↺ reactivate</button>` : ""}
  </div>`;
}

// checkStaleModals closes job-scoped modals (steer/unblock/reactivate,
// job editor) when the poll shows the job they're open for has moved to a
// different state since the modal was opened — the card behind it already
// reflects the new state, so a stale modal is just confusing (e.g. an
// "unblock" composer sitting on top of a job that unblocked itself). Not
// wired to the artifact overlay: that one isn't scoped to a job's state.
function checkStaleModals() {
  if (steerOverlay && !steerOverlay.hidden && steerSlug) {
    const j = jobsBySlug[steerSlug];
    if (!j || j.state !== steerOpenState) {
      flushSteerDraft(); // text survives server-side even though the modal is closing itself
      steerOverlay.hidden = true;
    }
  }
  if (editorOverlay && !editorOverlay.hidden && editorSlug) {
    const j = jobsBySlug[editorSlug];
    if (!editorStateCaptured) {
      // freshly-created job: take the baseline as soon as the board catches
      // up instead of comparing against "not captured yet"
      if (j) {
        editorOpenState = j.state;
        editorStateCaptured = true;
      }
    } else if (!j || j.state !== editorOpenState) {
      flushEditorAutosave();
      editorOverlay.hidden = true;
    }
  }
}

// render does the cheap, always-correct-regardless-of-payload-change work:
// rebuilds jobsBySlug, closes stale modals, and refreshes the status bar.
// Returns the column buckets so the caller can decide whether the (much
// pricier) #board innerHTML rebuild is actually warranted — see refresh().
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
  checkStaleModals();

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

  // stale-binary self-check (see selfcheck.go): both fields are absent on an
  // older server, which reads as "not stale" — never a hidden error
  const banner = document.getElementById("stale-banner");
  const webStale = !!board.stale;
  const daemonStale = !!(board.daemon_ok && board.daemon && board.daemon.stale);
  if (webStale || daemonStale) {
    const msgs = [];
    if (webStale) msgs.push("kraken was reinstalled since this server started — restart k web");
    if (daemonStale) msgs.push("kraken was reinstalled since the daemon started — restart the daemon");
    banner.textContent = "⚠ " + msgs.join(" · ");
    banner.hidden = false;
  } else {
    banner.hidden = true;
  }

  return { byCol, archived };
}

// renderBoardDOM is the expensive part of render(): only called when
// board.jobs actually changed (see refresh()).
function renderBoardDOM(byCol, archived) {
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
}

// updateAges refreshes the age chips in place without touching the rest of
// the DOM — runs every poll (see refresh()), including polls that skip the
// full board rebuild, so "3m" keeps ticking toward "4m" even when nothing
// else changed. textContent only — never innerHTML.
function updateAges() {
  document.querySelectorAll("[data-age]").forEach((el) => {
    el.textContent = age(+el.dataset.age);
  });
}

// ---- archived drawer (collapsible, lives under the DONE column) ----

let archiveOpen = localStorage.getItem("kraken-archive-open") === "1";

function archivedCardHTML(j) {
  const label = j.display_slug || j.slug;
  const renamed = j.display_slug && j.display_slug !== j.slug;
  const chips = chipsHTML(j.artifacts, j.slug, label);
  return `<div class="card archived" style="--state:var(--text-2)" data-slug="${esc(j.slug)}">
    <div class="card-top">
      <span class="slug">${esc(label)}</span>
      ${renamed ? `<span class="state">as ${esc(j.slug)}</span>` : ""}
      <span class="state">● archived</span>
    </div>
    <div class="meta">
      <span>${esc(j.type)}</span>
      <span>${esc(j.repo)}</span>
      <span data-age="${j.mtime}">${age(j.mtime)}</span>
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
    // server state changed and the current DOM may show disabled buttons —
    // force a rebuild even if the jobs payload happens to match
    invalidateBoard();
    await refresh();
    return true;
  } catch (e) {
    document.getElementById("tick-stat").textContent = "✕ " + e.message;
    invalidateBoard();
    await refresh();
    return false;
  }
}

// debounce: shared by the steer draft sync and the editor autosave below.
// flush() fires the last pending call immediately (every modal-close path
// uses this) and returns its result so a caller can await it; cancel() drops
// it unfired (used when the server already applied what we'd otherwise
// resend, so firing late would resurrect stale state).
function debounce(fn, ms) {
  let timer = null;
  let pending = null;
  const wrapped = (...args) => {
    pending = args;
    clearTimeout(timer);
    timer = setTimeout(() => {
      timer = null;
      const a = pending;
      pending = null;
      fn(...a);
    }, ms);
  };
  wrapped.flush = () => {
    if (timer === null) return undefined;
    clearTimeout(timer);
    timer = null;
    const a = pending;
    pending = null;
    return fn(...a);
  };
  wrapped.cancel = () => {
    clearTimeout(timer);
    timer = null;
    pending = null;
  };
  return wrapped;
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
  // must come before the .chip branch below — chip-more also matches .chip
  // (it needs the same size/padding) but must never open an artifact
  const chipMore = ev.target.closest(".chip-more");
  if (chipMore) {
    const slug = chipMore.dataset.slug;
    if (expandedArtifacts.has(slug)) expandedArtifacts.delete(slug);
    else expandedArtifacts.add(slug);
    localStorage.setItem("kraken-expanded-artifacts", JSON.stringify([...expandedArtifacts]));
    invalidateBoard();
    refresh();
    if (!steerOverlay.hidden && steerSlug === slug) {
      fillSteerContext(jobsBySlug[steerSlug], steerMode);
    }
    return;
  }
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
    invalidateBoard(); // archiveOpen changed — markup differs even if jobs didn't
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
  const steerDone = ev.target.closest(".steer-done");
  if (steerDone) {
    steerDone.disabled = true;
    post("/api/done", { slug: steerDone.dataset.slug }).then((ok) => {
      if (ok) {
        postDraft.cancel(); // done also clears the draft server-side
        steerOverlay.hidden = true;
      } else {
        steerDone.disabled = false;
      }
    });
    return;
  }
  const detail = ev.target.closest(".detail.clampable");
  if (detail && !window.getSelection().toString()) {
    const slug = detail.closest(".card")?.dataset.slug;
    if (detail.classList.toggle("expanded")) expandedDetails.add(slug);
    else expandedDetails.delete(slug);
    localStorage.setItem("kraken-expanded-details", JSON.stringify([...expandedDetails]));
    return;
  }
  const tmuxOpen = ev.target.closest(".open-tmux");
  if (tmuxOpen) {
    post("/api/peers/open", { slug: tmuxOpen.dataset.slug });
    return;
  }
  if (ev.target.closest("#new-job")) {
    openEditor(null);
    return;
  }
  // all cards open something on plain click (buttons/chips above already
  // returned first): a live working/blocked job opens the steer/unblock
  // modal on itself, everything else opens the editor — which renders
  // read-only when the server says the job isn't editable
  const card = ev.target.closest(".card[data-editable]");
  if (card && !window.getSelection().toString()) {
    const slug = card.dataset.slug;
    const liveCol = liveDisplayColumn(slug);
    if (liveCol === "working" || liveCol === "blocked") openSteer(slug, liveCol === "blocked" ? "unblock" : "steer");
    else openEditor(slug);
  }
});

// ---- steer: mid-run instructions ----

const steerOverlay = document.getElementById("steer-overlay");
let steerSlug = null;
let steerMode = "steer"; // "steer" | "reactivate" | "unblock" — same modal, different endpoint
// job.state captured at open time — see checkStaleModals below
let steerOpenState = undefined;

// server-synced draft for the composer textarea — one shared draft per job
// regardless of mode, so steer/unblock/reactivate all read and write the
// same text. postDraft is debounced per keystroke; flushSteerDraft is called
// from every path that closes the modal so a draft survives a reload.
const postDraft = debounce((slug, text) => {
  fetch("/api/draft", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ slug, text }),
  }).catch(() => {}); // fire-and-forget; the next keystroke's debounce retries
}, 600);

function flushSteerDraft() {
  return postDraft.flush();
}

// steer context: the cell's last message, carl's last sent message, and its
// artifact chips — populated for "steer"/"unblock" (the data is there either
// way); "reactivate" hides it outright since the cell is gone.
function fillSteerContext(j, mode) {
  const container = document.getElementById("steer-context");
  const detailEl = document.getElementById("steer-detail");
  const lastSentEl = document.getElementById("steer-lastsent");
  const chipsEl = document.getElementById("steer-chips");

  const detail = mode !== "reactivate" && j && j.detail;
  const lastSent = mode !== "reactivate" && j && j.last_sent;
  const artifacts = mode !== "reactivate" && j && j.artifacts && j.artifacts.length ? j.artifacts : null;

  detailEl.hidden = !detail;
  // renderMD escapes before transforming, so agent-authored text is inert
  detailEl.innerHTML = detail ? renderMD(detail) : "";

  lastSentEl.hidden = !lastSent;
  lastSentEl.textContent = lastSent ? `me→zeus: ${lastSent}` : "";

  chipsEl.hidden = !artifacts;
  chipsEl.innerHTML = artifacts ? chipsHTML(artifacts, j.slug, j.slug) : "";

  container.hidden = !detail && !lastSent && !artifacts;
}

// liveDisplayColumn mirrors render()'s column bucketing for a single job:
// an unhealthy live cell displays as blocked regardless of its file state.
// Returns null for a job that isn't live (or doesn't exist) — the caller
// treats that as "not cancellable/steerable".
function liveDisplayColumn(slug) {
  const j = jobsBySlug[slug];
  if (!j || !j.live) return null;
  return j.health ? "blocked" : j.state;
}

// steerActionsHTML makes the modal action-complete: retry + question are the
// two card actions that only make sense while a job is actually unhealthy or
// waiting on an answer. Tmux and mark-done moved to the panel head (static
// buttons, filled per-open — see openSteer) since they apply to any
// steer/unblock open regardless of these two conditions.
function steerActionsHTML(j) {
  if (!j) return "";
  const retry = j.health && !/is gone|startup prompt/.test(j.health)
    ? `<button class="btn retry" data-slug="${esc(j.slug)}">↻ retry</button>`
    : "";
  return retry + (j.question ? questionHTML(j) : "");
}

// specMetaHTML renders compact read-only rows from the /api/job/form payload
// already fetched in openSteer — repo/base/solo/prereqs (plus type), shown
// above the rendered spec markdown so the modal answers "what is this cell
// running with" without leaving it.
function specMetaHTML(d) {
  const rows = [
    ["type", esc(d.type || "—")],
    ["repo", esc(d.repo || "—")],
    ["base", esc(d.base || "origin/main")],
    ["solo", d.solo ? "✓" : "✗"],
  ];
  const prereqs = d.prereqs || [];
  if (prereqs.length) {
    // wide: prereq lists run long, so the row spans both meta columns
    rows.push(["prereqs", prereqs.map((p) => esc(p.job) + (p.gated ? " gated ⚑" : "")).join(", "), "wide"]);
  }
  return `<div class="spec-meta">${rows
    .map(([k, v, w]) => `<div class="spec-meta-row${w ? " " + w : ""}"><span class="k">${k}</span><span class="v">${v}</span></div>`)
    .join("")}</div>`;
}

function openSteer(slug, mode = "steer") {
  steerSlug = slug;
  steerMode = mode;
  steerOpenState = jobsBySlug[slug] ? jobsBySlug[slug].state : undefined;
  document.getElementById("steer-title").textContent =
    mode === "reactivate" ? slug + " — reactivate (same zeus session)"
    : mode === "unblock" ? slug + " — unblock"
    : slug + " — message zeus";
  const text = document.getElementById("steer-text");
  text.placeholder =
    mode === "reactivate"
      ? "optional: what to adjust — delivered as the resume prompt to the job's original zeus session (leave empty to just wake it)"
      : mode === "unblock"
      ? "tell the crew how to proceed — routed straight to the cell"
      : "message zeus directly — steering / pivot instruction, delivered straight to its pane";
  text.value = "";
  // fetch the shared draft after the composer is already showing empty —
  // only land it if the modal is still open for this slug and the user
  // hasn't started typing in the meantime
  fetch("/api/draft?slug=" + encodeURIComponent(slug))
    .then((r) => r.json())
    .then((d) => {
      if (steerOverlay.hidden || steerSlug !== slug) return;
      if (text.value === "") text.value = d.text || "";
    })
    .catch(() => {});
  document.getElementById("steer-note").textContent = "";
  document.getElementById("steer-send").textContent =
    mode === "reactivate" ? "reactivate ⌘⏎" : mode === "unblock" ? "unblock ⌘⏎" : "send ⌘⏎";
  fillSteerContext(jobsBySlug[slug], mode);

  const liveCol = liveDisplayColumn(slug);
  const killable = mode !== "reactivate" && (liveCol === "working" || liveCol === "blocked");
  document.getElementById("steer-kill").hidden = !killable;

  // head buttons are static markup (like steer-kill) — fill data-slug and
  // toggle visibility per open. reactivate targets a dead cell, so there's
  // nothing on it to tmux into or mark done regardless of live/column state.
  const tmuxBtn = document.getElementById("steer-tmux");
  const showTmux = mode !== "reactivate" && !!jobsBySlug[slug] && jobsBySlug[slug].live;
  tmuxBtn.hidden = !showTmux;
  tmuxBtn.dataset.slug = slug;

  const doneBtn = document.getElementById("steer-markdone");
  const showDone = mode !== "reactivate" && liveCol === "blocked";
  doneBtn.hidden = !showDone;
  doneBtn.dataset.slug = slug;

  // action-complete only for steer/unblock — reactivate targets a dead cell,
  // so there's nothing on it to retry or mark done
  const actions = document.getElementById("steer-actions");
  if (mode === "steer" || mode === "unblock") {
    actions.innerHTML = steerActionsHTML(jobsBySlug[slug]);
    actions.hidden = !actions.innerHTML;
  } else {
    actions.hidden = true;
    actions.innerHTML = "";
  }

  const specSection = document.getElementById("steer-spec");
  const specBody = document.getElementById("steer-spec-body");
  specSection.hidden = true;
  specBody.hidden = true;
  specBody.innerHTML = "";
  document.querySelector("#steer-spec-toggle .tri").textContent = "▸";
  if (mode === "steer" || mode === "unblock") {
    fetch("/api/job/form?slug=" + encodeURIComponent(slug))
      .then((r) => r.json())
      .then((d) => {
        // the modal may have closed, or reopened for a different slug,
        // while this fetch was in flight — a stale result must not land
        if (steerOverlay.hidden || steerSlug !== slug) return;
        if (d.error || !d.spec) return;
        specBody.innerHTML = specMetaHTML(d) + renderMD(d.spec);
        specSection.hidden = false;
      })
      .catch(() => {});
  }

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
  if (ok) {
    postDraft.cancel(); // server already cleared the draft — flushing would resurrect it
    steerOverlay.hidden = true;
  } else {
    document.getElementById("steer-note").textContent = "send failed — see status bar";
  }
});
document.getElementById("steer-text").addEventListener("input", (ev) => {
  if (steerSlug) postDraft(steerSlug, ev.target.value);
});
steerOverlay.addEventListener("click", (ev) => {
  if (ev.target === steerOverlay) { flushSteerDraft(); steerOverlay.hidden = true; }
});
document.getElementById("steer-text").addEventListener("keydown", (ev) => {
  if (ev.key === "Enter" && (ev.metaKey || ev.ctrlKey)) {
    ev.preventDefault();
    document.getElementById("steer-send").click();
  }
});
document.getElementById("steer-kill").addEventListener("click", async () => {
  if (!steerSlug) return;
  if (!confirm(`kill ${steerSlug}? the cell is torn down (artifacts preserved) and the job returns to draft.`)) return;
  const btn = document.getElementById("steer-kill");
  btn.disabled = true;
  const ok = await post("/api/kill", { slug: steerSlug });
  btn.disabled = false;
  if (ok) {
    flushSteerDraft(); // kill doesn't clear the draft server-side — preserve it
    steerOverlay.hidden = true;
  } else {
    document.getElementById("steer-note").textContent = "cancel failed — see status bar";
  }
});
document.getElementById("steer-spec-toggle").addEventListener("click", () => {
  const body = document.getElementById("steer-spec-body");
  const opening = body.hidden;
  body.hidden = !opening;
  document.querySelector("#steer-spec-toggle .tri").textContent = opening ? "▾" : "▸";
});

// ---- job editor (draft + spec in the browser) ----

const editorOverlay = document.getElementById("editor-overlay");
const editorText = document.getElementById("editor-text");
const editorNote = document.getElementById("editor-note");
let editorSlug = null;
let editorMTime = 0;
let editorReady = false; // captured from the last loaded form; the ready row is gone, sent as-is by the ready button
// job.state captured at open time — see checkStaleModals below. Capture is
// lazy (editorStateCaptured) because a freshly-created job isn't in
// jobsBySlug yet when loadForm first runs for it — the very next poll fills
// it in, and that's when we take the baseline instead of misreading "not in
// the map yet" as a state change.
let editorOpenState = undefined;
let editorStateCaptured = false;

function prereqRowHTML(p) {
  return `<div class="prereq-row">
    <input type="text" list="job-list" value="${esc(p.job || "")}" placeholder="job-slug" spellcheck="false">
    <label class="check"><input type="checkbox" ${p.gated ? "checked" : ""}> gated ⚑</label>
    <button class="rm" title="remove">✕</button>
  </div>`;
}

// repoSelectHTML lists the actually-existing checkouts (d.repos, from
// /api/job/form) as real options instead of a free-text field with a
// datalist caret that did nothing. d.repo is appended as an extra option
// when it's set but not among d.repos, so an existing job with an unusual
// repo still round-trips instead of silently resetting to "—".
function fillForm(d) {
  document.getElementById("f-type").value = d.type || "build";
  const repos = d.repos || [];
  const repoSel = document.getElementById("f-repo");
  repoSel.innerHTML =
    `<option value="">—</option>` +
    repos.map((x) => `<option value="${esc(x)}">${esc(x)}</option>`).join("") +
    (d.repo && !repos.includes(d.repo) ? `<option value="${esc(d.repo)}">${esc(d.repo)}</option>` : "");
  repoSel.value = d.repo || "";
  document.getElementById("f-base").value = d.base || "";
  editorReady = !!d.ready;
  document.getElementById("f-solo").checked = !!d.solo;
  document.getElementById("f-prereqs").innerHTML = (d.prereqs || []).map(prereqRowHTML).join("");
  editorText.value = d.spec || "";
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
    ready: editorReady,
    solo: document.getElementById("f-solo").checked,
    prereqs,
    spec: editorText.value,
    mtime: editorMTime,
  };
}

// setFormReadOnly locks every field when the server says the job isn't
// editable (a cell is in flight and the file belongs to the crew) — the
// spec textarea stays readonly rather than disabled so it's still
// selectable/copyable.
function setFormReadOnly(readOnly) {
  document.getElementById("f-type").disabled = readOnly;
  document.getElementById("f-repo").disabled = readOnly;
  document.getElementById("f-base").disabled = readOnly;
  document.getElementById("f-solo").disabled = readOnly;
  document.getElementById("f-add-prereq").disabled = readOnly;
  document.querySelectorAll("#f-prereqs .prereq-row input, #f-prereqs .prereq-row button")
    .forEach((el) => (el.disabled = readOnly));
  editorText.readOnly = readOnly;
  // archive lives here, inside the opened job, on purpose: no card-level
  // shortcut — the server still refuses jobs with a cell in flight
  document.getElementById("editor-archive").hidden = readOnly;
  document.getElementById("editor-hint").textContent = readOnly ? "read-only — cell in flight" : "";
}

async function loadForm(slug) {
  const r = await fetch("/api/job/form?slug=" + encodeURIComponent(slug));
  const d = await r.json();
  if (d.error) throw new Error(d.error);
  editorSlug = slug;
  editorMTime = d.mtime;
  if (jobsBySlug[slug]) {
    editorOpenState = jobsBySlug[slug].state;
    editorStateCaptured = true;
  } else {
    editorStateCaptured = false;
  }
  fillForm(d);
  document.getElementById("editor-title").textContent = slug + ".job.md";
  document.getElementById("editor-newrow").hidden = true;
  document.getElementById("editor-form").hidden = false;
  setFormReadOnly(!d.editable);
  // board-action parity: the drag/board moves for editable jobs live here
  // too — queue for an err-free draft, reactivate for a resumable done job
  const j = jobsBySlug[slug];
  document.getElementById("editor-queue").hidden =
    !(j && !j.live && j.state === "draft" && !j.err && !d.err);
  document.getElementById("editor-reactivate").hidden =
    !(j && !j.archived && j.state === "done" && j.reactivatable);
  if (d.err) editorNote.textContent = "current file won't run: " + d.err;
}

function openEditor(slug) {
  editorSlug = slug;
  editorMTime = 0;
  editorReady = false;
  editorStateCaptured = false;
  editorNote.textContent = "";
  editorNote.style.color = "";
  const isNew = !slug;
  document.getElementById("editor-title").textContent = isNew ? "new job" : slug + ".job.md";
  document.getElementById("editor-newrow").hidden = !isNew;
  document.getElementById("editor-form").hidden = true;
  document.getElementById("editor-archive").hidden = true;
  document.getElementById("editor-queue").hidden = true;
  document.getElementById("editor-reactivate").hidden = true;
  document.getElementById("editor-hint").textContent = "";
  document.getElementById("editor-slug").value = "";
  editorOverlay.hidden = false;
  if (isNew) {
    document.getElementById("editor-slug").focus();
    return;
  }
  loadForm(slug).catch((e) => { editorNote.textContent = String(e.message || e); });
}

document.getElementById("editor-queue").addEventListener("click", async () => {
  if (!editorSlug) return;
  const btn = document.getElementById("editor-queue");
  btn.disabled = true;
  await flushEditorAutosave(); // land any edit still sitting in the debounce window before queueing
  const ok = await post("/api/queue", { slug: editorSlug });
  btn.disabled = false;
  if (ok) editorOverlay.hidden = true;
  else editorNote.textContent = "queue failed — see status bar";
});

// reactivate is a composer flow, not a one-shot post — hand off to the
// steer modal in reactivate mode (optional message folded into the resume
// prompt), same as the card's ↺ button
document.getElementById("editor-reactivate").addEventListener("click", () => {
  if (!editorSlug) return;
  editorOverlay.hidden = true;
  openSteer(editorSlug, "reactivate");
});

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
    invalidateBoard();
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
  if (ev.target.closest(".rm")) {
    ev.target.closest(".prereq-row").remove();
    onEditorFormChange(); // removal changes the payload immediately, unlike an empty add row
  }
});

// spec editor autosave: any change inside the form debounces into a
// saveform POST with mtime: 0 (no optimistic-lock check — this is the only
// writer of the file while the editor's open, so there's nothing to
// conflict with). editorAutosave is the debounced call; flushEditorAutosave
// lands it immediately on modal close or before the ready button fires.
const editorAutosave = debounce(doEditorSave, 600);

function flushEditorAutosave() {
  return editorAutosave.flush();
}

function onEditorFormChange() {
  if (!editorSlug || editorText.readOnly || document.getElementById("editor-form").hidden) return;
  editorNote.style.color = "var(--text-2)";
  editorNote.textContent = "syncing…";
  editorAutosave();
}

async function doEditorSave() {
  if (!editorSlug || editorText.readOnly || document.getElementById("editor-form").hidden) return;
  try {
    const r = await fetch("/api/job/saveform", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ...readForm(), mtime: 0 }),
    });
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || r.status);
    editorMTime = d.mtime;
    editorNote.style.color = d.warn ? "" : "var(--solved)";
    editorNote.textContent = d.warn ? "synced — won't run: " + d.warn : "synced ✓";
    invalidateBoard();
    refresh();
  } catch (e) {
    editorNote.style.color = "";
    editorNote.textContent = "sync failed: " + (e.message || e);
  }
}

document.getElementById("editor-form").addEventListener("input", onEditorFormChange);
document.getElementById("editor-form").addEventListener("change", onEditorFormChange);

document.getElementById("editor-archive").addEventListener("click", async () => {
  if (!editorSlug) return;
  const ok = await post("/api/archive", { slug: editorSlug });
  if (ok) editorOverlay.hidden = true;
  else editorNote.textContent = "archive failed — see status bar";
});

editorOverlay.addEventListener("click", (ev) => {
  if (ev.target === editorOverlay) { flushEditorAutosave(); editorOverlay.hidden = true; }
});
document.getElementById("editor-form").addEventListener("keydown", (ev) => {
  if (ev.key === "Enter" && (ev.metaKey || ev.ctrlKey)) {
    ev.preventDefault();
    const readyBtn = document.getElementById("editor-queue");
    if (!readyBtn.hidden) readyBtn.click();
  }
});
document.getElementById("editor-slug").addEventListener("keydown", (ev) => {
  if (ev.key === "Enter") {
    ev.preventDefault();
    document.getElementById("editor-create").click();
  }
});

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
// one Escape closes one layer: the artifact viewer if it's on top (it can be
// opened from inside the steer/unblock modal), else whichever of editor/steer
// is open underneath.
document.addEventListener("keydown", (ev) => {
  if (ev.key !== "Escape") return;
  if (!overlay.hidden) { overlay.hidden = true; return; }
  if (!steerOverlay.hidden) { flushSteerDraft(); steerOverlay.hidden = true; return; }
  if (!editorOverlay.hidden) { flushEditorAutosave(); editorOverlay.hidden = true; return; }
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
    invalidateBoard(); // switching to dag must render it even though jobs are unchanged
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
      return `<div class="dag-node editable ${j.question || j.health ? "flagged" : ""}"
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
  if (!node || window.getSelection().toString()) return;
  const slug = node.dataset.slug;
  const liveCol = liveDisplayColumn(slug);
  if (liveCol === "working" || liveCol === "blocked") openSteer(slug, liveCol === "blocked" ? "unblock" : "steer");
  else openEditor(slug);
});

// ---- poll (paused while dragging so the board doesn't rebuild mid-drag,
// and while the tab is hidden — nothing is watching it either way) ----

async function refresh() {
  if (dragging) return;
  if (document.hidden) return;
  try {
    const r = await fetch("/api/board");
    const board = await r.json();
    // always: cheap, correctness-critical bookkeeping regardless of whether
    // the jobs payload itself changed since the last rebuild
    const { byCol, archived } = render(board);
    // board.daemon.last_tick changes every daemon tick, so compare jobs only
    // — a whole-payload compare would defeat the point of this check
    const snap = JSON.stringify(board.jobs || []);
    if (snap !== boardSnapshot) {
      boardSnapshot = snap;
      renderBoardDOM(byCol, archived);
      if (view === "dag") renderDag(board);
    } else {
      updateAges(); // a rebuild renders fresh ages itself
    }
  } catch {
    document.getElementById("daemon-stat").textContent = "kraken web server unreachable";
    document.getElementById("daemon-led").className = "led";
  }
}
// returning to the tab refreshes immediately instead of waiting out the rest
// of the current 3s tick
document.addEventListener("visibilitychange", () => {
  if (!document.hidden) refresh();
});
applyView();
refresh();
setInterval(refresh, 3000);

// ---- theme toggle: system → light → dark → system ----

const THEME_CYCLE = { system: "light", light: "dark", dark: "system" };

function themeLabel() {
  return "◐ " + window.krakenTheme.get().toUpperCase();
}

const themeToggle = document.getElementById("theme-toggle");
themeToggle.textContent = themeLabel();
themeToggle.addEventListener("click", () => {
  window.krakenTheme.set(THEME_CYCLE[window.krakenTheme.get()]);
  themeToggle.textContent = themeLabel();
});
