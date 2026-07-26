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

const MOVES = {
  draft: ["queued"],
  queued: ["draft", "working"],
  working: ["blocked"],
  blocked: ["done", "working"],
  done: ["blocked", "working", "archived"],
};

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
let draggedEl = null; // the dragged card's live DOM node
let dragOriginParent = null; // its .col-cards parent at dragstart
let dragOriginAnchor = null; // its nextElementSibling at dragstart — restore target on cancel
let lastSlotAnchor; // throttling: the anchor node last acted on during same-column dragover
let sameColumnDropHandled = false; // true once drop() takes over a same-column reorder's outcome

// last fetched board.jobs, in server order — used to predict what the next
// poll will look like after an optimistic same-column reorder (see
// postReorder), so the boardSnapshot guard below doesn't false-positive a
// rebuild the moment that poll lands.
let lastBoardJobs = [];

// serialized board.jobs of the last DOM rebuild — refresh() skips rebuilding
// #board while it still matches, so any client-side state change that
// affects markup (but doesn't itself change the jobs payload) must call
// invalidateBoard() before its next refresh().
let boardSnapshot = "";

function invalidateBoard() {
  boardSnapshot = "";
}

// per-slug artifact-chips-expanded state, persisted the same way archiveOpen
// is (see archiveOpen below) so it survives the 3s poll re-render, which
// rebuilds #board innerHTML and would otherwise wipe any .expanded class
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

