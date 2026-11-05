import { useEffect, useState } from "react";

import type { WebJob } from "../../api/types.ts";
import {
  activityName,
  leadPose,
  leadPoseExpiry,
  newestCall,
  ribbon,
} from "../../lib/agentmap/activity.ts";

/**
 * What a cell's lead is doing: `▸ <pose>` plus the newest call's label, then a
 * ribbon of one tick per recent event. JobCard renders it as the card's last
 * band when `mapMode` is set, so on the map it is part of the `.card`; the
 * column board never renders it. The steer modal's collapsed activity
 * disclosure (EventsPanel) shows the same strip.
 *
 * The tick colours come from style.css (`.act-ribbon i[data-act]`), keyed on
 * the activity id, so the theme owns them like every other board colour.
 *
 * Without `nowMs` the strip keeps its own clock: one timeout aimed at the
 * moment `leadPose`'s hold runs out re-renders it, so `▸ <act>` turns to
 * `▸ think` with no new payload. Nothing is scheduled for a pose time cannot
 * change. With `nowMs` it renders for exactly that instant and starts no timer.
 */
export function ActivityStrip({ job, nowMs }: { job: WebJob; nowMs?: number }) {
  const [tick, setTick] = useState(0);
  const activity = job.activity;
  // the instant this render reads the clock at (an explicit nowMs, or now)
  const at = nowMs ?? Date.now();
  const expiry = nowMs === undefined && activity ? leadPoseExpiry(job, at) : null;

  useEffect(() => {
    if (expiry === null) return;
    const id = window.setTimeout(() => setTick((n) => n + 1), Math.max(0, expiry - Date.now()));
    return () => window.clearTimeout(id);
    // cleared on unmount and whenever the job changes; after it fires, the
    // re-render finds the hold expired and schedules nothing more
  }, [expiry, job, tick]);

  if (!activity) return null;
  const pose = leadPose(job, at);
  const label = newestCall(activity.events)?.label ?? "";
  return (
    <div className="act-strip">
      <div className="act-now" data-act={pose}>
        <b>▸ {activityName(pose)}</b> <span className="act-label">{label}</span>
      </div>
      <div className="act-ribbon">
        {ribbon(job).map((e) => (
          <i key={e.id} data-act={e.act} title={`${activityName(e.act)}: ${e.label}`} />
        ))}
      </div>
    </div>
  );
}
