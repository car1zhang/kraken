// The DREAM view — the nightly corroboration loop's two surfaces side by
// side. A port of app.js's dream section (web/static/app.js:4571-4900) and
// the markup at web/static/index.html:104.
//
// Left pane: one collapsed header per night's report, newest first, expanding
// to the rendered `00-dream-report.md`. Right pane: the open watchlist, where
// carl rules on each parked observation. Read-only on the left, the loop's
// only inbound channel on the right — the next run reads these verdicts and
// comments FIRST, ahead of any evidence, so a row marked `fine` is never
// proposed again and a `fix` comment becomes the specification the run
// follows. That ordering is why the comment box is the primary control here
// rather than an afterthought behind an edit toggle.
//
// The two panes poll INDEPENDENTLY, one usePoll each, which is the same
// promise refreshDream's two-arm Promise.all made: the watchlist endpoint
// failing must not blank a report carl is mid-read, and vice versa. Neither
// rides the board poll, because neither reads /api/board.

import { useEffect, useRef, useState } from "react";

import { fetchDream, fetchWatchlist, setWatchVerdict } from "../../api/client.ts";
import type { DreamReports, WatchlistPage, WebDreamReport } from "../../api/types.ts";
import { usePoll } from "../../hooks/usePoll.ts";
import { renderMD } from "../../lib/markdown.ts";
import {
  dreamPRCount,
  dreamSectionCount,
  sortWatch,
  wlVerdict,
} from "./dreamText.ts";
import { WatchRow } from "./WatchRow.tsx";

/** The board tick, which is what the vanilla view refreshed on. */
const DREAM_POLL_MS = 3000;

export interface DreamViewProps {
  /**
   * Whether DREAM is the visible view. It gates both polls (the vanilla board
   * called refreshDream only while `view === "dream"`) and drives `hidden`.
   *
   * Keep this component MOUNTED across view switches, exactly as LogView is:
   * which report dates are expanded, and any half-typed watchlist comment,
   * are component state here where they were module state in the vanilla
   * board — unmounting would throw both away on every tab switch.
   */
  active: boolean;
  /**
   * The app's shared mutation path. Used for the ONE write this view has, so
   * a refused verdict lands in the status corner like every other failure.
   * Resolves to null on failure.
   */
  mutate: <T>(op: () => Promise<T>) => Promise<T | null>;
  /**
   * Also toast a verdict failure. The status line alone is easy to miss here:
   * unlike a board mutation there is no card behind this view to re-render,
   * so nothing else visibly changes (app.js:4862).
   */
  onToast(msg: string): void;
}

function ReportCard({
  report,
  open,
  onToggle,
}: {
  report: WebDreamReport;
  open: boolean;
  onToggle(date: string): void;
}) {
  const prs = dreamPRCount(report.body);
  const watching = dreamSectionCount(report.body, /^watching\b/i);
  const judged = dreamSectionCount(report.body, /^judgment calls?\b/i);
  const quiet = !prs && !watching && !judged;
  const tag = (cls: string, n: number, one: string, many: string) =>
    n ? (
      <span className={`rep-tag ${cls}`}>
        {n} {n === 1 ? one : many}
      </span>
    ) : null;
  return (
    <div className={"rep" + (open ? " open" : "")}>
      <button
        className="rep-h"
        data-date={report.date}
        aria-expanded={open}
        onClick={() => onToggle(report.date)}
      >
        <span className="tri">{open ? "▾" : "▸"}</span>
        <span className="d">{report.date}</span>
        {tag("pr", prs, "PR", "PRs")}
        {tag("watch", watching, "watching", "watching")}
        {tag("judge", judged, "judgment call", "judgment calls")}
        <span className="spacer" />
        <span className="sum">{quiet ? "nothing shipped" : report.slug}</span>
      </button>
      {open ? (
        <div
          className="rep-b md"
          dangerouslySetInnerHTML={{ __html: renderMD(report.body ?? "") }}
        />
      ) : null}
    </div>
  );
}