// countdown formats a whole number of seconds as "Xm Ys", floored at 0.
function countdown(seconds) {
  const clamped = Math.max(0, seconds);
  const m = Math.floor(clamped / 60);
  const s = clamped % 60;
  return `${m}m ${s}s`;
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

// ---- doc questions (questions.yaml, distinct from AskUserQuestion above) --
//
// settledJob: a done or archived job. Its questions are history, not a
// standing ask — nobody is waiting on an answer to keep working — so they
// stay off the board entirely (no panel on the card, no flag) and live one
// click into the job instead: the editor modal for a done job
// (fillEditorDocq), the steer modal in reactivate mode, the artifact-side
// surfaces. Answering there still works exactly as before (Q13: answers are
// editable forever, live or done); it just no longer competes for board
// space with the jobs that are actually running. Archived jobs never carry
// doc_questions on the wire at all (board.go), so this only ever fires for
// done ones — it's stated here anyway so the rule reads the same on both
// sides of the wire.
function settledJob(j) {
  return !!j && (j.archived || j.state === "done");
}

// docQuestionsOpenP0 counts open (unanswered) P0 doc questions for a job —
// cheap enough to fold into the card-level "flagged" state and the header
// flag counter alongside j.question/j.health. A settled job contributes 0:
// its questions aren't visible on the board, so flagging the board for them
// would point at something nothing on that surface shows.
function docQuestionsOpenP0(j) {
  if (settledJob(j)) return 0;
  return (j.doc_questions || []).filter((q) => q.importance === "P0" && !q.answer).length;
}

// sortDocQuestions orders open questions first (P0 < P1 < P2, ties keep file
// order — Array#sort is stable), then answered questions last in file order.
const DQ_IMPORTANCE_RANK = { P0: 0, P1: 1, P2: 2 };
function sortDocQuestions(list) {
  const open = [];
  const answered = [];
  list.forEach((q, i) => (q.answer ? answered : open).push({ q, i }));
  open.sort((a, b) => (DQ_IMPORTANCE_RANK[a.q.importance] ?? 3) - (DQ_IMPORTANCE_RANK[b.q.importance] ?? 3) || a.i - b.i);
  return [...open.map((x) => x.q), ...answered.map((x) => x.q)];
}

// reansweringDocQ tracks which already-answered questions are currently
// reopened for a re-answer, keyed "<slug>|<id>" — module-level (not
// localStorage-persisted, this is transient edit state) so it survives the
// wholesale #board innerHTML rebuild every poll triggers (see
// renderBoardDOM). A question leaves this set the moment its re-answer
// posts successfully; a failed post leaves it in place so the row (and
// whatever the user typed, preserved via snapshotDocInputs/restoreDocInputs)
// stays open for a retry.
const reansweringDocQ = new Set();
const dqReanswerKey = (slug, id) => slug + "|" + id;

// docQuestionRowHTML renders one editable doc-question row: importance
// badge, id + text, optional context/recommendation, and the answer
// affordance (accept the recommendation verbatim, or free-text). Used both
// for a genuinely open question (prefill undefined) and for an answered
// question reopened via reansweringDocQ (prefill = the current answer,
// plus a cancel button back to the read-only view). Nothing here talks to
// the agent directly — a post() just records the answer as a `questions`
// row UPDATE.
function docQuestionRowHTML(j, q, prefill) {
  const imp = (q.importance || "P2").toLowerCase();
  const rec = q.recommendation ? `<div class="dq-rec" title="${esc(q.recommendation)}">rec: ${esc(q.recommendation)}</div>` : "";
  const acceptBtn = q.recommendation
    ? `<button class="dq-accept" data-slug="${esc(j.slug)}" data-id="${esc(q.id)}" data-text="${esc(q.recommendation)}">accept rec</button>`
    : "";
  const reanswering = prefill != null;
  const cancelBtn = reanswering
    ? `<button class="dq-reanswer-cancel" data-slug="${esc(j.slug)}" data-id="${esc(q.id)}">cancel</button>`
    : "";
  return `<div class="dq-row">
    <div class="dq-head"><span class="dq-imp ${esc(imp)}">${esc(q.importance || "P2")}</span><span class="dq-q" title="${esc(q.text)}">${esc(q.id)} — ${esc(q.text)}</span></div>
    ${q.context ? `<div class="dq-ctx" title="${esc(q.context)}">${esc(q.context)}</div>` : ""}
    ${rec}
    <div class="dq-answer">
      ${acceptBtn}
      <input class="dq-input" type="text" placeholder="answer…" value="${esc(prefill || "")}" data-slug="${esc(j.slug)}" data-id="${esc(q.id)}">
      <button class="dq-send" data-slug="${esc(j.slug)}" data-id="${esc(q.id)}">send</button>
      ${cancelBtn}
    </div>
  </div>`;
}

// docQuestionAnsweredHTML renders a dimmed, read-only row for an already
// answered question — badge + id + question text stay visible (so the row
// is still self-describing once it's one of several answered rows),
// answered_at (if present) rides in a title attr rather than cluttering the
// row itself. Clickable (see the document click delegate) to reopen it as
// an editable row via reansweringDocQ — answers are editable forever (Q13),
// live or done.
function docQuestionAnsweredHTML(j, q) {
  const imp = (q.importance || "P2").toLowerCase();
  const title = q.answered_at ? `answered ${String(q.answered_at)} — click to re-answer` : "click to re-answer";
  return `<div class="dq-row dq-answered" data-slug="${esc(j.slug)}" data-id="${esc(q.id)}" title="${esc(title)}">
    <div class="dq-head"><span class="dq-imp ${esc(imp)}">${esc(q.importance || "P2")}</span><span class="dq-q" title="${esc(q.text)}">${esc(q.id)} — ${esc(q.text)}</span></div>
    <div class="dq-answered-text">✓ ${esc(q.answer)}</div>
  </div>`;
}

// docQuestionsHTML renders a job's doc-review questions section — visually
// distinct from both artifact chips and the AskUserQuestion block above
// (neutral panel vs. the counter-colored .question block). Renders nothing
// when there's nothing to show, so old-server payloads (no doc_questions
// field) and jobs with no questions look exactly as before. Questions are
// rows in kraken.db now (file mode retired outright, Q11) — there is no
// more malformed-file state to render.
function docQuestionsHTML(j) {
  const list = j.doc_questions || [];
  if (!list.length) return "";
  const sorted = sortDocQuestions(list);
  const rows = sorted
    .map((q) => {
      if (!q.answer) return docQuestionRowHTML(j, q);
      const key = dqReanswerKey(j.slug, q.id);
      return reansweringDocQ.has(key) ? docQuestionRowHTML(j, q, q.answer) : docQuestionAnsweredHTML(j, q);
    })
    .join("");
  const hasOpen = sorted.some((q) => !q.answer);
  return `<div class="doc-questions">
    ${rows}
    ${hasOpen ? `<div class="dq-hint">answers are read by the agent next time it works</div>` : ""}
  </div>`;
}

// ---- job feedback (`feedback` rows in kraken.db, captured from the
// artifact overlay — see openArtifact/panel-feedback below — and managed
// here) ----
//
// fbEditing tracks which feedback rows are currently open for editing, by
// row id — module-level for the same reason reansweringDocQ is: it must
// survive the wholesale #board innerHTML rebuild every poll triggers. Keyed
// on the bare id (unlike reansweringDocQ's slug|id) because feedback ids are
// global kraken.db rowids, not per-job qids — two different jobs can never
// share one, so there's nothing for the slug to disambiguate.
const fbEditing = new Set();

// feedbackRowHTML renders one feedback entry. An addressed entry (zeus posted
// its own addressed/addressed_at) is dimmed and terminal — carl can still
// edit or delete it, same as an open one, since a note staying accurate is
// more useful than freezing it the moment zeus reacts to it. An open entry in
// fbEditing shows an inline textarea (pre-filled with the current note)
// instead of the note + edit/delete controls.
function feedbackRowHTML(j, f) {
  const addressedTitleAttr = f.addressed_at ? ` title="${esc("addressed " + String(f.addressed_at))}"` : "";
  const addressed = f.addressed
    ? `<div class="fb-addressed-text"${addressedTitleAttr}>✓ ${esc(f.addressed)}</div>`
    : "";
  if (fbEditing.has(String(f.id))) {
    return `<div class="fb-row" data-fb-id="${esc(f.id)}">
      <div class="fb-head"><span class="fb-artifact">${esc(f.artifact)}</span></div>
      <textarea class="fb-edit-input" data-slug="${esc(j.slug)}" data-id="${esc(f.id)}">${esc(f.note)}</textarea>
      <div class="fb-edit-actions">
        <span class="fb-edit-note"></span>
        <button class="fb-cancel" data-id="${esc(f.id)}">cancel</button>
        <button class="fb-save" data-slug="${esc(j.slug)}" data-id="${esc(f.id)}">save</button>
      </div>
    </div>`;
  }
  return `<div class="fb-row${f.addressed ? " fb-addressed" : ""}" data-fb-id="${esc(f.id)}">
    <div class="fb-head">
      <span class="fb-artifact">${esc(f.artifact)}</span>
      <span class="fb-actions">
        <button class="fb-edit" data-id="${esc(f.id)}">edit</button>
        <button class="fb-delete" data-slug="${esc(j.slug)}" data-id="${esc(f.id)}">delete</button>
      </span>
    </div>
    <div class="fb-note">${esc(f.note)}</div>
    ${addressed}
  </div>`;
}

// feedbackHTML renders a job's feedback panel — modeled on docQuestionsHTML
// (own neutral panel, same empty-renders-nothing convention so a job with
// no feedback array, from an older server or simply no notes yet, looks
// exactly as before).
function feedbackHTML(j) {
  const list = j.feedback || [];
  if (!list.length) return "";
  const rows = list.map((f) => feedbackRowHTML(j, f)).join("");
  return `<div class="job-feedback">${rows}</div>`;
}

// wakeupChipHTML renders a live cell's poll-timer chip: a countdown to its
// next ScheduleWakeup tick, computed from wakeup.fires_at against wall-clock
// time. The full board rebuild is skipped when the payload hasn't changed —
// exactly the idle-between-ticks window this chip exists for — so the chip
// carries data-wakeup-fires and updateAges() re-ticks it in place on every
// poll, the same pattern as the age chips. Once fires_at is past (kraken
// hasn't caught the next discovery pass yet), it reads as "due" rather than
// a negative countdown.
function wakeupLabel(firesAt) {
  const remainingS = Math.floor((firesAt - Date.now()) / 1000);
  return remainingS <= 0 ? "due" : countdown(remainingS);
}

function wakeupChipHTML(j) {
  if (!j.wakeup || j.state !== "working") return "";
  return `<div class="wakeup-row"><span class="chip-wakeup" data-wakeup-fires="${+j.wakeup.fires_at}">⏱ next check in ${esc(wakeupLabel(j.wakeup.fires_at))}</span></div>`;
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

// zeusPillHTML renders the zeus status pill for crewHTML: one god icon
// (cloud) whose color is the sole status indicator — "running" green,
// "polling" cyan, "question" flags a pending AskUserQuestion (amber),
// anything else (including "waiting") falls back to the muted default
// rather than echoing an arbitrary status string verbatim. The name
// survives only in the title attribute, for hover/assistive tech.
const ZEUS_STATUS = {
  running: { cls: "run", title: "zeus — running" },
  polling: { cls: "polling", title: "zeus — polling" },
  question: { cls: "question", title: "zeus — question" },
};
function zeusPillHTML(status) {
  const s = ZEUS_STATUS[status] || { cls: "", title: "zeus — waiting" };
  return `<span class="crew-pill${s.cls ? " " + s.cls : ""}" title="${esc(s.title)}">↯</span>`;
}

const CREW_ROLES = ["hephaestus", "athena", "hermes", "other"];

// CREW_ICONS maps a crew role to its god's text-presentation glyph (VS15
// forces monochrome rendering, matching the rest of the UI's icon glyphs —
// ● ⚑ ↻ ⧉ ↺; zeus' ↯ has no emoji variant, so no VS15 needed there).
// "other" has no god and keeps its plain text label.
const CREW_ICONS = { hephaestus: "⚒︎", athena: "⚖︎", hermes: "✉︎" };

// crewHTML renders the crew line: a zeus status pill plus one working/total
// chip per subagent role present in the roster, each chip led by its god's
// icon in place of the role name (icon replaces the name; the name moves to
// title). Renders nothing for an old server payload (no zeus_status, no
// roster) so the card looks exactly as it did before these fields existed.
function crewHTML(j) {
  const roster = j.roster || [];
  if (!j.zeus_status && !roster.length) return "";
  const zeusPill = j.zeus_status ? zeusPillHTML(j.zeus_status) : "";
  const counts = {};
  for (const r of roster) {
    const role = CREW_ROLES.includes(r.role) ? r.role : "other";
    (counts[role] ||= { working: 0, total: 0 }).total++;
    if (r.working) counts[role].working++;
  }
  const chips = CREW_ROLES.filter((role) => counts[role])
    .map((role) => {
      const c = counts[role];
      const icon = CREW_ICONS[role];
      const label = icon
        ? `<span class="crew-ico">${icon}</span> ${c.working}/${c.total}`
        : `${esc(role)} ${c.working}/${c.total}`;
      const title = `${role} — ${c.working} of ${c.total} working`;
      return `<span class="crew-chip${c.working > 0 ? " active" : ""}" title="${esc(title)}">${label}</span>`;
    })
    .join("");
  if (!zeusPill && !chips) return "";
  return `<div class="crew">${zeusPill}${chips}</div>`;
}

function cardHTML(j, col) {
  const color = stateColor(j.state);
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
    .map((r) => `<div class="roster-row"><span class="roster-dot${r.working ? " on" : ""}">${r.working ? "●" : "○"}</span> ${esc(r.name)} <span class="dim">(${esc(r.model)})</span></div>`)
    .join("");
  const editable = !j.live && ["draft", "queued", "done"].includes(col);
  return `<div class="card editable ${j.question || j.health || docQuestionsOpenP0(j) ? "flagged" : ""}" style="--state:${color}"
      draggable="true" data-slug="${esc(j.slug)}" data-col="${col}" data-editable="${editable}">
    <div class="card-top">
      <span class="slug">${esc(j.slug)}</span>
      <span class="state">● ${esc(j.state)}</span>
      ${col === "queued" && !j.live ? `<button class="queue-cancel" data-slug="${esc(j.slug)}" title="cancel — return to draft (spec is kept)">✕</button>` : ""}
    </div>
    ${wakeupChipHTML(j)}
    ${prs ? `<div class="prs">${prs}</div>` : ""}
    <div class="meta">
      <span>${esc(j.repo)}</span>
      ${j.live ? `<span>${esc(j.cell)}</span>` : ""}
      <span data-age="${j.mtime}">${age(j.mtime)}</span>
      ${prereqs ? `<span class="prereq">${prereqs}</span>` : ""}
    </div>
    ${j.live ? `<div class="review-row"><button class="btn tmux-open open-tmux" data-slug="${esc(j.slug)}" title="raise zeus' pane in Ghostty">⧉ tmux</button></div>` : ""}
    ${j.err ? `<div class="err">✕ ${esc(j.err)}</div>` : ""}
    ${j.health ? `<div class="err">⚠ ${esc(j.health)}</div>` : ""}
    ${j.last_sent ? `<div class="last-sent">me→zeus: ${esc(j.last_sent)}</div>` : ""}
    ${j.detail ? `<div class="detail">${esc(j.detail)}</div>` : ""}
    ${j.wakeup && j.wakeup.reason && j.state === "working" ? `<div class="wakeup-reason">↻ ${esc(j.wakeup.reason)}</div>` : ""}
    ${crewHTML(j)}
    ${roster ? `<div class="roster">${roster}</div>` : ""}
    ${j.outcome && col === "done" ? `<div class="outcome">→ ${esc(j.outcome)}</div>` : ""}
    ${chips ? `<div class="chips">${chips}</div>` : ""}
    ${j.health && !/is gone|startup prompt/.test(j.health) ? `<button class="btn retry" data-slug="${esc(j.slug)}">↻ retry</button>` : ""}
    ${j.question ? questionHTML(j) : ""}
    ${settledJob(j) ? "" : docQuestionsHTML(j)}
    ${feedbackHTML(j)}
    ${(col === "done" || j.cell_gone) && j.reactivatable ? `<button class="btn reactivate" data-slug="${esc(j.slug)}">↺ reactivate</button>` : ""}
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
      clearSteerChips();
      clearSteerDocq();
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
      clearEditorDocq();
      editorOverlay.hidden = true;
    }
    // freshest jobsBySlug just landed — e.g. a just-created job finally
    // showing up, or an err clearing — re-derive arming from it
    if (!editorOverlay.hidden) updateEditorQueue();
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
    flags += docQuestionsOpenP0(j);
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
  // build-stat is NOT written here: it comes off the same record the ⟳
  // control does, so renderUpdate() owns it (one reader, one repaint path,
  // and it tracks the 1s status poll too instead of only this 3s one)
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

  // the ⟳ UPDATE control rides the same payload — board.update, absent on an
  // older server, which reads as a plain idle button (see the self-update
  // section at the bottom of this file)
  renderUpdateFromBoard(board.update);

  return { byCol, archived };
}

// snapshotDocInputs/restoreDocInputs preserve in-progress free-text inputs
// (value + focus + selection) across an innerHTML rebuild of any surface
// that renders .dq-input: #board (renderBoardDOM's wholesale replace on
// every changed poll), #steer-docq (fillSteerContext re-rendering the steer
// modal's own copy of the panel on open/chip-more/post-answer), and
// #editor-docq (fillEditorDocq doing the same for the job editor, which is
// where a done job's questions live now — see settledJob). All are
// wholesale replaces with no diffing, so anything typed into a
// .dq-input is gone the instant one rebuilds unless snapshotted first —
// every other composer lives in a modal textarea, whose draft is
// server-synced instead (see flushSteerDraft), which is why only .dq-input
// (and, since H6, the feedback panel's own .fb-edit-input — #board only, it
// has no modal copy) needs this. Keyed on slug+id (both already ride
// as data attrs on every covered element) rather than DOM position, since
// sortDocQuestions can reorder rows between polls, and rather than which
// surface rendered it, since the same question can appear in both the card
// and an open modal at once — a draft typed in one is meant to
// survive a rebuild of the other. A .dq-input id ("Q<digits>") and an
// .fb-edit-input id (a bare feedback row number) never collide as strings,
// so both element kinds safely share one values map.
//
// A modal copy is only ever a snapshot AUTHORITY while that modal is
// actually visible: snapshotDocInputs skips anything inside a hidden
// #steer-overlay/#editor-overlay, so a closed-but-not-yet-cleared modal
// can't outrank the card's own (visible) copy in document order.
// clearSteerDocq()/clearEditorDocq() (below) additionally empty the panel on
// every close path, so in practice a hidden modal never holds a .dq-input at
// all — the closest() check here is defense in depth for the instant between
// the two. Values are recorded
// even when empty, so a deliberately cleared draft on one surface actually
// propagates as "cleared" instead of being overwritten by the other
// surface's stale non-empty copy.
const DQ_INPUT_SELECTOR =
  "#board .dq-input, #board .fb-edit-input, #steer-docq .dq-input, #editor-docq .dq-input";

// the modal surfaces DQ_INPUT_SELECTOR reaches into — snapshot/restore skip
// an input inside whichever of these is currently hidden, so a
// closed-but-not-yet-cleared modal can never outrank the visible card copy
// in document order.
const HIDDEN_DOCQ_SURFACE = "#steer-overlay, #editor-overlay";

// SCROLL_PANEL_SELECTORS: card-embedded panels whose own scroll position
// (see style.css's per-card max-height caps) must survive a #board rebuild
// the same way input values do — see snapshotDocInputs/restoreDocInputs.
const SCROLL_PANEL_SELECTORS = [".doc-questions", ".job-feedback"];

function docInputKey(el) {
  return JSON.stringify([el.dataset.slug, el.dataset.id]);
}

// clearModalDocq empties a modal's rendered doc-questions panel (#steer-docq
// or #editor-docq). Called from every path that hides that overlay so a
// closed modal can never hold a zombie .dq-input that would otherwise
// outrank the visible card copy in snapshotDocInputs/restoreDocInputs. Each
// modal repopulates its panel on the next open (fillSteerContext /
// fillEditorDocq).
//
// Some close paths (checkStaleModals' auto-close) fire between polls, ahead
// of the next renderBoardDOM snapshot — without help, a draft typed in the
// modal but never sent would be destroyed here before it ever reaches the
// card. So before wiping, carry any non-empty modal draft into the card's
// own copy of the same question — but never clobber a non-empty card draft;
// that one's the user's most recent typing and wins. A settled job's card
// has no copy to carry into (see settledJob), so that draft is simply
// dropped — same as any other unsent text in a modal that's closing.
function clearModalDocq(elID) {
  const docqEl = document.getElementById(elID);
  if (!docqEl) return;
  const modalValues = {};
  docqEl.querySelectorAll(".dq-input").forEach((el) => {
    if (el.value) modalValues[docInputKey(el)] = el.value;
  });
  if (Object.keys(modalValues).length) {
    document.querySelectorAll("#board .dq-input").forEach((el) => {
      const key = docInputKey(el);
      if (!el.value && key in modalValues) el.value = modalValues[key];
    });
  }
  docqEl.innerHTML = "";
}

const clearSteerDocq = () => clearModalDocq("steer-docq");
const clearEditorDocq = () => clearModalDocq("editor-docq");

function snapshotDocInputs() {
  const values = {};
  document.querySelectorAll(DQ_INPUT_SELECTOR).forEach((el) => {
    if (el.closest(HIDDEN_DOCQ_SURFACE)?.hidden) return; // hidden surface is never a snapshot authority
    values[docInputKey(el)] = el.value;
  });
  const active = document.activeElement;
  let focused = null;
  if (active && active.matches && active.matches(DQ_INPUT_SELECTOR)) {
    focused = { key: docInputKey(active), start: active.selectionStart, end: active.selectionEnd };
  }
  const scroll = {};
  document.querySelectorAll(".card[data-slug]").forEach((card) => {
    SCROLL_PANEL_SELECTORS.forEach((sel) => {
      const el = card.querySelector(sel);
      if (el && el.scrollTop) scroll[card.dataset.slug + sel] = el.scrollTop;
    });
  });
  return { values, focused, scroll };
}

function restoreDocInputs({ values, focused, scroll }) {
  if (Object.keys(values).length || focused) {
    let focusEl = null;
    document.querySelectorAll(DQ_INPUT_SELECTOR).forEach((el) => {
      if (el.closest(HIDDEN_DOCQ_SURFACE)?.hidden) return; // never restore into / focus a hidden surface
      const key = docInputKey(el);
      if (key in values) el.value = values[key];
      if (focused && key === focused.key) focusEl = el;
    });
    if (focusEl) {
      focusEl.focus();
      if (focused.start != null) {
        try { focusEl.setSelectionRange(focused.start, focused.end); } catch {}
      }
    }
  }
  // E5: .card .doc-questions (and, since H6, .job-feedback) gained a scroll
  // cap (style.css) — renderBoardDOM replaces #board wholesale, which
  // destroys the scroller and its scrollTop on every poll, so restore it
  // per-card+panel the same way input values are restored above.
  if (scroll && Object.keys(scroll).length) {
    document.querySelectorAll(".card[data-slug]").forEach((card) => {
      SCROLL_PANEL_SELECTORS.forEach((sel) => {
        const key = card.dataset.slug + sel;
        if (key in scroll) {
          const el = card.querySelector(sel);
          if (el) el.scrollTop = scroll[key];
        }
      });
    });
  }
}

// renderBoardDOM is the expensive part of render(): only called when
// board.jobs actually changed (see refresh()).
function renderBoardDOM(byCol, archived) {
  const dqState = snapshotDocInputs();
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
  restoreDocInputs(dqState);
}

// updateAges refreshes the age chips and wakeup countdowns in place without
// touching the rest of the DOM — runs every poll (see refresh()), including
// polls that skip the full board rebuild, so "3m" keeps ticking toward "4m"
// (and "next check in 1m 32s" keeps counting down) even when nothing else
// changed. textContent only — never innerHTML.
function updateAges() {
  document.querySelectorAll("[data-age]").forEach((el) => {
    el.textContent = age(+el.dataset.age);
  });
  document.querySelectorAll("[data-wakeup-fires]").forEach((el) => {
    el.textContent = `⏱ next check in ${wakeupLabel(+el.dataset.wakeupFires)}`;
  });
  document.querySelectorAll("[data-resets]").forEach((el) => {
    el.textContent = usageCountdown(+el.dataset.resets);
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
      <span>${esc(j.repo)}</span>
      <span data-age="${j.mtime}">${age(j.mtime)}</span>
    </div>
    ${j.outcome ? `<div class="outcome">→ ${esc(j.outcome)}</div>` : ""}
    ${chips ? `<div class="chips">${chips}</div>` : ""}
    ${feedbackHTML(j)}
    <div class="review-row">
      <button class="btn unarchive" data-slug="${esc(j.slug)}">⬆ unarchive</button>
      <button class="btn danger delete" data-slug="${esc(j.slug)}">✕ delete forever</button>
    </div>
  </div>`;
}

// archiveDrawerHTML renders the drawer inside the DONE column's section —
// re-created on every render, so its toggle is handled by the delegated
// document click listener below, never a direct element listener. Always
// rendered (even with zero archived jobs) since it doubles as the drop
// target for a done→archived drag; data-col="archived" mirrors the .col
// sections' own data-col so the drag handlers can treat it uniformly.
function archiveDrawerHTML(archived) {
  const hasArchived = archived.length > 0;
  return `<div class="archive-drawer" data-col="archived">
    <button class="archive-head" id="archive-toggle" ${hasArchived ? "" : "disabled"}>
      <span class="tri">${hasArchived ? (archiveOpen ? "▾" : "▸") : "·"}</span> ARCHIVED <span class="n">${archived.length}</span>
    </button>
    ${hasArchived ? `<div class="archive-cards" ${archiveOpen ? "" : "hidden"}>
      ${archived.map(archivedCardHTML).join("")}
    </div>` : ""}
  </div>`;
}

// ---- drag & drop: within-column reorder + cross-column lifecycle moves ----
//
// A drag target is either a COLUMNS section (.col, data-col=<state>) or the
// archive drawer (.archive-drawer, data-col="archived") — dropZone() below
// resolves whichever the pointer is actually over, letting the rest of the
// handlers treat both uniformly via .dataset.col. Dragging over the SAME
// column as the drag started in is a reorder — the card live-shifts into its
// slot as the pointer moves (see flipMove) and /api/reorder saves the DOM
// order on drop; dragging over a different legal column/drawer is a
// lifecycle move (post or open the steer modal, per MOVES below).

function dropZone(ev) {
  return ev.target.closest(".archive-drawer, .col");
}

// insertionIndex finds where `y` (a pointer's clientY) falls among `cards`'
// vertical midpoints — the slot a dropped card would land in.
function insertionIndex(cards, y) {
  for (let i = 0; i < cards.length; i++) {
    const rect = cards[i].getBoundingClientRect();
    if (y < rect.top + rect.height / 2) return i;
  }
  return cards.length;
}

// flipMove relocates cardEl to sit immediately before `anchor` within
// cardsEl (null anchor = append at the end), animating every card that
// visibly shifts as a result — FLIP: measure First, mutate to Last, Invert
// with a transform, then transition (Play) back to identity. Used both for
// live same-column reordering on dragover and to snap a card back to its
// origin slot when a drag ends without a committed reorder. A no-op when
// cardEl is already sitting in that slot.
function flipMove(cardsEl, cardEl, anchor) {
  if (cardEl.nextElementSibling === anchor) return;
  const cards = [...cardsEl.querySelectorAll(".card")];
  const first = new Map(cards.map((c) => [c, c.getBoundingClientRect()]));
  cardsEl.insertBefore(cardEl, anchor);
  for (const c of cards) {
    const before = first.get(c);
    const after = c.getBoundingClientRect();
    const dy = before.top - after.top;
    if (Math.abs(dy) < 0.5) continue;
    c.style.transition = "none";
    c.style.transform = `translateY(${dy}px)`;
    c.offsetHeight; // force layout so the transform above paints before we animate away from it
    c.style.transition = "transform 150ms var(--ease)";
    c.style.transform = "";
    c.addEventListener("transitionend", () => { c.style.transition = ""; }, { once: true });
  }
}

document.addEventListener("dragstart", (ev) => {
  // drag-selecting text inside the in-card doc-question input/button row
  // must not also start a card drag — some browsers treat a press-and-drag
  // gesture starting on/over a form control inside a draggable ancestor as
  // dragging the ancestor rather than selecting text, which would turn
  // "select my typed answer to fix a word" into "post a real column move".
  if (ev.target.closest(".doc-questions")) {
    ev.preventDefault();
    ev.stopPropagation();
    return;
  }
  const card = ev.target.closest(".card[draggable='true']");
  if (!card) return;
  dragging = { slug: card.dataset.slug, from: card.dataset.col };
  draggedEl = card;
  dragOriginParent = card.parentElement;
  dragOriginAnchor = card.nextElementSibling;
  lastSlotAnchor = card.nextElementSibling;
  sameColumnDropHandled = false;
  card.classList.add("dragging");
  ev.dataTransfer.effectAllowed = "move";
  ev.dataTransfer.setData("text/plain", dragging.slug);
  for (const to of MOVES[dragging.from] || []) {
    document.querySelector(`.col[data-col="${to}"], .archive-drawer[data-col="${to}"]`)?.classList.add("droptarget");
  }
});

document.addEventListener("dragend", () => {
  // a same-column drop already left the DOM (and boardSnapshot) in its
  // final state — success or reverted — so only an uncommitted drag (ESC,
  // illegal drop, or a cross-column move that live-shifted the source
  // column before crossing) needs snapping back to where it started
  if (dragging && !sameColumnDropHandled && draggedEl && dragOriginParent) {
    flipMove(dragOriginParent, draggedEl, dragOriginAnchor);
  }
  const handled = sameColumnDropHandled;
  dragging = null;
  draggedEl = null;
  dragOriginParent = null;
  dragOriginAnchor = null;
  lastSlotAnchor = undefined;
  sameColumnDropHandled = false;
  document.querySelectorAll(".droptarget, .drophover").forEach((el) =>
    el.classList.remove("droptarget", "drophover"));
  document.querySelectorAll(".card.dragging").forEach((el) => el.classList.remove("dragging"));
  // corrects any staleness accumulated while refresh() was suppressed for
  // the drag (see the `if (dragging) return;` guard in refresh()) — cheap
  // even when nothing changed, since refresh() no-ops on a matching
  // snapshot. Skipped for a handled same-column drop: postReorder() already
  // settled the DOM (quietly on success, force-rebuilt on failure).
  if (!handled) refresh();
});

document.addEventListener("dragover", (ev) => {
  if (!dragging) return;
  const zone = dropZone(ev);
  if (!zone) return;
  const to = zone.dataset.col;
  if (to === dragging.from) {
    const cardsEl = zone.querySelector(".col-cards");
    if (!cardsEl || !draggedEl) return;
    ev.preventDefault();
    ev.dataTransfer.dropEffect = "move";
    // throttle: only touch the DOM when the pointer has actually crossed
    // into a different slot, not on every mousemove-driven dragover tick
    const others = [...cardsEl.querySelectorAll(".card")].filter((c) => c !== draggedEl);
    const anchor = others[insertionIndex(others, ev.clientY)] || null;
    if (anchor === lastSlotAnchor) return;
    lastSlotAnchor = anchor;
    flipMove(cardsEl, draggedEl, anchor);
    return;
  }
  if ((MOVES[dragging.from] || []).includes(to)) {
    ev.preventDefault();
    ev.dataTransfer.dropEffect = "move";
    zone.classList.add("drophover");
  }
});

document.addEventListener("dragleave", (ev) => {
  dropZone(ev)?.classList.remove("drophover");
});

document.addEventListener("drop", async (ev) => {
  const zone = dropZone(ev);
  if (!zone || !dragging) return;
  ev.preventDefault();
  const { slug, from } = dragging;
  const to = zone.dataset.col;

  if (to === from) {
    // same-column reorder: the dragged card is already sitting in its final
    // slot (dragover's live FLIP shifting put it there) — read the order
    // straight off the DOM, no indicator/insertion math needed at drop time
    sameColumnDropHandled = true;
    const cardsEl = zone.querySelector(".col-cards");
    if (!cardsEl) return;
    const slugs = [...cardsEl.querySelectorAll(".card")].map((c) => c.dataset.slug);
    await postReorder(to, slugs);
    return;
  }

  if (!(MOVES[from] || []).includes(to)) return;
  if (from === "draft" && to === "queued") await post("/api/queue", { slug });
  else if (from === "queued" && to === "draft") await post("/api/dequeue", { slug });
  else if (from === "blocked" && to === "done") await post("/api/done", { slug });
  else if (from === "queued" && to === "working") await post("/api/start", { slug });
  else if (from === "working" && to === "blocked") await post("/api/stop", { slug });
  else if (from === "blocked" && to === "working") openSteer(slug, "unblock");
  else if (from === "done" && to === "blocked") await post("/api/reactivate", { slug, text: "" });
  else if (from === "done" && to === "working") openSteer(slug, "reactivate", { requireText: true });
  else if (from === "done" && to === "archived") await post("/api/archive", { slug });
});

// postReorder persists a same-column drag reorder. Unlike post(), it does
// NOT refresh on success: the DOM already shows the final order (dragover's
// live FLIP shifting put it there), and boardSnapshot/lastBoardJobs are
// updated to the predicted post-save shape so the next 3s poll — expected to
// fetch that exact order back — reads as unchanged and skips the #board
// rebuild that would otherwise flash the same content back in. On failure
// the board is force-rebuilt from server truth and a toast explains why.
async function postReorder(column, slugs) {
  try {
    const r = await fetch("/api/reorder", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ column, slugs }),
    });
    const data = await r.json();
    if (!r.ok) throw new Error(data.error || r.status);
    const predicted = predictReorderedJobs(column, slugs);
    if (predicted) {
      lastBoardJobs = predicted;
      boardSnapshot = JSON.stringify(predicted);
    }
    return true;
  } catch (e) {
    showToast(`reorder failed: ${e.message} — reverted`);
    invalidateBoard();
    await refresh();
    return false;
  }
}

