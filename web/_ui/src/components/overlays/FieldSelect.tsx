import { useRef, useState } from "react";

import { useMenuDismiss } from "../../hooks/useMenuDismiss.ts";

interface FieldSelectProps {
  /** Lands on the BUTTON — the interactive control, where the old
   * `<select id>` sat, so getElementById + :disabled checks keep working. */
  id?: string;
  /** The current value; "" renders as the — row. */
  value: string;
  /** The pickable values, in order. "" is not one of them — the — row is
   * always rendered first, as the old `<option value="">—</option>` was. */
  options: string[];
  onPick: (value: string) => void;
  disabled?: boolean;
  /** Names the control for AT. The wrapper is a div rather than a <label>
   * (see the call site's comment), so without this the control's accessible
   * name would be its current value. Composed with the value at the button
   * ("repo: kraken") because aria-label OVERRIDES content — a bare field
   * name would swallow the selection, the exact case SteerOverlay's label
   * comment warns against. */
  label?: string;
}

/** What a value reads as: itself, or the em-dash the empty value has always
 * shown in this form. */
const EMPTY = "—";

/**
 * A plain-text form dropdown — the repo selector's replacement for the
 * browser-native `<select>`, wearing the job-colour dropdown's exact skin
 * (the `.colorsel*` classes) so the two controls read as one family. It is
 * SwatchStrip minus everything colour-specific: no dots, no palette, and the
 * caller owns the option list instead of JOB_COLORS.
 *
 * Deliberately NOT merged into SwatchStrip: the colour picker's props shape
 * is frozen by its contract tests, and a value list parameter would have to
 * thread through both of its call sites for nothing — the shared behaviour
 * (the dismissal rules) lives in useMenuDismiss instead. Also deliberately
 * absent: SwatchStrip's root-level stopPropagation handlers. Those exist so
 * a colour pick never reaches the form's change tracking; a repo pick is
 * form state, and its autosave rides the state effect watching `repo`, so
 * there is nothing here to contain.
 */
export function FieldSelect({ id, value, options, onPick, disabled, label }: FieldSelectProps) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLSpanElement | null>(null);

  useMenuDismiss(open, setOpen, rootRef);

  const row = (val: string) => {
    const sel = value === val;
    return (
      <span
        key={val || "__none__"}
        className={sel ? "colorsel-row sel" : "colorsel-row"}
        role="option"
        aria-selected={sel}
        onClick={() => {
          // Unconditional, even on the row already selected — same stance as
          // the colour picker: a re-pick still calls onPick rather than
          // silently doing nothing.
          onPick(val);
          setOpen(false);
        }}
      >
        {val || EMPTY}
        {sel ? <span className="tick">✓</span> : null}
      </span>
    );
  };

  return (
    <span ref={rootRef} className="colorsel">
      <button
        type="button"
        id={id}
        className="colorsel-btn"
        disabled={disabled}
        aria-label={label ? `${label}: ${value || EMPTY}` : undefined}
        aria-haspopup="listbox"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        {value || EMPTY} <span className="caret">▾</span>
      </button>
      {/* gated on !disabled too, not just the button: a disabled button
          cannot re-open the menu, but one already open when disabled flips
          true would otherwise stay painted over a locked form with its rows
          still clickable. */}
      {open && !disabled ? (
        <span className="colorsel-menu" role="listbox">
          {row("")}
          {options.map((x) => row(x))}
        </span>
      ) : null}
    </span>
  );
}
