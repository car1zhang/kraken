import type { WebJob } from "../../../api/types.ts";

/**
 * The chip's own rule, split out so the card can ask whether the chip row is
 * empty without duplicating it — the same split StageChip and AccountChip
 * make. One condition: the job runs on codex. Absence (the claude default) is
 * the wire's only spelling, and a claude cell shows NOTHING here rather than a
 * "claude" chip — claude is what every cell ran on before codex support, so a
 * badge for it would put a token saying "nothing unusual" on almost every
 * card, the same argument AccountChip makes for not badging the main account.
 *
 * Unlike AccountChip this is NOT gated on live state: the agent is a durable
 * choice made in the job's `agent:` frontmatter (like `color:`), true of a
 * draft codex job before its cell exists and of a done one after, so it reads
 * off `job.agent` directly with no state test.
 */
export function codexChipVisible(job: WebJob): boolean {
  return job.agent === "codex";
}

/**
 * The card's agent badge: `◇ codex` when the cell's zeus runs on codex,
 * nothing otherwise. A fact ABOUT the job rather than something it contains
 * (like the stage and goal chips), so the card slots it with the identity-ish
 * chips, not the inventory counts.
 *
 * Class rules inherited from the sibling chips, both load-bearing:
 *   1. `cx-chip`, and NOT `chip` — `.chip` is the artifact-overlay click
 *      target, so a chip-classed span would try to open an artifact with an
 *      undefined url instead of falling through to the card's own click
 *      handler. A plain span for the same reason: the badge is a word to read,
 *      not a new click target.
 *   2. its colour is the theme `--accent` token via style.css's `.cx-chip`, so
 *      both palettes follow with no hex here.
 * The glyph is aria-hidden — a screen reader gets the word `codex` through the
 * text, and `◇` has no useful spoken form.
 */
export function CodexChip({ job }: { job: WebJob }) {
  if (!codexChipVisible(job)) return null;
  return (
    <span className="cx-chip" title="agent: this cell's zeus runs on codex">
      <span aria-hidden="true">◇ </span>
      codex
    </span>
  );
}
