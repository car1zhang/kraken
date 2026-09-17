import type { WebUsage } from "../../api/types.ts";
import { UsageStatContent, usagePanelTitle } from "../overlays/UsageOverlay.tsx";

export interface UsageStatProps {
  /** Last successful /api/usage payload; undefined keeps the widget hidden. */
  usage: WebUsage | undefined;
  /** Opens the usage drilldown overlay. */
  onOpen(): void;
}

/**
 * The header's at-a-glance usage widget: the `#usage-stat` WRAPPER only.
 *
 * What goes inside it is `<UsageStatContent>`, which lives beside the
 * drilldown that renders the same numbers in full — one reader of the payload
 * shape, two mount points, the same split the vanilla board used between
 * usageStatHTML and usageAccountHTML.
 *
 * The wrapper is what belongs to the chrome slice, and it is four things: the
 * `hidden` rule (the widget stays hidden until /api/usage parses ONCE, so a
 * 404 from an older server or a transient network error never flashes broken
 * markup into view), the `role="button"` + `tabIndex` affordance, the
 * Enter/Space handler that the vanilla board wired by hand (app.js:3818), and
 * the tooltip — which names what CLICKING opens, so it comes from
 * `usagePanelTitle` rather than from a string of its own: the widget and the
 * panel it opens must never disagree about how many pools are on screen.
 */
export function UsageStat({ usage, onOpen }: UsageStatProps) {
  return (
    <span
      className="usage-stat"
      id="usage-stat"
      hidden={!usage}
      role="button"
      tabIndex={0}
      title={usagePanelTitle(usage)}
      onClick={onOpen}
      onKeyDown={(ev) => {
        if (ev.key === "Enter" || ev.key === " ") {
          ev.preventDefault();
          onOpen();
        }
      }}
    >
      <UsageStatContent data={usage} />
    </span>
  );
}
