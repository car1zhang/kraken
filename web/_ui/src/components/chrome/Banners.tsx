/**
 * The three banner strips that sit under the header: the stale-binary
 * self-check, the update run's strip, and the ship run's strip.
 *
 * They are siblings of `<header class="chrome">`, not children of it, which is
 * why `<Chrome>` renders them as a fragment after the header rather than
 * letting `<UpdateControl>` own both its button and its banner — the button
 * lives inside the header and the banner does not.
 */

export interface BannerAction {
  /** The vanilla `data-act` value, kept for e2e and for CSS's dismiss rule. */
  act: string;
  label: string;
}

export interface BannerModel {
  /** "" | "cyan" | "bad" — appended to `.update-banner`. */
  cls: string;
  text: string;
  action?: BannerAction;
  /**
   * The run's `updated_at`. Dismissal is keyed to the RUN, not the page, so it
   * survives the reload the update button performs on itself while the NEXT
   * run — which necessarily has a newer stamp — still gets to speak.
   */
  stamp?: number;
}

export interface BannerProps {
  /** The vanilla element id (index.html:82,88) — kept for parity and e2e. */
  id: string;
  model: BannerModel | null;
  onAction(act: string): void;
}

/**
 * One `.update-banner` strip. Port of paintUpdBanner (app.js:4348).
 *
 * Every banner gets the same trailing ✕, appended centrally rather than in
 * each state so a new banner state cannot forget to be dismissable.
 */
export function Banner({ id, model, onAction }: BannerProps) {
  // Hoisted so the closure below sees a narrowed value: `model.action` inside
  // the JSX widens again across the arrow, which is the only reason the
  // click handler used to need a non-null assertion.
  const action = model?.action;
  return (
    <div className={"update-banner" + (model?.cls ? " " + model.cls : "")} id={id} hidden={!model}>
      {model ? (
        <>
          <span>{model.text}</span>
          {action ? (
            <>
              <span className="spacer" />
              <button data-act={action.act} onClick={() => onAction(action.act)}>
                {action.label}
              </button>
            </>
          ) : null}
          <button data-act="dismiss" title="dismiss" onClick={() => onAction("dismiss")}>
            ✕
          </button>
        </>
      ) : null}
    </div>
  );
}

/**
 * The stale-binary strip (see selfcheck.go). Both wire fields are absent on an
 * older server, which reads as "not stale" — never a hidden error.
 */
export function StaleBanner({ id, messages }: { id: string; messages: string[] }) {
  return (
    <div className="stale-banner" id={id} hidden={!messages.length}>
      {messages.length ? "⚠ " + messages.join(" · ") : null}
    </div>
  );
}