// predictReorderedJobs mirrors the server's within-column reorder against
// the last fetched board.jobs: same job objects in the same positions,
// except column members now follow `slugs`' order (buildWebBoard groups
// each display state contiguously and reorders only within it — see
// jobs/order.go's ApplyColumnOrder). Returns null if slugs don't line up
// with what the client already knows, so the caller falls back to the next
// real poll instead of guessing.
function predictReorderedJobs(column, slugs) {
  const bySlug = Object.fromEntries(lastBoardJobs.map((j) => [j.slug, j]));
  const reordered = slugs.map((s) => bySlug[s]).filter(Boolean);
  if (reordered.length !== slugs.length) return null;
  let i = 0;
  return lastBoardJobs.map((j) => (j.state === column ? reordered[i++] : j));
}

// ---- toast: transient bottom-center notice for a reverted failed action ----

let toastTimer = null;

function showToast(message) {
  let el = document.getElementById("toast");
  if (!el) {
    el = document.createElement("div");
    el.id = "toast";
    el.className = "toast";
    document.body.appendChild(el);
  }
  el.textContent = message;
  el.classList.add("show");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.remove("show"), 4000);
}

// post() resolves with the parsed response body so callers that need a
// response field beyond the {"ok": true} convention — e.g. peers/open's
// additive "focus_error" — can read it without a second request; it
// resolves to null on failure, so a bare truthy/falsy check still works at
// every call site that only cares whether the request succeeded.
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
    return data;
  } catch (e) {
    document.getElementById("tick-stat").textContent = "✕ " + e.message;
    invalidateBoard();
    await refresh();
    return null;
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

// artifactParts recovers {slug, home, name} from an /api/artifact URL — the
// same triple the overlay was opened with, needed to address a feedback post
// at the exact artifact being viewed. This IS the overlay's own source of
// truth for what's open (openArtifact's only record of the artifact is
// artifactURL itself), so re-deriving from it here rather than threading a
// separate slug/home/name through every chip is the same-source approach.
// Cell-home names may themselves contain slashes (subpath artifacts), so
// only the first two segments are split off.
function artifactParts(url) {
  const m = url.match(/^\/api\/artifact\/([^/]+)\/([^/]+)\/(.+)$/);
  return m ? { slug: m[1], home: m[2], name: m[3] } : null;
}

let feedbackSavedTimer = null;

function hideFeedbackForm() {
  document.getElementById("feedback-form").hidden = true;
  document.getElementById("feedback-text").value = "";
  document.getElementById("feedback-note").textContent = "";
  // a lingering "saved ✓" must never carry over to another artifact (or
  // stack across rapid sends) — cancel the revert timer and reset now
  clearTimeout(feedbackSavedTimer);
  document.getElementById("panel-feedback").textContent = "feedback";
}

function openArtifact(url, name) {
  document.getElementById("panel-title").textContent = name;
  artifactURL = url;
  rawText = "";
  showRaw = false;
  overlay.hidden = false;
  document.getElementById("panel-raw").hidden = isImage(url) || /\.pdf$/i.test(url);
  document.getElementById("panel-feedback").hidden = false;
  hideFeedbackForm();
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

// refreshModalDocq re-renders whichever open modal carries its own copy of
// slug's doc-questions panel — the steer modal (via fillSteerContext, which
// rebuilds the whole context block) or the job editor (fillEditorDocq). The
// #board rebuild every action triggers never touches modal DOM, so without
// this a row inside an open modal keeps advertising state the board has
// already moved past — and for a done job the modal is the only surface
// showing the question at all (settledJob).
function refreshModalDocq(slug) {
  if (steerOverlay && !steerOverlay.hidden && steerSlug === slug) {
    fillSteerContext(jobsBySlug[slug], steerMode);
  }
  if (editorOverlay && !editorOverlay.hidden && editorSlug === slug) {
    fillEditorDocq(jobsBySlug[slug]);
  }
}

// sendDocQuestionAnswer posts one doc-question answer (either "accept rec"
// or free-text send), disabling the row's controls synchronously so a
// double-click/double-Enter can't double-post. Shared by the click handler's
// .dq-accept/.dq-send branches and the Enter-to-send keydown handler below.
// Always re-fills an open modal's own doc-questions panel afterward (see
// refreshModalDocq) — on success so the row
// flips to answered, and on failure too (post() never rejects, it resolves
// false and already invalidates+refreshes the board), so a failed post
// re-enables the row's controls for a retry instead of leaving them dead
// until the modal is closed and reopened.
function sendDocQuestionAnswer(row, slug, id, text) {
  row.querySelectorAll("button, input").forEach((el) => (el.disabled = true));
  return post("/api/doc-question", { slug, id, text }).then((ok) => {
    if (ok) {
      // success: leave re-answer mode so the next rebuild shows the fresh
      // answer read-only again — post() already rebuilt once against the
      // stale set, so force one more pass to reflect the cleared state now
      // rather than waiting out the rest of the 3s poll interval.
      reansweringDocQ.delete(dqReanswerKey(slug, id));
      invalidateBoard();
      refresh();
    }
    refreshModalDocq(slug);
    return ok;
  });
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
  const dqAccept = ev.target.closest(".dq-accept");
  if (dqAccept) {
    const row = dqAccept.closest(".dq-row");
    sendDocQuestionAnswer(row, dqAccept.dataset.slug, dqAccept.dataset.id, dqAccept.dataset.text);
    return;
  }
  const dqSend = ev.target.closest(".dq-send");
  if (dqSend) {
    const row = dqSend.closest(".dq-row");
    const input = row.querySelector(".dq-input");
    const text = (input.value || "").trim();
    if (!text) return;
    sendDocQuestionAnswer(row, dqSend.dataset.slug, dqSend.dataset.id, text);
    return;
  }
  const dqReanswerCancel = ev.target.closest(".dq-reanswer-cancel");
  if (dqReanswerCancel) {
    reansweringDocQ.delete(dqReanswerKey(dqReanswerCancel.dataset.slug, dqReanswerCancel.dataset.id));
    invalidateBoard();
    refresh();
    // the same question can be showing inside an open modal (its own copy of
    // this panel) — the #board rebuild above never touches modal DOM, so
    // without this the modal's row would keep advertising a re-answer
    // affordance that no longer does anything
    refreshModalDocq(dqReanswerCancel.dataset.slug);
    return;
  }
  // reopen an answered doc question for editing — Q13: answers are editable
  // forever, live or done. Must come after .dq-accept/.dq-send/
  // .dq-reanswer-cancel (distinct classes, but keeping the open-row branches
  // together above this one) and before the generic card-open fallback.
  const dqAnswered = ev.target.closest(".dq-answered");
  if (dqAnswered) {
    reansweringDocQ.add(dqReanswerKey(dqAnswered.dataset.slug, dqAnswered.dataset.id));
    invalidateBoard();
    refresh();
    // see the .dq-reanswer-cancel branch above — same reason: keep an open
    // modal's own copy of this row in sync with the board's.
    refreshModalDocq(dqAnswered.dataset.slug);
    return;
  }
  // ---- job feedback panel ----
  const fbEdit = ev.target.closest(".fb-edit");
  if (fbEdit) {
    fbEditing.add(fbEdit.dataset.id);
    invalidateBoard();
    refresh();
    return;
  }
  const fbCancel = ev.target.closest(".fb-cancel");
  if (fbCancel) {
    fbEditing.delete(fbCancel.dataset.id);
    invalidateBoard();
    refresh();
    return;
  }
  const fbSave = ev.target.closest(".fb-save");
  if (fbSave) {
    const row = fbSave.closest(".fb-row");
    const textarea = row.querySelector(".fb-edit-input");
    const text = (textarea.value || "").trim();
    const noteEl = row.querySelector(".fb-edit-note");
    if (!text) {
      if (noteEl) noteEl.textContent = "feedback is empty";
      return;
    }
    const id = fbSave.dataset.id;
    fbSave.disabled = true;
    post("/api/feedback", { slug: fbSave.dataset.slug, id: Number(id), feedback: text }).then((ok) => {
      if (ok) {
        fbEditing.delete(id);
        invalidateBoard();
        refresh();
        return;
      }
      // post() already rebuilt #board on the failure path (its own
      // invalidateBoard()+refresh() inside the catch branch), which orphans
      // `row`/`noteEl` captured above — writing into them would be a
      // silent no-op. fbEditing still holds `id`, so the fresh row rendered
      // read-only-editing again; re-query it by id to land the message.
      const freshNote = document.querySelector(`.fb-row[data-fb-id="${CSS.escape(id)}"] .fb-edit-note`);
      if (freshNote) {
        freshNote.textContent = "save failed — see status bar";
        return;
      }
      // no fresh row: the note is gone from the board entirely (deleted
      // elsewhere, or its job left the view). The message has nowhere to
      // land — the status bar already carries the failure — so drop the id
      // rather than leaving fbEditing pinned to a row that will never render
      // again, which would silently re-open an edit box if that id ever came
      // back.
      fbEditing.delete(id);
    });
    return;
  }
  const fbDelete = ev.target.closest(".fb-delete");
  if (fbDelete) {
    if (!confirm("delete this feedback note? this can't be undone.")) return;
    fbDelete.disabled = true;
    post("/api/feedback", { slug: fbDelete.dataset.slug, id: Number(fbDelete.dataset.id), delete: true });
    return;
  }
  // must come before the card-open fallback below (same reason as
  // .queue-cancel above): a click into the in-card free-text answer field
  // (doc-question or feedback edit) should focus/type into it, not fall
  // through to opening the steer/editor modal on top of it and stealing
  // focus. See sendDocQuestionAnswer for the Enter-to-send keydown path on
  // the doc-question input.
  if (ev.target.closest(".dq-input") || ev.target.closest(".fb-edit-input")) return;
  const retry = ev.target.closest(".retry");
  if (retry) {
    retry.disabled = true;
    post("/api/retry", { slug: retry.dataset.slug });
    return;
  }
  // must come before the card-open fallback below — it's inside the card's
  // clickable area and would otherwise also open the editor. Non-destructive
  // (no confirm): the spec survives a dequeue, so this just posts straight
  // through, matching the queued→draft drag move above.
  const cancelQueue = ev.target.closest(".queue-cancel");
  if (cancelQueue) {
    cancelQueue.disabled = true;
    post("/api/dequeue", { slug: cancelQueue.dataset.slug });
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
    post("/api/done", { slug: steerDone.dataset.slug }).then((res) => {
      if (res) {
        postDraft.cancel(); // done also clears the draft server-side
        clearSteerChips();
        clearSteerDocq();
        steerOverlay.hidden = true;
      } else {
        steerDone.disabled = false;
      }
    });
    return;
  }
  const tmuxOpen = ev.target.closest(".open-tmux");
  if (tmuxOpen) {
    post("/api/peers/open", { slug: tmuxOpen.dataset.slug }).then((data) => {
      if (data && data.focus_error) showToast(`couldn't focus the window: ${data.focus_error}`);
    });
    return;
  }
  const ccpBtn = ev.target.closest(".u-ccp");
  if (ccpBtn) {
    const text = `ccp ${ccpBtn.dataset.slug}`;
    const prev = ccpBtn.textContent;
    const done = () => {
      ccpBtn.textContent = "copied";
      setTimeout(() => { ccpBtn.textContent = prev; }, 1200);
    };
    if (navigator.clipboard) {
      navigator.clipboard.writeText(text).then(done).catch(() => {});
    } else {
      // --tailnet serves plain http on a non-localhost origin, which is not
      // a secure context: navigator.clipboard is undefined there. Fall back
      // to the legacy textarea + execCommand path.
      const ta = document.createElement("textarea");
      ta.value = text;
      document.body.appendChild(ta);
      ta.select();
      try {
        if (document.execCommand("copy")) done();
      } catch {}
      ta.remove();
    }
    return;
  }
  const makeMain = ev.target.closest(".u-make-main");
  if (makeMain) {
    if (usageActionInFlight) return;
    const slug = makeMain.dataset.slug;
    const email = makeMain.dataset.email || slug;
    if (!confirm(`switch main to ${slug} (${email})? every running session migrates at its next token refresh, and rotation pins to manual.`)) {
      return;
    }
    usageActionInFlight = true;
    makeMain.disabled = true;
    postUsageAction("/api/account/switch", { slug }, makeMain, "switch failed").finally(() => { usageActionInFlight = false; });
    return;
  }
  const switchAuto = ev.target.closest(".u-switch-auto");
  if (switchAuto) {
    if (usageActionInFlight) return;
    usageActionInFlight = true;
    switchAuto.disabled = true;
    postUsageAction("/api/account/mode", { mode: "auto" }, switchAuto, "mode switch failed").finally(() => { usageActionInFlight = false; });
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

// Enter-to-send for the in-card free-text doc-question answer — .dq-input
// had no keyboard submit path at all (only the .dq-send click above), and
// it's the one text input on the board that isn't inside a form. Guard IME
// composition (a CJK candidate's commit Enter also fires key:"Enter") and
// modifier combos before preventDefault — this is prose, and even though a
// posted answer is editable forever (Q13, via reansweringDocQ), swallowing a
// composition commit or a Shift/Alt+Enter here would still silently eat
// whatever the user just typed instead of sending it.
document.addEventListener("keydown", (ev) => {
  if (ev.key !== "Enter") return;
  if (ev.isComposing || ev.keyCode === 229 || ev.shiftKey || ev.altKey) return;
  const input = ev.target.closest(".dq-input");
  if (!input) return;
  ev.preventDefault();
  const text = (input.value || "").trim();
  if (!text) return;
  sendDocQuestionAnswer(input.closest(".dq-row"), input.dataset.slug, input.dataset.id, text);
});

// ---- attachments: paste/drop/pick images into a composer, upload each
// immediately, and track it as a chip. Shared by the steer modal and job
// editor — both keep their own chip array and just call these helpers.

// a Files drop that lands outside a panel's own drop handler (backdrop,
// board, anywhere else) would otherwise navigate the tab to the dropped
// file — block that everywhere, once, at startup. No stopPropagation: the
// panel/document drop handlers below still need to run.
for (const t of ["dragover", "drop"]) {
  window.addEventListener(t, (ev) => {
    if (ev.dataTransfer && ev.dataTransfer.types && ev.dataTransfer.types.includes("Files")) ev.preventDefault();
  });
}

const IMAGE_TYPE_RE = /^image\/(png|jpeg|gif|webp)$/;

function attChipHTML(c) {
  return `<div class="att-chip" data-path="${esc(c.path)}">
    <img src="${esc(c.url)}" alt="">
    <span class="att-name">${esc(c.name)}</span>
    <button class="att-rm" type="button" title="remove">✕</button>
  </div>`;
}

function renderAttChips(containerId, chips) {
  const el = document.getElementById(containerId);
  el.hidden = !chips.length;
  el.innerHTML = chips.map(attChipHTML).join("");
}

async function uploadAttachment(slug, file) {
  const fd = new FormData();
  fd.append("slug", slug);
  fd.append("file", file);
  const r = await fetch("/api/upload", { method: "POST", body: fd });
  const d = await r.json();
  if (!r.ok) throw new Error(d.error || String(r.status));
  return d; // { name, path, url }
}

function pickImageFiles(fileList) {
  return [...fileList].filter((f) => IMAGE_TYPE_RE.test(f.type));
}

// pastedFiles pulls raw File objects out of a paste event's clipboard —
// non-file items (plain text, html) are ignored so a normal text paste
// still lands in the focused field untouched. Deliberately unfiltered by
// type (unlike pickImageFiles) so callers can tell "nothing pasted" apart
// from "files pasted, but none were images" and give feedback for the latter.
function pastedFiles(ev) {
  const items = [...(ev.clipboardData?.items || [])].filter((it) => it.kind === "file");
  return items.map((it) => it.getAsFile()).filter(Boolean);
}

// ---- command chips: /info /aim /do /pr insertion into composer textareas ----
// one-tap insertion of the kraken build-pipeline commands (info -> aim -> do
// -> pr) into whichever prompt composer they're wired to. Always lands at the
// cursor position (replacing any selection) with a trailing space, then
// returns focus to the textarea so typing continues right after the command.
// Fires a bubbling "input" event afterward so listeners already wired to the
// textarea (draft sync, autosave) see the change like any other keystroke.
const CMD_CHIPS = ["/info", "/aim", "/do", "/pr"];

// ---- command highlight: mirrored backdrop rendering /info /aim /do /pr,
// each in its own skill color with glow (info blue, aim red, do yellow, pr
// green), behind the (now text-transparent) composer textareas — a
// <textarea> can't style substrings of its own value, so the real glyphs
// live in a sibling div instead (style.css's .cmd-hl-wrap/.cmd-hl-backdrop,
// unioned with #steer-text/#editor-text's own rule for font/padding/
// border-width so the two surfaces can't drift apart). Escaping runs first,
// then the boundary match — none of the four commands' characters are
// HTML-special, so matching against already-escaped text is safe. Derived
// from CMD_CHIPS (single source) rather than hand-duplicated, and anchored
// on a leading (^|\s) group in addition to the trailing \b: without it a
// mid-word slash-run like "[image: /Users/x/pr-notes.png]" or a path such
// as "web/static/do" lights up too, since \b alone only guards the *end* of
// the match. Accepted gap: the (^|\s) anchor guards the *start* of a run of
// non-whitespace, not "is actually a command" — a bracketed path whose
// first segment happens to be a command still lights up (e.g.
// "[image: /do/x.png]", since "/do" sits right after a space and "/"
// satisfies \b same as a space would). Harmless in practice: kraken's own
// upload paths are ~/.kraken/uploads/..., never /info, /aim, /do, or /pr.
const CMD_HL_RE = new RegExp("(^|\\s)(/(?:" + CMD_CHIPS.map((c) => c.slice(1)).join("|") + "))\\b", "g");

function cmdHighlightHTML(text) {
  // group 1 (the leading boundary char, or "" at start-of-text) is passed
  // through untouched; only group 2 (the command itself) gets wrapped. The
  // span always carries the generic .cmd-hl class (style.css's fallback
  // color/glow) plus a per-command .cmd-hl-<name> class carrying the actual
  // skill color — derived from cmd itself, not a hand-duplicated list, so it
  // can only ever name one of CMD_CHIPS.
  const highlighted = esc(text).replace(CMD_HL_RE, (_m, lead, cmd) => `${lead}<span class="cmd-hl cmd-hl-${cmd.slice(1)}">${cmd}</span>`);
  // a <textarea> reserves a trailing blank row that a pre-wrap div won't
  // unless told to — an unconditional sentinel newline reproduces it so
  // scrollTop syncing never runs the mirror out of vertical room before the
  // real textarea does
  return highlighted + "\n";
}

// syncCmdHighlightGutter keeps the backdrop's right inset matched to
// whatever width the textarea's scrollbar is actually eating. With classic
// (space-taking) scrollbars, `overflow:hidden` on the backdrop reserves no
// gutter, so the textarea's content box — and therefore its line-wrap
// point — ends up ~11-15px narrower than the backdrop's once content
// overflows; without this the two surfaces wrap differently and whole lines
// "ghost" out of alignment. The -2 accounts for the two 1px borders shared
// by the unioned metric rule in style.css.
function syncCmdHighlightGutter(textarea, backdrop) {
  backdrop.style.right = (textarea.offsetWidth - textarea.clientWidth - 2) + "px";
}

// cmdHighlightPending coalesces refreshCmdHighlight calls to one render per
// animation frame per textarea id — the escape+regex+innerHTML-parse pass
// is O(n) in the composer's text length, and re-running it on every single
// keystroke of a multi-KB spec is wasted work once keystrokes arrive faster
// than paint.
const cmdHighlightPending = new Set();

// renderCmdHighlight does the actual synchronous work: re-renders one
// composer's backdrop from its textarea's *current* value. Called directly
// for the first paint (attachCmdHighlight) and via the refreshCmdHighlight
// rAF wrapper everywhere else.
function renderCmdHighlight(textareaId) {
  const textarea = document.getElementById(textareaId);
  const backdrop = document.getElementById(textareaId + "-backdrop");
  if (!textarea || !backdrop) return;
  backdrop.innerHTML = cmdHighlightHTML(textarea.value);
  syncCmdHighlightGutter(textarea, backdrop);
  backdrop.scrollTop = textarea.scrollTop;
}

// refreshCmdHighlight schedules a re-render of one composer's backdrop from
// its textarea's *current* value, coalesced to the next animation frame.
// Call after every programmatic ".value =" write (draft restore, form load,
// inserted text) in addition to the live "input" listener attachCmdHighlight
// wires below — anything that skips a real keystroke skips the browser's own
// "input" event too, and the backdrop would otherwise show stale text.
function refreshCmdHighlight(textareaId) {
  if (cmdHighlightPending.has(textareaId)) return;
  cmdHighlightPending.add(textareaId);
  requestAnimationFrame(() => {
    cmdHighlightPending.delete(textareaId);
    renderCmdHighlight(textareaId);
  });
}

function attachCmdHighlight(textareaId) {
  const textarea = document.getElementById(textareaId);
  const backdrop = document.getElementById(textareaId + "-backdrop");
  const wrap = document.getElementById(textareaId + "-wrap");
  if (!textarea || !backdrop) return;
  // Gates the textareas' color:transparent/caret-color rules (style.css) on
  // JS having actually run — without this class, an exception earlier in
  // this script would leave the mirror never rendered while the textarea's
  // own text stays invisible, i.e. a silently unreadable composer.
  if (wrap) wrap.classList.add("cmd-hl-on");
  renderCmdHighlight(textareaId); // first paint: skip the rAF hop
  textarea.addEventListener("input", () => refreshCmdHighlight(textareaId));
  // Scroll sync stays synchronous (not coalesced through refreshCmdHighlight)
  // — a drag-resize can change the textarea/backdrop width split without
  // firing an "input" event, so the gutter needs recomputing here too, and
  // coalescing scroll itself would visibly lag the mirror behind a fast
  // scroll-wheel fling.
  textarea.addEventListener("scroll", () => {
    backdrop.scrollTop = textarea.scrollTop;
    syncCmdHighlightGutter(textarea, backdrop);
  });
}

// CMD_ACTIVE_RE: one boundary-anchored regex per command, same (^|\s)…\b
// semantics as CMD_HL_RE above but scoped to a single command — lets a chip
// ask "is my command present in the textarea" without matching a different
// command's substring or a mid-word/path occurrence (mirrors iOS'
// CmdChipsRow.isPresent). Built from CMD_CHIPS, not hand-duplicated.
const CMD_ACTIVE_RE = Object.fromEntries(
  CMD_CHIPS.map((cmd) => [cmd, new RegExp("(^|\\s)" + cmd + "\\b")]),
);

// cmdChipsContainers: textareaId -> chips containerId, registered by
// attachCmdChips — lets refreshCmdChips (called from the same programmatic
// ".value =" call sites as refreshCmdHighlight) find the right chip row
// without every caller having to know both ids.
const cmdChipsContainers = {};

// refreshCmdChips re-evaluates one composer's chip row against its
// textarea's *current* value, toggling "active" per chip. Call beside
// refreshCmdHighlight everywhere a textarea's value changes without a real
// keystroke (draft restore, form load, inserted text) — the live "input"
// listener wired below covers real typing and chip-click insertion.
function refreshCmdChips(textareaId) {
  const containerId = cmdChipsContainers[textareaId];
  const containerEl = containerId && document.getElementById(containerId);
  const textarea = document.getElementById(textareaId);
  if (!containerEl || !textarea) return;
  const value = textarea.value;
  for (const cmd of CMD_CHIPS) {
    const btn = containerEl.querySelector(`.cmd-chip-${cmd.slice(1)}`);
    if (btn) btn.classList.toggle("active", CMD_ACTIVE_RE[cmd].test(value));
  }
}

function attachCmdChips(containerId, textareaId) {
  const containerEl = document.getElementById(containerId);
  const textarea = document.getElementById(textareaId);
  if (!textarea || !containerEl) return;
  cmdChipsContainers[textareaId] = containerId;
  containerEl.innerHTML = "";
  for (const cmd of CMD_CHIPS) {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = `cmd-chip cmd-chip-${cmd.slice(1)}`;
    btn.textContent = cmd;
    // never let the button itself take focus — on mobile that dismisses and
    // then reopens the keyboard as focus bounces button -> textarea; the
    // textarea keeps its cursor/selection the whole time regardless
    btn.addEventListener("mousedown", (ev) => ev.preventDefault());
    btn.addEventListener("click", () => {
      if (textarea.disabled || textarea.readOnly) return;
      insertAtCursor(textarea, cmd + " ", { inlineGap: true });
      textarea.focus();
      textarea.dispatchEvent(new Event("input", { bubbles: true }));
    });
    containerEl.appendChild(btn);
  }
  textarea.addEventListener("input", () => refreshCmdChips(textareaId));
  refreshCmdChips(textareaId); // first paint
}

// ---- steer: mid-run instructions ----

const steerOverlay = document.getElementById("steer-overlay");
let steerSlug = null;
let steerMode = "steer"; // "steer" | "reactivate" | "unblock" — same modal, different endpoint
// job.state captured at open time — see checkStaleModals below
let steerOpenState = undefined;
// true only when this "reactivate" open came from a done→working drag —
// the ↺ card/editor button's optional-text behavior is unaffected (they
// never pass opts.requireText)
let steerRequireText = false;

// attachments captured this composer session — dropped whenever the modal
// closes (see clearSteerChips call sites below), never persisted server-side
let steerChips = []; // { name, path, url }
// bumped every time clearSteerChips runs (open or close) — lets an in-flight
// upload tell "still my session" apart from "same slug, but reopened since"
let steerGen = 0;
// in-flight uploadAttachment() calls this session — gates send while > 0
let steerPending = 0;

function clearSteerChips() {
  steerGen++;
  steerChips = [];
  renderAttChips("steer-chips-att", steerChips);
}

function addSteerFiles(files) {
  const slug = steerSlug;
  const gen = steerGen;
  if (!slug) return;
  const all = [...files];
  const imgs = pickImageFiles(all);
  if (all.length && !imgs.length) {
    document.getElementById("steer-note").textContent = "images only (png/jpeg/gif/webp)";
    return;
  }
  imgs.forEach(async (file) => {
    steerPending++;
    try {
      const c = await uploadAttachment(slug, file);
      // stale: composer moved on (closed, or reopened — same or different
      // slug) while this upload was in flight
      if (steerSlug !== slug || steerGen !== gen || steerOverlay.hidden) return;
      steerChips.push(c);
      renderAttChips("steer-chips-att", steerChips);
      document.getElementById("steer-note").textContent = "";
    } catch (e) {
      if (steerSlug !== slug || steerGen !== gen || steerOverlay.hidden) return;
      document.getElementById("steer-note").textContent = "upload failed: " + (e.message || e);
    } finally {
      steerPending--;
    }
  });
}

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
// way). "reactivate" hides the crew and chips (the cell is gone) but keeps
// the messages: a gone cell's last words are the one context that matters
// when deciding to reactivate, and the 2-line card clamp has no other path
// to the full text.
function fillSteerContext(j, mode) {
  const container = document.getElementById("steer-context");
  const crewEl = document.getElementById("steer-crew");
  const prsEl = document.getElementById("steer-prs");
  const detailEl = document.getElementById("steer-detail");
  const lastSentEl = document.getElementById("steer-lastsent");
  const chipsEl = document.getElementById("steer-chips");
  const docqEl = document.getElementById("steer-docq");

  const crew = mode !== "reactivate" && j && crewHTML(j);
  // PRs are job history, not cell state — shown in every mode, including
  // reactivate ("what already shipped" is exactly the reactivation context)
  const prs = j && j.prs && j.prs.length ? j.prs : null;
  const detail = j && j.detail;
  const lastSent = j && j.last_sent;
  const artifacts = mode !== "reactivate" && j && j.artifacts && j.artifacts.length ? j.artifacts : null;
  // doc questions are job history (questions.yaml), not cell state — shown
  // in every mode including reactivate, same as prs above; a torn-down
  // job's questions are still readable, answering just errors via toast.
  // This rebuild (below) is wholesale, same hazard as renderBoardDOM's —
  // snapshot/restore around it so a draft survives. openSteer flips
  // steerOverlay.hidden = false before calling this, so on a fresh open the
  // snapshot below sees the (now-visible) card's draft as the authority and
  // the restore step seeds it straight into the freshly-rendered #steer-docq
  // input — a card-side draft populates a just-opened modal. On a state flip
  // that force-closes the modal, checkStaleModals does touch steer-docq's
  // DOM (via clearSteerDocq), but that function now carries any non-empty
  // modal draft into the card's own copy first, so the close itself doesn't
  // lose it either — this snapshot/restore pair and clearSteerDocq's
  // carry-forward now cover both directions between the two draft copies.
  const docq = j ? docQuestionsHTML(j) : "";

  crewEl.hidden = !crew;
  crewEl.innerHTML = crew || "";

  prsEl.hidden = !prs;
  prsEl.innerHTML = prs ? prs.map(prChipHTML).join("") : "";

  detailEl.hidden = !detail;
  // renderMD escapes before transforming, so agent-authored text is inert
  detailEl.innerHTML = detail ? renderMD(detail) : "";

  lastSentEl.hidden = !lastSent;
  lastSentEl.textContent = lastSent ? `me→zeus: ${lastSent}` : "";

  chipsEl.hidden = !artifacts;
  chipsEl.innerHTML = artifacts ? chipsHTML(artifacts, j.slug, j.slug) : "";

  const dqState = snapshotDocInputs();
  docqEl.hidden = !docq;
  docqEl.innerHTML = docq;
  restoreDocInputs(dqState);

  container.hidden = !crew && !prs && !detail && !lastSent && !artifacts && !docq;
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
// already fetched in openSteer — repo/base/solo/prereqs, shown above the
// rendered spec markdown so the modal answers "what is this cell running
// with" without leaving it.
function specMetaHTML(d) {
  const rows = [
    ["repo", esc(d.repo || "—")],
    ["base", esc(d.base || "origin/main")],
    ["model", esc(d.model || "claude-opus-5")],
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

function openSteer(slug, mode = "steer", opts = {}) {
  steerSlug = slug;
  steerMode = mode;
  clearSteerChips();
  steerPending = 0;
  steerRequireText = !!opts.requireText;
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
  refreshCmdHighlight("steer-text");
  refreshCmdChips("steer-text");
  // fetch the shared draft after the composer is already showing empty —
  // only land it if the modal is still open for this slug and the user
  // hasn't started typing in the meantime
  fetch("/api/draft?slug=" + encodeURIComponent(slug))
    .then((r) => r.json())
    .then((d) => {
      if (steerOverlay.hidden || steerSlug !== slug) return;
      if (text.value === "") {
        text.value = d.text || "";
        refreshCmdHighlight("steer-text");
        refreshCmdChips("steer-text");
      }
    })
    .catch(() => {});
  document.getElementById("steer-note").textContent = "";
  document.getElementById("steer-send").textContent =
    mode === "reactivate" ? "reactivate ⌘⏎" : mode === "unblock" ? "unblock ⌘⏎" : "send ⌘⏎";
  // must flip visible before fillSteerContext: its snapshot/restore pair
  // treats a hidden #steer-overlay as never the snapshot/restore authority,
  // so the freshly-rendered modal input needs to already read as "visible"
  // for the restore step to seed it with the card's current draft.
  steerOverlay.hidden = false;
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

  text.focus();
}

document.getElementById("steer-send").addEventListener("click", async () => {
  if (steerPending > 0) {
    document.getElementById("steer-note").textContent = "uploading image…";
    return;
  }
  const rawText = document.getElementById("steer-text").value.trim();
  // attachments compose as trailing "[image: <path>]" lines — an images-only
  // steer (no typed text) is still a legitimate send
  const imgLines = steerChips.map((c) => "[image: " + c.path + "]").join("\n");
  const text = steerChips.length ? (rawText ? rawText + "\n\n" + imgLines : imgLines) : rawText;
  // a reactivation message is optional (unless this open came from a
  // done→working drag, which requires one); a steer without text or images is a mistake
  if (!text && (steerMode !== "reactivate" || steerRequireText)) {
    document.getElementById("steer-note").textContent = "message is empty";
    return;
  }
  const btn = document.getElementById("steer-send");
  btn.disabled = true;
  const slug = steerSlug;
  // cancel first — a debounce firing mid-send would land after the server's
  // clear and resurrect the sent message
  postDraft.cancel();
  const res = await post(steerMode === "reactivate" ? "/api/reactivate" : "/api/steer", { slug, text });
  btn.disabled = false;
  if (res) {
    // cancel again — a keystroke during the in-flight send would have
    // scheduled a fresh sync that the pre-send cancel never saw
    postDraft.cancel();
    clearSteerChips();
    clearSteerDocq();
    steerOverlay.hidden = true;
  } else {
    document.getElementById("steer-note").textContent = "send failed — see status bar";
    // failed send — re-arm the draft sync with the composer's raw value
    // (matching the input handler, which never trims); chips + text stay put
    postDraft(slug, document.getElementById("steer-text").value);
  }
});
document.getElementById("steer-text").addEventListener("input", (ev) => {
  if (steerSlug) postDraft(steerSlug, ev.target.value);
});
attachCmdChips("steer-cmd-chips", "steer-text");
attachCmdHighlight("steer-text");
steerOverlay.addEventListener("click", (ev) => {
  if (ev.target === steerOverlay) { flushSteerDraft(); clearSteerChips(); clearSteerDocq(); steerOverlay.hidden = true; }
});
document.getElementById("steer-att-btn").addEventListener("click", () => {
  document.getElementById("steer-att-input").click();
});
document.getElementById("steer-att-input").addEventListener("change", (ev) => {
  addSteerFiles(ev.target.files);
  ev.target.value = ""; // allow re-picking the same file
});
document.getElementById("steer-chips-att").addEventListener("click", (ev) => {
  const rm = ev.target.closest(".att-rm");
  if (!rm) return;
  const path = rm.closest(".att-chip").dataset.path;
  steerChips = steerChips.filter((c) => c.path !== path);
  renderAttChips("steer-chips-att", steerChips);
});
const steerPanel = document.querySelector("#steer-overlay .panel");
steerPanel.addEventListener("dragover", (ev) => {
  if (ev.dataTransfer?.types?.includes("Files")) ev.preventDefault();
});
steerPanel.addEventListener("drop", (ev) => {
  if (!ev.dataTransfer?.files?.length) return;
  ev.preventDefault();
  addSteerFiles(ev.dataTransfer.files);
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
  const res = await post("/api/kill", { slug: steerSlug });
  btn.disabled = false;
  if (res) {
    flushSteerDraft(); // kill doesn't clear the draft server-side — preserve it
    clearSteerChips();
    clearSteerDocq();
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
attachCmdChips("editor-cmd-chips", "editor-text");
attachCmdHighlight("editor-text");
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
// true when the freshest save/load says the current spec won't run (loadForm's
// d.err, or doEditorSave's d.warn) — one of updateEditorQueue's arming gates
let editorSyncWarn = false;

// attachments uploaded this composer session — visual confirmation only; the
// [image: <path>] reference already landed in editorText's spec prose, and
// stays there (user-editable text) even after the chip is ✕'d
let editorChips = []; // { name, path, url }
// bumped in openEditor — lets an in-flight upload tell "still my session"
// apart from "same slug, but reopened since" (e.g. a double card-click)
let editorGen = 0;
// in-flight uploadAttachment() calls this session — gates queue while > 0
let editorPending = 0;

function renderEditorChips() {
  renderAttChips("editor-chips-att", editorChips);
}

// insertAtCursor splices text into a textarea at its current cursor position
// (replacing any active selection) and leaves the cursor sitting right after
// it — callers that want focus to follow (e.g. attachCmdChips) call .focus()
// themselves. A textarea's selectionStart/End are never null (0 if it's
// never been focused), so no fallback is needed there. Two opt-in behaviors,
// off by default:
//   - ownLineWhenUnfocused: true (the image-attachment call site) treats a
//     textarea that's lost focus — e.g. an async upload resolving after the
//     user moved on — as stale cursor state and appends at the end instead
//     of trusting a selection the user isn't looking at anymore.
//   - inlineGap: true (the command-chip call site below) closes a gap against
//     the preceding non-whitespace character with a single space instead of
//     a newline — chips land inline with a trailing space, not on their own
//     line.
// Without inlineGap, mid-line text gets its own leading "\n" so it doesn't
// glue onto existing prose (the image-reference case).
function insertAtCursor(textarea, text, opts) {
  const { ownLineWhenUnfocused = false, inlineGap = false } = opts || {};
  const useEnd = ownLineWhenUnfocused && document.activeElement !== textarea;
  const start = useEnd ? textarea.value.length : textarea.selectionStart;
  const end = useEnd ? textarea.value.length : textarea.selectionEnd;
  const prevChar = start > 0 ? textarea.value[start - 1] : "";
  let gap = "";
  if (prevChar) {
    if (inlineGap) { if (!/\s/.test(prevChar)) gap = " "; }
    else if (prevChar !== "\n") gap = "\n";
  }
  const inserted = gap + text;
  textarea.value = textarea.value.slice(0, start) + inserted + textarea.value.slice(end);
  const pos = start + inserted.length;
  textarea.selectionStart = textarea.selectionEnd = pos;
  // programmatic write — no native "input" event, so the command-highlight
  // backdrop (if this textarea has one) needs an explicit nudge; harmless
  // no-op for textareas without a "-backdrop" sibling. Same reasoning for
  // the chip row's active state.
  if (textarea.id) {
    refreshCmdHighlight(textarea.id);
    refreshCmdChips(textarea.id);
  }
  return pos;
}

function addEditorFiles(files) {
  const slug = editorSlug;
  const gen = editorGen;
  if (!slug || editorText.readOnly) return;
  const all = [...files];
  const imgs = pickImageFiles(all);
  if (all.length && !imgs.length) {
    editorNote.style.color = "";
    editorNote.textContent = "images only (png/jpeg/gif/webp)";
    return;
  }
  imgs.forEach(async (file) => {
    editorPending++;
    try {
      const c = await uploadAttachment(slug, file);
      // stale: editor moved on (closed, or reopened — same or different
      // slug) while this upload was in flight
      if (editorSlug !== slug || editorGen !== gen || editorOverlay.hidden) return;
      editorChips.push(c);
      renderEditorChips();
      insertAtCursor(editorText, "[image: " + c.path + "]\n", { ownLineWhenUnfocused: true });
      onEditorFormChange(); // land the inserted reference into the debounced autosave
    } catch (e) {
      if (editorSlug !== slug || editorGen !== gen || editorOverlay.hidden) return;
      editorNote.style.color = "";
      editorNote.textContent = "upload failed: " + (e.message || e);
    } finally {
      editorPending--;
    }
  });
}

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
  const repos = d.repos || [];
  const repoSel = document.getElementById("f-repo");
  repoSel.innerHTML =
    `<option value="">—</option>` +
    repos.map((x) => `<option value="${esc(x)}">${esc(x)}</option>`).join("") +
    (d.repo && !repos.includes(d.repo) ? `<option value="${esc(d.repo)}">${esc(d.repo)}</option>` : "");
  repoSel.value = d.repo || "";
  document.getElementById("f-base").value = d.base || "";
  document.getElementById("f-model").value = d.model || "";
  editorReady = !!d.ready;
  document.getElementById("f-solo").checked = !!d.solo;
  document.getElementById("f-prereqs").innerHTML = (d.prereqs || []).map(prereqRowHTML).join("");
  editorText.value = d.spec || "";
  refreshCmdHighlight("editor-text");
  refreshCmdChips("editor-text");
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
    repo: document.getElementById("f-repo").value.trim(),
    base: document.getElementById("f-base").value.trim(),
    model: document.getElementById("f-model").value.trim(),
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
  document.getElementById("f-repo").disabled = readOnly;
  document.getElementById("f-base").disabled = readOnly;
  document.getElementById("f-model").disabled = readOnly;
  document.getElementById("f-solo").disabled = readOnly;
  document.getElementById("f-add-prereq").disabled = readOnly;
  document.querySelectorAll("#f-prereqs .prereq-row input, #f-prereqs .prereq-row button")
    .forEach((el) => (el.disabled = readOnly));
  editorText.readOnly = readOnly;
  document.getElementById("editor-cmd-chips").hidden = readOnly;
  document.getElementById("editor-att-btn").disabled = readOnly;
  // archive lives here, inside the opened job, on purpose: no card-level
  // shortcut — the server still refuses jobs with a cell in flight
  document.getElementById("editor-archive").hidden = readOnly;
  document.getElementById("editor-hint").textContent = readOnly ? "read-only — cell in flight" : "";
}

// updateEditorQueue is the reactive replacement for computing editor-queue's
// visibility once in loadForm — that one-shot snapshot read a stale board
// that could be missing a job created moments ago, forcing a close/reopen
// before carl could queue it. Called from every path that can change what
// "armed" depends on: form load, autosave completion, live typing, and the
// board catching up on the next poll.
function updateEditorQueue() {
  const btn = document.getElementById("editor-queue");
  const deq = document.getElementById("editor-dequeue");
  const hint = document.getElementById("editor-hint");
  if (!editorSlug || document.getElementById("editor-form").hidden || editorText.readOnly) {
    btn.hidden = true;
    deq.hidden = true;
    return; // readOnly already carries its own hint text (see setFormReadOnly) — don't stomp it
  }
  const j = jobsBySlug[editorSlug];
  // missing from jobsBySlug means the job was just created and the board
  // hasn't polled it in yet — treat that as a fresh draft, not "unarmable"
  const state = j ? j.state : "draft";
  const live = j ? !!j.live : false;
  const boardErr = j ? !!j.err : false; // board-level err (e.g. unhealthy cell) — distinct from editorSyncWarn's form-level d.err/d.warn
  if (state === "queued" && !live) {
    // queued: the modal offers the reverse move instead of the forward one
    btn.hidden = true;
    deq.hidden = false;
    hint.textContent = "queued — cancel returns it to draft";
    return;
  }
  deq.hidden = true;
  if (state !== "draft" || live) {
    btn.hidden = true;
    hint.textContent = "";
    return;
  }
  // a draft's queue button is always present — merely disabled until the
  // spec is non-empty and clean, so the affordance is discoverable from the
  // first moment the modal opens
  btn.hidden = false;
  const armed = editorText.value.trim() !== "" && !editorSyncWarn && !boardErr;
  btn.disabled = !armed;
  hint.textContent = armed ? "synced ✓ — queueable"
    : editorText.value.trim() === "" ? "write a spec to queue" : "";
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
  // too — queue for an err-free draft (arming is reactive, see
  // updateEditorQueue), reactivate for a resumable done job
  const j = jobsBySlug[slug];
  document.getElementById("editor-reactivate").hidden =
    !(j && !j.archived && j.state === "done" && j.reactivatable);
  editorSyncWarn = !!d.err;
  updateEditorQueue();
  if (d.err) editorNote.textContent = "current file won't run: " + d.err;
}

// fillEditorDocq renders the job editor's own copy of the doc-questions
// panel — the click-into surface for a done job's questions, which the card
// no longer shows (settledJob). Rendered for any job that has questions, not
// just done ones: the editor is also what a click on a draft/queued/dead
// card opens, and a question is worth reading wherever you land on the job.
// Wholesale replace, same hazard as fillSteerContext's — snapshot/restore
// around it so a draft typed on either surface survives, plus the panel's own
// scrollTop (this copy is capped, style.css, and the scroller itself is what
// gets replaced — without this, answering one question in a long list throws
// you back to the top). snapshotDocInputs' scroll map only covers card-
// embedded panels, hence the local save/restore here.
function fillEditorDocq(j) {
  const el = document.getElementById("editor-docq");
  const docq = j ? docQuestionsHTML(j) : "";
  const prev = el.querySelector(".doc-questions");
  const scrollTop = prev ? prev.scrollTop : 0;
  const dqState = snapshotDocInputs();
  el.hidden = !docq;
  el.innerHTML = docq ? `<div class="editor-docq-head">questions</div>${docq}` : "";
  restoreDocInputs(dqState);
  if (scrollTop) {
    const next = el.querySelector(".doc-questions");
    if (next) next.scrollTop = scrollTop;
  }
}

function openEditor(slug) {
  editorSlug = slug;
  editorMTime = 0;
  editorReady = false;
  editorStateCaptured = false;
  editorSyncWarn = false;
  editorChips = [];
  editorGen++;
  editorPending = 0;
  renderEditorChips();
  editorNote.textContent = "";
  editorNote.style.color = "";
  const isNew = !slug;
  document.getElementById("editor-title").textContent = isNew ? "new job" : slug + ".job.md";
  document.getElementById("editor-newrow").hidden = !isNew;
  document.getElementById("editor-form").hidden = true;
  document.getElementById("editor-archive").hidden = true;
  document.getElementById("editor-queue").hidden = true;
  document.getElementById("editor-dequeue").hidden = true;
  document.getElementById("editor-reactivate").hidden = true;
  document.getElementById("editor-hint").textContent = "";
  document.getElementById("editor-slug").value = "";
  // flip visible before filling, for the same reason openSteer does: the
  // snapshot/restore pair inside fillEditorDocq treats a hidden
  // #editor-overlay as never the authority, so the freshly-rendered panel
  // has to already read as visible for a card-side draft to seed it
  editorOverlay.hidden = false;
  fillEditorDocq(slug ? jobsBySlug[slug] : null);
  if (isNew) {
    document.getElementById("editor-slug").focus();
    return;
  }
  loadForm(slug).catch((e) => { editorNote.textContent = String(e.message || e); });
}

document.getElementById("editor-queue").addEventListener("click", async () => {
  if (!editorSlug) return;
  if (editorPending > 0) {
    editorNote.textContent = "uploading image…";
    return;
  }
  const btn = document.getElementById("editor-queue");
  btn.disabled = true;
  await flushEditorAutosave(); // land any edit still sitting in the debounce window before queueing
  const res = await post("/api/queue", { slug: editorSlug });
  if (res) {
    clearEditorDocq();
    editorOverlay.hidden = true;
  } else {
    editorNote.textContent = "queue failed — see status bar";
    updateEditorQueue(); // re-derive disabled instead of blindly re-enabling
  }
});

// the modal twin of the queued card's ✕: return the job to draft without
// leaving the editor — the buttons flip back to queue via updateEditorQueue
// on the refresh that post() triggers
document.getElementById("editor-dequeue").addEventListener("click", async () => {
  if (!editorSlug) return;
  const deq = document.getElementById("editor-dequeue");
  deq.disabled = true;
  // rebase the staleness baseline BEFORE posting: post() awaits the board
  // refresh, so checkStaleModals runs against the new queued→draft state
  // while we're still awaiting — a stale "queued" baseline there would read
  // our own action as someone else's change and close the editor
  const prevState = editorOpenState;
  const prevCaptured = editorStateCaptured;
  editorOpenState = "draft";
  editorStateCaptured = true;
  const res = await post("/api/dequeue", { slug: editorSlug });
  deq.disabled = false;
  if (!res) {
    editorOpenState = prevState;
    editorStateCaptured = prevCaptured;
    editorNote.textContent = "cancel failed — see status bar";
  }
  updateEditorQueue();
});

// reactivate is a composer flow, not a one-shot post — hand off to the
// steer modal in reactivate mode (optional message folded into the resume
// prompt), same as the card's ↺ button
document.getElementById("editor-reactivate").addEventListener("click", () => {
  if (!editorSlug) return;
  clearEditorDocq();
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
  updateEditorQueue(); // e.g. spec just went empty/non-empty — reflect it immediately, don't wait on the round trip
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
    editorSyncWarn = !!d.warn;
    updateEditorQueue();
    invalidateBoard();
    refresh();
  } catch (e) {
    editorNote.style.color = "";
    editorNote.textContent = "sync failed: " + (e.message || e);
  }
}

document.getElementById("editor-form").addEventListener("input", onEditorFormChange);
document.getElementById("editor-form").addEventListener("change", onEditorFormChange);

document.getElementById("editor-att-btn").addEventListener("click", () => {
  document.getElementById("editor-att-input").click();
});
document.getElementById("editor-att-input").addEventListener("change", (ev) => {
  // stop it here — otherwise it bubbles into #editor-form's delegated
  // change listener above and fires a spurious autosave before the upload
  // (and its insertAtCursor) has actually landed anything new
  ev.stopPropagation();
  addEditorFiles(ev.target.files);
  ev.target.value = ""; // allow re-picking the same file
});
document.getElementById("editor-chips-att").addEventListener("click", (ev) => {
  const rm = ev.target.closest(".att-rm");
  if (!rm) return;
  const path = rm.closest(".att-chip").dataset.path;
  editorChips = editorChips.filter((c) => c.path !== path);
  renderEditorChips();
});
const editorPanel = document.querySelector("#editor-overlay .panel");
editorPanel.addEventListener("dragover", (ev) => {
  if (editorText.readOnly) return;
  if (ev.dataTransfer?.types?.includes("Files")) ev.preventDefault();
});
editorPanel.addEventListener("drop", (ev) => {
  if (editorText.readOnly || !ev.dataTransfer?.files?.length) return;
  ev.preventDefault();
  addEditorFiles(ev.dataTransfer.files);
});

// document-level (not overlay-level) so ⌘V/Ctrl-V works even when focus
// sits on a non-input element inside the panel — mirrors the Escape
// handler's layering: steer is checked (and wins) before editor, so if both
// overlays somehow ended up open at once, a paste can't land in both.
document.addEventListener("paste", (ev) => {
  if (!steerOverlay.hidden) {
    const files = pastedFiles(ev);
    if (files.length) { ev.preventDefault(); addSteerFiles(files); }
    return;
  }
  if (!editorOverlay.hidden && !editorText.readOnly) {
    const files = pastedFiles(ev);
    if (files.length) { ev.preventDefault(); addEditorFiles(files); }
  }
});

document.getElementById("editor-archive").addEventListener("click", async () => {
  if (!editorSlug) return;
  const res = await post("/api/archive", { slug: editorSlug });
  if (res) { clearEditorDocq(); editorOverlay.hidden = true; }
  else editorNote.textContent = "archive failed — see status bar";
});

editorOverlay.addEventListener("click", (ev) => {
  if (ev.target === editorOverlay) { flushEditorAutosave(); clearEditorDocq(); editorOverlay.hidden = true; }
});
document.getElementById("editor-form").addEventListener("keydown", (ev) => {
  if (ev.key === "Enter" && (ev.metaKey || ev.ctrlKey)) {
    ev.preventDefault();
    const readyBtn = document.getElementById("editor-queue");
    if (!readyBtn.hidden && !readyBtn.disabled) readyBtn.click();
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
document.getElementById("panel-feedback").addEventListener("click", () => {
  const form = document.getElementById("feedback-form");
  form.hidden = !form.hidden;
  if (!form.hidden) document.getElementById("feedback-text").focus();
});
document.getElementById("feedback-cancel").addEventListener("click", () => hideFeedbackForm());
document.getElementById("feedback-send").addEventListener("click", async () => {
  const note = document.getElementById("feedback-note");
  const parts = artifactParts(artifactURL);
  const text = document.getElementById("feedback-text").value.trim();
  if (!parts) { note.textContent = "can't identify this artifact"; return; }
  if (!text) { note.textContent = "feedback is empty"; return; }
  const sendBtn = document.getElementById("feedback-send");
  sendBtn.disabled = true;
  try {
    const r = await fetch("/api/feedback", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ slug: parts.slug, home: parts.home, name: parts.name, feedback: text }),
    });
    const data = await r.json();
    if (!r.ok) throw new Error(data.error || String(r.status));
    hideFeedbackForm();
    invalidateBoard(); // feedback rides the board payload — the card's panel must pick this up too
    refresh();
    const label = document.getElementById("panel-feedback");
    label.textContent = "saved ✓";
    feedbackSavedTimer = setTimeout(() => { label.textContent = "feedback"; }, 2000);
  } catch (e) {
    note.textContent = "failed: " + e.message;
  } finally {
    sendBtn.disabled = false;
  }
});
overlay.addEventListener("click", (ev) => { if (ev.target === overlay) overlay.hidden = true; });
// one Escape closes one layer: the artifact viewer if it's on top (it can be
// opened from inside the steer/unblock modal), else whichever of editor/steer
// is open underneath.
document.addEventListener("keydown", (ev) => {
  if (ev.key !== "Escape") return;
  if (!overlay.hidden) { overlay.hidden = true; return; }
  if (!steerOverlay.hidden) { flushSteerDraft(); clearSteerChips(); clearSteerDocq(); steerOverlay.hidden = true; return; }
  if (!editorOverlay.hidden) { flushEditorAutosave(); clearEditorDocq(); editorOverlay.hidden = true; return; }
  if (!usageOverlay.hidden) { usageOverlay.hidden = true; return; }
});

// ---- dag view: jobs layered by dependency depth, edges = prereqs ----

let view = localStorage.getItem("kraken-view") || "board";

// setView is the tabs' handler, and also how a surface outside the tab strip
// sends you somewhere — the failed-update banner's OPEN LOG, say.
function setView(v) {
  view = v;
  localStorage.setItem("kraken-view", view);
  applyView();
  invalidateBoard(); // switching to dag must render it even though jobs are unchanged
  refresh();
}

document.querySelectorAll(".viewtab").forEach((tab) =>
  tab.addEventListener("click", () => setView(tab.dataset.view)));

function applyView() {
  document.querySelectorAll(".viewtab").forEach((t) =>
    t.classList.toggle("active", t.dataset.view === view));
  document.getElementById("board").hidden = view !== "board";
  document.getElementById("dag").hidden = view !== "dag";
  document.getElementById("log").hidden = view !== "log";
  if (view === "log") refreshLog();
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
        <span class="nstate">● ${esc(j.state)}</span><span class="nrepo">${esc(j.repo)}</span>
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

// ---- usage widget: header at-a-glance + drilldown modal over GET /api/usage
// (core.AccountUsage — see api/usage.go). The "main" account is whichever
// entry carries current:true (the server sorts it first); everything else is
// pool. Absent on an older server (404) or offline (network error) — the
// widget stays hidden rather than showing broken state, see fetchUsage.

const usageStat = document.getElementById("usage-stat");
const usageOverlay = document.getElementById("usage-overlay");
let usageData = null; // last successful /api/usage payload

// usageActionInFlight gates both account-rotation click handlers below (make
// main / switch back to auto) against a double-fire: a rapid double-click,
// or the 60s poll's fetchUsage() replacing the button node mid-click (the
// per-button .disabled flag alone can't catch that — a freshly re-rendered
// button starts out enabled again even while the first click's request is
// still in flight).
let usageActionInFlight = false;

// usageColorClass thresholds a percent into the same ok/warn/danger palette
// used everywhere else in the board (solved/revealed/counter).
function usageColorClass(percent) {
  if (percent >= 80) return "u-danger";
  if (percent >= 50) return "u-warn";
  return "u-ok";
}

// usageCountdown formats a resets_at (unix millis) as a compact countdown —
// "3h12m", "4d", "8m", "now" — mirroring wakeupLabel's countdown feel but at
// day/hour/minute granularity instead of minutes/seconds.
function usageCountdown(millis) {
  const remainingS = Math.floor((millis - Date.now()) / 1000);
  if (remainingS <= 0) return "now";
  const d = Math.floor(remainingS / 86400);
  if (d >= 1) return `${d}d`;
  const h = Math.floor(remainingS / 3600);
  const m = Math.floor((remainingS % 3600) / 60);
  return h >= 1 ? `${h}h${m}m` : `${m}m`;
}

function usageMainAccount(data) {
  const accounts = data.accounts || [];
  return accounts.find((a) => a.current) || accounts[0];
}

function usageSegHTML(label, percent) {
  return `<span class="u-seg ${usageColorClass(percent)}">${esc(label)} ${Math.round(percent)}%</span>`;
}

// usageStatHTML renders the header widget's inner content: the current
// account's session/weekly/model segments, a pool summary, and a staleness
// note. Never touches `hidden` — fetchUsage only reveals the widget after a
// successful parse, so a transient error can't flash broken markup into view.
function usageStatHTML(data) {
  const accounts = data.accounts || [];
  if (!data.available || !accounts.length) return `<span class="dim">usage —</span>`;
  const main = usageMainAccount(data);
  const segs = [];
  if (main.session) segs.push(usageSegHTML("5h", main.session.percent));
  if (main.weekly) segs.push(usageSegHTML("7d", main.weekly.percent));
  for (const m of main.models || []) segs.push(usageSegHTML(m.name.toLowerCase(), m.percent));
  const pool = accounts.filter((a) => a !== main);
  let poolHTML = "";
  if (pool.length) {
    const weeklies = pool.map((a) => a.weekly?.percent).filter((p) => p !== undefined);
    const worst = weeklies.length ? Math.max(...weeklies) : null;
    poolHTML = `<span class="u-sep">·</span><span class="u-pool">pool ${pool.length}` +
      (worst !== null ? ` · worst 7d <span class="${usageColorClass(worst)}">${Math.round(worst)}%</span>` : "") +
      `</span>`;
  }
  const stale = !data.daemon_ok || data.age_seconds > 900
    ? `<span class="u-stale">(stale ${Math.floor(data.age_seconds / 60)}m)</span>`
    : "";
  // manual-pin chip: absent on an older server (rotation_mode undefined
  // there, so this just never equals "manual") and absent in auto mode too
  // — status quo look unless someone has actually pinned the main account.
  const modeChip = data.rotation_mode === "manual"
    ? `<span class="u-sep">·</span><span class="u-chip u-chip-warn">manual</span>`
    : "";
  const out = segs.join("") + poolHTML + modeChip + stale;
  // an account with no windows at all (fresh install, reauth everywhere)
  // must not leave a visible-but-blank widget
  return out || `<span class="dim">usage —</span>`;
}

// usageBarHTML renders one labeled window row (session/weekly/a model) —
// track+fill bar plus a right-aligned "P% · <reset-in>". Returns "" for an
// absent window (e.g. reauth/no_creds accounts have none) so callers can just
// filter(Boolean) the rows together.
function usageBarHTML(label, w) {
  if (!w) return "";
  const pct = Math.max(0, Math.min(100, w.percent));
  const cls = usageColorClass(w.percent);
  const reset = w.resets_at ? ` · <span data-resets="${w.resets_at}">${usageCountdown(w.resets_at)}</span>` : "";
  return `<div class="u-row">
    <span class="u-row-label">${esc(label)}</span>
    <span class="u-bar"><span class="u-bar-fill ${cls}" style="width:${pct}%"></span></span>
    <span class="u-row-val">${Math.round(w.percent)}%${reset}</span>
  </div>`;
}

// usageChipsHTML renders an account's status chips — cooldown/reauth/no_creds/
// stale/error — any subset of which may apply at once.
function usageChipsHTML(a) {
  const chips = [];
  if (a.cooldown_until && a.cooldown_until > Date.now()) {
    chips.push(`<span class="u-chip u-chip-warn">cooling ${usageCountdown(a.cooldown_until)}</span>`);
  }
  if (a.reauth) chips.push(`<span class="u-chip u-chip-danger">reauth needed — ccp login</span>`);
  if (a.no_creds) chips.push(`<span class="u-chip u-chip-danger">no creds</span>`);
  if (a.stale) chips.push(`<span class="u-chip u-chip-dim">stale</span>`);
  if (a.error) chips.push(`<span class="u-chip u-chip-danger" title="${esc(a.error)}">error</span>`);
  return chips.join("");
}

// usageAccountHTML renders one account block: header line (slug, current
// chip, email, tier, status chips, make-main + pool-only ccp buttons) plus a
// bar row per window. Rows are empty (no session/weekly/models) for
// reauth/no_creds accounts — the block then falls back to just the header.
// `data` is the whole /api/usage payload (not just this account) — it's only
// consulted for rotation_mode, to gate the make-main button off entirely on
// an older server that never sends the field (see the click handler for
// ".u-make-main" and the POST /api/account/switch contract).
function usageAccountHTML(a, data) {
  const tier = esc((a.tier || "").replace(/^default_claude_/, ""));
  const rows = [usageBarHTML("session", a.session), usageBarHTML("weekly", a.weekly)]
    .concat((a.models || []).map((m) => usageBarHTML(m.name.toLowerCase(), m)))
    .filter(Boolean)
    .join("");
  const ccpBtn = a.current ? "" : `<button class="btn ghost mini u-ccp" data-slug="${esc(a.slug)}" title="copy ccp ${esc(a.slug)}">⧉ ccp</button>`;
  // make-main: any non-current account with usable creds — cooling/stale
  // rows still get the button (the server is the one that decides whether a
  // switch to them actually succeeds), only no_creds/reauth rows don't. A
  // row whose email mirrors the current main account's is also excluded:
  // it's the save-back slot a rotation just parked the outgoing main creds
  // into, and core refuses to ever rotate onto it (core.ErrSaveBackIsTarget)
  // — every rotation would otherwise re-offer a "make main" button here that
  // is guaranteed to fail.
  const main = usageMainAccount(data);
  const mirrorsMain = !!(a.email && main && main.email && a.email.toLowerCase() === main.email.toLowerCase());
  const canMakeMain = data.rotation_mode !== undefined && !a.current && !a.no_creds && !a.reauth && !mirrorsMain;
  const makeMainBtn = canMakeMain
    ? `<button type="button" class="btn ghost mini u-make-main" data-slug="${esc(a.slug)}" data-email="${esc(a.email || a.slug)}" title="switch the main account to ${esc(a.slug)}">make main</button>`
    : "";
  return `<div class="u-account">
    <div class="u-account-head">
      <span class="u-slug">${esc(a.slug)}</span>
      ${a.current ? `<span class="u-chip u-chip-current">current</span>` : ""}
      ${a.email ? `<span class="dim">${esc(a.email)}</span>` : ""}
      ${tier ? `<span class="u-chip">${tier}</span>` : ""}
      ${usageChipsHTML(a)}
      <span class="spacer"></span>
      ${makeMainBtn}
      ${ccpBtn}
    </div>
    ${rows ? `<div class="u-rows">${rows}</div>` : ""}
  </div>`;
}

// rotationModeChipHTML renders the drilldown panel-head's always-visible
// mode chip ("rotation: auto" / "rotation: manual"). Undefined
// rotation_mode (older server) renders nothing, same gate as everywhere else
// in this section.
function rotationModeChipHTML(data) {
  if (data.rotation_mode === undefined) return "";
  const manual = data.rotation_mode === "manual";
  return `<span class="u-chip ${manual ? "u-chip-warn" : "u-chip-ok"}">rotation: ${manual ? "manual" : "auto"}</span>`;
}

// manualBannerHTML renders the "pinned to X" banner at the top of the
// drilldown body when rotation is manually pinned. rotation_set_at is unix
// millis (0 = default/unset, per the API contract) — age() already treats a
// falsy unix as "" so the trailing "<time> ago" clause just drops out
// instead of rendering "0s ago" or similar nonsense. The CTA POSTs
// /api/account/mode {mode:"auto"}; see the ".u-switch-auto" click handler.
function manualBannerHTML(data) {
  if (data.rotation_mode !== "manual") return "";
  const main = usageMainAccount(data) || {};
  const who = main.email || "main";
  const setBy = data.rotation_set_by || "unknown";
  const whenS = data.rotation_set_at ? age(Math.floor(data.rotation_set_at / 1000)) : "";
  const when = whenS ? ` ${whenS} ago` : "";
  return `<div class="u-manual-banner">
    <span>pinned to ${esc(who)} by ${esc(setBy)}${when} — auto-rotation off</span>
    <button type="button" class="btn ghost mini u-switch-auto">switch back to auto</button>
  </div>`;
}

// renderUsageModal fills the drilldown's head (fetched-ago / daemon-down
// warning) and body (one block per account, server order = current-first).
function renderUsageModal(data) {
  document.getElementById("usage-mode-chip").innerHTML = rotationModeChipHTML(data);
  const head = document.getElementById("usage-fetched");
  if (!data.available) {
    head.innerHTML = `<span class="dim">no data yet</span>`;
  } else if (!data.daemon_ok) {
    head.innerHTML = `<span class="u-chip u-chip-danger">daemon down — data frozen</span>`;
  } else {
    head.innerHTML = `<span class="dim">fetched ${age(Math.floor(data.fetched_at / 1000))} ago · daemon</span>`;
  }
  const body = document.getElementById("usage-body");
  const accounts = data.accounts || [];
  body.innerHTML = manualBannerHTML(data) + (accounts.length
    ? accounts.map((a) => usageAccountHTML(a, data)).join("")
    : `<div class="empty">no usage data yet</div>`);
}

// postUsageAction is the account-rotation counterpart of postReorder: POST,
// and on success let fetchUsage() do the re-render (it repaints both the
// header widget and, when open, this drilldown — same path a normal 60s
// poll takes, so the new mode chip/banner/buttons just fall out of it). On
// failure, toast the server's error and re-enable the button that triggered
// it so a transient failure doesn't leave a dead control behind.
async function postUsageAction(url, body, button, errLabel) {
  try {
    const r = await fetch(url, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    const data = await r.json();
    if (!r.ok) throw new Error(data.error || String(r.status));
    await fetchUsage();
    return true;
  } catch (e) {
    showToast(`${errLabel}: ${e.message}`);
    return false;
  } finally {
    // Always re-enable, not just on the catch path: fetchUsage() swallows
    // its own network errors internally (see its try/catch) rather than
    // throwing, so a POST success followed by a silently-failed refetch
    // would otherwise skip both the catch branch above AND the re-render
    // that would normally discard this (disabled) button node — leaving a
    // dead, permanently-disabled control until the next 60s poll happens to
    // succeed. Harmless no-op when the button was already discarded by a
    // successful re-render.
    if (button) button.disabled = false;
  }
}

async function fetchUsage() {
  try {
    const r = await fetch("/api/usage");
    if (!r.ok) return; // 404 on an older server — leave the widget hidden
    usageData = await r.json();
    usageStat.innerHTML = usageStatHTML(usageData);
    usageStat.hidden = false;
    if (!usageOverlay.hidden) renderUsageModal(usageData);
  } catch {
    // network error — leave the widget in whatever state it was already in
  }
}

usageStat.addEventListener("click", () => {
  usageOverlay.hidden = false;
  if (usageData) renderUsageModal(usageData);
  fetchUsage();
});
// role="button" promises keyboard operability — honor Enter/Space
usageStat.addEventListener("keydown", (ev) => {
  if (ev.key === "Enter" || ev.key === " ") {
    ev.preventDefault();
    usageStat.click();
  }
});
usageOverlay.addEventListener("click", (ev) => {
  if (ev.target === usageOverlay) usageOverlay.hidden = true;
});

fetchUsage();
setInterval(fetchUsage, 60000);

// ---- unified event log view ----
//
// Pagination is file-granular (the server rotates by size): the view loads
// the newest log file, and scrolling to the top pulls the previous file into
// the DOM, one fetch per file, using the `prev` name each response carries.
// State is a chain of loaded files oldest→newest; the newest entry of the
// chain is re-fetched on every poll so live events stream in.

let logChain = []; // file names, oldest→newest; last is the live "latest"
let logData = {}; // name → { entries, prev }
let logLoading = false;

const logTime = (ts) => {
  const d = new Date(ts);
  const p = (n) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}/${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
};

function logRowHTML(e) {
  return `<div class="log-row lvl-${esc(e.level)}">
    <span class="log-ts">${logTime(e.ts)}</span>
    <span class="log-src">${esc(e.src)}</span>
    <span class="log-event">${esc(e.event)}</span>
    <span class="log-msg">${esc(e.msg)}</span>
  </div>`;
}

// renderLog rebuilds the entry list. keepBottom pins the view to the newest
// entry (initial load / already-at-bottom tail-follow); otherwise the scroll
// offset is preserved relative to the content that was already there, so
// prepending an older file doesn't yank the view.
function renderLog(keepBottom) {
  const scroll = document.getElementById("log-scroll");
  const entriesEl = document.getElementById("log-entries");
  const prevHeight = scroll.scrollHeight;
  const prevTop = scroll.scrollTop;
  const parts = [];
  for (const name of logChain) {
    parts.push(`<div class="log-file-divider">${esc(name)}</div>`);
    parts.push(...(logData[name]?.entries || []).map(logRowHTML));
  }
  entriesEl.innerHTML = parts.join("") || '<div class="log-empty">no log entries yet</div>';
  // the head of the chain still having a prev means more history exists
  const first = logChain[0];
  document.getElementById("log-more").hidden = !(first && logData[first]?.prev);
  if (keepBottom) {
    scroll.scrollTop = scroll.scrollHeight;
  } else {
    scroll.scrollTop = prevTop + (scroll.scrollHeight - prevHeight);
  }
}

// refreshLog re-fetches the newest file (and adopts a rotation: a new latest
// name appends to the chain, the old latest keeps its cached entries).
async function refreshLog() {
  try {
    const r = await fetch("/api/logs");
    const d = await r.json();
    if (!r.ok || !d.file) return;
    const scroll = document.getElementById("log-scroll");
    const firstLoad = logChain.length === 0;
    // tail-follow only when the user is already looking at the newest lines
    const atBottom = scroll.scrollHeight - scroll.scrollTop - scroll.clientHeight < 40;
    // store mode's virtual "events" file name never rotates (unlike a
    // file-mode log file, which gets a fresh name — and therefore an
    // immediate !logData[d.file] hit — on every rotation): once the store
    // has accrued more than one page's worth of events, entries.length pins
    // at the page size forever even as the window keeps sliding (oldest
    // entries drop off the top, newest ones enter the tail), so a
    // length-only comparison reads as "unchanged" and the view freezes.
    // Comparing the newest entry too catches that slide.
    const prevEntries = logData[d.file]?.entries;
    const changed =
      !prevEntries ||
      prevEntries.length !== d.entries.length ||
      (d.entries.length > 0 &&
        JSON.stringify(prevEntries[prevEntries.length - 1]) !== JSON.stringify(d.entries[d.entries.length - 1]));
    logData[d.file] = { entries: d.entries, prev: d.prev };
    if (!logChain.includes(d.file)) logChain.push(d.file);
    if (changed || firstLoad) renderLog(firstLoad || atBottom);
  } catch {} // the status bar already reports an unreachable server
}

// loadOlderLog prepends the file before the current head of the chain.
async function loadOlderLog() {
  const first = logChain[0];
  const prev = first && logData[first]?.prev;
  if (!prev || logLoading || logData[prev]) return;
  logLoading = true;
  try {
    const r = await fetch("/api/logs?file=" + encodeURIComponent(prev));
    const d = await r.json();
    if (r.ok && d.file) {
      logData[d.file] = { entries: d.entries, prev: d.prev };
      logChain.unshift(d.file);
      renderLog(false);
    }
  } catch {}
  logLoading = false;
}

document.getElementById("log-scroll").addEventListener("scroll", (ev) => {
  if (ev.target.scrollTop < 40) loadOlderLog();
});

// ---- poll (paused while dragging so the board doesn't rebuild mid-drag,
// and while the tab is hidden — nothing is watching it either way) ----

async function refresh() {
  if (dragging) return;
  if (document.hidden) return;
  if (view === "log") refreshLog(); // stream new entries while the log is up
  try {
    const r = await fetch("/api/board");
    const board = await r.json();
    lastBoardJobs = board.jobs || [];
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
    // This path is already quiet — it writes the status bar and toasts
    // nothing — which is what keeps an update's restart gap from turning
    // into a wall of notices as this 3s poll and the 1s update poll both
    // fail through it. All it needs is the right words: mid-update the
    // server is gone on purpose, and "unreachable" would read as a crash.
    document.getElementById("daemon-stat").textContent =
      updIsRestarting() ? "daemon — restarting" : "kraken web server unreachable";
    document.getElementById("daemon-led").className = "led";
  }
}
// returning to the tab refreshes immediately instead of waiting out the rest
// of the current 3s tick
document.addEventListener("visibilitychange", () => {
  if (document.hidden) return;
  // The update poll's gap clock is wall-clock, but the ticks that read it are
  // not: Chrome freezes hidden tabs, so hiding the board through a restart
  // gap would let the first tick back measure the whole hidden stretch and
  // synthesize "kraken serve did not come back" for a run that finished
  // while nobody was looking. Time spent hidden doesn't count — the same
  // reset the iOS poll gets by restarting its loop on scene-phase resume.
  updGapSince = 0;
  updFailStreak = 0;
  refresh();
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

// ---- self-update: the ⟳ UPDATE control (internal/api/update.go) ----
//
// The button is a view over one server record — board.update, and GET
// /api/update/status, which are the same webUpdate shape — plus two facts
// only the browser knows: whether THIS page watched a run start (so only a
// page that did reloads itself when it ends) and the last refusal, which no
// server record carries because a refused update never happened.
//
// While a run is live the button polls /api/update/status every second
// rather than riding the 3s board poll, and it polls with a BARE fetch whose
// failures are swallowed. That is the whole trick: an update's last act is
// restarting the server that answers these requests, so for a couple of
// seconds every fetch fails. That gap is the normal middle of a SUCCESSFUL
// update — post(), which reports any failure, would call it an error and be
// wrong every single run.

// jobs.UpdateTerminal: no run is in progress, so the button is live again.
const UPD_TERMINAL = new Set(["idle", "done", "failed", "uptodate"]);
// how long a finished run keeps the button. ~/.kraken/update.json holds its
// last record forever, so without a window every page load for the rest of
// time would reopen a green ✓ UPDATED — or a pink failure — from days ago.
const UPD_RECENT_MS = 60000;
// past this a restart that hasn't come back has stopped being a restart
const UPD_GAP_MAX_MS = 120000;
const UPD_POLL_MS = 1000;
const UPD_RELOAD_MS = 1500;

let updState = null; // last webUpdate seen, from either poll
let updPolling = false; // a run is live and the 1s poll is running
let updPollTimer = null;
let updGapSince = 0; // ms of this gap's first failed fetch; 0 = server answering
let updFailStreak = 0; // consecutive failed status fetches — see updPollTick
let updRefusal = ""; // last refusal text — sticky until the next click
let updWatching = false; // this page saw a run start, so it owes itself a reload
let updReloadTimer = null;
// what a click means right now, decided by renderUpdate — the button is a
// button most of the time, but a fresh failure turns it into the door to the
// log that explains it, and only the render knows which state is on screen
let updBtnAction = "update";

// updIsRestarting: what refresh()'s failed-fetch path asks so a board that
// went dark mid-update reads as "daemon — restarting" and not as a crash.
function updIsRestarting() {
  return updPolling;
}

// updIngest takes a fresh record from either poll. Reload scheduling lives
// here rather than in renderUpdate so that repainting the button is never
// itself an event.
function updIngest(u) {
  if (!u) return;
  updState = u;
  updGapSince = 0;
  if (u.phase === "done") scheduleUpdateReload();
}

// scheduleUpdateReload arms the post-update reload. MANDATORY, not cosmetic:
// web/static is embedded in the binary (web/embed.go), so the moment the new
// kraken serve is up this page is running the OLD build's SPA against the NEW
// server — its markup, its CSS and its idea of every API shape are all one
// version behind. Only a page that watched the run start reloads: a page
// opened afterwards reads the same `done` record, and reloading on it would
// loop forever.
function scheduleUpdateReload() {
  if (updReloadTimer || !updWatching) return;
  updWatching = false;
  updReloadTimer = setTimeout(async () => {
    // The reload is mandatory but the tab getting it may not have asked for
    // anything — renderUpdateFromBoard arms it on any tab that merely watched
    // a CLI-started run go by — and the spec editor is 600ms of debounce away
    // from the server at all times. location.reload() drops that debounce on
    // the floor, so land it first. Not the whole story: whatever is typed
    // during the restart gap can't be saved either (there is no server to
    // POST to), but that half is inherent and this half is not.
    await flushEditorAutosave();
    location.reload();
  }, UPD_RELOAD_MS);
}

// renderUpdateFromBoard folds the 3s board poll's copy of the record in. The
// 1s status poll wins outright while it is running: the two race, and a board
// response can easily be older than a status response that landed just before
// it — applying it would rewind the phase on screen.
function renderUpdateFromBoard(u) {
  if (!updPolling && u) {
    if (!UPD_TERMINAL.has(u.phase)) {
      // a run someone else started — the CLI, another tab. This page holds
      // the old build's SPA just the same, so it wants the reload just as
      // much as the tab that clicked.
      updWatching = true;
      updRefusal = ""; // whatever we were refused, something is running now
      startUpdatePoll();
    }
    updIngest(u);
  }
  renderUpdate();
}

function startUpdatePoll() {
  if (updPolling) return;
  updPolling = true;
  // The gap clock belongs to THIS poll run. It measures wall-clock time, but
  // the ticks that read it stop firing whenever the tab is hidden (Chrome
  // freezes background tabs after ~5min), so a gap inherited from an earlier
  // run counts minutes nobody was watching.
  updGapSince = 0;
  updFailStreak = 0;
  updPollTimer = setTimeout(updPollTick, UPD_POLL_MS);
}

function stopUpdatePoll() {
  updPolling = false;
  clearTimeout(updPollTimer);
  updPollTimer = null;
  updGapSince = 0;
  updFailStreak = 0;
}

async function updPollTick() {
  updPollTimer = null;
  let u = null;
  try {
    const r = await fetch("/api/update/status", { cache: "no-store" });
    if (r.ok) u = await r.json();
  } catch {} // expected: the restart gap, where there is no server to answer
  if (u) {
    updIngest(u);
    updFailStreak = 0;
  } else {
    updFailStreak++;
    if (!updGapSince) updGapSince = Date.now();
  }
  // The two resets (poll start, tab becoming visible) aren't enough on their
  // own, because a freeze can also land between them and the next tick. So
  // the ceiling additionally needs two failures BACK TO BACK: at 1s a real
  // two-minute outage racks up a hundred of them, while a single blip on
  // unfreeze is followed 1s later by a success that clears the gap.
  if (updFailStreak >= 2 && updGapSince && Date.now() - updGapSince > UPD_GAP_MAX_MS) {
    // Two minutes of silence is no longer a restart. Say so rather than
    // spinning a cyan button at an empty socket forever — update.log is the
    // only place that can explain a serve which never bound.
    updState = {
      ...(updState || {}),
      phase: "failed",
      error: "kraken serve did not come back after the restart — see ~/.kraken/update.log",
      updated_at: Date.now(),
    };
    stopUpdatePoll();
  } else if (u && UPD_TERMINAL.has(u.phase)) {
    stopUpdatePoll();
  }
  renderUpdate();
  if (updPolling) updPollTimer = setTimeout(updPollTick, UPD_POLL_MS);
}

// renderUpdate paints button + banner from updState. Pure repaint: the only
// state it reads that isn't in the record is the refusal and the pending
// reload, and it changes neither.
function renderUpdate() {
  const btn = document.getElementById("update-btn");
  const banner = document.getElementById("update-banner");
  if (!btn || !banner) return;

  const u = updState || {};
  const phase = u.phase || "idle";
  const running = !UPD_TERMINAL.has(phase);
  // A terminal record only speaks for the button while it's fresh — bounded
  // on BOTH sides. An `updated_at` in the future (clock skew, a corrupt
  // update.json) is otherwise fresh forever, and a `failed` record that is
  // fresh forever pins the button at UPDATE FAILED, whose click opens the
  // log instead of starting a run — so the button never updates again.
  const fresh = !!u.updated_at && Math.abs(Date.now() - u.updated_at) < UPD_RECENT_MS;
  const build = u.build_sha ? " · running " + u.build_sha : "";

  // the header's @sha chip rides the same record — written here rather than
  // in the board render so there's one reader of build_sha and it repaints
  // on the 1s status poll too, not only the 3s board poll. "dev" means the
  // binary was built without the Makefile's -X stamp; show nothing rather
  // than a sha-shaped word that isn't one.
  const buildStat = document.getElementById("build-stat");
  if (buildStat) {
    const chip = u.build_sha && u.build_sha !== "dev" ? "@" + u.build_sha : "";
    if (buildStat.textContent !== chip) buildStat.textContent = chip;
  }

  let cls = "";
  // glyph as (class, character) rather than markup: the node itself is
  // reused across repaints (see the bottom of this function), so there is
  // nothing to build a string for.
  let glyphCls = "gly";
  let glyphChar = "⟳";
  let label = "UPDATE";
  let disabled = false;
  let title = "update kraken — fast-forward main, rebuild, reinstall, restart the daemon" + build;
  let bannerCls = "";
  let bannerHTML = "";
  updBtnAction = "update";

  if (updRefusal) {
    // refused before anything was spawned (409 from the POST, already
    // toasted): nothing ran, so the record behind it is untouched and the
    // button stays clickable for the retry after you've fixed the reason.
    cls = "bad";
    glyphChar = "✕";
    label = "UPDATE BLOCKED";
    title = updRefusal;
  } else if (running && (updGapSince || phase === "restarting")) {
    // either the updater says it's restarting, or the endpoint stopped
    // answering mid-run — which IS the restart, seen from this side
    cls = "gap";
    label = "RESTARTING…";
    disabled = true;
    bannerCls = "cyan";
    bannerHTML =
      "<span>⟳ kraken serve is restarting on the new binary — board data is stale for a few seconds. Cells are unaffected.</span>";
  } else if (running) {
    const secs = u.started_at ? Math.max(0, Math.round((Date.now() - u.started_at) / 1000)) : 0;
    cls = "busy";
    label = `${phase.toUpperCase()}… ${secs}s`;
    disabled = true;
  } else if (phase === "failed" && fresh) {
    // a failure after the updater detached can't come back as an HTTP status
    // — the request returned long ago — so it arrives here, in the record
    cls = "bad";
    glyphChar = "✕";
    label = "UPDATE FAILED";
    title = u.error || "update failed";
    updBtnAction = "log";
    bannerCls = "bad";
    bannerHTML =
      `<span>✕ update failed — ${esc(u.error || "no error recorded")}</span>` +
      '<span class="spacer"></span><button data-act="log">OPEN LOG</button>';
  } else if (phase === "done" && fresh) {
    const secs =
      u.started_at && u.updated_at ? Math.max(0, Math.round((u.updated_at - u.started_at) / 1000)) : 0;
    cls = "ok";
    glyphChar = "✓";
    label = ("UPDATED " + (u.to_sha || "")).trim();
    // only while this page is on its way out — a page that merely reads a
    // recent `done` (the one that just reloaded, say) stays usable
    disabled = !!updReloadTimer;
    bannerHTML =
      `<span>✓ updated ${esc(u.from_sha || "?")} → ${esc(u.to_sha || "?")}` +
      (secs ? ` in ${secs}s` : "") +
      (updReloadTimer ? " · reloading…" : "") +
      '</span><span class="spacer"></span><button data-act="reload">RELOAD NOW</button>';
  } else if (phase === "uptodate" && fresh) {
    // a run that found nothing to do built and restarted nothing. Without
    // this the click would flash busy and land back on a bare UPDATE, which
    // reads as "did that even do anything".
    cls = "ok";
    glyphChar = "✓";
    label = "UP TO DATE";
  } else if (u.behind > 0) {
    // the daemon's background check (jobs.refreshUpdateAvail, every 10min)
    cls = "avail";
    glyphCls = "dot";
    glyphChar = "";
    label = `UPDATE · ${u.behind} BEHIND`;
    const checked = u.checked_at ? ", checked " + age(Math.floor(u.checked_at / 1000)) + " ago" : "";
    title = `origin/main is ${u.behind} commit(s) ahead${checked}${build}`;
  }

  // Written node by node, and only where something actually changed, instead
  // of the one innerHTML assignment this used to be. `.upd.busy .gly` spins on
  // a 1.1s keyframe animation while this function repaints on BOTH the 1s
  // status poll and the 3s board poll — so replacing the glyph element (which
  // is what assigning innerHTML does, even for identical markup) restarted the
  // animation from 0° about once a second and the spinner never once completed
  // a turn during a build. The label is a text node for the same reason, and
  // needs no esc(): a text node cannot be markup.
  const btnCls = "upd" + (cls ? " " + cls : "");
  if (btn.className !== btnCls) btn.className = btnCls;
  let gly = btn.firstElementChild;
  if (!gly) {
    gly = document.createElement("span");
    btn.prepend(gly);
  }
  if (gly.className !== glyphCls) gly.className = glyphCls;
  if (gly.textContent !== glyphChar) gly.textContent = glyphChar;
  let lab = btn.lastChild;
  if (!lab || lab.nodeType !== Node.TEXT_NODE) {
    lab = document.createTextNode("");
    btn.appendChild(lab);
  }
  if (lab.nodeValue !== label) lab.nodeValue = label;
  btn.disabled = disabled;
  btn.title = title;
  banner.className = "update-banner" + (bannerCls ? " " + bannerCls : "");
  banner.innerHTML = bannerHTML;
  banner.hidden = !bannerHTML;
}

// requestUpdate fires the run. No confirmation step: the click IS the
// decision (Q5). Deliberately not post() — a refusal here is one specific
// sentence about the machine's state ("… has uncommitted changes", "… is on
// carl/foo, not main"), and post()'s ✕ in the clock corner truncates exactly
// the part worth reading.
async function requestUpdate() {
  updRefusal = "";
  const btn = document.getElementById("update-btn");
  btn.disabled = true; // no double-fire while the POST is in flight
  try {
    const r = await fetch("/api/update", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    });
    const data = await r.json();
    if (!r.ok) throw new Error(data.error || String(r.status));
    // the handler has already written the claim (phase preflight) before
    // spawning, so paint it now rather than waiting out the first poll tick
    updWatching = true;
    updIngest({
      ...(updState || {}),
      phase: data.phase || "preflight",
      from_sha: data.from_sha || "",
      to_sha: "",
      error: "",
      started_at: Date.now(),
      updated_at: Date.now(),
    });
    startUpdatePoll();
  } catch (e) {
    updRefusal = e.message || "update refused";
    showToast("✕ " + updRefusal);
  }
  renderUpdate();
}

document.getElementById("update-btn").addEventListener("click", () => {
  // a fresh failure's button is the way into the log that explains it (see
  // updBtnAction); the failure ages out after UPD_RECENT_MS and the button
  // is a plain UPDATE again from there
  if (updBtnAction === "log") setView("log");
  else requestUpdate();
});

// banner actions, delegated: renderUpdate replaces the banner's innards on
// every repaint, so nothing inside it can own a listener
document.getElementById("update-banner").addEventListener("click", async (ev) => {
  const act = ev.target.closest("button")?.dataset.act;
  // same debounce courtesy the scheduled reload pays: RELOAD NOW is one
  // click away from an editor whose last keystroke is still in flight
  if (act === "reload") {
    await flushEditorAutosave();
    location.reload();
  }
  if (act === "log") setView("log");
});
