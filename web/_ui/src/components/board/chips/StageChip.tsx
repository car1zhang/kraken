import type { WebJob } from "../../../api/types.ts";

/**
 * The chip's own rule, split out so the card can ask whether the chip row is
 * empty without duplicating it — the same split DocQuestionChip and
 * FeedbackChip make. One condition: the server sent a non-empty stage. The
 * server already scopes the key to started non-done jobs with a resolvable
 * transcript (see webJob.Stage, which also documents what absence covers), so
 * there is no settledJob test here — a done or archived card never carries
 * the key.
 */
export function stageChipVisible(job: WebJob): boolean {
  return !!job.stage;
}

/**
 * One glyph per pipeline skill, paired with the skill's own palette colour
 * (`.st-chip-<stage>` in style.css aliases `--skill-<stage>`, the same skill
 * palette the steer modal's cmd-chips wear) so a
 * column of stage badges sorts by eye — the argument LoadChip's colour bands
 * make for the blocked column. The iOS StageChip carries the SF Symbol
 * sibling of each glyph; the two lists move together. `⑂` is the board's
 * existing branch glyph, reused for /pr because a PR is the branch leaving
 * the cell — the thread count chip beside it wears the same glyph, and the
 * skill-pr green is what separates them. `⚙` carries
 * VARIATION SELECTOR-15: U+2699 is the one emoji-capable glyph of the five,
 * and only Menlo's cmap currently keeps it in text presentation — the
 * selector pins that so a `--mono` stack edit cannot flip the badge to a
 * colour emoji that ignores its skill hue.
 *
 * A stage with no entry here — a pipeline skill newer than this build —
 * renders the bare name in the base accent look rather than nothing: the
 * name is the payload, the glyph and colour are decoration. The lookup is
 * `Object.hasOwn`-guarded because the map is a plain object literal: a bare
 * index would answer truthily for inherited keys (`"constructor"`,
 * `"toString"`), putting a function where the glyph goes — React drops it
 * with a warning, leaving a stray leading space and an inert
 * `st-chip-constructor` class.
 */
const STAGE_GLYPHS: Record<string, string> = {
  info: "ⓘ",
  aim: "⌖",
  do: "⚙︎",
  pr: "⑂",
  dream: "☾",
};

/**
 * The card's stage badge: which pipeline skill zeus most recently used
 * (`info` / `aim` / `do` / `pr` / `dream`), rendered as the
 * skill's glyph plus its bare name in the skill's own colour — the name IS
 * the phase vocabulary carl steers with (`/aim`, `/do`), so it stays even
 * with the glyph carrying the colour code. The glyph is aria-hidden so a
 * screen reader gets the stage word rather than a glyph with no spoken form
 * — the same call the iOS chip makes when it ignores its icon and labels
 * itself `stage: <name>`.
 *
 * Two rules inherited from the sibling chips, both load-bearing:
 *
 *   1. `st-chip`, and NOT `chip` — `.chip` is the artifact-overlay click
 *      target, so a chip-classed span would try to open an artifact with an
 *      undefined url instead of falling through to the card's own click
 *      handler. A plain span for the same reason: the badge is a word to
 *      read, not a new click target (style.css's pointer cursor on it is the
 *      CARD's affordance showing through, LoadChip's rule).
 *   2. it is a fact ABOUT the job rather than something the job contains —
 *      like the goal chip — so the card slots it with the identity-ish chips
 *      (after the thread tokens, before the goal), not among the inventory
 *      counts.
 */
export function StageChip({ job }: { job: WebJob }) {
  if (!job.stage) return null;
  const glyph = Object.hasOwn(STAGE_GLYPHS, job.stage) ? STAGE_GLYPHS[job.stage] : undefined;
  return (
    <span
      className={"st-chip" + (glyph ? " st-chip-" + job.stage : "")}
      title={"stage: last pipeline skill used — /" + job.stage}
    >
      {glyph ? (
        <>
          <span aria-hidden="true">{glyph} </span>
          {job.stage}
        </>
      ) : (
        job.stage
      )}
    </span>
  );
}
