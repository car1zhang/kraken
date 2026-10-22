import { useMemo, useRef, useState, type ReactNode } from "react";

import type { WebJob, WebNode } from "../../api/types.ts";
import { useMenuDismiss } from "../../hooks/useMenuDismiss.ts";
import { fleetButtonLabel, jobNodeKey, LOCAL_NODE } from "../../lib/fleet.ts";

export interface FleetPickerProps {
  /** the payload's registry — may be empty when only a persisted selection keeps the picker on screen */
  nodes: readonly WebNode[];
  /** the payload's `node_label` — this Mac's device label; absent on an older server */
  selfLabel?: string;
  /** the payload's `operator` */
  operator?: string;
  /** the UNFILTERED cards, for each row's count */
  jobs: readonly WebJob[];
  /** the machine keys shown (lib/fleet.ts: `local` or a node id); empty is no filter */
  selected: readonly string[];
  /** the whole next selection on every pick; `[]` from the reset row */
  onChange: (next: string[]) => void;
}

/**
 * The board bar's machines dropdown — the fleet filter's one visible owner.
 * A multi-select in the goal picker's skin (`.colorsel` + `.gm-*`,
 * GoalMultiPicker.tsx), because both say "show me these" over a short list and
 * two dropdown families on one bar would read as two different ideas.
 *
 * Rows: `all machines` leads as the RESET (Q1 of job 634 asked for one by
 * name) and is the only row that is not a toggle; then this Mac; then the
 * registry in the payload's order (online first, then label — store.ListNodes'
 * ORDER BY); then a dimmed row per selected key the registry no longer names,
 * so a persisted filter can always be undone from the bar even after the
 * node it named is gone. The menu stays OPEN across picks, like the goal
 * picker, since "this Mac and MJ's" is one gesture.
 *
 * This component owns no state but `open`: the selection is App's (persisted
 * there through lib/fleet.ts) and the filtering is useBoard's.
 */
export function FleetPicker({ nodes, selfLabel, operator, jobs, selected, onChange }: FleetPickerProps) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLSpanElement | null>(null);
  useMenuDismiss(open, setOpen, rootRef);

  const counts = useMemo(() => {
    const m = new Map<string, number>();
    for (const j of jobs) {
      const k = jobNodeKey(j);
      m.set(k, (m.get(k) ?? 0) + 1);
    }
    return m;
  }, [jobs]);

  const picked = useMemo(() => new Set(selected), [selected]);
  const label = fleetButtonLabel(selected, nodes);

  const toggle = (key: string) => {
    onChange(picked.has(key) ? selected.filter((k) => k !== key) : [...selected, key]);
  };

  // the self row's words: `label · operator`, the label alone, or the bare
  // `this Mac` when an older server sent no node_label
  const selfText = selfLabel ? (operator ? `${selfLabel} · ${operator}` : selfLabel) : "this Mac";
  const stale = selected.filter((k) => k !== LOCAL_NODE && !nodes.some((n) => n.node_id === k));

  const row = (key: string, body: ReactNode, ticked: boolean, onPick: () => void, extra = "") => (
    <span
      key={key}
      className={"colorsel-row gm-row fp-row" + (ticked ? " sel" : "") + extra}
      role="option"
      aria-selected={ticked}
      data-node-key={key}
      onClick={onPick}
    >
      <span className="gm-tick" aria-hidden="true">
        {ticked ? "✓" : ""}
      </span>
      {body}
    </span>
  );

  return (
    <span ref={rootRef} className={"colorsel gm fp" + (selected.length ? " fp-on" : "")}>
      <button
        type="button"
        id="fleet-filter"
        className="colorsel-btn"
        aria-label={`machines: ${label}`}
        aria-haspopup="listbox"
        aria-expanded={open}
        title="show only the cells of the machines you tick (this browser only)"
        onClick={() => setOpen((o) => !o)}
      >
        {label} <span className="caret">▾</span>
      </button>
      {open ? (
        <span className="colorsel-menu gm-menu fp-menu" role="listbox" aria-multiselectable="true">
          {row("__all__", <span className="gm-lbl">↺ all machines</span>, false, () => onChange([]))}
          <span className="fp-sep" aria-hidden="true" />
          {row(
            LOCAL_NODE,
            <>
              <span className="fp-dot on" aria-hidden="true" />
              <span className="gm-lbl">{selfText}</span>
              <span className="fp-self">this Mac</span>
              <span className="fp-count">{counts.get(LOCAL_NODE) ?? 0}</span>
            </>,
            picked.has(LOCAL_NODE),
            () => toggle(LOCAL_NODE),
          )}
          {nodes.map((n) =>
            row(
              n.node_id,
              <>
                <span
                  className={"fp-dot" + (n.online ? " on" : "")}
                  aria-label={n.online ? "online" : "offline"}
                  title={n.online ? "online" : "offline — last polled cards still shown"}
                />
                <span className="gm-lbl">{n.operator ? `${n.label || n.node_id} · ${n.operator}` : n.label || n.node_id}</span>
                <span className="fp-count">{counts.get(n.node_id) ?? 0}</span>
              </>,
              picked.has(n.node_id),
              () => toggle(n.node_id),
            ),
          )}
          {stale.map((k) =>
            row(
              k,
              <>
                <span className="fp-dot" aria-hidden="true" />
                <span className="gm-lbl">{k}</span>
                <span className="fp-count">{counts.get(k) ?? 0}</span>
              </>,
              true,
              () => toggle(k),
              " fp-stale",
            ),
          )}
        </span>
      ) : null}
    </span>
  );
}
