// The DAG view — a straight port of renderDag() (web/static/app.js:3485).
//
// The picture is absolutely-positioned nodes over an SVG edge layer, and both
// coordinate systems come out of lib/dagLayers.ts. Nothing here polls: the
// board arrives as a prop, because the DAG and the board render the same
// /api/board payload and two pollers would show two different graphs.

import type { CSSProperties } from "react";
import { Fragment, useMemo } from "react";

import type { WebBoard, WebJob } from "../../api/types.ts";
import { dagEdges, dagLayout } from "../../lib/dagLayers.ts";
import { columnFor, stateColor } from "../../lib/format.ts";

export interface DagViewProps {
  /** The same /api/board payload the board view renders. */
  board: WebBoard | undefined;
  /** Whether the DAG is the visible view; drives `hidden` on <main>. */
  active: boolean;
  /**
   * A node click. The vanilla handler (app.js:3556) routed by the job's LIVE
   * display column — working/blocked opened the steer modal (blocked in
   * `unblock` mode), anything else opened the editor — which is overlay
   * knowledge this component does not have. The integrator owns that routing;
   * the selection guard (a click that is really a text selection) is already
   * applied here.
   */
  onNodeClick?: (job: WebJob) => void;
}

export function DagView({ board, active, onNodeClick }: DagViewProps) {
  // Archived jobs stay out of the DAG: they are out of the active namespace,
  // and their prereq slugs may now point at unrelated newer jobs.
  const jobs = useMemo(
    () => (board?.jobs ?? []).filter((j) => !j.archived),
    [board],
  );
  const layout = useMemo(() => dagLayout(jobs), [jobs]);
  const edges = useMemo(() => dagEdges(jobs, layout.pos), [jobs, layout]);

  const empty = jobs.length === 0;
  const { width, height } = layout;

  // ONE wrapper skeleton, empty state and all. renderDag() (app.js:3491-3495)
  // swaps `nodesEl.innerHTML` and never the wrapper for the same reason: the
  // whole contract of this component is that
  // `main.dag > .dag-canvas > svg#dag-edges + #dag-nodes` matches the ids
  // style.css addresses, and a second copy of that skeleton is a nesting fix
  // waiting to be applied to only one of them.
  return (
    <main className="dag" id="dag" hidden={!active}>
      <div
        className="dag-canvas"
        id="dag-canvas"
        // Sizing is omitted while empty rather than set to the pad: vanilla
        // returned before touching the canvas, leaving CSS's min-height to
        // hold the shape.
        style={empty ? undefined : { width: `${width}px`, height: `${height}px` }}
      >
        <svg
          id="dag-edges"
          {...(empty
            ? {}
            : { viewBox: `0 0 ${width} ${height}`, width, height })}
        >
          {/* A keyed Fragment rather than a <g>: style.css addresses
              `#dag-edges path` / `#dag-edges text`, and an extra grouping
              element would still match but would not be the same DOM. */}
          {edges.map((e) => (
            <Fragment key={e.key}>
              <path className={e.cls} d={e.d} />
              {e.flag ? (
                <text x={e.flag.x} y={e.flag.y}>
                  ⚑
                </text>
              ) : null}
            </Fragment>
          ))}
        </svg>
        <div id="dag-nodes" style={empty ? undefined : { height: `${height}px` }}>
          {empty ? (
            <div className="dag-empty">no jobs — + new on the board creates one</div>
          ) : null}
          {jobs.map((j) => {
            // dagLayout places every job it was handed — `layers` is built
            // from this same array — so this is the noUncheckedIndexedAccess
            // tax, not a reachable degradation.
            const p = layout.pos.get(j.slug) ?? { x: 0, y: 0 };
            // `editable` rides data-editable only — the `editable` CLASS is
            // unconditional in app.js:3521 and stays that way here, since the
            // hover affordance it carries is part of the look being diffed.
            const editable =
              !j.live && ["draft", "queued", "done"].includes(columnFor(j.state));
            return (
              <div
                // A bare slug is a safe key ONLY because archived jobs are
                // filtered out above: the duplicate-slug hazard in
                // lib/jobKey.ts is archived-vs-active, and there are no
                // archived entries left here to collide with.
                key={j.slug}
                className={`dag-node editable${j.question || j.health ? " flagged" : ""}`}
                style={
                  {
                    left: `${p.x}px`,
                    top: `${p.y}px`,
                    "--state": stateColor(j.state),
                  } as CSSProperties
                }
                data-slug={j.slug}
                data-editable={editable}
                onClick={() => {
                  // A click that ends a text selection is not a click on the
                  // node — dragging across a slug to copy it must not open a
                  // modal (app.js:3557).
                  if (window.getSelection()?.toString()) return;
                  onNodeClick?.(j);
                }}
              >
                <span className="slug">{j.slug}</span>
                <span className="nstate">● {j.state}</span>
                <span className="nrepo">{j.repo}</span>
              </div>
            );
          })}
        </div>
      </div>
    </main>
  );
}
