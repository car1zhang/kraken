// The board-background control: a ▨ BG button that opens a file picker
// (upload, or replace when one is already set) and — only while a background
// is set — a ▨ ✕ remove button beside it. It lives with the HUD preference
// buttons but is NOT one of them: the background is server state (a blob in
// kraken.db), not a localStorage preference, so it rides the shared mutation
// path — the board refetch that follows a successful write is what makes the
// new image appear, and a refusal lands in the clock corner like every other
// one. Unlike its krakenHud siblings there is no pre-paint concern: the
// image can only be known after the first board payload, so the stamp is
// React's alone (App.tsx).

import { useRef } from "react";

import { removeBackground, uploadBackground } from "../../api/client.ts";

export interface BackgroundControlProps {
  /** Whether the board payload carries a background right now. */
  hasBg: boolean;
  /** The shared mutation path (useBoard.mutate): write, then silent refetch. */
  mutate: (fn: () => Promise<unknown>) => Promise<Error | null>;
}

export function BackgroundControl({ hasBg, mutate }: BackgroundControlProps) {
  const fileRef = useRef<HTMLInputElement>(null);
  return (
    <>
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
        id="bg-upload"
        title={
          hasBg
            ? "replace the board background image"
            : "upload a board background image"
        }
        onClick={() => fileRef.current?.click()}
      >
        ▨ BG{hasBg ? " ✓" : ""}
      </button>
      {hasBg && (
        <button
          className="theme-btn"
          id="bg-remove"
          title="remove the board background image"
          onClick={() => void mutate(() => removeBackground())}
        >
          ▨ ✕
        </button>
      )}
    </>
  );
}
