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

// jobKey is a board entry's unique client-side identity, and `slug` alone is
// NOT it: the wire's slug is unique only WITHIN a namespace. An archived job
// rides as its numbered archive name (board.go), which is numbered only
// against the archive's own contents — so archiving `foo` (no conflict there,
// archive name stays `foo`) and then creating a new active `foo` puts two
// entries with slug "foo" on the board, and buildWebBoard appends the
// archived ones last. Keying anything by bare slug therefore gives the
// ARCHIVED entry last-writer-wins, which is how a live job stopped being
// steerable: jobsBySlug[slug] returned an archived entry with live:false, so
// liveDisplayColumn read null and a click on the live card fell through to
// the read-only editor instead of the steer/unblock modal.
const jobKey = (j) => (j.archived ? "archive:" + j.slug : j.slug);

// jobLabel is the human-facing name for a board entry — the slug it was
// created under, which for an archived job whose file got renumbered is
// display_slug rather than the (numbered) slug. It prefixes every artifact
// chip's data-name, so the artifact viewer has to reproduce it exactly when
// it rebuilds a sequence from the job payload instead of from a chip.
const jobLabel = (j) => j.display_slug || j.slug;

// keyed by jobKey, NOT slug — every read below is for an active job, whose
// key is its plain slug, so callers still index it with a bare slug
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

function columnFor(state) {
  for (const c of COLUMNS) {
    if (c.states.includes(state)) return c.key;
  }
  return "draft";
}

function stateColor(state) {
  return STATE_COLOR[state] || "var(--text-2)";
}

// the eight colours a job may carry — Claude Code's own /color names, in
// /color's order, which is the order the editor's swatch strip renders and
// the order style.css declares --job-*. Absent/empty means genuinely no
// colour, not a default: an uncoloured job renders exactly as it always did.
const JOB_COLORS = ["red", "blue", "green", "yellow", "purple", "orange", "pink", "cyan"];

// jobColorVar resolves a job's colour to the CSS var holding it, or "" when
// the job has none. The whitelist check is what makes the result safe to
// interpolate into a style attribute unescaped — an unrecognised value from
// the wire drops to "" rather than reaching the stylesheet.
function jobColorVar(j) {
  const c = j && j.color;
  return JOB_COLORS.includes(c) ? `var(--job-${c})` : "";
}

// swatchStripHTML builds a colour strip's nine radios from JOB_COLORS. Both
// strips — the job editor's and the steer modal's — are generated from this
// one function rather than hand-written twice in index.html, so they cannot
// drift in palette, order or markup. `name` must differ per strip: both live
// in the DOM at once, and radios sharing a name are ONE group, so a reused
// name would make picking a colour in one strip silently unpick the other.
function swatchStripHTML(name) {
  const sw = (val, style) =>
    `<label class="f-sw${val ? "" : " none"}" title="${esc(val || "no color")}">` +
    `<input type="radio" name="${esc(name)}" value="${esc(val)}"${val ? "" : " checked"}>` +
    `<span class="f-sw-fill"${style}></span></label>`;
  // the "none" swatch first, then /color's own eight in /color's own order
  return sw("", "") + JOB_COLORS.map((c) => sw(c, ` style="--sw:var(--job-${c})"`)).join("");
}

// setSwatchStrip reflects a job's colour into one strip. "No colour" is a
// real radio (value "") rather than an absence, so an unrecognised colour
// from the wire falls back to it instead of leaving the strip with nothing
// checked and no way to click back to neutral.
function setSwatchStrip(el, v) {
  const want = JOB_COLORS.includes(v) ? v : "";
  el.querySelectorAll('input[type="radio"]').forEach((r) => (r.checked = r.value === want));
}

