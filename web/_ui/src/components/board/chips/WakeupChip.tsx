import type { WebJob } from "../../../api/types.ts";
import { wakeupLabel } from "../../../lib/format.ts";

/**
 * The chip's SHORT countdown — the card text, at minute precision: `47m`, `4m`,
 * `12s` under a minute, `due` once it is past.
 *
 * Derived here rather than in lib/format.ts beside wakeupLabel because the long
 * form is still what the tooltip needs, and the two are one reading of one
 * stamp: `wakeupLabel` already owns the past/future decision (it answers "due"),
 * so this only has to shorten what it returns. Parsing its `Xm Ys` back apart
 * would make the short form depend on the long one's punctuation; recomputing
 * the remainder from `firesAt` cannot drift that way.
 *
 * Minutes are the coarsest unit deliberately: a wakeup is a poll timer measured
 * in minutes, so an hours unit would round every value on the board to `1h`.
 */
function wakeupShort(firesAt: number): string {
  const remainingS = Math.floor((firesAt - Date.now()) / 1000);
  if (remainingS <= 0) return "due";
  if (remainingS < 60) return `${remainingS}s`;
  return `${Math.floor(remainingS / 60)}m`;
}

/**
 * A live cell's poll-timer chip: a countdown to its next ScheduleWakeup tick.
 * Port of wakeupChipHTML (app.js:369).
 *
 * The text is the glyph and the countdown, nothing else — `⏱ 4m`. "next check
 * in" is what the ⏱ already says, and it is the words rather than the number
 * that made this the widest thing on a working card. The long form survives
 * whole in `title` ("next check in 4m 12s"), seconds included, which is where a
 * countdown you actually want to read to the second belongs.
 *
 * The vanilla chip carried `data-wakeup-fires` so updateAges() could re-tick
 * the text in place on polls that skipped the (expensive) full board rebuild.
 * React re-renders the card on every board tick, so the countdown recomputes
 * for free — the attribute is kept anyway because it is a cheap, stable hook
 * for e2e assertions on a value that is otherwise a moving target.
 */
export function WakeupChip({ job }: { job: WebJob }) {
  if (!job.wakeup || job.state !== "working") return null;
  return (
    <div className="wakeup-row">
      <span
        className="chip-wakeup"
        data-wakeup-fires={+job.wakeup.fires_at}
        title={`next check in ${wakeupLabel(job.wakeup.fires_at)}`}
      >
        ⏱ {wakeupShort(job.wakeup.fires_at)}
      </span>
    </div>
  );
}
