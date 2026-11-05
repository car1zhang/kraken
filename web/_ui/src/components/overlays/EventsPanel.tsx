import { useCallback, useState } from "react";

import { fetchJobEvents } from "../../api/client.ts";
import type { AgentEvent, WebJob } from "../../api/types.ts";
import { usePoll } from "../../hooks/usePoll.ts";
import { activityName } from "../../lib/agentmap/activity.ts";
import { ActivityStrip } from "../board/ActivityStrip.tsx";

/** How many of the lead's newest events the expanded list shows. */
const EVENTS_N = 60;
const EVENTS_POLL_MS = 4000;

/**
 * The steer modal's recent-activity disclosure, in the place the zeus
 * transcript panel used to take. Collapsed (the default) it is the same
 * horizontal summary the map card carries: the lead's current action and
 * the tick ribbon, from the board payload, so it costs no request. Expanded
 * it is the written list behind those ticks, newest first, polled from
 * `/api/job/events`. Every row is an activity word and a label, neither of
 * which ever carries message or prompt text (the server builds them that
 * way), so the list needs no redaction step.
 *
 * `enabled` is the modal being open: closed, the panel renders nothing and
 * polls nothing.
 */
export function EventsPanel({
  slug,
  enabled,
  job,
}: {
  slug: string;
  enabled: boolean;
  job?: WebJob;
}) {
  const [open, setOpen] = useState(false);
  const fetcher = useCallback(
    (signal: AbortSignal) => fetchJobEvents(slug, EVENTS_N, signal),
    [slug],
  );
  const poll = usePoll<AgentEvent[]>(fetcher, EVENTS_POLL_MS, { enabled: enabled && open });
  if (!enabled) return null;
  const events = open ? (poll.data ?? []) : [];
  return (
    <div className="steer-activity" id="steer-activity">
      <button
        type="button"
        className="steer-activity-toggle"
        id="steer-activity-toggle"
        aria-expanded={open ? "true" : "false"}
        aria-controls="steer-activity-body"
        onClick={() => setOpen((o) => !o)}
      >
        {open ? "▾" : "▸"} recent activity
      </button>
      <div className="steer-activity-body" id="steer-activity-body">
        {open ? (
          events.length ? (
            <ol className="steer-events" id="steer-events">
              {[...events].reverse().map((e) => (
                <li key={e.id} data-act={e.act}>
                  <i />
                  <b>{activityName(e.act)}</b>
                  <span>{e.label}</span>
                </li>
              ))}
            </ol>
          ) : null
        ) : job?.activity ? (
          <ActivityStrip job={job} />
        ) : null}
      </div>
    </div>
  );
}
