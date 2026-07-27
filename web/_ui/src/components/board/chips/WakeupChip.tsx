import type { WebJob } from "../../../api/types.ts";
import { wakeupLabel } from "../../../lib/format.ts";

/**
 * A live cell's poll-timer chip: a countdown to its next ScheduleWakeup tick.
 * Port of wakeupChipHTML (app.js:369).
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
      <span className="chip-wakeup" data-wakeup-fires={+job.wakeup.fires_at}>
        ⏱ next check in {wakeupLabel(job.wakeup.fires_at)}
      </span>
    </div>
  );
}
