// One event-log line — a port of logRowHTML() (web/static/app.js:3849).
//
// The row's level rides a class rather than an inline color so style.css owns
// the palette: `.log-row.lvl-error .log-msg` and `.lvl-error .log-event` are
// the only level-specific rules today, but any new level is a CSS change
// alone.

import type { LogEntry } from "../../api/types.ts";
import { logTime } from "../../lib/format.ts";

export interface LogRowProps {
  entry: LogEntry;
}

export function LogRow({ entry }: LogRowProps) {
  return (
    <div className={`log-row lvl-${entry.level ?? ""}`}>
      <span className="log-ts">{logTime(entry.ts)}</span>
      <span className="log-src">{entry.src ?? ""}</span>
      <span className="log-event">{entry.event ?? ""}</span>
      <span className="log-msg">{entry.msg ?? ""}</span>
    </div>
  );
}
