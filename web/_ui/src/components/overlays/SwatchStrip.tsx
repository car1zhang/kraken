import type { CSSProperties } from "react";

import { JOB_COLORS } from "../../lib/format.ts";

interface SwatchStripProps {
  /**
   * The radio group's name. It MUST differ per strip: the editor's and the
   * steer modal's both live in the DOM at once, and radios sharing a name are
   * ONE group — a reused name would make picking a colour in one strip
   * silently unpick the other.
   */
  name: string;
  /** The job's current colour; anything not in JOB_COLORS reads as "none". */
  value: string | undefined;
  onPick: (color: string) => void;
  /** `.compact` for the steer modal's head; the editor's form strip omits it. */
  compact?: boolean;
}

/**
 * `swatchStripHTML` + `setSwatchStrip` (app.js:113/125) — a job's colour as
 * nine radios: "no colour" first, then `/color`'s own eight in `/color`'s own
 * order.
 *
 * Exported so BOTH strips are generated from this one component rather than
 * hand-written twice, exactly as the vanilla pair shares one builder: they
 * cannot then drift in palette, order or markup.
 *
 * "No colour" is a REAL radio with value `""` rather than an absence, so an
 * unrecognised colour off the wire falls back to it instead of leaving the
 * strip with nothing checked and no way to click back to neutral.
 */
export function SwatchStrip({ name, value, onPick, compact }: SwatchStripProps) {
  const want = (JOB_COLORS as readonly string[]).includes(value ?? "")
    ? (value as string)
    : "";
  const sw = (val: string) => (
    <label
      className={val ? "f-sw" : "f-sw none"}
      title={val || "no color"}
      key={val || "__none__"}
    >
      <input
        type="radio"
        name={name}
        value={val}
        checked={want === val}
        onChange={() => onPick(val)}
      />
      <span
        className="f-sw-fill"
        style={val ? ({ "--sw": `var(--job-${val})` } as CSSProperties) : undefined}
      ></span>
    </label>
  );
  return (
    <span
      className={compact ? "f-swatches compact" : "f-swatches"}
      id={compact ? undefined : "f-swatches"}
      // Both events are stopped so an editor pick never ALSO reaches the form's
      // change tracking and autosaves every other field for nothing — and, more
      // than cosmetically, so a colour pick on a job whose cell is live cannot
      // trigger a saveform the server is bound to refuse.
      onInput={(ev) => ev.stopPropagation()}
      onChange={(ev) => ev.stopPropagation()}
    >
      {sw("")}
      {JOB_COLORS.map((c) => sw(c))}
    </span>
  );
}
