import type { WebJob } from "../../api/types.ts";
import { age, fmtTokens } from "../../lib/format.ts";

/**
 * Per-job token spend. Port of `tokensHTML` (app.js:565), rendered into the
 * steer modal's context block (`#steer-tokens`).
 *
 * Two states, and they are NOT the same thing:
 *   - `tokens` ABSENT — never measured (no transcripts yet, or the daemon
 *     hasn't reached this job). Renders nothing at all. A zero here would read
 *     as "this job was free", which is a different and false claim. The api
 *     emits the key only when a store row exists, so presence is the whole
 *     test.
 *   - measured — render it.
 *
 * Layout is one headline plus a quiet breakdown, not four equal numbers: the
 * sum answers "how big was this job" and output answers "how much work came
 * out of it", which are the two questions a drilldown gets asked; the
 * per-counter split is deliberately smaller and dimmer. Counters stay RAW with
 * no blended cost — cache reads bill far below input, and weighting them here
 * would bake today's price ratio into the UI (the same reason the wire carries
 * no total).
 *
 * Wording avoids claiming exactness on purpose: a transcript caught mid-write
 * is resumed on the next pass, so a freshly-scanned job's numbers are a floor.
 */
export function TokensPanel({ job }: { job: WebJob | undefined }) {
  const t = job?.tokens;
  if (!t) return null;

  const inTok = t.in || 0;
  const out = t.out || 0;
  const cacheWrite = t.cache_write || 0;
  const cacheRead = t.cache_read || 0;
  const sessions = t.sessions || 0;
  const total = inTok + out + cacheWrite + cacheRead;
  if (!total && !sessions) return null;

  const rows: Array<[string, number]> = [
    ["in", inTok],
    ["out", out],
    ["cache w", cacheWrite],
    ["cache r", cacheRead],
  ];

  // updated_at is unix MILLIS (WebJob.mtime's convention); age() takes
  // seconds. A missing/zero stamp drops the clause rather than rendering
  // "0s ago" — the same treatment the update banner gives its own timestamp.
  const when = t.updated_at ? age(Math.floor(t.updated_at / 1000)) : "";
  const note = [
    sessions
      ? `measured across ${sessions} transcript${sessions === 1 ? "" : "s"}`
      : "measured",
    when ? `${when} ago` : "",
  ]
    .filter(Boolean)
    .join(" · ");

  const title =
    "token spend summed from this job's own Claude Code transcripts, subagent sidecars included. " +
    "Counters are raw and unweighted; a transcript still being written is resumed next pass, so treat these as a floor.";

  return (
    <div className="tok" title={title}>
      <div className="tok-head">
        <span className="tok-big">{fmtTokens(total)}</span>
        <span className="tok-unit">tokens</span>
        <span className="tok-dot">·</span>
        <span className="tok-mid">{fmtTokens(out)}</span>
        <span className="tok-unit">out</span>
      </div>
      <div className="tok-break">
        {rows.map(([k, v]) => (
          <span className="tok-cell" key={k}>
            <i>{k}</i>
            {fmtTokens(v)}
          </span>
        ))}
      </div>
      <div className="tok-note">{note}</div>
    </div>
  );
}
