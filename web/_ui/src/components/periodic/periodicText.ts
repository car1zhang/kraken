// The PERIODIC view's two derived labels, kept pure and apart from the
// component so the wording has one home.

import type { WebPeriodic } from "../../api/types.ts";

const pad2 = (n: number) => String(n).padStart(2, "0");

/** A Date's local calendar date as YYYY-MM-DD. */
function localDate(d: Date): string {
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`;
}

/**
 * What the row's last scheduled firing did: `never`, or its date followed by
 * the job it created and, when something went wrong, why. A row's `run now`
 * clicks do not move these fields, so this only ever describes the daemon's
 * own firings.
 */
export function lastFiringLabel(p: WebPeriodic): string {
  if (!p.last_date) return "never";
  const parts = [p.last_date];
  if (p.last_slug) parts.push(p.last_slug);
  if (p.last_note) parts.push(p.last_note);
  return parts.join(" · ");
}

/**
 * When the row fires next, in the browser's local time: `paused`, `none`,
 * `due now` (the daemon's next tick will fire it), `today HH:MM`, or
 * `YYYY-MM-DD HH:MM` for a later day.
 */
export function nextDueLabel(p: WebPeriodic, nowMs: number): string {
  if (!p.enabled) return "paused";
  if (!p.next_due) return "none";
  if (p.next_due <= nowMs) return "due now";
  const due = new Date(p.next_due);
  const hhmm = `${pad2(due.getHours())}:${pad2(due.getMinutes())}`;
  const day = localDate(due);
  return day === localDate(new Date(nowMs)) ? `today ${hhmm}` : `${day} ${hhmm}`;
}
