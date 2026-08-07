import { useEffect, useRef, useState } from "react";
import type { CSSProperties } from "react";

import { JOB_COLORS } from "../../lib/format.ts";

interface SwatchStripProps {
  /**
   * Vestigial since the strip became a dropdown: there are no radios left to
   * group, so nothing reads this any more. It stays in the props type (and in
   * both call sites) because the contract freezes the props shape — and it is
   * still the cheapest per-instance label to hand a future control that needs
   * one.
   */
  name: string;
  /** The job's current colour; anything not in JOB_COLORS reads as "none". */
  value: string | undefined;
  onPick: (color: string) => void;
  /** `.compact` for the steer modal's head; the editor's form strip omits it. */
  compact?: boolean;
}

/** The row label: /color's own names, upper-cased, plus the neutral row's. */
const NO_COLOR = "NO COLOR";

/**
 * A job's colour as a DROPDOWN: a `.colorsel-btn` showing the current colour's
 * dot and name, and a `.colorsel-menu` listing "no colour" first, then
 * /color's own eight in /color's own order.
 *
 * It replaced a nine-radio swatch strip. The palette is nine choices wide and
 * the strip spent that width in both the steer head and the editor form to say
 * one word; a dropdown says it in one chip and spends the width only while it
 * is being used.
 *
 * Exported so BOTH pickers are generated from this one component rather than
 * hand-written twice: they cannot then drift in palette, order or markup.
 *
 * "No colour" is a REAL row with value `""` rather than an absence, so an
 * unrecognised colour off the wire falls back to it instead of leaving the
 * button blank with no way to click back to neutral.
 */
// `name` is deliberately not destructured: it is frozen into the props type and
// still passed by both call sites, but nothing in a dropdown reads it.
export function SwatchStrip({ value, onPick, compact }: SwatchStripProps) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLSpanElement | null>(null);

  const want = (JOB_COLORS as readonly string[]).includes(value ?? "")
    ? (value as string)
    : "";

  // Escape closes the MENU and nothing else. It is captured on the document so
  // the press is stopped before useEscapeStack's own document listener sees it
  // (that one is registered first, when the overlay opened, so a bubble-phase
  // listener here could never get in front of it) — otherwise one Escape would
  // close the menu AND the overlay underneath it.
  useEffect(() => {
    if (!open) return;
    const onKey = (ev: KeyboardEvent) => {
      if (ev.key !== "Escape") return;
      ev.stopPropagation();
      setOpen(false);
    };
    document.addEventListener("keydown", onKey, true);
    return () => document.removeEventListener("keydown", onKey, true);
  }, [open]);

  // Outside-click closes it, on mousedown rather than click: the click that
  // follows may well land on a control that is about to be re-rendered away,
  // and a menu left open over the next overlay is the failure people notice.
  // Presses INSIDE the component are ignored here — the button toggles and a
  // row picks, both of which own their own close.
  useEffect(() => {
    if (!open) return;
    const onDown = (ev: MouseEvent) => {
      const root = rootRef.current;
      if (root && ev.target instanceof Node && root.contains(ev.target)) return;
      setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);

  const label = (val: string) => (val ? val.toUpperCase() : NO_COLOR);
  // The dot's colour is set inline rather than left to the sheet because the
  // eight are data (JOB_COLORS), not eight hand-written rules. `--sw` rides
  // along for any skin that reads it; `background` is what actually paints, and
  // the neutral dot sets NEITHER, so `.dot.none`'s own treatment shows through.
  const dot = (val: string) => (
    <span
      className={val ? "dot" : "dot none"}
      style={
        val
          ? ({ background: `var(--job-${val})`, "--sw": `var(--job-${val})` } as CSSProperties)
          : undefined
      }
    ></span>
  );

  const row = (val: string) => {
    const sel = want === val;
    return (
      <span
        key={val || "__none__"}
        className={sel ? "colorsel-row sel" : "colorsel-row"}
        role="option"
        aria-selected={sel}
        title={val || "no color"}
        onClick={() => {
          // Unconditional, even on the row already selected: the endpoint is
          // idempotent and the radios behaved the same way, so a re-pick after
          // a failed POST still retries rather than silently doing nothing.
          onPick(val);
          setOpen(false);
        }}
      >
        {dot(val)}
        {label(val)}
        {sel ? <span className="tick">✓</span> : null}
      </span>
    );
  };

  return (
    <span
      ref={rootRef}
      className={compact ? "colorsel compact" : "colorsel"}
      id={compact ? undefined : "f-swatches"}
      // All three events are stopped so a pick never ALSO reaches the form's
      // change tracking and autosaves every other field for nothing — and, more
      // than cosmetically, so a colour pick on a job whose cell is live cannot
      // trigger a saveform the server is bound to refuse. `click` is stopped
      // for the card's sake too: the steer head's picker sits inside surfaces
      // whose own click means something else entirely.
      onClick={(ev) => ev.stopPropagation()}
      onInput={(ev) => ev.stopPropagation()}
      onChange={(ev) => ev.stopPropagation()}
    >
      <button
        type="button"
        className="colorsel-btn"
        aria-haspopup="listbox"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        {dot(want)}
        {label(want)} <span className="caret">▾</span>
      </button>
      {open ? (
        <span className="colorsel-menu" role="listbox">
          {row("")}
          {JOB_COLORS.map((c) => row(c))}
        </span>
      ) : null}
    </span>
  );
}
