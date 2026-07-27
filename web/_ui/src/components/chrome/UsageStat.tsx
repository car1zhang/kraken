import type { WebUsage } from "../../api/types.ts";
import { UsageStatContent } from "../overlays/UsageOverlay.tsx";

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
 * The wrapper is what belongs to the chrome slice, and it is three things:
 * the `hidden` rule (the widget stays hidden until /api/usage parses ONCE, so
 * a 404 from an older server or a transient network error never flashes broken
 * markup into view), the `role="button"` + `tabIndex` affordance, and the
 * Enter/Space handler that the vanilla board wired by hand (app.js:3818).
 */
export function UsageStat({ usage, onOpen }: UsageStatProps) {
  return (
    <span
      className="usage-stat"
      id="usage-stat"
      hidden={!usage}
      role="button"
      tabIndex={0}
      title="usage — claude code pool"
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
