import { useEffect, useState } from "react";

// The transient bottom-center notice (app.js:1114-1130). Deliberately a
// module-level store rather than a context or a prop: in the vanilla board
// showToast() is a bare function anyone can call, and its three callers sit in
// three different layers — a reverted drag-reorder (board), a refused account
// rotation (usage overlay), an update/ship refusal (chrome). Threading a
// setter through all of those would be strictly more plumbing for the same
// behaviour, and there is only ever ONE toast on screen: a second message
// replaces the first and restarts its 4s timer, exactly as the vanilla single
// #toast element did.

type Listener = (message: string, seq: number) => void;

let listener: Listener | null = null;
let seq = 0;

/** Show a transient notice. Safe to call before <Toast/> mounts (it no-ops). */
export function showToast(message: string): void {
  seq++;
  listener?.(message, seq);
}

/**
 * Renders `#toast`. Mount ONCE, at the app root.
 *
 * The node is always present and only its `.show` class toggles — style.css
 * animates opacity/transform between those two states, and a node that is
 * created at the same moment the class lands has nothing to transition FROM.
 * (The vanilla board created it lazily and so silently skipped the fade on the
 * very first toast of a page load; it faded correctly from the second on.
 * Keeping the node mounted makes every toast behave like that second one.)
 * Idle cost is nil: `opacity: 0; pointer-events: none`.
 */
export function Toast() {
  const [state, setState] = useState<{ message: string; seq: number } | null>(
    null,
  );
  const [shown, setShown] = useState(false);

  useEffect(() => {
    listener = (message, s) => {
      setState({ message, seq: s });
      setShown(true);
    };
    return () => {
      listener = null;
    };
  }, []);

  useEffect(() => {
    if (!state) return;
    // keyed on seq, not message: two identical messages in a row must restart
    // the timer rather than let the first one's timeout close the second
    const id = window.setTimeout(() => setShown(false), 4000);
    return () => window.clearTimeout(id);
  }, [state]);

  // the message stays rendered through the fade-out — clearing it on the same
  // frame the class drops would blank the toast instead of dissolving it
  return (
    <div id="toast" className={shown ? "toast show" : "toast"}>
      {state?.message ?? ""}
    </div>
  );
}