export function DreamView({ active, mutate, onToast }: DreamViewProps) {
  const reportsPoll = usePoll<DreamReports>(
    (signal) => fetchDream(signal),
    DREAM_POLL_MS,
    { enabled: active },
  );
  const watchPoll = usePoll<WatchlistPage>(
    (signal) => fetchWatchlist("watching", signal),
    DREAM_POLL_MS,
    { enabled: active },
  );

  // An endpoint error EMPTIES its pane and shows the reason in place of the
  // empty state — vanilla parity (app.js:4805, :4818 both reset the array on
  // catch). usePoll holds the last good payload, but rendering it under a
  // failure would claim a freshness this pane does not have.
  const reports = reportsPoll.error ? [] : (reportsPoll.data?.reports ?? []);
  const watch = watchPoll.error ? [] : (watchPoll.data?.watchlist ?? []);
  const reportsNote = reportsPoll.error
    ? "couldn't load reports: " + reportsPoll.error.message
    : "";
  const watchNote = watchPoll.error
    ? "couldn't load the watchlist: " + watchPoll.error.message
    : "";

  // Which report dates are expanded. Component state, so a poll cannot
  // collapse what carl opened; seeded ONCE with the newest night, since that
  // is the one he came to read, and an explicit collapse ends the seeding
  // for good (app.js:4800, :4880).
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
  const seeded = useRef(false);
  const newest = reports[0]?.date;
  useEffect(() => {
    if (seeded.current || !newest) return;
    seeded.current = true;
    setExpanded((prev) => new Set(prev).add(newest));
  }, [newest]);

  const toggle = (date: string) => {
    seeded.current = true;
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(date)) next.delete(date);
      else next.add(date);
      return next;
    });
  };

  const rows = sortWatch(watch);
  const openCount = rows.filter((w) => wlVerdict(w) !== "fine").length;

  const onVerdict = async (
    id: number,
    verdict: "fine" | "fix" | "",
    comment: string,
  ): Promise<void> => {
    const res = await mutate(() => setWatchVerdict(id, verdict, comment));
    if (res === null) {
      onToast("verdict failed — see the status bar");
      return;
    }
    // The row's own markup changed; pull it back now rather than waiting out
    // the tick, so the button lights up the moment the server agrees. A
    // concurrent tick can abort this refetch (usePoll runs one request at a
    // time), in which case the row simply shows its previous verdict until
    // that tick lands — it is a freshness shortcut, never the row's only way
    // back to a usable state. WatchRow re-enables itself regardless.
    await watchPoll.refetch();
  };

  return (
    <main className="dreamview" id="dream" hidden={!active}>
      <section className="dream-pane reports">
        <div className="dream-panehdr">
          reports{" "}
          <span className="ct" id="dream-reports-ct">
            {reports.length
              ? `· ${reports.length} night${reports.length === 1 ? "" : "s"}`
              : ""}
          </span>
        </div>
        <div className="dream-scroll" id="dream-reports">
          {reports.length ? (
            reports.map((r) => (
              <ReportCard
                // The date IS the identity: one dream job per local calendar
                // day, and the server breaks a same-date tie by slug order.
                key={r.slug}
                report={r}
                open={expanded.has(r.date)}
                onToggle={toggle}
              />
            ))
          ) : (
            <div className="dream-empty">
              {reportsNote ||
                "no reports yet — the nightly pass writes one per night"}
            </div>
          )}
        </div>
      </section>
      <section className="dream-pane watch">
        <div className="dream-panehdr">
          watchlist{" "}
          <span className="ct" id="dream-watch-ct">
            {rows.length ? `· ${openCount} open` : ""}
          </span>
        </div>
        <div className="dream-scroll" id="dream-watchlist">
          {rows.length ? (
            rows.map((w) => (
              <WatchRow key={w.id} item={w} onVerdict={onVerdict} />
            ))
          ) : (
            <div className="dream-empty">
              {watchNote ||
                "nothing on the watchlist — the loop parks observations here"}
            </div>
          )}
        </div>
      </section>
    </main>
  );
}
