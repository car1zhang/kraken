import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  type MouseEvent as ReactMouseEvent,
} from "react";
import { codeRefTarget, type CodeRefTarget } from "../../lib/codeRefs.ts";
import { compileCard } from "../../lib/isoscene/index.ts";

/**
 * The compiled 3D rendering of one diagram card — the `3D` toggle's body.
 *
 * Unlike the flat card, the compiled scene lives in the BOARD's own document
 * rather than an iframe: the emitted SVG is inert markup (compileCard strips
 * nothing into it beyond elements the renderer itself wrote — no scripts, no
 * external URLs, per the isoscene contract), and mounting it here is what lets
 * the board's stylesheet retheme it through the --iso-* custom properties.
 *
 * Code refs keep working through the SAME delegated-click pattern the markdown
 * body uses (one onClick, codeRefTarget's closest('[data-code]') walk) rather
 * than wireCodeRefs — that helper wires a foreign document (the iframe's),
 * while React owns the events in this one.
 *
 * The caller only mounts this when cardCompilable(html) is true, so a
 * non-scene compile result here means the input changed under us — render the
 * reason rather than a blank panel; the caller's fallback iframe is one toggle
 * away and the card itself is never lost.
 */
export function IsoSceneView({
  html,
  onOpenRef,
}: {
  html: string;
  onOpenRef: (t: CodeRefTarget) => void;
}) {
  // Read once per mount — deliberately NOT GraphView's live listener: that
  // view lives for the whole session, while this one remounts on every toggle
  // and every pager step, so a mid-view System Settings flip lands at the
  // next card anyway and a live listener would only re-compile the scene
  // under the reader.
  const reducedMotion = useMemo(
    () => window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false,
    [],
  );

  const result = useMemo(() => {
    try {
      return compileCard(html, { reducedMotion });
    } catch (e) {
      return {
        kind: "not-parseable" as const,
        reason: e instanceof Error ? e.message : String(e),
      };
    }
  }, [html, reducedMotion]);

  const onClick = useCallback(
    (ev: ReactMouseEvent<HTMLDivElement>) => {
      const el = ev.target;
      if (!(el instanceof Element)) return;
      const target = codeRefTarget(el);
      if (target) onOpenRef(target);
    },
    [onOpenRef],
  );

  // The scene arrives as innerHTML, so there are no React elements to hang a
  // title on — decorate [data-code] hover tooltips the way the iframe path's
  // decorate() does, against whatever the current compile emitted.
  const bodyEl = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    const root = bodyEl.current;
    if (!root) return;
    for (const el of Array.from(root.querySelectorAll("[data-code]"))) {
      const ref = el.getAttribute("data-code");
      if (ref) el.setAttribute("title", ref);
    }
  }, [result]);

  if (result.kind !== "scene") {
    // Reachable only when the card changed between the availability probe and
    // this render; the flat card is still there behind the toggle.
    return (
      <div className="panel-body iso" id="panel-body">
        this card no longer compiles —{" "}
        {result.kind === "not-parseable" ? result.reason : "not a diagram card"}
        ; toggle back to the flat view
      </div>
    );
  }
  return (
    <div
      ref={bodyEl}
      className="panel-body iso"
      id="panel-body"
      onClick={onClick}
      dangerouslySetInnerHTML={{ __html: result.svg }}
    />
  );
}
