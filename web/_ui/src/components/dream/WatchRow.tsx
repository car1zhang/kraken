// One parked watchlist observation and its verdict controls — a port of
// watchRowHTML (web/static/app.js:4731) plus postWatchVerdict (:4841).

import { useRef, useState } from "react";

import type { WebWatchItem } from "../../api/types.ts";
import { inlineMD } from "../../lib/markdown.ts";
import {
  dreamDay,
  wlPRLabel,
  wlPRLinkable,
  wlSeenText,
  wlVerdict,
} from "./dreamText.ts";

export interface WatchRowProps {
  item: WebWatchItem;
  /**
   * Send a verdict. Resolves once the write has settled, whatever its
   * outcome — the caller owns surfacing a failure, and this row re-enables
   * itself either way (see `send`).
   *
   * It deliberately reports NOTHING back. A success/failure boolean here used
   * to decide whether to re-enable the controls, and that is exactly the
   * shape that latched a row shut forever; there is no longer any question
   * for an answer to settle.
   */
  onVerdict(
    id: number,
    verdict: "fine" | "fix" | "",
    comment: string,
  ): Promise<void>;
}

/**
 * Three states, per the spec's own framing:
 *
 *   untouched — the loop is still counting sightings; comment box empty
 *   fix       — carl wants it done; the comment IS the spec, so the box stays
 *               open and editable and shows what he already wrote
 *   fine      — "stop even watching this". The row dims, sinks to the bottom
 *               of the list (see sortWatch) and drops its comment box for a
 *               read-back of what he said, because there is nothing left to
 *               specify. Clicking either button un-settles it — the verdict
 *               is carl's field, and he is allowed to change his mind.
 *
 * The comment box is a real composer on a surface that re-polls every 3s. The
 * vanilla board needed snapshotDocInputs/restoreDocInputs to keep a long
 * comment from losing characters every rebuild; React reconciles rather than
 * replacing, so the draft simply lives here as component state. What it does
 * still need is the SERVER-CHANGE sync below: a verdict posted from this row
 * comes back on the next poll, and a row whose stored comment genuinely
 * changed must show the new text rather than a stale draft.
 */
export function WatchRow({ item: w, onVerdict }: WatchRowProps) {
  const server = w.comment ?? "";
  const [comment, setComment] = useState(server);
  const [busy, setBusy] = useState(false);

  // Adjusting state during render — React's own documented pattern for
  // "derive from props when the prop actually changes". A useEffect here
  // would paint one frame of the stale draft first.
  const lastServer = useRef(server);
  if (lastServer.current !== server) {
    lastServer.current = server;
    setComment(server);
  }

  const verdict = wlVerdict(w);
  const settled = verdict === "fine";

  const send = async (kind: "fine" | "fix") => {
    // Clicking the verdict already in force clears it — back to untouched,
    // which is the only way out of a mis-click on `fine`.
    const next = verdict === kind ? "" : kind;
    setBusy(true);
    try {
      // A settled row has no box; its comment comes off what the server last
      // stored, so re-clicking a button cannot silently erase what he wrote.
      await onVerdict(w.id, next, (settled ? server : comment).trim());
    } finally {
      // UNCONDITIONALLY, success included — the row is never handed fresh
      // controls by anything else.
      //
      // Nothing remounts this component: DreamView keys it by `w.id`, and
      // store.SetWatchVerdict writes only verdict/comment/commented_at, so
      // `state` stays "watching" and the row keeps coming back in the same
      // GET /api/watchlist?state=watching list — reconciled in place, forever.
      // DreamView itself also stays mounted across view switches, so leaving
      // the tab and returning resets nothing either.
      //
      // Vanilla re-enabled only on failure (app.js:4841) and got away with it
      // for a reason that no longer exists: renderDreamWatchlist replaced
      // #dream-watchlist's innerHTML wholesale, so every success produced
      // brand-new, enabled elements. That accidental reset is exactly what
      // React's reconciliation removes, and leaning on a wholesale rebuild is
      // the one assumption this port must never make. Left one-sided, a
      // mis-click on `fine as-is` was unrecoverable without a reload, and a
      // `fix` row's comment — the spec the next dream run follows — could
      // never be edited again.
      setBusy(false);
    }
  };

  const btn = (kind: "fine" | "fix", label: string, title: string) => (
    <button
      className={`wl-btn ${kind}${verdict === kind ? " on" : ""}`}
      data-id={w.id}
      data-verdict={kind}
      title={title}
      disabled={busy}
      onClick={() => void send(kind)}
    >
      {label}
    </button>
  );

  return (
    <div
      className={"wl" + (settled ? " done" : "")}
      data-wl-id={w.id}
      data-comment={server}
    >
      <div className="wl-h">
        {/* Every string on this row is AGENT-authored, so it goes through
            inlineMD, which escapes first and transforms the escaped text —
            see lib/markdown.ts for why that is the whole safety argument. */}
        <span
          className="obs"
          dangerouslySetInnerHTML={{ __html: inlineMD(w.observation ?? "") }}
        />
      </div>
      <div className="wl-meta">
        {w.class ? <span className="cls">{w.class}</span> : null}
        <span className="seen">{wlSeenText(w)}</span>
        {wlPRLinkable(w.pr_url) ? (
          <a
            className="pr-chip pr-open"
            href={w.pr_url}
            target="_blank"
            rel="noreferrer"
          >
            {wlPRLabel(w.pr_url ?? "")}
          </a>
        ) : null}
      </div>
      {w.suggested_fix ? (
        // one div, arrow and text in the same node — the vanilla DOM exactly
        <div
          className="wl-fix"
          dangerouslySetInnerHTML={{ __html: "→ " + inlineMD(w.suggested_fix) }}
        />
      ) : null}
      <div className="wl-ctrl">
        {btn(
          "fine",
          "fine as-is",
          "stop watching this — the loop never proposes it again",
        )}
        {btn(
          "fix",
          "needs fixing",
          "the next run fixes this — whatever you write below is the spec it follows",
        )}
        {w.commented_at ? (
          <span className="wl-stamp">{dreamDay(w.commented_at)}</span>
        ) : null}
      </div>
      {settled ? (
        server ? (
          <div
            className="wl-said"
            dangerouslySetInnerHTML={{
              __html: '<span class="who">you said</span>' + inlineMD(server),
            }}
          />
        ) : null
      ) : (
        <textarea
          className="wl-comment"
          data-slug="watchlist"
          data-id={w.id}
          rows={2}
          disabled={busy}
          placeholder="how should this be fixed? — whatever you write here becomes the spec"
          value={comment}
          onChange={(ev) => setComment(ev.target.value)}
        />
      )}
    </div>
  );
}
