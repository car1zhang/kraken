// The sleep button: whether this Mac may sleep, and the switch for it. The
// board's form of a `togglesleep` shell function, showing the two words that
// function prints, SLEEPABLE and ALWAYS ON. Backed by /api/sleep, which runs
// `pmset -a disablesleep` and a caffeinate beside it.
//
// The state is SERVER state, and not this board's alone: a terminal can
// change it too. So the label only ever shows what a poll read back, never
// what a click hoped for, and a click asks for the opposite of the state on
// screen by name; the server takes the named state, not a flip.
//
// Before the first answer, and on a board that cannot read the route at all
// (a node-tier board is refused), there is no button: a label would be a
// guess about whether the machine will sleep.

import { useState } from "react";

import { fetchSleep, setSleepDisabled } from "../../api/client.ts";
import type { WebSleep } from "../../api/types.ts";
import { usePoll } from "../../hooks/usePoll.ts";

/** How often the button re-reads the state a terminal may have changed. */
const SLEEP_POLL_MS = 10_000;

export interface SleepControlProps {
  /** Toast host: where a refused switch says why. */
  onToast(message: string): void;
}

export function SleepControl({ onToast }: SleepControlProps) {
  const poll = usePoll<WebSleep>((signal) => fetchSleep(signal), SLEEP_POLL_MS);
  const [busy, setBusy] = useState(false);
  if (!poll.data) return null;
  const held = poll.data.disabled;
  return (
    <button
      type="button"
      className={"theme-btn" + (held ? " sleep-held" : "")}
      id="sleep-toggle"
      aria-pressed={held}
      disabled={busy}
      title={
        held
          ? "sleep is disabled — this Mac stays awake. Click to let it sleep"
          : "this Mac may sleep. Click to keep it awake"
      }
      onClick={async () => {
        setBusy(true);
        try {
          await setSleepDisabled(!held);
        } catch (e) {
          onToast("✕ " + (e instanceof Error ? e.message : String(e)));
        }
        // re-read on success and failure alike: the label is the poll's
        await poll.refetch();
        setBusy(false);
      }}
    >
      {held ? "☀ ALWAYS ON" : "☾ SLEEPABLE"}
    </button>
  );
}
