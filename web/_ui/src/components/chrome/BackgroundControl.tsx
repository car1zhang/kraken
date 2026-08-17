// The board-background control: ONE ▨ BG button opening a two-option
// dropdown — UPLOAD… (file picker; replaces when one is already set) and
// CLEAR (back to the plain board) — per carl's adjustment that retired the
// separate ▨ ✕ remove button. The menu is SwatchStrip's dropdown idiom
// (relative wrapper, absolute menu, useMenuDismiss for Escape and
// outside-click), with real <button> rows so the pointer-cursor list's bare
// `button` term covers them without a new entry. It lives with the HUD
// preference buttons but is NOT one of them: the background is server state
// (a blob in kraken.db), not a localStorage preference, so it rides the
// shared mutation path — the board refetch that follows a successful write
// is what makes the change appear, and a refusal lands in the clock corner
// like every other one. Unlike its krakenHud siblings there is no pre-paint
// concern: the image can only be known after the first board payload, so
// the stamp is React's alone (App.tsx).

import { useRef, useState } from "react";

import { removeBackground, uploadBackground } from "../../api/client.ts";
import { useMenuDismiss } from "../../hooks/useMenuDismiss.ts";

export interface BackgroundControlProps {
  /** Whether the board payload carries a background right now. */
  hasBg: boolean;
  /** The shared mutation path (useBoard.mutate): write, then silent refetch. */
  mutate: (fn: () => Promise<unknown>) => Promise<Error | null>;
}

export function BackgroundControl({ hasBg, mutate }: BackgroundControlProps) {
  const [open, setOpen] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const rootRef = useRef<HTMLSpanElement | null>(null);
  useMenuDismiss(open, setOpen, rootRef);
  return (
    <span className="bg-menu-root" ref={rootRef}>
      {/* The picker itself. accept mirrors the server's sniff allow-list —
          advisory only, since the server re-sniffs the bytes regardless of
          what the picker let through. The bare `hidden` attribute, not an
          inline display:none — the sheet's global [hidden] rule exists for
          exactly this, and it is how the editor's and steer modal's file
          inputs hide. */}
      <input
        ref={fileRef}
        type="file"
        accept="image/png,image/jpeg,image/gif,image/webp"
        hidden
        id="bg-file-input"
        onChange={(e) => {
          const f = e.currentTarget.files?.[0];
          // clear the input so picking the SAME file again still fires
          // change — replacing an image with a re-edited copy of itself is
          // the common flow this covers
          e.currentTarget.value = "";
          if (f) void mutate(() => uploadBackground(f));
        }}
      />
      <button
        className="theme-btn"
        id="bg-toggle"
        title="board background image: upload or clear"
        onClick={() => setOpen((o) => !o)}
      >
        ▨ BG{hasBg ? " ✓" : ""}
      </button>
      {open && (
        <span className="bg-menu">
          {/* Both options always render — the ask was a two-option menu —
              with CLEAR disabled when there is nothing to clear: a live
              CLEAR on an unset background would 200 (the DELETE is
              idempotent) while visibly doing nothing, which reads as the
              button being broken. */}
          <button
            className="bg-menu-row"
            id="bg-upload"
            title={
              hasBg
                ? "replace the board background image"
                : "upload a board background image"
            }
            onClick={() => {
              setOpen(false);
              fileRef.current?.click();
            }}
          >
            UPLOAD…
          </button>
          <button
            className="bg-menu-row"
            id="bg-clear"
            disabled={!hasBg}
            title={hasBg ? "remove the board background image" : "no background set"}
            onClick={() => {
              setOpen(false);
              void mutate(() => removeBackground());
            }}
          >
            CLEAR
          </button>
        </span>
      )}
    </span>
  );
}
