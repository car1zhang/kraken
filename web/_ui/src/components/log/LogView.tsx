// The unified event log view — a port of renderLog() (web/static/app.js:3862)
// and the scroll handler at :3931.
//
// Newest at the bottom; scrolling to the top pulls the previous (older) page
// into view, one page per fetch. All of the paging, the change detection and
// the scroll arithmetic live in useLogPaging — this component is the markup.

import { Fragment } from "react";

import { useLogPaging } from "../../hooks/useLogPaging.ts";
import { LogRow } from "./LogRow.tsx";

export interface LogViewProps {
  /**
   * Whether the log is the visible view. It gates the poll (the vanilla board
   * refreshed the log only while `view === "log"`) and drives `hidden`.
   *
   * Keep this component MOUNTED across view switches: the loaded chain lived
   * in module state in the vanilla board, so switching away and back did not
   * throw away the history that had been scrolled in.
   */
  active: boolean;
}

export function LogView({ active }: LogViewProps) {
  const { chain, data, hasMore, scrollRef, handleScroll } = useLogPaging(active);

  // Emptiness is measured in ENTRIES, not in loaded files. app.js:3873 tested
  // `parts.join("") || empty`, and `parts` already held one file divider by
  // then — so a fresh kraken.db (a page that loads and returns zero rows) drew
  // an `events` divider with nothing under it and never the empty message.
  // Deliberate divergence: a lone divider is not a log.
  const total = chain.reduce((n, name) => n + (data[name]?.entries.length ?? 0), 0);

  return (
    <main className="logview" id="log" hidden={!active}>
      <div className="log-scroll" id="log-scroll" ref={scrollRef} onScroll={handleScroll}>
        <div className="log-more" id="log-more" hidden={!hasMore}>
          · scroll up for older ·
        </div>
        <div id="log-entries">
          {total === 0 ? (
            <div className="log-empty">no log entries yet</div>
          ) : (
            chain.map((name) => (
              <Fragment key={name}>
                <div className="log-file-divider">{name}</div>
                {(data[name]?.entries ?? []).map((e, i) => (
                  // Index within its own file: prepending an older page
                  // cannot disturb a key that is already scoped to a file.
                  <LogRow key={`${name}:${i}`} entry={e} />
                ))}
              </Fragment>
            ))
          )}
        </div>
      </div>
    </main>
  );
}
