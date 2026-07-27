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

  // The origin row is the one thing here not on the wire directly: the daemon
  // stores zeus' four counters as a SUBSET of the four totals, so the
  // delegated half is the REMAINDER and is computed here. Storing both halves
  // would let them drift out of agreement with the total; subtracting cannot.
  // "zeus" is the cell's lead agent and its side threads; "subagents" is every
  // teammate and in-process subagent it delegated to.
  //
  // Presence is checked on ONE key, not on their sum: the four ride together
  // or not at all, and a job whose lead genuinely burned nothing (every token
  // spent by a teammate) is a real state a sum-based test would hide. A server
  // predating these keys sends none, which drops the row rather than rendering
  // a 100%-subagent lie.
  const hasOrigin = t.zeus_in !== undefined;
  const zeus = hasOrigin
    ? (t.zeus_in || 0) +
      (t.zeus_out || 0) +
      (t.zeus_cache_write || 0) +
      (t.zeus_cache_read || 0)
    : 0;
  // Clamped for the same reason every derived number here is: zeus is a subset
  // so this cannot go negative, but a stale zeus row paired with a fresh total
  // mid-rescan is exactly the window where it could look like it.
  const origin: Array<[string, number]> | null = hasOrigin
    ? [
        ["zeus", Math.min(zeus, total)],
        ["subagents", Math.max(0, total - zeus)],
      ]
    : null;

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
    "zeus is the cell's lead agent and its side threads; subagents is every teammate and in-process subagent it delegated to. " +
    "Counters are raw and unweighted; a transcript still being written is resumed next pass, so treat these as a floor.";

  const cells = (list: Array<[string, number]>) =>
    list.map(([k, v]) => (
      <span className="tok-cell" key={k}>
        <i>{k}</i>
        {fmtTokens(v)}
      </span>
    ));

  return (
    <div className="tok" title={title}>
      <div className="tok-head">
        <span className="tok-big">{fmtTokens(total)}</span>
        <span className="tok-unit">tokens</span>
      </div>
      <div className="tok-break">{cells(rows)}</div>
      {origin ? <div className="tok-break tok-origin">{cells(origin)}</div> : null}
      <div className="tok-note">{note}</div>
    </div>
  );
}
