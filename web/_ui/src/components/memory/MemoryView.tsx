// The MEMORY view — the `#/memory` tab (a legacy `#/graph` hash still lands
// here, see useHashRoute's RENAMED). Two segments over two panes: LIBRARY, the
// saved artifacts carl chose to keep, and MODEL, the mental model of what he
// has actually read and decided. Both are kraken remembering something for
// carl, which is the whole reason they share a tab.
//
// No poll of its own: the library and the mental model are App's ONE poll
// each, shared with the capture surfaces, so this view only renders them.

import { useState } from "react";

import type { LibraryModel } from "../../hooks/useLibrary.ts";
import type { MMModel } from "../../hooks/useMM.ts";
import { LibraryPane } from "./LibraryPane.tsx";
import { MentalModelPane } from "./MentalModelPane.tsx";

/** The two things the MEMORY view can be showing. */
type MemoryMode = "library" | "mm";

export interface MemoryViewProps {
  /**
   * Whether MEMORY is the visible view. Drives `hidden`.
   *
   * Keep this component MOUNTED across view switches, exactly as LogView and
   * DreamView are: the segment, the library filter and any half-typed note are
   * component state, and unmounting would throw them away on every tab switch.
   */
  active: boolean;
  /**
   * The saved-artifact library — App's ONE poll, shared with the artifact
   * viewer's ☆/★ button so the two can never disagree about what is saved.
   */
  library: LibraryModel;
  /**
   * The mental model — App's ONE unfiltered mm poll. The Model pane runs its
   * own filtered poll beside it while its filter box holds text.
   */
  mm: MMModel;
  /** Write a note into the mental model. Routed through the shared mutate. */
  onAddNote: (text: string) => Promise<void>;
  /** Drop one mark by id — invalidates the row rather than deleting it. */
  onRemoveMM: (id: number) => Promise<void>;
  /** Confirm one nightly `proposed` item so it goes live. Routed through the shared mutate. */
  onConfirmMM: (id: number) => Promise<void>;
  /** A raised flag's `open job`: the steer modal of that job. Optional. */
  onOpenJob?: (slug: string) => void;
  /**
   * Open a saved snapshot in the artifact viewer, by its LIBRARY url. That url
   * serves the frozen bytes rather than the live file, which is what makes a
   * row from a torn-down (or hard-deleted) job still readable.
   */
  onOpenArtifact: (url: string, name: string) => void;
  /** Drop one saved row. Routed through App's shared mutate. */
  onRemoveSaved: (id: number) => Promise<void>;
}

export function MemoryView({
  active,
  library,
  mm,
  onAddNote,
  onRemoveMM,
  onConfirmMM,
  onOpenJob,
  onOpenArtifact,
  onRemoveSaved,
}: MemoryViewProps) {
  // Component state, so which half carl was reading survives a trip to the
  // board. Library first: it is the default segment.
  const [mode, setMode] = useState<MemoryMode>("library");

  return (
    <main className="memoryview" id="memory" hidden={!active}>
      <div className="memory-hdr">
        <span className="memory-title">memory</span>
        <span className="memory-modes">
          <button
            className={"memory-mode" + (mode === "library" ? " on" : "")}
            aria-pressed={mode === "library"}
            title="saved artifacts — frozen copies you chose to keep"
            onClick={() => setMode("library")}
          >
            Library
          </button>
          <button
            className={"memory-mode" + (mode === "mm" ? " on" : "")}
            aria-pressed={mode === "mm"}
            title="the mental model — what you have read and decided"
            onClick={() => setMode("mm")}
          >
            Model
          </button>
        </span>
      </div>

      <LibraryPane
        hidden={mode !== "library"}
        library={library}
        onOpenArtifact={onOpenArtifact}
        onRemoveSaved={onRemoveSaved}
      />

      <MentalModelPane
        hidden={mode !== "mm"}
        mm={mm}
        onAddNote={onAddNote}
        onRemove={onRemoveMM}
        onConfirm={onConfirmMM}
        onOpenJob={onOpenJob}
      />
    </main>
  );
}

export default MemoryView;