// initSwatchStrip fills a strip and wires its one behaviour: POST the pick to
// /api/job/color immediately. Deliberately NOT the debounced form autosave
// that every other editor control rides — saveform refuses any job whose cell
// is live (which is exactly when recolouring is useful), a full-replace save
// from an older client could wipe the colour by omitting the key, and a
// discrete one-click choice has nothing to debounce. Both events are stopped
// so an editor pick never also reaches #editor-form's delegated autosave and
// saves every other field for nothing. post() surfaces a non-200 in the tick
// line and re-renders the board, so the card's swatch catches up without
// waiting for the next poll; on failure the strip is put back to whatever the
// freshly-refreshed board says, so it can never show a colour the server
// rejected. getSlug is a thunk because the target job changes per open.
function initSwatchStrip(id, name, getSlug) {
  const el = document.getElementById(id);
  el.innerHTML = swatchStripHTML(name);
  el.addEventListener("input", (ev) => ev.stopPropagation());
  el.addEventListener("change", async (ev) => {
    ev.stopPropagation();
    const slug = getSlug();
    if (!slug) return;
    const ok = await post("/api/job/color", { slug, color: ev.target.value });
    if (!ok) setSwatchStrip(el, (jobsBySlug[slug] || {}).color);
  });
  return el;
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
// standing ask — nobody is waiting on an answer to keep working — so it
// gets no board presence at all: no count chip (docQuestionsChipHTML), no
// flag. They live one click into the job instead, like every job's
// questions now do: the editor modal for a done job (fillEditorDocq), the
// steer modal in reactivate mode, the artifact-side surfaces. Answering
// there still works exactly as before (Q13: answers are editable forever,
// live or done). Archived jobs never carry
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
// live or done. Only the modals render the panel at all now, so this row —
// like every other question row — is a drilldown affordance, one click into
// the job rather than on the card.
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
//
// This panel is a DRILLDOWN surface only: the steer modal (fillSteerContext)
// and the job editor (fillEditorDocq). The board card renders no questions
// at all — just docQuestionsChipHTML's count chip — so both callers want the
// same full list, answered rows included (Q13: answers are editable
// forever), and there is no showAnswered switch any more.
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

// docQuestionsChipHTML renders the BOARD's entire question surface: one chip
// carrying the open count, nothing else. The card used to render the whole
// answerable panel inline, which made a job with a handful of open questions
// taller than everything around it and buried the state the board exists to
// show — where each job is, and which ones need a human. A count is all the
// board has to say; the questions themselves (and every answer affordance)
// live one click into the job, in the steer modal or the job editor, which
// is where the card's own click already goes. No new click target: the chip
// is a plain span, so it falls through to the card-open fallback.
//
// Open questions only, matching docQuestionsOpenP0's rule and the panel's
// old board behavior: an answered question is a decision, not a standing
// ask, so the chip disappears entirely once the last one is answered. A
// settled job (done/archived) shows no chip at all — nobody is waiting on
// its answers — same rule settledJob already applies to the flag counter.
function docQuestionsChipHTML(j) {
  if (settledJob(j)) return "";
  const open = (j.doc_questions || []).filter((q) => !q.answer);
  if (!open.length) return "";
  const p0 = open.filter((q) => q.importance === "P0").length;
  const label = `${open.length} question${open.length === 1 ? "" : "s"}`;
  const title = `${label} open${p0 ? ` (${p0} P0)` : ""} — click the card to read and answer`;
  return `<span class="dq-chip${p0 ? " p0" : ""}" title="${esc(title)}">⚑ ${esc(label)}</span>`;
}

// ---- job feedback (`feedback` rows in kraken.db, captured from the
// artifact overlay — see openArtifact/panel-feedback below — and managed
// here) ----
//
// fbEditing tracks which feedback rows are currently open for editing, by
// row id — module-level for the same reason reansweringDocQ is: it must
// survive the wholesale innerHTML rebuild of whichever surface renders the
// panel (the steer modal's context block or the editor's copy — the board
// carries only a count chip now). Keyed
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
        <button class="fb-cancel" data-slug="${esc(j.slug)}" data-id="${esc(f.id)}">cancel</button>
        <button class="fb-save" data-slug="${esc(j.slug)}" data-id="${esc(f.id)}">save</button>
      </div>
    </div>`;
  }
  return `<div class="fb-row${f.addressed ? " fb-addressed" : ""}" data-fb-id="${esc(f.id)}">
    <div class="fb-head">
      <span class="fb-artifact">${esc(f.artifact)}</span>
      <span class="fb-actions">
        <button class="fb-edit" data-slug="${esc(j.slug)}" data-id="${esc(f.id)}">edit</button>
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
//
// This panel is a DRILLDOWN surface only, exactly like docQuestionsHTML's:
// the steer modal (fillSteerContext) and the job editor (fillEditorFeedback)
// render it, the card renders only feedbackChipHTML's count. It used to sit
// on the card itself, which made a job carrying a handful of notes taller
// than everything around it — and a note is a paragraph of prose, so it did
// that far more readily than a question ever did.
function feedbackHTML(j) {
  const list = j.feedback || [];
  if (!list.length) return "";
  const rows = list.map((f) => feedbackRowHTML(j, f)).join("");
  return `<div class="job-feedback">${rows}</div>`;
}

// feedbackChipHTML renders the BOARD's entire feedback surface: one chip
// carrying the totals, nothing else — the same subtraction docQuestionsChipHTML
// already made for questions, and for the same reason. A plain span, not a
// .chip (that class opens an artifact) and not a button, so a click falls
// through to the card-open fallback that reaches the panel itself.
//
// Both counts ride it: the total is what the job accumulated, the open count
// (notes zeus hasn't posted an `addressed` line for) is what is still owed.
// Unlike a question, a note is never "settled" by the job being done — carl
// leaves most of them ON done work — so there is no settledJob() suppression
// here.
function feedbackChipHTML(j) {
  const list = j.feedback || [];
  if (!list.length) return "";
  const open = list.filter((f) => !f.addressed).length;
  const label = open ? `${list.length} feedback · ${open} open` : `${list.length} feedback`;
  const title = `${list.length} feedback note${list.length === 1 ? "" : "s"}${open ? `, ${open} not yet addressed` : ", all addressed"} — click the card to read them`;
  return `<span class="fb-chip${open ? " open" : ""}" title="${esc(title)}">✎ ${esc(label)}</span>`;
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

// ---- per-job token spend (webJob.tokens) ----
//
// fmtTokens renders one raw counter at a glance: 15756009 -> "15.8M".
// Precision shrinks as the magnitude grows (2dp under 10, 1dp under 100,
// none above) so every value lands in four characters plus a unit. Four
// full-width integers side by side is the shape the wire carries and it
// reads as noise in a drilldown; nothing here is exact enough to earn the
// digits either (see tokensHTML on the floor caveat).
function fmtTokens(n) {
  const v = Math.max(0, Number(n) || 0);
  const units = [[1e9, "B"], [1e6, "M"], [1e3, "K"]];
  for (let i = 0; i < units.length; i++) {
    const [div, suffix] = units[i];
    if (v < div) continue;
    const scaled = v / div;
    let s = scaled.toFixed(scaled < 10 ? 2 : scaled < 100 ? 1 : 0);
    // rounding can carry a value into the next unit up — 999_999 formats as
    // "1000K", which is both wrong-looking and wider than the unit exists to
    // keep it. Promote instead.
    if (parseFloat(s) >= 1000 && i > 0) return "1" + units[i - 1][1];
    // trim only INSIDE a decimal part — a blanket /0+$/ would turn "100"
    // into "1"
    if (s.includes(".")) s = s.replace(/0+$/, "").replace(/\.$/, "");
    return s + suffix;
  }
  return String(Math.round(v));
}

// tokensHTML renders what this job actually burned: the four raw counters
// the daemon sums out of the job's own Claude Code transcripts
// (internal/jobs/tokens.go's tokensPass writes them, the board serves them
// as webJob.tokens).
//
// Two states, and they are NOT the same thing:
//   - `tokens` ABSENT — never measured (no transcripts yet, or the daemon
//     hasn't reached this job). Renders nothing at all. A zero here would
//     read as "this job was free", which is a different and false claim.
//   - measured — render it. The api emits the key only when a store row
//     exists, so presence is the whole test.
//
// Layout is one headline over three quieter rows, all of them in the same
// dim ink: the sum answers "how big was this job", the per-counter row is
// there for whoever wants it, the origin row says who burned it, and the
// provenance line dates it. Nothing here is hot or interactive — it is a
// fact about the past — so the whole block sits in --text-2 and separates
// its tiers by size and opacity rather than by brightness, and the box
// itself is the same raised/border-left panel .doc-questions and
// .job-feedback use two containers up.
//
// The headline carries the SUM alone. It used to repeat `out` beside it,
// which put the same number twice in a block whose next row already lists
// all four counters — a duplicate reads as two different facts at a glance.
//
// The origin row is the one thing here that is not on the wire directly:
// the daemon stores zeus' four counters as a SUBSET of the four totals
// (webTokens.zeus_*), so the delegated half is the REMAINDER and is
// computed here. Storing both halves would let them drift out of agreement
// with the total; subtracting cannot. "zeus" is the cell's lead agent and
// its side threads; "subagents" is every teammate and in-process subagent
// it delegated to. A server that predates those keys sends none of them,
// which drops the row rather than rendering a 100%-subagent lie.
//
// The counters stay RAW, with no blended cost — cache reads bill far below
// input, and weighting them here would bake today's price ratio into the UI
// (the same reason the wire carries no total).
//
// Wording avoids claiming exactness on purpose: a transcript caught
// mid-write is resumed on the next pass, so a freshly-scanned job's numbers
// are a floor rather than a settled total.
function tokensHTML(j) {
  const t = j && j.tokens;
  if (!t) return "";
  const inTok = t.in || 0, out = t.out || 0;
  const cacheWrite = t.cache_write || 0, cacheRead = t.cache_read || 0;
  const sessions = t.sessions || 0;
  const total = inTok + out + cacheWrite + cacheRead;
  if (!total && !sessions) return "";
  const rows = [["in", inTok], ["out", out], ["cache w", cacheWrite], ["cache r", cacheRead]];
  // Presence of the zeus keys is the whole test, and it is checked on ONE
  // key rather than on their sum: the four ride together or not at all, and
  // a job whose lead genuinely burned nothing (every token spent by a
  // teammate) is a real state that a sum-based test would hide.
  const hasOrigin = t.zeus_in !== undefined;
  const zeus = hasOrigin
    ? (t.zeus_in || 0) + (t.zeus_out || 0) + (t.zeus_cache_write || 0) + (t.zeus_cache_read || 0)
    : 0;
  // clamped for the same reason every derived number here is: zeus is a
  // subset so this cannot go negative, but a stale row paired with a fresh
  // total during a re-scan is exactly the window where it could look like it
  const origin = hasOrigin
    ? [["zeus", Math.min(zeus, total)], ["subagents", Math.max(0, total - zeus)]]
    : null;
  // updated_at is unix MILLIS (webJob.mtime's convention); age() takes
  // seconds. A missing/zero stamp drops the clause rather than rendering
  // "0s ago" — same treatment the update banner gives its own timestamp.
  const when = t.updated_at ? age(Math.floor(t.updated_at / 1000)) : "";
  const note = [
    sessions ? `measured across ${sessions} transcript${sessions === 1 ? "" : "s"}` : "measured",
    when ? `${when} ago` : "",
  ].filter(Boolean).join(" · ");
  const title =
    "token spend summed from this job's own Claude Code transcripts, subagent sidecars included. " +
    "zeus is the cell's lead agent and its side threads; subagents is every teammate and in-process subagent it delegated to. " +
    "Counters are raw and unweighted; a transcript still being written is resumed next pass, so treat these as a floor.";
  const cells = (list) =>
    list.map(([k, v]) => `<span class="tok-cell"><i>${esc(k)}</i>${esc(fmtTokens(v))}</span>`).join("");
  return (
    `<div class="tok" title="${esc(title)}">` +
    `<div class="tok-head">` +
    `<span class="tok-big">${esc(fmtTokens(total))}</span><span class="tok-unit">tokens</span>` +
    `</div>` +
    `<div class="tok-break">${cells(rows)}</div>` +
    (origin ? `<div class="tok-break tok-origin">${cells(origin)}</div>` : "") +
    `<div class="tok-note">${esc(note)}</div>` +
    `</div>`
  );
}

// ---- side threads: one chip per fork of zeus' session (see openThread) ----
//
// threadChipsHTML renders the chips alone so the same markup can fill both
// the card's unified chip row and the steer modal's static #steer-threads
// container — exactly the split prChipHTML/.prs already uses. Live threads
// take the cyan --polling tone (a second conversation is actually running);
// closed ones dim and stay as history. Renders nothing at all for a job with
// no threads on a dead cell (and for an older server payload with no threads
// array).
//
// withBranch appends the dashed ⑂ branch affordance (live cells only). It
// defaults ON, which is the MODAL's behaviour — the modal passes no argument
// at all. The CARD is the one that opts out, with an explicit false: a branch
// chip is a STATUS ("this branch exists, and it is live or closed"), which is
// what the card's chip row is for, while starting a new branch is a detail
// action and belongs behind the click-in with the rest of them.
function threadChipsHTML(j, withBranch = true) {
  const list = j.threads || [];
  const chips = list
    .map((t) => {
      const closed = t.state !== "live";
      const label = `⑂ ${t.tid}${t.title ? " · " + t.title : ""}`;
      // the title repeats the thread's name, which the label also carries: the
      // label is ellipsised on a card (style.css .thread-chip) because a name
      // is renamable to any length, and the tooltip is where the tail it cuts
      // stays readable — same job title does for every count chip in the row.
      const title =
        (t.title ? `${t.tid} · ${t.title}\n` : "") +
        (closed ? `${t.tid} — closed thread; open it to revive` : `${t.tid} — live thread`);
      return `<button class="thread-chip${closed ? " closed" : ""}" data-slug="${esc(j.slug)}" data-tid="${esc(t.tid)}" title="${esc(title)}"><span class="dot"></span>${esc(label)}</button>`;
    })
    .join("");
  const branch = withBranch && j.live ? branchBtnHTML(j) : "";
  return chips + branch;
}

function branchBtnHTML(j) {
  return `<button class="branch-btn" data-slug="${esc(j.slug)}" title="branch a side thread off zeus' session — zeus is not interrupted">⑂ branch</button>`;
}

// artifactListHTML renders every one of a job's artifacts as an openable
// chip. label prefixes each chip's data-name (it differs from slug for a
// renamed archived job); key is the job's jobKey, carried onto every chip as
// data-key so the artifact viewer can rebuild the job's ordered list when one
// is clicked — a chip knows its own url, but next/prev needs the SEQUENCE it
// sits in, and the poll's re-sync needs to know which job to re-derive from.
// jobKey rather than a bare slug, or an archived job and an active one that
// reused its freed slug would resolve to each other's artifacts.
//
// This is now a DRILLDOWN-only renderer — the steer modal and the job editor
// call it, the card does not (see artifactChipHTML). It used to be the card's
// own row, capped at 3 chips behind a "+N more" toggle whose expanded state
// was persisted per job in localStorage. All of that is gone: a modal scrolls,
// so it can just show the list, and the card shows a count. The cap and its
// toggle only ever existed to stop a twelve-artifact job from owning its
// column, which a count solves outright rather than bounds.
function artifactListHTML(artifacts, key, label) {
  return (artifacts || [])
    .map(
      (a) =>
        `<button class="chip" data-url="${esc(a.url)}" data-key="${esc(key)}" data-name="${esc(label)}/${esc(a.name)}">▤ ${esc(a.name)}</button>`,
    )
    .join("");
}

// artifactChipHTML renders the BOARD's entire artifact surface: one count,
// no names — the same subtraction docQuestionsChipHTML and feedbackChipHTML
// already made for questions and notes, now applied to the third and largest
// list on the card. A plain span, not a .chip (that class is claimed by the
// delegated handler that opens an artifact) and not a button, so a click falls
// through to the card-open fallback that reaches the list itself.
function artifactChipHTML(j) {
  const n = (j.artifacts || []).length;
  if (!n) return "";
  const title = `${n} artifact${n === 1 ? "" : "s"} — click the card to open them`;
  return `<span class="ct-chip" title="${esc(title)}">▤ ${n}</span>`;
}

// zeusPillHTML renders the zeus status pill for crewChipsHTML: one god icon
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

// most teammate names a crew chip's tooltip lists before it summarises the
// remainder — see the cap's reasoning in crewChipsHTML
const CREW_TITLE_MAX = 12;

// crewChipsHTML renders the crew tokens: a zeus status pill plus one working/
// total chip per subagent role present in the roster, each chip led by its
// god's icon in place of the role name (icon replaces the name; the name moves
// to title). Renders nothing for an old server payload (no zeus_status, no
// roster) so the card looks exactly as it did before these fields existed.
//
// Bare chips with no wrapper, so the same markup fills both the card's unified
// chip row and the modal's static #steer-crew — which carries .crew itself,
// exactly as #steer-threads carries .threads for threadChipsHTML. There is no
// crewHTML wrapper any more: it existed only to inject a <div class="crew">
// into a container classed .steer-crew that nothing styled.
//
// The chip's title also names every agent counted in it. That used to be a
// second block on the card — a .roster row per teammate restating what the
// chips had just counted, the card's only outright duplication. The chips are
// the version that survives: they fit on one line whatever the crew size, and
// nowhere else ever showed the names, so a hover keeps them reachable.
function crewChipsHTML(j) {
  const roster = j.roster || [];
  if (!j.zeus_status && !roster.length) return "";
  const zeusPill = j.zeus_status ? zeusPillHTML(j.zeus_status) : "";
  const counts = {};
  for (const r of roster) {
    const role = CREW_ROLES.includes(r.role) ? r.role : "other";
    const c = (counts[role] ||= { working: 0, total: 0, names: [] });
    c.total++;
    if (r.working) c.working++;
    // `?? ""` per field, not on the joined blob: every other field the card
    // renders degrades a missing value to blank through esc(), and composing
    // first would make this the one place that prints the literal "undefined".
    // The wire says both are always present (board.go carries no omitempty on
    // them) — this is what that contract slipping looks like if it ever does.
    c.names.push(`${r.working ? "●" : "○"} ${r.name ?? ""} (${r.model ?? ""})`);
  }
  const chips = CREW_ROLES.filter((role) => counts[role])
    .map((role) => {
      const c = counts[role];
      const icon = CREW_ICONS[role];
      const label = icon
        ? `<span class="crew-ico">${icon}</span> ${c.working}/${c.total}`
        : `${esc(role)} ${c.working}/${c.total}`;
      // capped: a native tooltip does not scroll, so a large hephaestus fleet
      // would render a name list taller than the window with no way to see its
      // end. The on-card .roster block this replaced was unbounded too, but it
      // at least wrapped inside the column.
      const shown = c.names.slice(0, CREW_TITLE_MAX);
      const rest = c.names.length - shown.length;
      const body = rest > 0 ? [...shown, `…and ${rest} more`] : shown;
      const title = `${role} — ${c.working} of ${c.total} working\n${body.join("\n")}`;
      return `<span class="crew-chip${c.working > 0 ? " active" : ""}" title="${esc(title)}">${label}</span>`;
    })
    .join("");
  if (!zeusPill && !chips) return "";
  return zeusPill + chips;
}

// cardHTML lays a job out in six bands, in this order and always this order,
// whatever column the card is in. Bands 1-5 answer, top to bottom, the five
// questions you ask a card in the order you ask them; band 6 gives you the
// controls. These numbers are shared vocabulary — style.css and the iOS
// JobCardView both reference bands by them, so renumbering here means
// renumbering there. Before this they were interleaved: the countdown chip and the PR/
// thread rows sat BETWEEN the slug and the repo/age line, so identity was
// split in two, and zeus' own words landed below a full-width button, that
// same countdown chip and two chip rows.
//
//   1. IDENTITY — slug · state, then repo · cell · age · prereqs. Contiguous:
//      nothing may be inserted between these two lines.
//   2. CHIPS — one wrapped row of tokens directly under the repo line, and the
//      card's inventory surface: agent/subagent status, branches, then a bare
//      count for each of questions, artifacts and feedback. It sits with
//      identity rather than down by the actions because it answers "what is in
//      this job", which is read at the same glance as "which job is this".
//      Nothing here names an individual thing except a branch, which has
//      nothing longer to say. Everything the counts stand for is one click away
//      in the modal — that is the rule this row exists to enforce, and it is
//      why the artifact NAMES moved out (see artifactChipHTML) rather than
//      being capped.
//   3. STATUS — what is true right now, worst-first: outcome, err/health, then
//      the me→zeus/zeus conversation pair (chronological, so it reads as a
//      transcript), then the poll countdown WITH its stated reason (those two
//      were 9 rows apart).
//   4. NEEDS YOU — the pending AskUserQuestion. Above the PR row, not below
//      it: it is the one block on a card carrying buttons you must press.
//   5. PRS — their own row, deliberately NOT folded into band 2. A PR is the
//      job's OUTPUT, not part of its inventory: it is the last thing that
//      happened rather than something the job contains, it is the one chip that
//      leaves the app entirely, and it is the only one that names a specific
//      external thing. So it sits low, next to the actions, where the "what
//      came of this" reading is.
//   6. ACTIONS — tmux, big, at the foot, plus retry and reactivate when the
//      job's state offers them. ⑂ branch is deliberately NOT here: branching
//      is a detail action and lives in the modal, which has always had its own
//      button for it.
function cardHTML(j, col) {
  const color = stateColor(j.state);
  const prereqs = (j.prereqs || [])
    .map((p) => {
      const cls = p.done ? "ok" : p.gated ? "gate" : "pend";
      const mark = p.done ? "✓" : p.gated ? "⚑" : "✗";
      return `<span class="${cls}">${esc(p.job)}${mark}</span>`;
    })
    .join(" ");
  // band 2 — assembled most-live-first: who is working on it, what has been
  // branched off it, then what it has accumulated.
  const chips = [
    crewChipsHTML(j),
    threadChipsHTML(j, false),
    docQuestionsChipHTML(j),
    artifactChipHTML(j),
    feedbackChipHTML(j),
  ].join("");
  // band 5 — PRs alone; see the band note above for why they are not band 2
  const prs = (j.prs || []).map(prChipHTML).join("");
  // band 6, assembled here rather than inline so the row can be dropped
  // whole when a card offers nothing to press (every draft/queued card)
  const actions = [
    j.live ? `<button class="btn tmux-open open-tmux" data-slug="${esc(j.slug)}" title="raise zeus' pane in Ghostty">⧉ tmux</button>` : "",
    j.health && !/is gone|startup prompt/.test(j.health) ? `<button class="btn retry" data-slug="${esc(j.slug)}">↻ retry</button>` : "",
    (col === "done" || j.cell_gone) && j.reactivatable ? `<button class="btn reactivate" data-slug="${esc(j.slug)}">↺ reactivate</button>` : "",
  ].join("");
  const editable = !j.live && ["draft", "queued", "done"].includes(col);
  // an uncoloured job emits no --job-color declaration at all and its accent
  // bar falls through to grey. --state is still emitted: it no longer feeds
  // the bar, but the `● working` chip reads it.
  const jc = jobColorVar(j);
  return `<div class="card editable" style="--state:${color}${jc ? `;--job-color:${jc}` : ""}"
      draggable="true" data-slug="${esc(j.slug)}" data-col="${col}" data-editable="${editable}">
    <div class="card-top">
      <span class="slug">${esc(j.slug)}</span>
      <span class="state">● ${esc(j.state)}</span>
      ${col === "queued" && !j.live ? `<button class="queue-cancel" data-slug="${esc(j.slug)}" title="cancel — return to draft (spec is kept)">✕</button>` : ""}
    </div>
    <div class="meta">
      <span>${esc(j.repo)}</span>
      ${j.live ? `<span>${esc(j.cell)}</span>` : ""}
      <span data-age="${j.mtime}">${age(j.mtime)}</span>
      ${prereqs ? `<span class="prereq">${prereqs}</span>` : ""}
    </div>
    ${chips ? `<div class="chips">${chips}</div>` : ""}
    ${j.outcome && col === "done" ? `<div class="outcome">→ ${esc(j.outcome)}</div>` : ""}
    ${j.err ? `<div class="err">✕ ${esc(j.err)}</div>` : ""}
    ${j.health ? `<div class="err">⚠ ${esc(j.health)}</div>` : ""}
    ${j.last_sent ? `<div class="last-sent">me→zeus: ${esc(j.last_sent)}</div>` : ""}
    ${j.detail ? `<div class="detail">${esc(j.detail)}</div>` : ""}
    ${wakeupChipHTML(j)}
    ${j.wakeup && j.wakeup.reason && j.state === "working" ? `<div class="wakeup-reason">↻ ${esc(j.wakeup.reason)}</div>` : ""}
    ${j.question ? questionHTML(j) : ""}
    ${prs ? `<div class="prs">${prs}</div>` : ""}
    ${actions ? `<div class="card-actions">${actions}</div>` : ""}
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
  // the thread overlay is scoped to a job but NOT to its state (a thread
  // outlives working→blocked→done, and a closed one outlives the cell), so
  // unlike the two above it only force-closes when the job itself leaves the
  // board entirely — archived, deleted, renamed out from under it. While it
  // stays, the freshest board payload is what the head reads its title/live
  // state from.
  if (threadOverlay && !threadOverlay.hidden && threadSlug) {
    if (!jobsBySlug[threadSlug]) closeThread();
    else syncThreadFromBoard();
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
    jobsBySlug[jobKey(j)] = j;
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
  // …and its sibling, the ⇧ SHIP iOS control (board.ship_ios, same additive
  // treatment — see the ship-ios section below the self-update one)
  renderShipFromBoard(board.ship_ios);

  return { byCol, archived };
}

// snapshotDocInputs/restoreDocInputs preserve in-progress free-text inputs
// (value + focus + selection) across an innerHTML rebuild of any surface
// that renders one: #steer-docq/#steer-feedback (fillSteerContext re-rendering
// the steer modal's copies of the questions and feedback panels on
// open/post-answer), #editor-docq/#editor-feedback (fillEditorDocq
// and fillEditorFeedback doing the same for the job editor), and
// #dream-watchlist (renderDreamWatchlist rebuilding the watchlist on every
// poll while the DREAM view is up — the comment box there is a composer carl
// types whole sentences into, and it is the one input on a surface that
// rebuilds on a timer rather than only on an action). #board renders none of
// these any more: both panels left the card for the drilldowns, leaving only
// their count chips behind. All are wholesale replaces with no diffing, so
// anything typed into one is gone the instant it rebuilds unless snapshotted
// first — every other composer lives in a modal textarea, whose draft is
// server-synced instead (see flushSteerDraft). Keyed on slug+id (both already
// ride as data attrs on every covered element) rather than DOM position,
// since sortDocQuestions can reorder rows between polls. A .dq-input id
// ("Q<digits>"), an .fb-edit-input id (a bare feedback row number) and a
// .wl-comment id (a watchlist rowid, under the reserved pseudo-slug
// "watchlist" no real job can take) never collide as strings, so all three
// element kinds safely share one values map.
//
// A modal copy is only ever a snapshot AUTHORITY while that modal is
// actually visible: snapshotDocInputs skips anything inside a hidden
// #steer-overlay/#editor-overlay, so a closed-but-not-yet-cleared modal
// can't outrank a visible surface in document order.
// clearSteerDocq()/clearEditorDocq() (below) additionally empty the panel on
// every close path, so in practice a hidden modal never holds a .dq-input at
// all — the closest() check here is defense in depth for the instant between
// the two. Values are recorded even when empty, so a deliberately cleared
// draft actually propagates as "cleared" instead of being overwritten by a
// stale non-empty copy.
const DQ_INPUT_SELECTOR =
  "#steer-docq .dq-input, #steer-feedback .fb-edit-input, " +
  "#editor-docq .dq-input, #editor-feedback .fb-edit-input, " +
  "#dream-watchlist .wl-comment";

// the modal surfaces DQ_INPUT_SELECTOR reaches into — snapshot/restore skip
// an input inside whichever of these is currently hidden, so a
// closed-but-not-yet-cleared modal can never outrank a visible surface in
// document order. (#dream-watchlist isn't listed: it's a view, not a modal,
// and applyView hides the whole <main> — an input inside a hidden view is
// unreachable either way, and its value is worth carrying across a tab
// switch rather than dropping.)
const HIDDEN_DOCQ_SURFACE = "#steer-overlay, #editor-overlay";

const dqDraftKey = (slug, id) => JSON.stringify([slug, id]);

function docInputKey(el) {
  return dqDraftKey(el.dataset.slug, el.dataset.id);
}

// dqDrafts holds unsent doc-question answers across a modal close/reopen,
// keyed the same slug+id string docInputKey builds. The questions panel is
// modal-only now, so a closing modal has nowhere on the board to hand its
// draft to — and the closes that matter most aren't the user's: checkStaleModals
// force-closes the modal the moment the job changes state, which is exactly
// when someone is mid-answer. Module-level, like reansweringDocQ, so it
// survives every rebuild; an entry is dropped when its answer posts (see
// sendDocQuestionAnswer) or when the modal closes with that input empty.
const dqDrafts = new Map();

// clearModalDocq empties a modal's rendered doc-questions panel (#steer-docq
// or #editor-docq). Called from every path that hides that overlay so a
// closed modal can never hold a zombie .dq-input that would otherwise
// outrank a visible surface in snapshotDocInputs/restoreDocInputs. Each
// modal repopulates its panel on the next open (fillSteerContext /
// fillEditorDocq), seeding whatever this stashed.
//
// Empty inputs DELETE their draft rather than being skipped: a deliberately
// cleared answer must not come back on the next open from a stale stash.
function clearModalDocq(elID) {
  const docqEl = document.getElementById(elID);
  if (!docqEl) return;
  docqEl.querySelectorAll(".dq-input").forEach((el) => {
    const key = docInputKey(el);
    if (el.value) dqDrafts.set(key, el.value);
    else dqDrafts.delete(key);
  });
  docqEl.innerHTML = "";
}

// seedDocqDrafts refills a freshly-rendered modal panel from dqDrafts. Runs
// after the innerHTML replace and before restoreDocInputs, so anything the
// live snapshot carries (a rebuild while the modal is open and typed into)
// still wins over the older stash.
function seedDocqDrafts(el) {
  if (!el || !dqDrafts.size) return;
  el.querySelectorAll(".dq-input").forEach((input) => {
    const draft = dqDrafts.get(docInputKey(input));
    if (draft && !input.value) input.value = draft;
  });
}

// clearModalFeedback is clearModalDocq's counterpart for the feedback panel.
// No draft stash on this side: an unsaved edit is discarded when the modal
// closes, and the row re-opens (fbEditing outlives the close) pre-filled from
// the server's copy of the note. That's deliberate — a feedback note is
// edited in place against text that already exists, so re-seeding half-typed
// text over the authoritative version would be the surprising behavior. The
// innerHTML wipe itself is what matters: it stops a closed modal from holding
// an .fb-edit-input that snapshotDocInputs would otherwise have to reason
// about.
function clearModalFeedback(elID) {
  const el = document.getElementById(elID);
  if (el) el.innerHTML = "";
}

const clearSteerDocq = () => { clearModalDocq("steer-docq"); clearModalFeedback("steer-feedback"); };
const clearEditorDocq = () => { clearModalDocq("editor-docq"); clearModalFeedback("editor-feedback"); };

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
  return { values, focused };
}

function restoreDocInputs({ values, focused }) {
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
}

// renderBoardDOM is the expensive part of render(): only called when
// board.jobs actually changed (see refresh()).
//
// No snapshot/restore pair around the rebuild any more: the card renders
// count chips only — every free-text input this board ever carried
// (.dq-input, then .fb-edit-input) moved to a modal, and the modals are not
// what this replaces. See DQ_INPUT_SELECTOR for where they went.
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
  const label = jobLabel(j);
  const renamed = j.display_slug && j.display_slug !== j.slug;
  // an archived card carries the same two counts an active one does, in the
  // same place (band 2, directly under the repo line), and for the same
  // reason: both open in the job editor, which is what a click on an archived
  // card already opens, and which now renders the artifact list itself
  // (fillEditorChips). Nothing about an archived job is worth naming on the
  // card that isn't worth naming on a live one. It has no PR row — an archived
  // job's PRs are not on the wire — so band 5 simply doesn't arise here.
  //
  // data-slug is the jobKey here, not the bare archive name: nothing reads an
  // archived card's data-slug as a server key (unarchive/delete carry their
  // own, below), but snapshotDocInputs/restoreDocInputs use it as a per-card
  // scroll key — and a bare slug would make this card and an active job that
  // reused the freed slug restore each other's feedback-panel scroll position.
  const chips = artifactChipHTML(j) + feedbackChipHTML(j);

  // the colour survives archiving — the card is dimmed as a whole by
  // .card.archived's opacity, so the accent bar reads as muted without
  // needing a treatment of its own. Same all-or-nothing emission as the live
  // card: no --job-color means the bar falls through to --text-2.
  const jc = jobColorVar(j);
  // data-editable="false" makes the card CLICKABLE — the delegated fallback
  // matches `.card[data-editable]` on presence, and reads the value only to
  // decide whether the editor renders read-only. An archived card carried no
  // such attribute and so had never been clickable, which was harmless only
  // while it rendered real .chip buttons the artifact handler could open
  // directly. It carries a bare ▤ count now, whose entire affordance is
  // "click the card", so without this the archive's artifacts would have no
  // path in the UI at all.
  return `<div class="card archived" style="--state:var(--text-2)${jc ? `;--job-color:${jc}` : ""}" data-slug="${esc(jobKey(j))}" data-editable="false">
    <div class="card-top">
      <span class="slug">${esc(label)}</span>
      ${renamed ? `<span class="state">as ${esc(j.slug)}</span>` : ""}
      <span class="state">● archived</span>
    </div>
    <div class="meta">
      <span>${esc(j.repo)}</span>
      <span data-age="${j.mtime}">${age(j.mtime)}</span>
    </div>
    ${chips ? `<div class="chips">${chips}</div>` : ""}
    ${j.outcome ? `<div class="outcome">→ ${esc(j.outcome)}</div>` : ""}
    <div class="card-actions">
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
  // drag-selecting text inside an in-card panel's input/button row must not
  // also start a card drag — some browsers treat a press-and-drag gesture
  // starting on/over a form control inside a draggable ancestor as dragging
  // the ancestor rather than selecting text, which would turn "select my
  // typed note to fix a word" into "post a real column move". BOTH panels are
  // modal-only now (never inside a draggable card), but they keep their
  // guards: a modal drag has nothing to do either way, and the rule is
  // "panels with text inputs never start a drag", not "cards only".
  if (ev.target.closest(".doc-questions") || ev.target.closest(".job-feedback")) {
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
  // `slugs` is a column's DOM order — active jobs only — so archived entries
  // are filtered out before keying: one that shares a reused slug would
  // otherwise win last-writer-wins and get spliced into the column in the
  // active job's place until the next poll corrected it
  const bySlug = Object.fromEntries(lastBoardJobs.filter((j) => !j.archived).map((j) => [j.slug, j]));
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
// the artifact SEQUENCE: the ordered list the open artifact came from and the
// position in it, so ‹ › and the arrow keys can walk a job's artifact set
// without closing the viewer. Derived at open time and re-derived on every
// board poll, never persisted. Invariant, whenever the overlay is open:
// artifactSeq[artifactIdx].url === artifactURL — openArtifact is the only
// writer and enforces it by falling back to a singleton.
let artifactSeq = [];
let artifactIdx = 0;
// the jobKey the sequence was derived from, kept only so the poll can
// re-derive from the same job. Written by the chip click handler (the one
// caller that has a chip to read data-key off), never by openArtifact — a
// step within a sequence must not change which job it came from.
let artifactSeqKey = "";
// artifactGen counts overlay opens, and it is the ONLY safe way for an async
// tail in here to ask "is what I fetched still what's on screen?". Both of
// the viewer's fetches (the markdown body, the raw toggle) bind their artifact
// synchronously and then resolve into whatever world exists later; a step is
// synchronous and can happen in between, so an unguarded resolution paints
// artifact A's text under artifact B's title, with B's feedback target and
// A's rawText behind `raw`. Comparing the captured URL against artifactURL is
// not enough on its own: paging away and BACK lands on the same url with the
// body already repainted and showRaw/rawText already reset, so the stale
// write would pass a url check and still desync the toggle from the view.
// A generation counter catches that too. Bumped by openArtifact only.
let artifactGen = 0;

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

// feedbackDrafts holds an unsent feedback note per artifact url, so a step
// through the pager can't eat one. openArtifact clears the composer on every
// open, and ‹ › now sit a few pixels from the text you're writing about — so
// without this, one click on › silently destroys a half-typed note with no
// undo. Refusing to step instead would make the pager look broken; carrying
// the draft with its artifact is what the rest of this file does
// (flushSteerDraft, flushEditorAutosave, snapshotDocInputs/restoreDocInputs
// all exist so a rebuild or a reopen can't eat in-progress text).
//
// A Map, deliberately, not localStorage: surviving the tab is a bigger
// promise than a scratch composer should make, and these are per-session by
// nature. Every way of LEAVING an artifact banks the note — a step, Escape,
// a backdrop click — and only the two gestures that mean "I'm finished with
// it" drop the entry: send and an explicit cancel. That has to be all three
// leave paths or none: "leaving banks the note, except by Escape" is
// arbitrary from the outside, and it is the exception the rest of this file
// refuses to make (flushSteerDraft and flushEditorAutosave are wired into
// every close path of their overlays, and clearModalDocq says so out loud).
const feedbackDrafts = new Map();

// stashFeedbackDraft banks the composer's text against the artifact it was
// typed about — called BEFORE openArtifact repoints the overlay, which is
// why it reads artifactURL rather than taking one. An emptied composer drops
// its stash rather than keeping a stale one.
function stashFeedbackDraft() {
  if (!artifactURL) return;
  const text = document.getElementById("feedback-text").value;
  if (!document.getElementById("feedback-form").hidden && text.trim()) {
    feedbackDrafts.set(artifactURL, text);
  } else {
    feedbackDrafts.delete(artifactURL);
  }
}

// restoreFeedbackDraft re-opens the composer on an artifact you left a note
// half-written on. Runs after hideFeedbackForm has reset the form, so it is
// the last word on both the text and the form's visibility.
function restoreFeedbackDraft(url) {
  const draft = feedbackDrafts.get(url);
  if (!draft) return;
  document.getElementById("feedback-text").value = draft;
  document.getElementById("feedback-form").hidden = false;
}

// closeArtifact is the ONE way the viewer goes away — both close gestures
// (Escape, backdrop click) route through it so neither can forget to bank the
// draft. Same reason stepArtifact calls the stash: the composer sits pixels
// from the controls that dismiss it, and Escape destroys a half-typed note
// exactly as thoroughly as › would.
function closeArtifact() {
  stashFeedbackDraft();
  overlay.hidden = true;
}

// artifactSeqFor builds the {url, name} sequence for a jobKey out of the job's
// artifact list as the board currently has it — the job payload, not the DOM
// the click came from, which is what lets the 3s poll re-derive the sequence
// under an open viewer and reach an artifact a live cell wrote after it was
// opened. Names are labelled exactly as artifactListHTML labels its chips
// (both go through jobLabel), so the panel title reads identically whether you
// arrived by clicking a chip or by paging onto it. Returns null for a key that
// resolves to nothing: no data-key at all, or a job that left the board
// between the render and the click.
function artifactSeqFor(key) {
  const j = key ? jobsBySlug[key] : null;
  const list = (j && j.artifacts) || [];
  if (!list.length) return null;
  const label = jobLabel(j);
  return list.map((a) => ({ url: a.url, name: `${label}/${a.name}` }));
}

// renderPager paints the ‹ n / N › group: hidden outright for a sequence of
// one (an unresolvable chip, or a job with a single artifact — behaviour is
// then exactly what it was before the pager existed), and each end button
// disabled at its end, because the step CLAMPS rather than wrapping.
function renderPager() {
  const n = artifactSeq.length;
  document.getElementById("panel-pager").hidden = n < 2;
  // compare before writing. resyncArtifactSeq calls this on every 3s board
  // poll, and #panel-pos is an aria-live region: assigning textContent
  // replaces the text node, which is an announceable mutation even when the
  // string is identical — so an unconditional write reads "3 / 7" aloud every
  // three seconds for as long as the artifact is open, which is exactly the
  // chattiness that gets a live region switched off. Guarded, it announces
  // the two things worth hearing: a navigation, and N growing under an open
  // viewer because a live cell wrote an artifact. Same shape as the poll's
  // other small-text writes (see the build/usage chips).
  const pos = `${artifactIdx + 1} / ${n}`;
  const posEl = document.getElementById("panel-pos");
  if (posEl.textContent !== pos) posEl.textContent = pos;
  document.getElementById("panel-prev").disabled = artifactIdx <= 0;
  document.getElementById("panel-next").disabled = artifactIdx >= n - 1;
}

// stepArtifact is the one navigation command — ±1 through the sequence,
// clamped to its ends: a step past either end is a no-op, never a wrap.
//
// It re-opens through openArtifact rather than swapping the body in place,
// deliberately: openArtifact is what resets showRaw, the cached rawText and
// the open feedback form, and all three are per-artifact state that must not
// survive a step.
function stepArtifact(delta) {
  const i = artifactIdx + delta;
  if (i < 0 || i >= artifactSeq.length) return;
  stashFeedbackDraft(); // openArtifact is about to clear the composer
  openArtifact(artifactSeq[i].url, artifactSeq[i].name, artifactSeq, i);
}

// openArtifact renders one artifact into the overlay. seq/idx are optional:
// with them the pager walks the job's list, without them (or with a sequence
// that doesn't actually contain url) the artifact opens as a SINGLETON — the
// pager hides itself and this behaves exactly as it did before next/prev
// existed. That fallback is what keeps a chip whose job can't be resolved
// from throwing, and it is also what enforces the
// artifactSeq[artifactIdx].url === artifactURL invariant: the index is
// re-found by url rather than trusted.
function openArtifact(url, name, seq, idx) {
  let list = seq && seq.length ? seq : null;
  let at = -1;
  if (list) {
    at = list[idx] && list[idx].url === url ? idx : list.findIndex((e) => e.url === url);
  }
  if (at < 0) {
    list = [{ url, name }];
    at = 0;
  }
  artifactSeq = list;
  artifactIdx = at;
  const gen = ++artifactGen; // invalidates every async tail still in flight
  document.getElementById("panel-title").textContent = name;
  artifactURL = url;
  rawText = "";
  showRaw = false;
  overlay.hidden = false;
  renderPager();
  document.getElementById("panel-raw").hidden = isImage(url) || /\.pdf$/i.test(url);
  document.getElementById("panel-feedback").hidden = false;
  hideFeedbackForm();
  restoreFeedbackDraft(url);
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
    hookIframeKeys();
    return;
  }
  panelBody.className = "panel-body";
  panelBody.textContent = "loading…";
  fetch(url)
    .then((r) => { if (!r.ok) throw new Error(r.status); return r.text(); })
    .then((text) => {
      // held → across a markdown set fires several of these at once and the
      // LAST to resolve would otherwise win the body — whichever artifact
      // that happens to be. Both tails drop: the failure message would wipe
      // the artifact you actually navigated to just as thoroughly.
      if (gen !== artifactGen) return;
      rawText = text;
      panelBody.className = "panel-body md";
      panelBody.innerHTML = renderMD(text);
    })
    .catch((e) => {
      if (gen !== artifactGen) return;
      panelBody.textContent = "failed to load: " + e;
    });
}

// isTextEntry answers "is this keystroke someone typing?" — the pager's arrow
// keys must never steal a caret move inside the feedback composer.
function isTextEntry(el) {
  if (!el || !el.tagName) return false;
  if (el.isContentEditable) return true;
  const tag = el.tagName.toUpperCase();
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT";
}

// artifactKeydown is the ArrowLeft/ArrowRight half of the pager. It is a
// NAMED handler rather than an inline listener because it gets attached
// twice: once to the board document, and again to each artifact iframe's own
// document (see hookIframeKeys). A modified arrow is left alone — those are
// the browser's (word-jump, history) and none of them mean "next artifact".
function artifactKeydown(ev) {
  if (ev.key !== "ArrowLeft" && ev.key !== "ArrowRight") return;
  if (overlay.hidden) return;
  if (ev.metaKey || ev.ctrlKey || ev.altKey) return;
  if (isTextEntry(ev.target)) return;
  ev.preventDefault();
  stepArtifact(ev.key === "ArrowRight" ? 1 : -1);
}

// hookIframeKeys re-attaches artifactKeydown to the artifact iframe's OWN
// document. Once focus lands inside the frame — one click on a diagram card
// does it — keydown fires on contentDocument and never bubbles out to the
// board, so without this the arrows die on exactly the artifacts you page
// through most. Must be called on every path that creates the iframe, which
// is openArtifact AND the raw toggle turning raw back off.
//
// try/catch, and a null-tolerant read: a document that isn't reachable must
// degrade to buttons-only, never throw. A PDF is the accepted case of that —
// the browser's built-in viewer is its own plugin document, unreachable from
// here, so on a .pdf the arrows stay with the viewer and ‹ › are the pager.
function hookIframeKeys() {
  const frame = panelBody.querySelector("iframe");
  if (!frame) return;
  frame.addEventListener("load", () => {
    try {
      const doc = frame.contentDocument;
      if (doc) doc.addEventListener("keydown", artifactKeydown);
    } catch {
      /* cross-origin or torn down: buttons still work */
    }
  });
}

// resyncArtifactSeq re-derives the open artifact's sequence from the board
// payload that just landed. A live cell writes artifacts while the viewer is
// open, so N has to grow and › has to reach the new ones without a reopen.
// Position is re-found by URL, never carried as an index — the list can gain
// entries ahead of the open one.
//
// It NEVER closes the overlay, and that is not an oversight: checkStaleModals
// deliberately leaves the artifact overlay alone (it isn't scoped to a job's
// state), and this must not become the back door that changes that. An
// artifact that has vanished from the job's list — deleted, renamed — keeps
// the sequence it was opened with, so it stays readable and its neighbours
// stay reachable.
function resyncArtifactSeq() {
  if (overlay.hidden) return;
  const seq = artifactSeqFor(artifactSeqKey);
  if (!seq) return;
  const i = seq.findIndex((e) => e.url === artifactURL);
  if (i < 0) return;
  artifactSeq = seq;
  artifactIdx = i;
  renderPager();
}

// refreshModalDocq re-renders whichever open modal carries its own copy of
// slug's doc-questions panel — the steer modal (via fillSteerContext, which
// rebuilds the whole context block) or the job editor (fillEditorDocq). The
// #board rebuild every action triggers never touches modal DOM, so without
// this a row inside an open modal keeps advertising state the board has
// already moved past — and since the card shows only a count chip, the
// modal is the only surface showing the question at all.
function refreshModalDocq(slug) {
  if (steerOverlay && !steerOverlay.hidden && steerSlug === slug) {
    fillSteerContext(jobsBySlug[slug], steerMode);
  }
  if (editorOverlay && !editorOverlay.hidden && editorSlug === slug) {
    fillEditorDocq(jobsBySlug[slug]);
  }
}

// refreshModalFeedback is refreshModalDocq's sibling for the feedback panel,
// and it exists for the same reason: the card carries only a count chip, so
// an open modal is the ONLY surface showing the notes themselves, and the
// #board rebuild every action triggers never reaches modal DOM. Called from
// every fb-* handler — the edit/cancel toggles (pure local state, no server
// round trip at all) and after a save/delete lands.
function refreshModalFeedback(slug) {
  if (steerOverlay && !steerOverlay.hidden && steerSlug === slug) {
    fillSteerContext(jobsBySlug[slug], steerMode);
  }
  if (editorOverlay && !editorOverlay.hidden && editorSlug === slug) {
    fillEditorFeedback(jobsBySlug[slug]);
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
      // the answer landed — drop any stashed draft for it, so reopening the
      // modal doesn't re-seed text that's already been sent
      dqDrafts.delete(dqDraftKey(slug, id));
      invalidateBoard();
      refresh();
    }
    refreshModalDocq(slug);
    return ok;
  });
}

document.addEventListener("click", (ev) => {
  // side threads come FIRST: both affordances sit inside the card's own
  // clickable area (and inside the steer modal's context block), so they
  // must return before the generic .chip branch below — which would open an
  // artifact — and before the card-open fallback at the bottom, which would
  // drop the steer/editor modal on top of the thread overlay.
  const threadChip = ev.target.closest(".thread-chip");
  if (threadChip) {
    openThread(threadChip.dataset.slug, threadChip.dataset.tid);
    return;
  }
  const branchBtn = ev.target.closest(".branch-btn");
  if (branchBtn) {
    openThread(branchBtn.dataset.slug, null);
    return;
  }
  const chip = ev.target.closest(".chip");
  if (chip) {
    // the chip is the ONE place that knows which job this artifact belongs
    // to, so the sequence is resolved here and the key stashed for the poll's
    // re-derive. A chip with no resolvable job (missing data-key, or the job
    // dropped off the board since render) passes no sequence at all and
    // openArtifact opens it as a singleton.
    artifactSeqKey = chip.dataset.key || "";
    const seq = artifactSeqFor(artifactSeqKey);
    const idx = seq ? seq.findIndex((e) => e.url === chip.dataset.url) : -1;
    openArtifact(chip.dataset.url, chip.dataset.name, seq, idx);
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
  // ---- job feedback panel (modal-only — see feedbackHTML) ----
  //
  // Toggling edit mode is a purely local state change: the board shows a
  // count, not the note, so only the modal's own copy has to re-render.
  const fbEdit = ev.target.closest(".fb-edit");
  if (fbEdit) {
    fbEditing.add(fbEdit.dataset.id);
    refreshModalFeedback(fbEdit.dataset.slug);
    return;
  }
  const fbCancel = ev.target.closest(".fb-cancel");
  if (fbCancel) {
    fbEditing.delete(fbCancel.dataset.id);
    refreshModalFeedback(fbCancel.dataset.slug);
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
    const slug = fbSave.dataset.slug;
    fbSave.disabled = true;
    post("/api/feedback", { slug, id: Number(id), feedback: text }).then((ok) => {
      if (ok) {
        fbEditing.delete(id);
        refreshModalFeedback(slug);
        return;
      }
      // post()'s failure path rebuilds #board, which no longer holds this
      // row — the panel lives in a modal, and nothing here re-renders it —
      // so `row`/`noteEl` are still live and the message lands directly.
      // The row stays in edit mode with its text intact for a retry.
      if (noteEl) noteEl.textContent = "save failed — see status bar";
      fbSave.disabled = false;
    });
    return;
  }
  const fbDelete = ev.target.closest(".fb-delete");
  if (fbDelete) {
    if (!confirm("delete this feedback note? this can't be undone.")) return;
    const slug = fbDelete.dataset.slug;
    fbDelete.disabled = true;
    post("/api/feedback", { slug, id: Number(fbDelete.dataset.id), delete: true }).then((ok) => {
      // the deleted row has to leave the open modal's copy of the panel too
      // — the #board rebuild post() already ran only updates the count chip
      if (ok) refreshModalFeedback(slug);
      else fbDelete.disabled = false;
    });
    return;
  }
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
    // a pane on the button wins over a slug: the thread head's button targets
    // the THREAD's own tmux window, while the card's button carries only a
    // slug — which the server resolves to zeus' pane, a different window
    const pane = tmuxOpen.dataset.pane;
    post("/api/peers/open", pane ? { pane } : { slug: tmuxOpen.dataset.slug }).then((data) => {
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

// Enter-to-send for the free-text doc-question answer in a modal's panel —
// .dq-input had no keyboard submit path at all (only the .dq-send click
// above), and it isn't inside a form. Guard IME
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

// ---- command chips: /info /aim /do /pr /dream insertion into composer
// textareas ----
// one-tap insertion of the kraken pipeline commands (info -> aim -> do
// -> pr, plus /dream, which is not part of that chain — it's the nightly
// corroboration pass, and the only one a job usually gets from the daemon
// rather than from carl) into whichever prompt composer they're wired to.
// Always lands at the cursor position (replacing any selection) with a
// trailing space, then returns focus to the textarea so typing continues
// right after the command. Fires a bubbling "input" event afterward so
// listeners already wired to the textarea (draft sync, autosave) see the
// change like any other keystroke.
const CMD_CHIPS = ["/info", "/aim", "/do", "/pr", "/dream"];

// ---- command highlight: mirrored backdrop rendering /info /aim /do /pr
// /dream, each in its own skill color with glow (info blue, aim red, do
// yellow, pr green, dream violet), behind the (now text-transparent)
// composer textareas — a
// <textarea> can't style substrings of its own value, so the real glyphs
// live in a sibling div instead (style.css's .cmd-hl-wrap/.cmd-hl-backdrop,
// unioned with #steer-text/#editor-text's own rule for font/padding/
// border-width so the two surfaces can't drift apart). Escaping runs first,
// then the boundary match — none of the five commands' characters are
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
// upload paths are ~/.kraken/uploads/..., never /info, /aim, /do, /pr, or
// /dream. The trailing \b is also what keeps the alternation order harmless
// now that one command is a prefix of another: leftmost-first would match
// "/do" inside "/dream", but the boundary check fails between "o" and "r"
// and the engine backtracks to the "/dream" branch.
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
  const threadsEl = document.getElementById("steer-threads");
  const detailEl = document.getElementById("steer-detail");
  const lastSentEl = document.getElementById("steer-lastsent");
  const chipsEl = document.getElementById("steer-chips");
  const docqEl = document.getElementById("steer-docq");
  const fbEl = document.getElementById("steer-feedback");
  const tokEl = document.getElementById("steer-tokens");

  const crew = mode !== "reactivate" && j && crewChipsHTML(j);
  // PRs are job history, not cell state — shown in every mode, including
  // reactivate ("what already shipped" is exactly the reactivation context)
  const prs = j && j.prs && j.prs.length ? j.prs : null;
  // side threads are job history too (a closed thread outlives its cell), so
  // like PRs they render in every mode — threadChipsHTML itself drops the
  // ⑂ branch button once the cell isn't live, which is the only part of the
  // row a reactivate-mode open couldn't act on
  const threads = j ? threadChipsHTML(j) : "";
  const detail = j && j.detail;
  const lastSent = j && j.last_sent;
  const artifacts = mode !== "reactivate" && j && j.artifacts && j.artifacts.length ? j.artifacts : null;
  // doc questions are job history, not cell state — shown in every mode
  // including reactivate, same as prs above; a torn-down job's questions are
  // still readable, answering just errors via toast. This modal (and the job
  // editor) is the ONLY place they render: the card carries a count chip and
  // nothing else, so this panel is the drilldown the chip advertises.
  // The rebuild below is wholesale, same hazard as renderBoardDOM's —
  // seedDocqDrafts refills it from whatever a previous close stashed, then
  // snapshot/restore lets a live in-modal draft win over that stash.
  const docq = j ? docQuestionsHTML(j) : "";
  // carl's per-artifact feedback, same story as the questions above: job
  // history, every mode, and this modal is one of the only two surfaces that
  // renders it at all now that the card shows a count chip.
  const fb = j ? feedbackHTML(j) : "";
  // token spend is job history in the strongest sense — it's summed from
  // transcripts that outlive the cell dir — so like PRs it renders in every
  // mode, reactivate included: "what did this already cost" is exactly the
  // question a reactivation asks. Empty string when the job was never
  // measured, which keeps its container hidden (see tokensHTML).
  const tok = j ? tokensHTML(j) : "";

  crewEl.hidden = !crew;
  crewEl.innerHTML = crew || "";

  prsEl.hidden = !prs;
  prsEl.innerHTML = prs ? prs.map(prChipHTML).join("") : "";

  threadsEl.hidden = !threads;
  threadsEl.innerHTML = threads;

  detailEl.hidden = !detail;
  // renderMD escapes before transforming, so agent-authored text is inert
  detailEl.innerHTML = detail ? renderMD(detail) : "";

  lastSentEl.hidden = !lastSent;
  lastSentEl.textContent = lastSent ? `me→zeus: ${lastSent}` : "";

  chipsEl.hidden = !artifacts;
  chipsEl.innerHTML = artifacts ? artifactListHTML(artifacts, jobKey(j), jobLabel(j)) : "";

  // one snapshot spans both panels: they're replaced back to back, and an
  // .fb-edit-input being retyped while a poll re-opens this context block
  // deserves exactly the treatment a .dq-input gets
  const dqState = snapshotDocInputs();
  docqEl.hidden = !docq;
  docqEl.innerHTML = docq;
  fbEl.hidden = !fb;
  fbEl.innerHTML = fb;
  seedDocqDrafts(docqEl);
  restoreDocInputs(dqState);

  tokEl.hidden = !tok;
  tokEl.innerHTML = tok;

  container.hidden =
    !crew && !prs && !threads && !detail && !lastSent && !artifacts && !docq && !fb && !tok;
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

  // the colour strip is per-job state like the head's buttons, so it's set
  // on every open. It rides no mode check: recolouring is valid in steer,
  // unblock and reactivate alike, and it never touches the cell.
  setSwatchStrip(document.getElementById("steer-swatches"), (jobsBySlug[slug] || {}).color);

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

// ---- side threads (a fifth overlay, beside artifact/steer/usage/editor) ----
//
// A thread is a fork of zeus' session running as a second claude in its own
// tmux window of the same cell: somewhere to ask without disturbing zeus.
// One overlay serves two modes — "new" (branch: required name + optional
// seed question) and "thread" (read/steer/kill/revive one that exists) —
// because they share a head, a body column and a close path; openThread's
// tid argument picks between them.
//
// Every op goes through post("/api/threads"), so a server refusal ("no live
// cell to branch", "3 live threads already", "t2 can't be revived: …") lands
// in the status bar in the server's own words — this file never invents
// error copy for a failed op, it only points at the status bar.

const threadOverlay = document.getElementById("thread-overlay");
let threadSlug = null;
let threadTid = null; // null = "new" mode (branching), else the thread's id
// last known truth about the open thread, merged from two sources: the board
// payload (checkStaleModals → syncThreadFromBoard, every 3s) and the read
// endpoint's own live/pane fields (fresher — it just looked at tmux).
let threadTitle = "";
let threadLive = false;
let threadPane = "";
let threadPoll = null;
// bumped by every open/close/mode switch — an in-flight read/capture that
// resolves after the overlay moved on must not paint into it
let threadGen = 0;
// true while the head's title is swapped for its rename input: the 3s board
// sync must not overwrite what's being typed
let threadRenaming = false;
// last conversation HTML painted — re-rendering identical markup every 3s
// would reset the scroll position mid-read for no reason
let threadConvoHTML = "";
// set when a read fails, cleared when one succeeds — so a recovered read
// clears its OWN note without also wiping a "send failed"/"message is empty"
// message the user just triggered (the 3s poll would otherwise eat it)
let threadReadFailed = false;
// blocks the last read said are kept on the job — quoted by the closed note,
// which paintThreadHead now owns (it can be repainted without a fresh read)
let threadKept = 0;
// the live-pane mirror is opt-in and sticky, the same gating iOS does with
// its Terminal tab: a capture nobody opened is a second tmux round-trip every
// 3s for a pane nobody is looking at. One click, remembered from then on.
let threadMirror = localStorage.getItem("kraken-thread-mirror") === "1";

function threadRecord(slug, tid) {
  const j = jobsBySlug[slug];
  if (!j || !tid) return null;
  return (j.threads || []).find((t) => t.tid === tid) || null;
}

// created_at/closed_at aren't pinned to a unit by the wire contract and
// age() takes seconds — normalize a millisecond stamp rather than rendering
// a 56-year-old thread. Same defensive read as every other foreign field.
function threadSeconds(ts) {
  const n = Number(ts) || 0;
  return n > 1e12 ? Math.floor(n / 1000) : n;
}

function threadNote(msg) {
  document.getElementById("thread-note").textContent = msg || "";
}

// paintThreadHead renders the WHOLE live/closed presentation from the module
// state above — head buttons, pill, composer, closed note and the mirror —
// so the read endpoint and the board poll can both just update state and
// repaint. One owner is load-bearing: the two callers run on out-of-phase
// timers, and a head that says "closed · ⟲ revive" over a still-visible
// composer earns the user a 409 for a message they were invited to type.
function paintThreadHead() {
  const tidEl = document.getElementById("thread-tid");
  const nameEl = document.getElementById("thread-name-label");
  const pill = document.getElementById("thread-pill");
  const tmuxBtn = document.getElementById("thread-tmux");
  const reviveBtn = document.getElementById("thread-revive");
  const killBtn = document.getElementById("thread-kill");
  if (!threadTid) {
    tidEl.textContent = "⑂ branch — " + (threadSlug || "");
    nameEl.hidden = true;
    nameEl.textContent = "";
    document.getElementById("thread-name-input").hidden = true;
    pill.hidden = true;
    tmuxBtn.hidden = true;
    reviveBtn.hidden = true;
    killBtn.hidden = true;
    return;
  }
  tidEl.textContent = "⑂ " + threadTid;
  // hidden only while the rename input has taken its place
  if (!threadRenaming) {
    nameEl.hidden = false;
    nameEl.textContent = threadTitle;
  }
  pill.hidden = false;
  pill.className = "thread-pill " + (threadLive ? "live" : "closed");
  pill.textContent = threadLive ? "live" : "closed";
  // tmux raises THIS THREAD's window in Ghostty, by pane — pointless once the
  // window is gone, and impossible from a phone either way. Hidden without a
  // pane too: a slug-only post would raise zeus' window instead, which is the
  // one window a side thread exists in order not to disturb.
  tmuxBtn.hidden = !threadLive || !threadPane;
  tmuxBtn.dataset.pane = threadPane || "";
  reviveBtn.hidden = threadLive;
  killBtn.hidden = !threadLive;

  // body: composer for a live thread, the closed note for a dead one
  document.getElementById("thread-composer").hidden = !threadLive;
  const closedNote = document.getElementById("thread-closed-note");
  closedNote.hidden = threadLive;
  if (!threadLive) {
    const rec = threadRecord(threadSlug, threadTid);
    const killedAt = rec && rec.closed_at ? threadSeconds(rec.closed_at) : 0;
    closedNote.textContent =
      "tmux window closed" +
      // 0 means "no read has landed yet", not "nothing was kept" — say
      // nothing rather than undercount the conversation sitting right above
      (threadKept ? ` · ${threadKept} blocks kept on the job` : "") +
      (killedAt ? ` · killed ${age(killedAt)} ago` : "") +
      " · revive to continue";
  }
  paintThreadMirror();
}

// syncThreadFromBoard pulls the open thread's row out of the freshest board
// payload. It fills what the board alone knows — the title — and refreshes
// live/pane from the board's own view of the cell; the read endpoint writes
// the same two fields on its own tick, so whichever landed last wins. That's
// safe only because paintThreadHead repaints head AND body together: the two
// out-of-phase timers can disagree about the age of the truth, never about
// what's on screen.
function syncThreadFromBoard() {
  const rec = threadRecord(threadSlug, threadTid);
  if (!rec) return;
  if (!threadRenaming) threadTitle = rec.title || "";
  threadLive = rec.state === "live";
  if (rec.pane) threadPane = rec.pane;
  paintThreadHead();
}

function stopThreadPoll() {
  clearInterval(threadPoll);
  threadPoll = null;
}

function startThreadPoll() {
  stopThreadPoll();
  // the board's own cadence; pollThread no-ops while the tab is hidden
  threadPoll = setInterval(pollThread, 3000);
}

function pollThread() {
  if (document.hidden || threadOverlay.hidden || !threadSlug || !threadTid) return;
  loadThread();
}

function closeThread() {
  stopThreadPoll();
  cancelThreadRename();
  threadOverlay.hidden = true;
  threadGen++; // orphan any in-flight read/capture
  threadSlug = null;
  threadTid = null;
  threadTitle = "";
  threadLive = false;
  threadPane = "";
  threadConvoHTML = "";
  threadKept = 0;
}

// openThread(slug, tid) — tid null/undefined opens the branch dialog.
function openThread(slug, tid) {
  threadGen++;
  cancelThreadRename();
  threadSlug = slug;
  threadTid = tid || null;
  threadConvoHTML = "";
  threadKept = 0; // the previous thread's count must not leak into this one
  threadReadFailed = false;
  threadNote("");
  threadOverlay.hidden = false;

  const newBox = document.getElementById("thread-new");
  const viewBox = document.getElementById("thread-view");
  if (!threadTid) {
    stopThreadPoll(); // nothing to read yet — and never leave a prior thread's tick running
    threadTitle = "";
    threadLive = false;
    threadPane = "";
    newBox.hidden = false;
    viewBox.hidden = true;
    document.getElementById("thread-new-name").value = "";
    document.getElementById("thread-seed").value = "";
    document.getElementById("thread-branch").disabled = false;
    paintThreadHead();
    document.getElementById("thread-new-name").focus();
    return;
  }
  newBox.hidden = true;
  viewBox.hidden = false;
  document.getElementById("thread-text").value = "";
  document.getElementById("thread-convo").innerHTML = '<div class="thread-empty">loading…</div>';
  // seed the head from the board payload so it's filled in before the first
  // read lands, then let the read correct live/pane. paintThreadHead resets
  // the body with it — composer, closed note and mirror all follow this
  // thread's state, never the state of the one that was open a click ago.
  const rec = threadRecord(slug, threadTid);
  threadTitle = rec ? rec.title || "" : "";
  threadLive = rec ? rec.state === "live" : false;
  threadPane = rec && rec.pane ? rec.pane : "";
  paintThreadHead();
  loadThread();
  startThreadPoll();
}

// switchToThread flips a just-branched "new" overlay onto the thread it
// created, without closing and reopening (which would lose the overlay).
// The slug is a parameter, not a re-read of the global: by the time a branch
// resolves the overlay may have been dismissed, and openThread(null, tid)
// would resurrect it around a thread it can never read.
function switchToThread(slug, tid, title) {
  openThread(slug, tid);
  if (title && !threadTitle) {
    threadTitle = title; // the board hasn't caught the new row yet
    paintThreadHead();
  }
}

const THREAD_BLOCK_KINDS = {
  user: { cls: "user", icon: "❯", who: "carl" },
  text: { cls: "asst", icon: "↯", who: "" },
  thinking: { cls: "think", icon: "✳", who: "thinking" },
  tool: { cls: "tool", icon: "→", who: "" },
  teammate: { cls: "asst", icon: "✉", who: "teammate" },
};

function threadBlockHTML(b, inherited) {
  // hasOwnProperty, not a truthiness check: a kind of "constructor" or
  // "toString" would otherwise resolve to an Object.prototype member
  const known = Object.prototype.hasOwnProperty.call(THREAD_BLOCK_KINDS, b.kind);
  const kind = known ? b.kind : "text";
  const spec = THREAD_BLOCK_KINDS[kind];
  const cls = `blk ${spec.cls}${inherited ? " inherited" : ""}`;
  const text = b.text || "";
  if (kind === "tool") {
    // tool lines are already terse and mono — no speaker row, no markdown
    return `<div class="${cls}">${spec.icon} ${esc(text)}</div>`;
  }
  const who = b.from || spec.who || (kind === "text" ? "thread " + (threadTid || "") : kind);
  // renderMD escapes before transforming, so agent-authored text is inert
  return `<div class="${cls}"><div class="who">${spec.icon} ${esc(who)}</div><div class="blk-body">${renderMD(text)}</div></div>`;
}

// threadSplitIndex locates the first block the thread itself produced —
// everything before it is history inherited from zeus and renders dimmed
// above the "⑂ branched here" divider.
//
// The server tags each block with an `inherited` boolean (internal/api
// threads.go, ios/API.md) and that is the only shape there is, so the split
// is simply one past the last inherited block: 0 when the thread has said
// everything itself, blocks.length when it has said nothing yet.
function threadSplitIndex(data) {
  const blocks = data.blocks || [];
  let last = -1;
  blocks.forEach((b, i) => { if (b.inherited) last = i; });
  return last + 1;
}

function threadConvoRender(data) {
  const blocks = data.blocks || [];
  if (!blocks.length) {
    return '<div class="thread-empty">no messages yet</div>';
  }
  const split = threadSplitIndex(data);
  const forkLabel = data.fork_uuid ? " · " + data.fork_uuid : "";
  const out = [];
  blocks.forEach((b, i) => {
    if (i === split) out.push(`<div class="thread-divider">⑂ branched here${esc(forkLabel)}</div>`);
    out.push(threadBlockHTML(b, i < split));
  });
  // a thread whose every block is inherited (branched, nothing asked yet)
  // still gets its divider — at the very end, which is exactly where it is
  if (split >= blocks.length) {
    out.push(`<div class="thread-divider">⑂ branched here${esc(forkLabel)}</div>`);
  }
  return out.join("");
}

async function loadThread() {
  const gen = threadGen;
  const slug = threadSlug;
  const tid = threadTid;
  if (!slug || !tid) return;
  let data;
  try {
    // no n: the server's own default is the right size, and this runs every
    // 3s for as long as the overlay is open. LastBlocks counts NON-tool
    // blocks, so a big number means re-reading and re-parsing multi-megabyte
    // transcript tails on every tick to redraw a column nobody scrolled.
    const r = await fetch(
      `/api/thread/read?slug=${encodeURIComponent(slug)}&tid=${encodeURIComponent(tid)}`,
    );
    data = await r.json();
    if (!r.ok) throw new Error(data.error || r.status);
  } catch (e) {
    if (gen !== threadGen) return;
    threadReadFailed = true;
    threadNote("couldn't read this thread: " + e.message);
    return;
  }
  // the overlay moved on (closed, or opened another thread) mid-fetch
  if (gen !== threadGen) return;
  if (threadReadFailed) {
    threadReadFailed = false;
    threadNote("");
  }
  threadLive = !!data.live;
  if (data.pane) threadPane = data.pane;
  threadKept = typeof data.counted === "number" ? data.counted : (data.blocks || []).length;
  paintThreadHead(); // head AND body — composer/closed note/mirror included

  const convo = document.getElementById("thread-convo");
  const html = threadConvoRender(data);
  if (html !== threadConvoHTML) {
    // keep a mid-read scroll position; stay pinned to the bottom if that's
    // where the reader already was (the live-tail case)
    const atBottom = convo.scrollHeight - convo.scrollTop - convo.clientHeight < 24;
    const prevTop = convo.scrollTop;
    threadConvoHTML = html;
    convo.innerHTML = html;
    convo.scrollTop = atBottom ? convo.scrollHeight : prevTop;
  }

  refreshThreadPane();
}

// paintThreadMirror places the live-pane mirror from state alone — no
// request. The header row is the toggle, so it shows whenever there IS a
// pane to mirror; the <pre> below it only when the reader has opened it.
function paintThreadMirror() {
  const paneEl = document.getElementById("thread-pane");
  const headEl = document.getElementById("thread-panehead");
  const has = threadLive && !!threadPane;
  headEl.hidden = !has;
  paneEl.hidden = !has || !threadMirror;
  if (!has) {
    paneEl.textContent = "";
    return;
  }
  const j = jobsBySlug[threadSlug];
  document.getElementById("thread-panelabel").textContent =
    (threadMirror ? "▾ live pane · " : "▸ live pane · ") +
    (j && j.cell ? j.cell + " · " : "") + threadPane;
}

// refreshThreadPane mirrors the thread's tmux pane verbatim — the same
// capture-pane readout k v shows, and the only way to watch a live thread
// from a phone, where ⧉ tmux (Ghostty, on the Mac) can do nothing. It's a
// second tmux round-trip on every 3s tick, so it only runs while the mirror
// is actually open — the same gating iOS does with its Terminal tab.
async function refreshThreadPane() {
  const gen = threadGen;
  const paneEl = document.getElementById("thread-pane");
  const headEl = document.getElementById("thread-panehead");
  paintThreadMirror();
  if (!threadLive || !threadPane || !threadMirror) return;
  const pane = threadPane;
  try {
    const r = await fetch("/api/peers/capture?pane=" + encodeURIComponent(pane));
    const data = await r.json();
    if (!r.ok) throw new Error(data.error || r.status);
    if (gen !== threadGen || threadPane !== pane || !threadLive || !threadMirror) return;
    paneEl.textContent = data.content || "";
  } catch {
    // a pane that just died is the common case here, and the next poll
    // re-reads live/pane from the read endpoint — stay quiet, hide the mirror
    if (gen !== threadGen || threadPane !== pane) return;
    paneEl.hidden = true;
    headEl.hidden = true;
  }
}

// the header row doubles as the mirror's open/close control — clicking it
// starts (or stops) the capture requests, and the choice sticks
function toggleThreadMirror() {
  threadMirror = !threadMirror;
  localStorage.setItem("kraken-thread-mirror", threadMirror ? "1" : "0");
  refreshThreadPane();
}

// ---- thread ops ----

async function branchThread() {
  const gen = threadGen;
  const slug = threadSlug;
  const nameEl = document.getElementById("thread-new-name");
  const title = nameEl.value.trim();
  if (!slug) return;
  if (!title) {
    threadNote("name this thread — one line on what it's for");
    nameEl.focus();
    return;
  }
  const btn = document.getElementById("thread-branch");
  btn.disabled = true;
  const res = await post("/api/threads", {
    slug,
    op: "branch",
    title,
    text: document.getElementById("thread-seed").value.trim(),
  });
  btn.disabled = false;
  // branching spawns a claude in a new tmux window — seconds, during which
  // Escape/backdrop/checkStaleModals can all close the overlay. A cancelled
  // branch stays cancelled: the thread exists either way and its chip is on
  // the card, so there's nothing to reopen an overlay for.
  if (gen !== threadGen) return;
  // post() already put the server's own sentence in the status bar
  if (!res) {
    threadNote("branch failed — see status bar");
    return;
  }
  if (res.tid) switchToThread(slug, res.tid, title);
  else closeThread(); // branched, but no id came back — the card carries it
}

async function threadOp(op, extra) {
  const slug = threadSlug;
  const tid = threadTid;
  if (!slug || !tid) return null;
  const res = await post("/api/threads", { slug, op, tid, ...(extra || {}) });
  // an op can outlive the overlay it was fired from
  if (threadSlug !== slug || threadTid !== tid) return res;
  if (!res) threadNote(op + " failed — see status bar");
  else {
    threadNote("");
    loadThread();
  }
  return res;
}

document.getElementById("thread-branch").addEventListener("click", branchThread);
document.getElementById("thread-cancel").addEventListener("click", closeThread);
document.getElementById("thread-kill").addEventListener("click", async () => {
  // no confirm: killing a thread only closes its tmux window — the row and
  // the conversation stay on the job and ⟲ revive brings it back
  const btn = document.getElementById("thread-kill");
  btn.disabled = true;
  await threadOp("kill");
  btn.disabled = false;
});
document.getElementById("thread-revive").addEventListener("click", async () => {
  const btn = document.getElementById("thread-revive");
  btn.disabled = true;
  await threadOp("revive");
  btn.disabled = false;
});
document.getElementById("thread-send").addEventListener("click", async () => {
  const text = document.getElementById("thread-text").value.trim();
  if (!text) {
    threadNote("message is empty");
    return;
  }
  const btn = document.getElementById("thread-send");
  btn.disabled = true;
  const res = await threadOp("send", { text });
  btn.disabled = false;
  if (res) document.getElementById("thread-text").value = "";
});
document.getElementById("thread-text").addEventListener("keydown", (ev) => {
  if (ev.key === "Enter" && (ev.metaKey || ev.ctrlKey)) {
    ev.preventDefault();
    document.getElementById("thread-send").click();
  }
});
for (const id of ["thread-new-name", "thread-seed"]) {
  document.getElementById(id).addEventListener("keydown", (ev) => {
    if (ev.key === "Enter" && (ev.metaKey || ev.ctrlKey)) {
      ev.preventDefault();
      document.getElementById("thread-branch").click();
    }
  });
}

// ---- rename: the head's title IS the input, swapped in place ----

function cancelThreadRename() {
  threadRenaming = false;
  const input = document.getElementById("thread-name-input");
  if (input) input.hidden = true;
  const label = document.getElementById("thread-name-label");
  if (label && threadTid) {
    label.hidden = false;
    label.textContent = threadTitle;
  }
}

document.getElementById("thread-name-label").addEventListener("click", () => {
  if (!threadTid) return;
  threadRenaming = true;
  const label = document.getElementById("thread-name-label");
  const input = document.getElementById("thread-name-input");
  label.hidden = true;
  input.hidden = false;
  input.value = threadTitle;
  input.focus();
  input.select();
});
// role="button" promises keyboard operability — honor Enter/Space. Renaming
// is the ONLY thing this label does, so without it the rename is mouse-only.
document.getElementById("thread-name-label").addEventListener("keydown", (ev) => {
  if (ev.key === "Enter" || ev.key === " ") {
    ev.preventDefault();
    document.getElementById("thread-name-label").click();
  }
});
document.getElementById("thread-name-input").addEventListener("keydown", async (ev) => {
  // Escape here means "stop renaming", not "close the overlay" — the
  // document-level Escape chain must not also fire
  if (ev.key === "Escape") {
    ev.stopPropagation();
    cancelThreadRename();
    return;
  }
  if (ev.key !== "Enter" || ev.isComposing || ev.keyCode === 229) return;
  ev.preventDefault();
  const input = document.getElementById("thread-name-input");
  const title = input.value.trim();
  if (!title || title === threadTitle) {
    cancelThreadRename();
    return;
  }
  input.disabled = true;
  const res = await threadOp("rename", { title });
  input.disabled = false;
  if (res) threadTitle = title;
  cancelThreadRename();
  paintThreadHead();
});
document.getElementById("thread-name-input").addEventListener("blur", () => cancelThreadRename());

threadOverlay.addEventListener("click", (ev) => {
  if (ev.target === threadOverlay) closeThread();
});
document.getElementById("thread-panehead").addEventListener("click", toggleThreadMirror);
// role="button" promises keyboard operability — honor Enter/Space
document.getElementById("thread-panehead").addEventListener("keydown", (ev) => {
  if (ev.key === "Enter" || ev.key === " ") {
    ev.preventDefault();
    toggleThreadMirror();
  }
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
  setFormColor(d.color);
  editorReady = !!d.ready;
  document.getElementById("f-solo").checked = !!d.solo;
  document.getElementById("f-prereqs").innerHTML = (d.prereqs || []).map(prereqRowHTML).join("");
  editorText.value = d.spec || "";
  refreshCmdHighlight("editor-text");
  refreshCmdChips("editor-text");
  document.getElementById("job-list").innerHTML = (d.slugs || []).map((x) => `<option value="${esc(x)}">`).join("");
}

// the editor's copy of the colour strip. Reading it into the form is the
// only direction the form owns — the strip writes itself back through
// initSwatchStrip's own POST, never through readForm.
function setFormColor(v) {
  setSwatchStrip(document.getElementById("f-swatches"), v);
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
  // the colour strip is deliberately NOT in this list. Everything else here
  // is job-file state that the crew owns while a cell is in flight, so the
  // server refuses it; a colour is carl's own label on the card and rides
  // its own ungated endpoint (/api/job/color). Recolouring a *running* job
  // is the main thing the feature is for — disabling these would kill it.
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

// fillEditorChips renders the job's artifacts in the editor — the third and
// last panel to move off the card and behind the click-in, after questions and
// feedback, and the one whose absence would bite hardest. A done or archived
// job routes a plain click HERE, never to the steer modal, so with the card
// down to a bare count (artifactChipHTML) this is the only surface left that
// names a settled job's docs at all — and settled jobs are exactly the ones
// carrying ten or twelve of them.
//
// No snapshot/restore and no scroll save, unlike its two siblings below —
// and NOT because the panel can't scroll (it is capped and scrolls, same as
// they are). Because it is rendered exactly once per open: openEditor is its
// only caller, and nothing re-runs it, where fillEditorFeedback is re-run
// after every feedback post and fillEditorDocq after every answer. There is no
// innerHTML replacement here for a scroll position or a half-typed input to
// survive, so there is nothing to snapshot.
function fillEditorChips(j) {
  const el = document.getElementById("editor-chips");
  const chips = j ? artifactListHTML(j.artifacts, jobKey(j), jobLabel(j)) : "";
  el.hidden = !chips;
  // .editor-docq-head, the same class both sibling panels emit — a shared
  // class, not a shared rule, so the three labels cannot drift apart
  el.innerHTML = chips ? `<div class="editor-docq-head">artifacts</div><div class="chips">${chips}</div>` : "";
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
// you back to the top). snapshotDocInputs carries no scroll map at all (the
// panels it covers are all uncapped now bar this one), hence the local
// save/restore here.
function fillEditorDocq(j) {
  const el = document.getElementById("editor-docq");
  const docq = j ? docQuestionsHTML(j) : "";
  const prev = el.querySelector(".doc-questions");
  const scrollTop = prev ? prev.scrollTop : 0;
  const dqState = snapshotDocInputs();
  el.hidden = !docq;
  el.innerHTML = docq ? `<div class="editor-docq-head">questions</div>${docq}` : "";
  seedDocqDrafts(el);
  restoreDocInputs(dqState);
  if (scrollTop) {
    const next = el.querySelector(".doc-questions");
    if (next) next.scrollTop = scrollTop;
  }
}

// fillEditorFeedback is fillEditorDocq's sibling for the feedback panel, and
// it matters more here than the questions one does: a note is usually left on
// a SHIPPED artifact, so most feedback belongs to a done or archived job —
// exactly the jobs whose card opens this editor rather than the steer modal.
// Without this copy, moving the panel off the card would have made a done
// job's notes unreachable from the board entirely. Same capped-scroller and
// snapshot/restore treatment as the questions panel above it, for the same
// two reasons.
function fillEditorFeedback(j) {
  const el = document.getElementById("editor-feedback");
  const fb = j ? feedbackHTML(j) : "";
  const prev = el.querySelector(".job-feedback");
  const scrollTop = prev ? prev.scrollTop : 0;
  const dqState = snapshotDocInputs();
  el.hidden = !fb;
  el.innerHTML = fb ? `<div class="editor-docq-head">feedback</div>${fb}` : "";
  restoreDocInputs(dqState);
  if (scrollTop) {
    const next = el.querySelector(".job-feedback");
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
  fillEditorChips(slug ? jobsBySlug[slug] : null);
  fillEditorDocq(slug ? jobsBySlug[slug] : null);
  fillEditorFeedback(slug ? jobsBySlug[slug] : null);
  if (isNew) {
    document.getElementById("editor-slug").focus();
    return;
  }
  // an archived job's key is jobKey's synthetic "archive:<slug>" (there is no
  // such job file), so there is no form to load — the panels filled above ARE
  // the whole modal for it. Asking the server for one would 404 and paint an
  // error over a view that is working exactly as intended.
  const aj = jobsBySlug[slug];
  if (aj && aj.archived) {
    // setFormReadOnly FIRST, and not only for the form it hides: it is the one
    // writer of editorText.readOnly, and three paths gate on that flag ALONE
    // rather than on #editor-form being hidden — the panel's dragover and drop
    // handlers and the document paste handler, all funnelling into
    // addEditorFiles. Skipping it would leave them armed at whatever the last
    // editable open set, so a ⌘V on this read-only modal would upload against
    // the synthetic "archive:<slug>" key and paint the server's 404 into a
    // panel that just said it was read-only.
    // It must come BEFORE the two writes below, because it sets #editor-hint
    // itself and this branch's wording has to win.
    setFormReadOnly(true);
    document.getElementById("editor-title").textContent = (aj.display_slug || aj.slug) + " — archived";
    document.getElementById("editor-hint").textContent = "read-only · unarchive to edit";
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

// the two colour strips. Two, not one, because the board routes a card click
// by liveness: a live working/blocked job opens the STEER modal and never the
// editor (see the .card handler above), so an editor-only picker would be
// reachable exactly when /api/job/color's ungated-ness is pointless and
// missing exactly when it matters. Same markup, same handler, different
// radio-group name and different slug thunk.
initSwatchStrip("f-swatches", "f-color", () => editorSlug);
initSwatchStrip("steer-swatches", "steer-color", () => steerSlug);

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
document.getElementById("panel-prev").addEventListener("click", () => stepArtifact(-1));
document.getElementById("panel-next").addEventListener("click", () => stepArtifact(1));
document.addEventListener("keydown", artifactKeydown);
document.getElementById("panel-raw").addEventListener("click", async () => {
  // this handler's await window is the widest in the viewer — an html/svg
  // diagram card reaches raw ONLY through the fetch below (the iframe path
  // never populates rawText) and those are the biggest payloads as well as
  // the most-paged. An arrow pressed meanwhile is not swallowed (isTextEntry
  // is false for the button that has focus), so openArtifact runs, resets
  // showRaw/rawText and paints the next artifact — and an unguarded resume
  // would then paint THIS artifact's source with showRaw already false,
  // desyncing the toggle from the view until you leave the artifact entirely.
  const gen = artifactGen;
  const url = artifactURL;
  showRaw = !showRaw;
  if (showRaw) {
    let text = rawText;
    if (!text) {
      // pages render via iframe without a fetch; raw needs the text
      try { text = await (await fetch(url)).text(); } catch { text = "failed to load"; }
      if (gen !== artifactGen) return;
      rawText = text;
    }
    panelBody.className = "panel-body raw";
    panelBody.textContent = text;
  } else if (isPage(url)) {
    panelBody.className = "panel-body page";
    panelBody.innerHTML = `<iframe src="${esc(url)}" title="artifact"></iframe>`;
    hookIframeKeys(); // a brand new frame, and a brand new document to route arrows out of
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
// cancel is the one gesture that means "throw this away" — it must also drop
// the stash, or the note would reappear the next time you page onto this
// artifact
document.getElementById("feedback-cancel").addEventListener("click", () => {
  feedbackDrafts.delete(artifactURL);
  hideFeedbackForm();
});
document.getElementById("feedback-send").addEventListener("click", async () => {
  const note = document.getElementById("feedback-note");
  const url = artifactURL;
  // "is the composer still showing the note I sent?" — an IDENTITY question,
  // which is why this is a url comparison and not the artifactGen counter the
  // body/raw tails use. Those ask "has the view moved?"; here the view moving
  // away and BACK (→ then ←, which re-opens A with its stashed draft restored)
  // must count as still-open, or a posted note is left sitting in an open
  // composer under a `feedback` label — every visible signal saying "not sent"
  // — and the obvious next move is a send that duplicates the row.
  // Re-evaluated at each use rather than snapshotted: there is an await in
  // between, and a step across it must be seen.
  const onSentArtifact = () => url === artifactURL;
  const parts = artifactParts(url);
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
    feedbackDrafts.delete(url); // it's sent; it is no longer a draft
    // the board/modal refresh below is about the artifact that was SENT and
    // is right either way, but the panel belongs to whatever is on screen —
    // page onto another artifact mid-send and clearing the composer here
    // would wipe THAT artifact's restored draft, and "saved ✓" would appear
    // over a note nobody has sent
    if (onSentArtifact()) hideFeedbackForm();
    invalidateBoard(); // feedback rides the board payload — the card's count chip must pick this up
    await refresh();
    // …and so must the panel itself, one layer down: this overlay is usually
    // opened from a chip inside the steer modal or the job editor, and
    // refresh() never touches modal DOM
    refreshModalFeedback(parts.slug);
    if (onSentArtifact()) {
      const label = document.getElementById("panel-feedback");
      label.textContent = "saved ✓";
      feedbackSavedTimer = setTimeout(() => { label.textContent = "feedback"; }, 2000);
    }
  } catch (e) {
    if (onSentArtifact()) note.textContent = "failed: " + e.message;
  } finally {
    sendBtn.disabled = false;
  }
});
overlay.addEventListener("click", (ev) => { if (ev.target === overlay) closeArtifact(); });
// one Escape closes one layer: the artifact viewer if it's on top (it can be
// opened from inside the steer/unblock modal), else the thread overlay (which
// can itself be opened from inside the steer modal's context block), else
// whichever of editor/steer is open underneath. Matches the z-index order in
// style.css — #overlay 30, #thread-overlay 25, the rest 20.
document.addEventListener("keydown", (ev) => {
  if (ev.key !== "Escape") return;
  if (!overlay.hidden) { closeArtifact(); return; }
  if (!threadOverlay.hidden) { closeThread(); return; }
  if (!steerOverlay.hidden) { flushSteerDraft(); clearSteerChips(); clearSteerDocq(); steerOverlay.hidden = true; return; }
  if (!editorOverlay.hidden) { flushEditorAutosave(); clearEditorDocq(); editorOverlay.hidden = true; return; }
  if (!usageOverlay.hidden) { usageOverlay.hidden = true; return; }
});

// ---- view switching: BOARD · LOG · DREAM, the whole tab strip ----

const VIEWS = ["board", "log", "dream"];

// a stored view that no longer exists falls back to the board rather than
// hiding every <main> and leaving a blank page — this is the read path a
// browser still holding "dag" from the removed prereq-graph view takes, and
// setView overwrites the stale value the first time a tab is clicked.
// Validating against VIEWS rather than `|| "board"` is the whole point: a
// stale-but-truthy value passes an || guard and then matches no <main>.
const storedView = localStorage.getItem("kraken-view");
let view = VIEWS.includes(storedView) ? storedView : "board";

// setView is the tabs' handler, and also how a surface outside the tab strip
// sends you somewhere — the failed-update banner's OPEN LOG, say.
function setView(v) {
  view = v;
  localStorage.setItem("kraken-view", view);
  applyView();
  refresh();
}

document.querySelectorAll(".viewtab").forEach((tab) =>
  tab.addEventListener("click", () => setView(tab.dataset.view)));

function applyView() {
  document.querySelectorAll(".viewtab").forEach((t) =>
    t.classList.toggle("active", t.dataset.view === view));
  for (const v of VIEWS) document.getElementById(v).hidden = view !== v;
  if (view === "log") refreshLog();
  if (view === "dream") refreshDream();
}

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

// ---- DREAM view: the nightly corroboration loop's two surfaces ----
//
// Left pane: one collapsed header per night's report, newest first, expanding
// to the rendered `00-dream-report.md`. Right pane: the open watchlist, where
// carl rules on each parked observation. Read-only on the left, the loop's
// only inbound channel on the right — the next run reads these verdicts and
// comments FIRST, ahead of any evidence, so a row marked `fine` is never
// proposed again and a `fix` comment becomes the specification the run
// follows. That ordering is why the comment box is the primary control here
// rather than an afterthought behind an edit toggle.
//
// Both panes re-fetch on the 3s board tick while the view is up, and both
// rebuild wholesale — hence the snapshot compare before each rebuild (the
// cheap defense: an unchanged payload never touches the DOM at all) plus
// snapshotDocInputs/restoreDocInputs around the watchlist's (the real one:
// a payload that DID change must not eat a half-typed comment).

let dreamReports = [];
let dreamWatch = [];
let dreamReportsSnapshot = "";
let dreamWatchSnapshot = "";
let dreamReportsNote = ""; // an endpoint error, shown in place of the empty state
let dreamWatchNote = "";
// which report dates are expanded — module-level so a rebuild can't collapse
// what carl opened. Seeded once with the newest night, since that is the one
// he came to read.
const dreamExpanded = new Set();
let dreamSeeded = false;
let dreamLoading = false;

async function dreamFetch(url) {
  const r = await fetch(url);
  const d = await r.json();
  if (!r.ok) throw new Error(d.error || String(r.status));
  return d;
}

// dreamDay shortens a watchlist timestamp to a local "MM-DD" for the sighting
// line. It has to take two shapes, because the row carries both: first_seen
// and last_seen are unix MILLIS (numbers, the same wire convention job mtimes
// use), while commented_at is an RFC3339 string stamped server-side. A number
// goes through Date so the day is the LOCAL one — a sighting logged at 23:30
// belongs to the evening carl remembers, not to the UTC tomorrow — and a
// string is read off its ISO prefix rather than parsed, since that prefix is
// already local-clock text. Anything else (0, "", an unexpected shape) reads
// as blank instead of "Invalid Date" or "1970".
function dreamDay(v) {
  if (typeof v === "number") {
    if (!v) return "";
    const d = new Date(v);
    if (isNaN(d.getTime())) return "";
    const p = (n) => String(n).padStart(2, "0");
    return `${p(d.getMonth() + 1)}-${p(d.getDate())}`;
  }
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(String(v || ""));
  return m ? `${m[2]}-${m[3]}` : "";
}

// dreamSectionCount counts the bullets under the FIRST heading whose text
// matches `re`, stopping at the next heading of any level. The report's
// section names are fixed by .claude/skills/dream/SKILL.md ("Shipped",
// "Judgment calls", "Watching", …) but the heading LEVEL it writes them at
// isn't, so the match is level-agnostic. A section that spells out "None"
// carries no bullets and counts 0 — which is exactly the header a quiet
// night should get.
function dreamSectionCount(body, re) {
  let inside = false;
  let n = 0;
  for (const line of String(body || "").split("\n")) {
    const h = /^#{1,6}\s+(.*)$/.exec(line);
    if (h) {
      if (inside) break;
      inside = re.test(h[1].trim());
      continue;
    }
    if (inside && /^\s*[-*]\s+\S/.test(line)) n++;
  }
  return n;
}

// dreamPRCount counts the DISTINCT PRs a night shipped, straight out of the
// report body — the loop's PRs belong to no job's `prs:` list (they're
// kraken's own hardening, opened from the dream cell), so the report text is
// the only place they're recorded. Lazy \S*? so trailing punctuation — the
// ")" of a markdown link, a sentence's full stop — stays out of the match.
function dreamPRCount(body) {
  return new Set(String(body || "").match(/https?:\/\/\S*?\/pull\/\d+/g) || []).size;
}

function dreamTagHTML(cls, n, one, many) {
  if (!n) return "";
  return `<span class="rep-tag ${cls}">${n} ${n === 1 ? one : many}</span>`;
}

function dreamReportHTML(r) {
  const open = dreamExpanded.has(r.date);
  const prs = dreamPRCount(r.body);
  const watching = dreamSectionCount(r.body, /^watching\b/i);
  const judged = dreamSectionCount(r.body, /^judgment calls?\b/i);
  const quiet = !prs && !watching && !judged;
  return `<div class="rep${open ? " open" : ""}">
    <button class="rep-h" data-date="${esc(r.date)}" aria-expanded="${open}">
      <span class="tri">${open ? "▾" : "▸"}</span>
      <span class="d">${esc(r.date)}</span>
      ${dreamTagHTML("pr", prs, "PR", "PRs")}
      ${dreamTagHTML("watch", watching, "watching", "watching")}
      ${dreamTagHTML("judge", judged, "judgment call", "judgment calls")}
      <span class="spacer"></span>
      <span class="sum">${esc(quiet ? "nothing shipped" : r.slug || "")}</span>
    </button>
    ${open ? `<div class="rep-b md">${renderMD(r.body || "")}</div>` : ""}
  </div>`;
}

function renderDreamReports() {
  const el = document.getElementById("dream-reports");
  const ct = document.getElementById("dream-reports-ct");
  ct.textContent = dreamReports.length
    ? `· ${dreamReports.length} night${dreamReports.length === 1 ? "" : "s"}`
    : "";
  el.innerHTML =
    dreamReports.map(dreamReportHTML).join("") ||
    `<div class="dream-empty">${esc(dreamReportsNote || "no reports yet — the nightly pass writes one per night")}</div>`;
}

// wlSeenText: sightings plus the window they span. A single sighting has no
// window worth printing, and first == last collapses to one date.
function wlSeenText(w) {
  const n = w.sightings || 1;
  const parts = [`${n} sighting${n === 1 ? "" : "s"}`];
  const first = dreamDay(w.first_seen);
  const last = dreamDay(w.last_seen);
  if (first) parts.push(`first ${first}`);
  if (last && last !== first) parts.push(`last ${last}`);
  return parts.join(" · ");
}

function wlPRHTML(url) {
  // agent-written field — only linkify http(s), same rule prChipHTML applies
  if (!/^https?:\/\//i.test(String(url || ""))) return "";
  const m = /\/pull\/(\d+)/.exec(url);
  return `<a class="pr-chip pr-open" href="${esc(url)}" target="_blank" rel="noreferrer">${esc(m ? "#" + m[1] : "PR")}</a>`;
}

// wlVerdict normalizes the server's field to the three states this UI knows
// — anything unexpected reads as "not ruled on yet" rather than rendering a
// row whose buttons all look off while the server thinks otherwise.
const wlVerdict = (w) => (w.verdict === "fine" || w.verdict === "fix" ? w.verdict : "");

// watchRowHTML renders one parked observation and its verdict controls.
//
// Three states, per the spec's own framing:
//   untouched — the loop is still counting sightings; comment box empty
//   fix       — carl wants it done; the comment IS the spec, so the box stays
//               open and editable and shows what he already wrote
//   fine      — "stop even watching this". The row dims, sinks to the bottom
//               of the list (see sortWatch) and drops its comment box for a
//               read-back of what he said, because there is nothing left to
//               specify. Clicking either button un-settles it — the verdict is
//               carl's field, and he is allowed to change his mind.
function watchRowHTML(w) {
  const verdict = wlVerdict(w);
  const settled = verdict === "fine";
  const comment = w.comment || "";
  const btn = (kind, label, title) =>
    `<button class="wl-btn ${kind}${verdict === kind ? " on" : ""}" data-id="${esc(w.id)}" data-verdict="${kind}" title="${esc(title)}">${label}</button>`;
  return `<div class="wl${settled ? " done" : ""}" data-wl-id="${esc(w.id)}" data-comment="${esc(comment)}">
    <div class="wl-h"><span class="obs">${inlineMD(w.observation || "")}</span></div>
    <div class="wl-meta">
      ${w.class ? `<span class="cls">${esc(w.class)}</span>` : ""}
      <span class="seen">${esc(wlSeenText(w))}</span>
      ${wlPRHTML(w.pr_url)}
    </div>
    ${w.suggested_fix ? `<div class="wl-fix">→ ${inlineMD(w.suggested_fix)}</div>` : ""}
    <div class="wl-ctrl">
      ${btn("fine", "fine as-is", "stop watching this — the loop never proposes it again")}
      ${btn("fix", "needs fixing", "the next run fixes this — whatever you write below is the spec it follows")}
      ${w.commented_at ? `<span class="wl-stamp">${esc(dreamDay(w.commented_at))}</span>` : ""}
    </div>
    ${settled
      ? (comment ? `<div class="wl-said"><span class="who">you said</span>${inlineMD(comment)}</div>` : "")
      : `<textarea class="wl-comment" data-slug="watchlist" data-id="${esc(w.id)}" rows="2"
          placeholder="how should this be fixed? — whatever you write here becomes the spec">${esc(comment)}</textarea>`}
  </div>`;
}

// sortWatch: everything still open first (untouched and fix alike — both are
// work the loop hasn't finished with), then the rows ruled `fine`, dimmed at
// the bottom. Within each group this reproduces the server's own order
// (ListWatchItems is last_seen DESC, id DESC) rather than inventing a second
// one — numerically, since last_seen is unix millis, and with id as the
// tiebreak so the order is stable across polls.
function sortWatch(list) {
  const rank = (w) => (wlVerdict(w) === "fine" ? 1 : 0);
  return [...list].sort(
    (a, b) => rank(a) - rank(b) || (b.last_seen || 0) - (a.last_seen || 0) || (b.id || 0) - (a.id || 0),
  );
}

function renderDreamWatchlist() {
  const el = document.getElementById("dream-watchlist");
  const ct = document.getElementById("dream-watch-ct");
  const rows = sortWatch(dreamWatch);
  const open = rows.filter((w) => wlVerdict(w) !== "fine").length;
  ct.textContent = rows.length ? `· ${open} open` : "";
  // the comment box is a real composer on a surface that rebuilds on a timer
  // — snapshot/restore is what keeps a long comment from losing characters
  // every 3s (see DQ_INPUT_SELECTOR, which reaches .wl-comment)
  const dqState = snapshotDocInputs();
  el.innerHTML =
    rows.map(watchRowHTML).join("") ||
    `<div class="dream-empty">${esc(dreamWatchNote || "nothing on the watchlist — the loop parks observations here")}</div>`;
  restoreDocInputs(dqState);
}

// refreshDream re-fetches both panes. They're fetched and rendered
// independently: the watchlist endpoint failing must not blank a report carl
// is mid-read, and vice versa. Overlapping calls are dropped rather than
// queued — the next 3s tick is never far away.
async function refreshDream() {
  if (dreamLoading) return;
  dreamLoading = true;
  await Promise.all([
    (async () => {
      try {
        const d = await dreamFetch("/api/dream");
        dreamReports = d.reports || [];
        dreamReportsNote = "";
        // seed the newest night open, once — that's the one you came to read
        if (!dreamSeeded && dreamReports.length) {
          dreamExpanded.add(dreamReports[0].date);
          dreamSeeded = true;
          dreamReportsSnapshot = "";
        }
      } catch (e) {
        dreamReports = [];
        dreamReportsNote = "couldn't load reports: " + e.message;
      }
      const snap = JSON.stringify([dreamReports, [...dreamExpanded], dreamReportsNote]);
      if (snap !== dreamReportsSnapshot) {
        dreamReportsSnapshot = snap;
        renderDreamReports();
      }
    })(),
    (async () => {
      try {
        const d = await dreamFetch("/api/watchlist?state=watching");
        dreamWatch = d.watchlist || [];
        dreamWatchNote = "";
      } catch (e) {
        dreamWatch = [];
        dreamWatchNote = "couldn't load the watchlist: " + e.message;
      }
      const snap = JSON.stringify([dreamWatch, dreamWatchNote]);
      if (snap !== dreamWatchSnapshot) {
        dreamWatchSnapshot = snap;
        renderDreamWatchlist();
      }
    })(),
  ]);
  dreamLoading = false;
}

// postWatchVerdict sends one row's verdict together with whatever is in its
// comment box right now — one click, no separate save. The comment travels
// even on `fine` (carl explaining why he's suppressing something is worth
// keeping), and on a settled row, whose box is gone, it comes off the row's
// data-comment so re-clicking a button can't silently erase what he wrote.
async function postWatchVerdict(row, id, verdict) {
  const ta = row.querySelector(".wl-comment");
  const comment = ta ? ta.value.trim() : row.dataset.comment || "";
  const controls = [...row.querySelectorAll("button, textarea")];
  controls.forEach((el) => (el.disabled = true));
  try {
    const r = await fetch("/api/watchlist", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id: Number(id), verdict, comment }),
    });
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || String(r.status));
    dreamWatchSnapshot = ""; // the row's own markup changed — force the rebuild
    await refreshDream();
  } catch (e) {
    // same surfacing as post()'s failure path, plus a toast: this view has no
    // card behind it to re-render, so the status bar alone is easy to miss
    document.getElementById("tick-stat").textContent = "✕ " + e.message;
    showToast("verdict failed: " + e.message);
    controls.forEach((el) => (el.disabled = false));
  }
}

// one delegated listener on the static #dream container — both panes rebuild
// wholesale on every changed poll, so a per-element listener would leak on
// every tick
document.getElementById("dream").addEventListener("click", (ev) => {
  const repHead = ev.target.closest(".rep-h");
  if (repHead) {
    const date = repHead.dataset.date;
    if (dreamExpanded.has(date)) dreamExpanded.delete(date);
    else dreamExpanded.add(date);
    dreamSeeded = true; // an explicit collapse must not be re-opened by the seed
    renderDreamReports();
    // keep the snapshot in step with what's now on screen, so the next tick
    // doesn't rebuild (and re-scroll) the pane for a change already applied
    dreamReportsSnapshot = JSON.stringify([dreamReports, [...dreamExpanded], dreamReportsNote]);
    return;
  }
  const wlBtn = ev.target.closest(".wl-btn");
  if (wlBtn) {
    const row = wlBtn.closest(".wl");
    if (!row) return;
    // clicking the verdict already in force clears it — back to untouched,
    // which is the only way out of a mis-click on `fine`
    const next = wlBtn.classList.contains("on") ? "" : wlBtn.dataset.verdict;
    postWatchVerdict(row, wlBtn.dataset.id, next);
    return;
  }
});

// ---- poll (paused while dragging so the board doesn't rebuild mid-drag,
// and while the tab is hidden — nothing is watching it either way) ----

async function refresh() {
  if (dragging) return;
  if (document.hidden) return;
  if (view === "log") refreshLog(); // stream new entries while the log is up
  if (view === "dream") refreshDream(); // …and pick up a running dream job's report/rows
  try {
    const r = await fetch("/api/board");
    const board = await r.json();
    lastBoardJobs = board.jobs || [];
    // always: cheap, correctness-critical bookkeeping regardless of whether
    // the jobs payload itself changed since the last rebuild
    const { byCol, archived } = render(board);
    // …including the open artifact's sequence: render() has just rebuilt
    // jobsBySlug, and a live cell writing a new artifact must extend the
    // pager under an open viewer rather than waiting for a reopen
    resyncArtifactSeq();
    // board.daemon.last_tick changes every daemon tick, so compare jobs only
    // — a whole-payload compare would defeat the point of this check
    const snap = JSON.stringify(board.jobs || []);
    if (snap !== boardSnapshot) {
      boardSnapshot = snap;
      renderBoardDOM(byCol, archived);
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
// of the current 3s tick — the board, and any thread overlay open over it
// (its own tick is paused while hidden too; see pollThread)
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
  pollThread();
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

  paintUpdControl(btn, { cls, glyphCls, glyphChar, label, disabled, title });
  paintUpdBanner(banner, bannerCls, bannerHTML, "upd", u.updated_at);
}

// paintUpdControl writes one .upd button node by node, and only where
// something actually changed, instead of the one innerHTML assignment this
// used to be. `.upd.busy .gly` spins on a 1.1s keyframe animation while its
// caller repaints on BOTH the 1s status poll and the 3s board poll — so
// replacing the glyph element (which is what assigning innerHTML does, even
// for identical markup) restarted the animation from 0° about once a second
// and the spinner never once completed a turn during a build. The label is a
// text node for the same reason, and needs no esc(): a text node cannot be
// markup.
//
// Shared by renderUpdate and renderShipIOS. The two controls are deliberately
// one widget with two records behind it: same classes, same glyph/label
// grammar, same repaint discipline — only the state machine that decides what
// to paint differs.
function paintUpdControl(btn, { cls, glyphCls, glyphChar, label, disabled, title }) {
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
}

// ---- banner dismissal ----
//
// The update/ship banners self-clear once their record ages past the freshness
// window (60s / 120s), but that is a long time to stare at a strip you have
// already read, and reloading inside the window legitimately brings it back —
// which reads as "it will not go away".
//
// Dismissal is therefore keyed to the RUN, not to the page, and persisted:
// remembering it in memory alone would be undone by the very reload the update
// button performs on itself. The key is the record's updated_at, so the next
// run — which necessarily has a newer stamp — still gets to speak.
//
// localStorage can throw (blocked cookies, sandboxed context); a dead store
// must degrade to "nothing dismissed", never to a broken banner. Same
// defensive shape as the theme stamp in index.html.
function bannerDismissed(kind, stamp) {
  if (!stamp) return false;
  try {
    return localStorage.getItem("kraken-dismissed-" + kind) === String(stamp);
  } catch {
    return false;
  }
}

function dismissBanner(kind, stamp) {
  if (!stamp) return;
  try {
    localStorage.setItem("kraken-dismissed-" + kind, String(stamp));
  } catch {
    /* a store that cannot remember just means the banner ages out normally */
  }
}

// Every banner gets the same trailing ✕. Appended centrally rather than in
// each branch so a new banner state cannot forget to be dismissable.
const BANNER_DISMISS = '<button data-act="dismiss" title="dismiss">✕</button>';

// paintUpdBanner writes one .update-banner strip. innerHTML rather than nodes:
// unlike the button, a banner repaints only on genuine state changes and
// carries markup (a spacer, an action button), and nothing inside it is
// animated.
function paintUpdBanner(banner, bannerCls, bannerHTML, kind, stamp) {
  // A dismissed run is suppressed here, at the one place every branch funnels
  // through, so no caller has to remember the rule.
  if (bannerHTML && kind && bannerDismissed(kind, stamp)) bannerHTML = "";
  banner.className = "update-banner" + (bannerCls ? " " + bannerCls : "");
  banner.innerHTML = bannerHTML ? bannerHTML + BANNER_DISMISS : "";
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
  // Dismiss remembers THIS run (updState?.updated_at), so the banner stays
  // gone across the reload the update button performs on itself, while the
  // next run still gets to speak.
  if (act === "dismiss") {
    dismissBanner("upd", updState?.updated_at);
    renderUpdate();
  }
});

// ---- ship-ios: the ⇧ SHIP iOS control (internal/api/shipios.go) ----
//
// Structurally the update button's twin — same .upd classes, the same
// paintUpdControl/paintUpdBanner painters, the same "one server record plus
// the one refusal no record can carry" shape — with two deliberate
// differences that are the reason this is a separate state machine rather
// than a second caller of renderUpdate():
//
//  1. NO restart-gap machinery of its OWN. A ship replaces nothing on this
//     Mac — serve, the daemon and the binary all stay up from preflight
//     through upload — so a failed status fetch here is a genuine error and
//     is reported as one, after a single retry to ride out a dropped request.
//     The one exception is not the ship's doing: a concurrent UPDATE ends in
//     `launchctl kickstart -k`, which takes serve down for a couple of
//     seconds, and the shipper is given its own session (api/shipios.go's
//     Setsid) precisely so it SURVIVES that. Painting "✕ ship failed" over the
//     one outage the run is engineered to live through would be exactly
//     backwards, so the ceiling stretches while an update is in flight — see
//     shipFailCeiling.
//  2. NO reload when it finishes. Nothing this page is running was replaced.
//
// And the wording, which is the part worth getting right: `done` means the
// .ipa reached App Store Connect. TestFlight still has to process it, which
// takes minutes, and someone still has to install it from the TestFlight app
// afterwards. So the control says UPLOADED, never SHIPPED, and the banner
// spells out what is left to do — a green ✓ that read as "the phone has it"
// would simply be a lie.

// jobs.ShipTerminal: no run is in flight, so the button is live again.
const SHIP_TERMINAL = new Set(["idle", "done", "failed"]);
// How long a finished run keeps the button. Twice UPD_RECENT_MS: the update
// button's window is sized for a page that reloads itself the instant the run
// ends, whereas nothing here interrupts whoever is reading — and the thing
// they most need to read is the build number and the "not installed yet" note.
const SHIP_RECENT_MS = 120000;
const SHIP_POLL_MS = 1000;
// One dropped request is a hiccup; two in a row means the server really has
// stopped answering. Deliberately NOT the update button's 120s gap clock —
// there is no expected outage to ride out here.
const SHIP_FAIL_MAX = 2;
// …unless an update is in flight, whose `launchctl kickstart -k` is a real,
// expected, seconds-long outage that the ship is built to survive. 30 ticks is
// ~30s: ten times the couple of seconds a kickstart takes, and still short
// enough that a serve which never comes back is reported while anyone is still
// watching. The update poller's own 120s gap clock owns the "did serve come
// back at all" question; this number only has to avoid lying in the meantime.
const SHIP_FAIL_MAX_UPDATING = 30;

let shipState = null; // last webShipIOS seen, from either poll
let shipPolling = false; // a run is live and the 1s poll is running
let shipPollTimer = null;
let shipFailStreak = 0; // consecutive failed status fetches
let shipRefusal = ""; // last refusal text
let shipRefusalAt = 0; // when it was set — a refusal has no record to age out with
// The POST is in flight but hasn't answered. Set synchronously in the click
// handler's callee so a board poll landing mid-POST can't repaint the button
// enabled underneath it — which it did, because paintUpdControl writes
// btn.disabled on EVERY repaint, so the one assignment requestShipIOS made was
// undone 3s later at the latest and a second click earned a guaranteed 409.
let shipStarting = false;
// what a click means right now, decided by renderShipIOS — a fresh failure
// turns the button into the door to the log that explains it
let shipBtnAction = "ship";

// shipFailCeiling is how many consecutive failed status fetches mean "the
// server is really gone". It reads the UPDATE record because that is the only
// thing on this Mac that legitimately takes serve down mid-ship; see the
// section comment above.
function shipFailCeiling() {
  const updating = updPolling || (updState && !UPD_TERMINAL.has(updState.phase || "idle"));
  return updating ? SHIP_FAIL_MAX_UPDATING : SHIP_FAIL_MAX;
}

// shipIngest takes a fresh record from either poll, and is also where a stale
// refusal dies. Two ways it can outlive its cause, both of which used to leave
// SHIP BLOCKED painted over the truth:
//
//   - a 409 refused this click because a run started elsewhere was already in
//     flight. That run IS the answer to "what is happening"; the refusal was
//     only ever a toast.
//   - that same run then finishes, and its "uploaded build N" banner is the
//     one thing worth reading. A refusal older than the record it complained
//     about has nothing left to say.
function shipIngest(s) {
  if (!s) return;
  const running = !SHIP_TERMINAL.has(s.phase || "idle");
  if (shipRefusal && (running || (s.updated_at && s.updated_at > shipRefusalAt))) {
    shipRefusal = "";
    shipRefusalAt = 0;
  }
  shipState = s;
}

// renderShipFromBoard folds the 3s board poll's copy of the record in. The 1s
// status poll wins outright while it is running: the two race, and a board
// response can easily be older than a status response that landed just before
// it — applying it would rewind the phase on screen.
function renderShipFromBoard(s) {
  if (!shipPolling && s) {
    if (!SHIP_TERMINAL.has(s.phase)) {
      // a run someone else started — the CLI, another tab, the phone. Follow
      // it just as closely as one started from here. This is also how the
      // poll recovers on its own after a lost-contact failure: the board poll
      // is the first thing to come back, and it restarts the fast one.
      startShipPoll();
    }
    shipIngest(s); // clears a refusal this record has outlived
  }
  renderShipIOS();
}

function startShipPoll() {
  if (shipPolling) return;
  shipPolling = true;
  shipFailStreak = 0;
  shipPollTimer = setTimeout(shipPollTick, SHIP_POLL_MS);
}

function stopShipPoll() {
  shipPolling = false;
  clearTimeout(shipPollTimer);
  shipPollTimer = null;
  shipFailStreak = 0;
}

async function shipPollTick() {
  shipPollTimer = null;
  let s = null;
  try {
    const r = await fetch("/api/ship-ios/status", { cache: "no-store" });
    if (r.ok) s = await r.json();
  } catch {} // counted below, not swallowed — see SHIP_FAIL_MAX
  if (s) {
    shipIngest(s);
    shipFailStreak = 0;
  } else {
    shipFailStreak++;
  }
  if (shipFailStreak >= shipFailCeiling()) {
    // Past the ceiling — which is generous while an update is restarting serve
    // (see shipFailCeiling) and tight otherwise, because nothing else on this
    // Mac takes the server down mid-ship. Say what is and isn't known: the
    // shipper is a detached child of a process that may well have died under
    // it, so the upload itself could still be running — the log is the only
    // witness.
    shipState = {
      ...(shipState || {}),
      phase: "failed",
      error: "kraken serve stopped answering mid-ship — the run itself may still be going; see ~/.kraken/ship-ios.log",
      updated_at: Date.now(),
    };
    stopShipPoll();
  } else if (s && SHIP_TERMINAL.has(s.phase)) {
    stopShipPoll();
  }
  renderShipIOS();
  if (shipPolling) shipPollTimer = setTimeout(shipPollTick, SHIP_POLL_MS);
}

// renderShipIOS paints button + banner from shipState. Pure repaint: the only
// state it reads that isn't in the record is the refusal, and it changes
// nothing.
function renderShipIOS() {
  const btn = document.getElementById("ship-btn");
  const banner = document.getElementById("ship-banner");
  if (!btn || !banner) return;

  const s = shipState || {};
  const phase = s.phase || "idle";
  const running = !SHIP_TERMINAL.has(phase);
  // A terminal record only speaks for the button while it's fresh, bounded on
  // BOTH sides — an `updated_at` in the future (clock skew, a corrupt
  // ship-ios.json) is otherwise fresh forever, and a `failed` record that is
  // fresh forever pins the button at SHIP FAILED, whose click opens the log
  // instead of starting a run, so the button never ships again.
  const fresh = !!s.updated_at && Math.abs(Date.now() - s.updated_at) < SHIP_RECENT_MS;
  const build = s.build ? " b" + s.build : "";

  let cls = "";
  let glyphCls = "gly";
  let glyphChar = "⇧";
  let label = "SHIP iOS";
  let disabled = false;
  // The default title carries the honest version of what the button does, so
  // the distinction is on screen before anyone clicks rather than only after.
  let title =
    "build the iOS app and upload it to App Store Connect — TestFlight then " +
    "takes a few minutes to process it, and the new build still has to be " +
    "installed from the TestFlight app on the phone";
  let bannerCls = "";
  let bannerHTML = "";
  shipBtnAction = "ship";

  if (shipStarting && !running) {
    // the click landed, the POST hasn't answered, and the record still reads
    // terminal — without this the next repaint (a board poll 3s later at the
    // latest) hands the button back and a second click earns a 409
    cls = "busy";
    label = "STARTING…";
    disabled = true;
  } else if (shipRefusal && Date.now() - shipRefusalAt < SHIP_RECENT_MS) {
    // refused before anything was spawned (409 from the POST, already
    // toasted): nothing ran, so the record behind it is untouched and the
    // button stays clickable for the retry once the reason is fixed. Bounded
    // by the same window a finished run gets: a refusal has no record to age
    // out with, and left unbounded it outranks every branch below forever.
    cls = "bad";
    glyphChar = "✕";
    label = "SHIP BLOCKED";
    title = shipRefusal;
  } else if (running) {
    const secs = s.started_at ? Math.max(0, Math.round((Date.now() - s.started_at) / 1000)) : 0;
    cls = "busy";
    label = `${phase.toUpperCase()}… ${secs}s`;
    disabled = true;
    title = "ship in flight — archiving and uploading takes several minutes" + build;
  } else if (phase === "failed" && fresh) {
    // a failure after the shipper detached can't come back as an HTTP status
    // — the request returned long ago — so it arrives here, in the record
    cls = "bad";
    glyphChar = "✕";
    label = "SHIP FAILED";
    title = s.error || "ship failed";
    shipBtnAction = "log";
    bannerCls = "bad";
    bannerHTML =
      `<span>✕ ship${build ? " of build " + esc(String(s.build)) : ""} failed — ${esc(s.error || "no error recorded")}</span>` +
      '<span class="spacer"></span><button data-act="ship-log">OPEN LOG</button>';
  } else if (phase === "done" && fresh) {
    const secs =
      s.started_at && s.updated_at ? Math.max(0, Math.round((s.updated_at - s.started_at) / 1000)) : 0;
    cls = "ok";
    glyphChar = "✓";
    // UPLOADED, not SHIPPED: the .ipa is at Apple, the phone is unchanged.
    // `build` is either "" or already leading-spaced, so there is nothing to
    // trim (update's version needs it because its space is unconditional).
    label = "UPLOADED" + build;
    title = "uploaded to App Store Connect — not yet installed anywhere";
    bannerHTML =
      `<span>✓ build ${esc(String(s.build || "?"))} uploaded to App Store Connect` +
      (secs ? ` in ${secs}s` : "") +
      " · TestFlight still has to process it (usually a few minutes), then install " +
      "the new build from the TestFlight app on the phone — nothing on this Mac changed" +
      "</span>";
  }

  paintUpdControl(btn, { cls, glyphCls, glyphChar, label, disabled, title });
  paintUpdBanner(banner, bannerCls, bannerHTML, "ship", s.updated_at);
}

// requestShipIOS fires the run. No confirmation step, matching the update
// button: the click IS the decision. Deliberately not post() — a refusal here
// is one specific paragraph about the machine ("App Store Connect credentials
// missing: ASC_KEY_ID not set…"), and post()'s ✕ in the clock corner truncates
// exactly the part worth reading.
async function requestShipIOS() {
  shipRefusal = "";
  shipRefusalAt = 0;
  // A flag, not `btn.disabled = true`: paintUpdControl writes btn.disabled on
  // every repaint, so a direct assignment survives only until the next board
  // poll — which, mid-POST, is exactly when it must not.
  shipStarting = true;
  renderShipIOS();
  try {
    const r = await fetch("/api/ship-ios", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    });
    const data = await r.json();
    if (!r.ok) throw new Error(data.error || String(r.status));
    // the handler has already written the claim (phase preflight) before
    // spawning, so paint it now rather than waiting out the first poll tick
    shipIngest({
      ...(shipState || {}),
      phase: data.phase || "preflight",
      build: 0,
      error: "",
      started_at: Date.now(),
      updated_at: Date.now(),
    });
    startShipPoll();
  } catch (e) {
    shipRefusal = e.message || "ship refused";
    shipRefusalAt = Date.now();
    showToast("✕ " + shipRefusal);
  }
  shipStarting = false;
  renderShipIOS();
}

document.getElementById("ship-btn").addEventListener("click", () => {
  // a fresh failure's button is the way into the log that explains it (see
  // shipBtnAction); the failure ages out after SHIP_RECENT_MS and the button
  // is a plain SHIP iOS again from there
  if (shipBtnAction === "log") setView("log");
  else requestShipIOS();
});

// banner actions, delegated: renderShipIOS replaces the banner's innards on
// every repaint, so nothing inside it can own a listener. No RELOAD NOW twin
// here — a ship replaces nothing this page is running.
document.getElementById("ship-banner").addEventListener("click", (ev) => {
  const act = ev.target.closest("button")?.dataset.act;
  if (act === "ship-log") setView("log");
  if (act === "dismiss") {
    dismissBanner("ship", shipState?.updated_at);
    renderShipIOS();
  }
});
