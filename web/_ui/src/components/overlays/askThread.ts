import type { WebDocQuestion } from "../../api/types.ts";

// askThread.ts — the pure title/seed builders behind every "branch a side
// thread to ask about THIS" affordance. The artifact viewer's ask composer
// (ArtifactOverlay) and the questions panel's per-row ask (DocQuestions) both
// post the SAME `/api/threads` branch op ThreadOverlay's own dialog posts;
// what differs per surface is only how the thread's title and seed prompt are
// derived from the thing being looked at, and that derivation is what lives
// here — in one module, because the two surfaces share the title budget, the
// 80-unit cap, and the surrogate-safe cut, and a second copy of any of those
// is how they drift.

/**
 * A cell-home artifact name may be a subpath — the title wants its tail. An
 * empty tail (a name that ends in the separator) falls back to the whole name
 * rather than to nothing, which would title the thread with a bare em-dash.
 */
const baseName = (name: string) =>
  name.slice(name.lastIndexOf("/") + 1) || name;

/**
 * Cut to at most `n` UTF-16 units WITHOUT splitting a surrogate pair.
 *
 * `slice` counts code units, so a cut landing between the halves of an astral
 * character (emoji, and everything above the BMP) leaves a lone high surrogate.
 * `JSON.stringify` will happily escape it, Go's `encoding/json` decodes an
 * unpaired escape to U+FFFD, and the thread's stored title then carries a
 * replacement glyph nothing can repair but a rename.
 */
function cutUnits(s: string, n: number): string {
  const cut = s.slice(0, n);
  return /[\uD800-\uDBFF]$/.test(cut) ? cut.slice(0, -1) : cut;
}

/** The `maxLength` ThreadOverlay's rename input enforces, in UTF-16 units. */
const TITLE_MAX = 80;
/** How much of that a long artifact name may take before it is itself cut. */
const TITLE_BASE_MAX = 40;

/** Cap a "<base> — <first line>" title at TITLE_MAX, surrogate-safely. */
function capTitle(title: string): string {
  if (title.length <= TITLE_MAX) return title;
  return cutUnits(title, TITLE_MAX - 1).trimEnd() + "…";
}

/**
 * The thread name a question about an artifact branches under.
 *
 * A thread's name is what its chip says wherever threads are listed, so "which
 * artifact" alone is not enough: ask two things about `00-overview.md` and you
 * would get two chips nobody can tell apart. The first line of the question is
 * what separates them.
 *
 * Which is why the artifact half is budgeted rather than simply written first.
 * A single cut at the end would let a long section-doc name eat the whole 80
 * and truncate the question away entirely — every chip identical again, the
 * exact failure this function exists to prevent.
 *
 * Capped at 80 to match the rename input's own `maxLength` — a title longer
 * than that could be branched but never edited back to in place.
 */
export function askThreadTitle(artifact: string, question: string): string {
  let base = baseName(artifact);
  if (base.length > TITLE_BASE_MAX) base = cutUnits(base, TITLE_BASE_MAX - 1) + "…";
  const first = (question.trim().split("\n")[0] ?? "").trim();
  return capTitle(first ? `${base} — ${first}` : base);
}

/**
 * The seed prompt the branched thread comes up with.
 *
 * It names the artifact rather than its URL or its on-disk path: the thread is
 * a fork of zeus in the cell's own worktree, so the artifact set is already the
 * thing it knows by name, and a path derived from an /api/artifact URL would be
 * a second, weaker way of saying so.
 *
 * The last line restates the thread contract (ThreadOverlay's own banner:
 * "Threads are for asking, not for doing") — the artifact viewer's composer
 * sits on a doc, and without it "elaborate on this section" reads as a
 * licence to go edit it.
 */
export function askThreadSeed(artifact: string, question: string): string {
  return (
    "I'm reading the artifact `" +
    artifact +
    "` from this job's artifact set. A question about it:\n\n" +
    question.trim() +
    "\n\nAnswer here in the thread — this is a question about the work, " +
    "not a request to change it. Don't edit the artifact."
  );
}

/**
 * The thread name an ask about an open question branches under.
 *
 * Same budget question as askThreadTitle, answered the same way: the qid alone
 * ("Q3") separates nothing once two asks exist on one job, so the first line of
 * what carl typed — or of the question itself when he typed nothing — is what
 * tells the chips apart. A qid is short, so unlike an artifact name it needs no
 * budget of its own.
 */
export function questionThreadTitle(
  qid: string,
  question: string,
  ask: string,
): string {
  const first = ((ask.trim() || question).trim().split("\n")[0] ?? "").trim();
  return capTitle(first ? `${qid} — ${first}` : qid);
}

/**
 * The seed prompt a question-ask thread comes up with.
 *
 * The row is quoted VERBATIM — text, context, recommendation — rather than
 * referenced by qid alone: the thread is a fork of zeus, whose transcript may
 * predate the question's latest `questions update`, so the seed carries the
 * row as the board shows it and the fork answers about that, not about
 * whatever wording it remembers writing.
 *
 * The empty ask has a default for the same reason this affordance exists:
 * "I don't understand this question" IS the common case, and making carl
 * phrase it before he can branch would be the re-typing the artifact composer
 * was built to remove.
 *
 * The last two lines restate the thread contract, with the question-specific
 * half spelled out: a fork that "helpfully" writes the answer into the
 * questions record would be answering on carl's behalf — `answer` is carl's
 * column, written from the board, never by an agent.
 */
export function questionThreadSeed(q: WebDocQuestion, ask: string): string {
  let row = `${q.id} (${q.importance || "P2"}): ${q.text.trim()}`;
  if (q.context?.trim()) row += `\ncontext: ${q.context.trim()}`;
  if (q.recommendation?.trim())
    row += `\nrecommendation: ${q.recommendation.trim()}`;
  const want =
    ask.trim() ||
    "Help me understand this question: what is it really asking, what context do I need to answer it, and what hinges on the answer?";
  return (
    "I'm looking at an open question this job raised on the board. The row as filed:\n\n" +
    row +
    "\n\n" +
    want +
    "\n\nAnswer here in the thread — this is a question about the question, " +
    "not an answer to it. Don't write into the questions record or change " +
    "any files; I'll answer on the board myself once it's clear."
  );
}
