// The KRONOS panel's pure half: everything about the context meter that can be
// decided without a DOM.
//
// The meter is fed by the context sentinel's last statusline report, which
// reaches the board as `/api/kronos`'s `context` POINTER — absent when kronos
// has never reported one. That absence is the whole reason this module exists
// as its own function rather than as a `${pct.toFixed(1)}%` in the markup: a
// missing report and a report of zero are different facts, and rendering the
// first as "0.0%" would claim a measurement nobody took. Null in, "—" out.

/**
 * The three warning thresholds, in the order the meter draws its ticks. They
 * mirror the daemon-side ctxPass thresholds (50/75/90) — the same crossings
 * that fire carl a macOS notification — so the bar a reader is looking at is
 * marked at the points that actually fire something.
 */
export const KRONOS_THRESHOLDS = [50, 75, 90] as const;

/** ok below 50, then one band per threshold crossed. */
export type KronosBand = "ok" | "warn50" | "warn75" | "warn90";

export interface KronosMeter {
  /** The reported percentage, or null when there is no report. */
  pct: number | null;
  band: KronosBand;
  /** Display string: one decimal plus a percent sign, or "—" for no report. */
  label: string;
}

/**
 * Classify a context-usage percentage for the meter.
 *
 * `undefined` is accepted alongside `null` deliberately: the wire's `context`
 * key is OMITTED when kronos has never reported, so the natural read at the
 * call site (`status?.context?.pct`) produces undefined rather than null, and a
 * function that took only null would push a `?? null` onto every caller. Both
 * mean the same thing here — no report — and so does a non-finite number, the
 * defensive read the rest of this codebase gives foreign fields.
 *
 * The value is NOT clamped: a percentage outside 0-100 is a fact about what was
 * reported, and the meter's own fill is what clamps (kronosFillPct). Bands are
 * inclusive at each threshold — exactly 50 is already warn50 — because the
 * daemon fires its warning ON the crossing.
 */
export function kronosMeterState(pct: number | null | undefined): KronosMeter {
  if (pct == null || !Number.isFinite(pct)) {
    return { pct: null, band: "ok", label: "—" };
  }
  const band: KronosBand =
    pct >= 90 ? "warn90" : pct >= 75 ? "warn75" : pct >= 50 ? "warn50" : "ok";
  return { pct, band, label: `${pct.toFixed(1)}%` };
}

/**
 * The bar's fill width in percent, clamped into 0-100 so a nonsense report
 * cannot draw outside the track. No report fills nothing.
 */
export function kronosFillPct(pct: number | null | undefined): number {
  if (pct == null || !Number.isFinite(pct)) return 0;
  return Math.max(0, Math.min(100, pct));
}
