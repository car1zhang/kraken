import type { WebJob } from "../../api/types.ts";
import { age, contextColorClass, fmtTokens } from "../../lib/format.ts";

/**
 * The cell lead's context-window fill, rendered into the steer modal's
 * context block beside TokensPanel (`#steer-ctxwin`, inside `.steer-meters`).
 *
 * Reads `job.context` (WebJobContext) and renders nothing when it is absent:
 * absent means UNREPORTED, and a 0% here would claim an empty window a
 * billed session can never have. The number is zeus' alone and is as fresh
 * as its last statusline RENDER, and both facts are stated in the panel
 * rather than left to be inferred — a fill that read as "the cell" or as
 * "now" would be the two ways this panel could lie.
 *
 * Layout follows TokensPanel tier for tier: a headline (`25%` + the unit
 * `context`), one meter row in the usage drilldown's own `.u-row` classes
 * with a tick at the daemon's handoff line, a breakdown row (reported age,
 * and the auto-handoff streak when it has fired), and a provenance note.
 *
 * Two derived values, both marked as such. `used` 0 means the reporting hook
 * predates the exact-count field, so the count is `pct × window / 100` and
 * prefixed `~`; `window` 0 means no size was sent, so no `of` clause at all.
 * Colour comes from contextColorClass against the served `handoff_pct`, so
 * the panel never hardcodes the threshold the daemon acts on.
 */
export function ContextPanel({ job }: { job: WebJob | undefined }) {
  const c = job?.context;
  if (!c) return null;

  const pct = Math.max(0, Math.min(100, c.pct || 0));
  const handoffPct = c.handoff_pct || 0;
  const cls = contextColorClass(pct, handoffPct);
  const headline = `${Math.round(pct)}%`;

  // The count beside the bar. Exact when the hook sent one; derived (and
  // marked) when it did not; absent when there is no window to divide by.
  let count = "";
  if (c.window > 0) {
    const exact = c.used > 0;
    const used = exact ? c.used : Math.round((pct / 100) * c.window);
    count = `${exact ? "" : "~"}${fmtTokens(used)} / ${fmtTokens(c.window)}`;
  } else {
    count = headline;
  }

  // `at` is unix MILLIS and age() takes SECONDS — the same divide TokensPanel
  // and LoadDrill each spell out. A zero stamp drops the clause rather than
  // rendering "0s ago".
  const when = c.at ? age(Math.floor(c.at / 1000)) : "";
  const live = !!job?.live;
  const handoff = c.handoff && c.handoff.fires > 0 ? c.handoff : null;
  const handoffWhen = handoff && handoff.last_at ? age(Math.floor(handoff.last_at / 1000)) : "";

  // Provenance wording turns on liveness, not on display state: a done or
  // cell-gone job's row is the last render before the cell went down, which
  // is also roughly where a reactivation resumes the same session.
  const note = live
    ? "zeus' statusline, last render · teammates and threads not counted"
    : "last statusline render before the cell went down · a reactivation resumes this session near this fill";

  const title =
    "how full the cell lead's context window is, as Claude Code's own statusline last reported it " +
    "(input + cache tokens over the model's real window). zeus only — teammates and side threads run their own windows. " +
    (handoffPct ? `kraken types /handoff into the pane at ${handoffPct}%.` : "");

  const band = cls === "u-danger" ? "danger" : cls === "u-warn" ? "warn" : "ok";

  return (
    <div className={`cw cw-${band}${live ? "" : " cw-dead"}`} title={title} data-testid="context-panel">
      <div className="cw-head">
        <span className="cw-big">{headline}</span>
        <span className="cw-unit">context</span>
      </div>
      <div className="u-rows">
        <div className="u-row">
          <span className="u-row-label">window</span>
          <span className="u-bar cw-bar">
            <span className={`u-bar-fill ${cls}`} style={{ width: `${pct}%` }}></span>
            {handoffPct > 0 && handoffPct <= 100 ? (
              <span
                className="u-bar-tick"
                data-testid="handoff-tick"
                style={{ left: `${handoffPct}%` }}
                title={`/handoff at ${handoffPct}%`}
              ></span>
            ) : null}
          </span>
          <span className="u-row-val">{count}</span>
        </div>
      </div>
      <div className="cw-break">
        {when ? (
          <span className="cw-cell">
            <i>{live ? "reported" : "last report"}</i>
            {when} ago
          </span>
        ) : null}
        {handoff ? (
          <span className="cw-cell" data-testid="handoff-row">
            <i>auto-handoff</i>
            typed {handoff.fires}× in the current streak
            {handoffWhen ? ` · ${handoffWhen} ago` : ""}
            {handoff.exhausted ? " · gave up" : ""}
          </span>
        ) : null}
      </div>
      <div className="cw-note">{note}</div>
    </div>
  );
}
