import { useCallback, useState, type ReactNode } from "react";
import { setRotationMode, switchAccount } from "../../api/client.ts";
import type { WebUsage, WebUsageAccount } from "../../api/types.ts";
import { ESC_PRIORITY } from "../../hooks/useEscapeStack.ts";
import { age, usageColorClass, usageCountdown } from "../../lib/format.ts";
import { Overlay } from "./Overlay.tsx";
import { showToast } from "./Toast.tsx";

// The claude-code-pool usage widget and its drilldown (app.js:3560-3829).
//
// The "main" account is whichever entry carries current:true (the server sorts
// it first); everything else is pool. The whole surface is absent on an older
// server (404) or offline — fetchUsage leaves the widget hidden rather than
// showing broken state, which is why every field below is read defensively and
// `rotation_mode === undefined` gates the rotation controls off entirely.
//
// /api/usage's account shape is `[k: string]: unknown` in types.ts (it mirrors
// core.AccountUsage, which grows fields), so the windows are narrowed here by
// readers rather than by a cast: a renamed field degrades to a missing bar, not
// to a crash.

interface UsageWindow {
  percent: number;
  resets_at?: number;
}

function readWindow(v: unknown): UsageWindow | null {
  if (!v || typeof v !== "object") return null;
  const o = v as Record<string, unknown>;
  if (typeof o.percent !== "number") return null;
  return {
    percent: o.percent,
    ...(typeof o.resets_at === "number" ? { resets_at: o.resets_at } : {}),
  };
}

interface NamedWindow extends UsageWindow {
  name: string;
}

function readModels(v: unknown): NamedWindow[] {
  if (!Array.isArray(v)) return [];
  const out: NamedWindow[] = [];
  for (const m of v) {
    const w = readWindow(m);
    if (!w) continue;
    const name = (m as Record<string, unknown>).name;
    if (typeof name !== "string") continue;
    out.push({ ...w, name });
  }
  return out;
}

const str = (v: unknown): string => (typeof v === "string" ? v : "");
const num = (v: unknown): number | undefined =>
  typeof v === "number" ? v : undefined;
const bool = (v: unknown): boolean => v === true;

// usageColorClass (app.js:3580) and usageCountdown (app.js:3589) live in
// lib/format.ts, not here. Its own comment gives the reason and it is the same
// reason age() lives there: the HEADER widget and this DRILLDOWN are two
// surfaces of one rule, and a second copy is a threshold that can silently
// disagree with itself.

function mainAccount(data: WebUsage): WebUsageAccount | undefined {
  const accounts = data.accounts || [];
  return accounts.find((a) => a.current) || accounts[0];
}

function Seg({ label, percent }: { label: string; percent: number }) {
  return (
    <span className={`u-seg ${usageColorClass(percent)}`}>
      {label} {Math.round(percent)}%
    </span>
  );
}

/**
 * The HEADER widget's inner content (app.js:3612 usageStatHTML).
 *
 * Exported separately from the overlay because the `#usage-stat` wrapper —
 * with its `role="button"`, its `tabindex` and its `hidden` until the first
 * successful parse — belongs to the chrome slice. This is only what goes
 * inside it.
 *
 * Never decides visibility: a transient error must not flash broken markup into
 * view, so revealing the widget stays the caller's call.
 */
export function UsageStatContent({ data }: { data: WebUsage | undefined }) {
  const accounts = data?.accounts || [];
  if (!data || !data.available || !accounts.length) {
    return <span className="dim">usage —</span>;
  }
  const main = mainAccount(data);
  const segs: ReactNode[] = [];
  const session = readWindow(main?.session);
  const weekly = readWindow(main?.weekly);
  if (session) segs.push(<Seg key="5h" label="5h" percent={session.percent} />);
  if (weekly) segs.push(<Seg key="7d" label="7d" percent={weekly.percent} />);
  for (const m of readModels(main?.models)) {
    segs.push(
      <Seg key={"m:" + m.name} label={m.name.toLowerCase()} percent={m.percent} />,
    );
  }
  const pool = accounts.filter((a) => a !== main);
  const weeklies = pool
    .map((a) => readWindow(a.weekly)?.percent)
    .filter((p): p is number => p !== undefined);
  const worst = weeklies.length ? Math.max(...weeklies) : null;
  const ageS = num(data.age_seconds) ?? 0;
  const stale = !data.daemon_ok || ageS > 900;
  // manual-pin chip: absent on an older server (rotation_mode undefined there,
  // so this never equals "manual") and in auto mode too — status quo look
  // unless someone has actually pinned the main account
  const manual = data.rotation_mode === "manual";

  // an account with no windows at all (fresh install, reauth everywhere) must
  // not leave a visible-but-blank widget
  if (!segs.length && !pool.length && !manual && !stale) {
    return <span className="dim">usage —</span>;
  }
  return (
    <>
      {segs}
      {pool.length ? (
        <>
          <span className="u-sep">·</span>
          <span className="u-pool">
            pool {pool.length}
            {worst !== null ? (
              <>
                {" · worst 7d "}
                <span className={usageColorClass(worst)}>
                  {Math.round(worst)}%
                </span>
              </>
            ) : null}
          </span>
        </>
      ) : null}
      {manual ? (
        <>
          <span className="u-sep">·</span>
          <span className="u-chip u-chip-warn">manual</span>
        </>
      ) : null}
      {stale ? (
        <span className="u-stale">(stale {Math.floor(ageS / 60)}m)</span>
      ) : null}
    </>
  );
}

export interface UsageOverlayProps {
  open: boolean;
  /** Last successful /api/usage payload — the 60s poll's `data`. */
  data: WebUsage | undefined;
  onClose: () => void;
  /**
   * Refetch /api/usage. A successful rotation action does NOT re-render from
   * its own response: it lets this repaint both the header widget and this
   * panel, the same path the 60s poll takes, so the new mode chip / banner /
   * buttons just fall out of it.
   *
   * It must SWALLOW its own errors (usePoll's refetch does), which is exactly
   * why postUsageAction re-enables its button in `finally` — see below.
   */
  refetch: () => Promise<void>;
}

export function UsageOverlay({ open, data, onClose, refetch }: UsageOverlayProps) {
  return (
    <Overlay
      id="usage-overlay"
      open={open}
      priority={ESC_PRIORITY.usage}
      onClose={onClose}
    >
      <UsageBody data={data} refetch={refetch} />
    </Overlay>
  );
}

function UsageBody({
  data,
  refetch,
}: {
  data: WebUsage | undefined;
  refetch: () => Promise<void>;
}) {
  // Gates BOTH rotation handlers against a double-fire: a rapid double-click,
  // or the 60s poll replacing the button mid-click. A per-button disabled flag
  // alone cannot catch the second one — a freshly re-rendered button starts out
  // enabled again even while the first click's request is still in flight.
  const [inFlight, setInFlight] = useState(false);

  const action = useCallback(
    async (op: () => Promise<unknown>, errLabel: string) => {
      if (inFlight) return;
      setInFlight(true);
      try {
        await op();
        await refetch();
      } catch (e) {
        showToast(`${errLabel}: ${e instanceof Error ? e.message : e}`);
      } finally {
        // ALWAYS, not just on the catch path: refetch() swallows its own
        // network errors rather than throwing, so a POST success followed by a
        // silently-failed refetch would skip both the catch branch AND the
        // re-render that would normally discard a disabled button — leaving a
        // dead control until the next 60s poll happens to succeed.
        setInFlight(false);
      }
    },
    [inFlight, refetch],
  );

  const accounts = data?.accounts || [];
  const main = data ? mainAccount(data) : undefined;
  const manual = data?.rotation_mode === "manual";

  return (
    <>
      <div className="panel-head">
        <span className="panel-title">usage — claude code pool</span>
        <span id="usage-mode-chip">
          {/* undefined rotation_mode (older server) renders nothing — the same
              gate as everywhere else in this section */}
          {data && data.rotation_mode !== undefined ? (
            <span className={`u-chip ${manual ? "u-chip-warn" : "u-chip-ok"}`}>
              rotation: {manual ? "manual" : "auto"}
            </span>
          ) : null}
        </span>
        <span className="stat dim" id="usage-fetched">
          {!data || !data.available ? (
            <span className="dim">no data yet</span>
          ) : !data.daemon_ok ? (
            <span className="u-chip u-chip-danger">
              daemon down — data frozen
            </span>
          ) : (
            <span className="dim">
              fetched {age(Math.floor((num(data.fetched_at) ?? 0) / 1000))} ago ·
              daemon
            </span>
          )}
        </span>
        <span className="spacer"></span>
      </div>
      <div className="panel-body" id="usage-body">
        {manual && data ? (
          <ManualBanner
            data={data}
            disabled={inFlight}
            onSwitchAuto={() =>
              void action(
                () => setRotationMode("auto"),
                "couldn't switch to auto",
              )
            }
          />
        ) : null}
        {accounts.length ? (
          accounts.map((a) => (
            <Account
              key={str(a.slug)}
              a={a}
              main={main}
              rotationKnown={data?.rotation_mode !== undefined}
              disabled={inFlight}
              onMakeMain={() =>
                void action(
                  () => switchAccount(str(a.slug)),
                  `couldn't switch to ${str(a.slug)}`,
                )
              }
            />
          ))
        ) : (
          <div className="empty">no usage data yet</div>
        )}
      </div>
    </>
  );
}

/**
 * app.js:3734. rotation_set_at is unix millis (0 = unset per the API contract);
 * age() already renders a falsy unix as "", so the trailing "<time> ago" clause
 * drops out rather than reading "0s ago".
 */
function ManualBanner({
  data,
  disabled,
  onSwitchAuto,
}: {
  data: WebUsage;
  disabled: boolean;
  onSwitchAuto: () => void;
}) {
  const main = mainAccount(data);
  const who = str(main?.email) || "main";
  const setBy = data.rotation_set_by || "unknown";
  const setAt = Number(data.rotation_set_at) || 0;
  const whenS = setAt ? age(Math.floor(setAt / 1000)) : "";
  return (
    <div className="u-manual-banner">
      <span>
        pinned to {who} by {setBy}
        {whenS ? ` ${whenS} ago` : ""} — auto-rotation off
      </span>
      <button
        type="button"
        className="btn ghost mini u-switch-auto"
        disabled={disabled}
        onClick={onSwitchAuto}
      >
        switch back to auto
      </button>
    </div>
  );
}

/** app.js:3648 — "" for an absent window, so callers can just render null. */
function Bar({ label, w }: { label: string; w: UsageWindow | null }) {
  if (!w) return null;
  const pct = Math.max(0, Math.min(100, w.percent));
  return (
    <div className="u-row">
      <span className="u-row-label">{label}</span>
      <span className="u-bar">
        <span
          className={`u-bar-fill ${usageColorClass(w.percent)}`}
          style={{ width: `${pct}%` }}
        ></span>
      </span>
      <span className="u-row-val">
        {Math.round(w.percent)}%
        {w.resets_at ? (
          <>
            {" · "}
            <span data-resets={w.resets_at}>{usageCountdown(w.resets_at)}</span>
          </>
        ) : null}
      </span>
    </div>
  );
}

/** app.js:3662 — any subset of these may apply at once. */
function Chips({ a }: { a: WebUsageAccount }) {
  const chips: ReactNode[] = [];
  const cooldown = num(a.cooldown_until);
  if (cooldown && cooldown > Date.now()) {
    chips.push(
      <span className="u-chip u-chip-warn" key="cool">
        cooling {usageCountdown(cooldown)}
      </span>,
    );
  }
  if (bool(a.reauth)) {
    chips.push(
      <span className="u-chip u-chip-danger" key="reauth">
        reauth needed — ccp login
      </span>,
    );
  }
  if (bool(a.no_creds)) {
    chips.push(
      <span className="u-chip u-chip-danger" key="nocreds">
        no creds
      </span>,
    );
  }
  if (bool(a.stale)) {
    chips.push(
      <span className="u-chip u-chip-dim" key="stale">
        stale
      </span>,
    );
  }
  if (str(a.error)) {
    chips.push(
      <span className="u-chip u-chip-danger" key="err" title={str(a.error)}>
        error
      </span>,
    );
  }
  return <>{chips}</>;
}

function Account({
  a,
  main,
  rotationKnown,
  disabled,
  onMakeMain,
}: {
  a: WebUsageAccount;
  main: WebUsageAccount | undefined;
  rotationKnown: boolean;
  disabled: boolean;
  onMakeMain: () => void;
}) {
  const slug = str(a.slug);
  const email = str(a.email);
  const tier = str(a.tier).replace(/^default_claude_/, "");
  const rows = (
    <>
      <Bar label="session" w={readWindow(a.session)} />
      <Bar label="weekly" w={readWindow(a.weekly)} />
      {readModels(a.models).map((m) => (
        <Bar key={m.name} label={m.name.toLowerCase()} w={m} />
      ))}
    </>
  );
  const hasRows =
    !!readWindow(a.session) ||
    !!readWindow(a.weekly) ||
    readModels(a.models).length > 0;

  // make-main: any non-current account with usable creds. Cooling/stale rows
  // still get the button (the server decides whether the switch succeeds); only
  // no_creds/reauth do not. A row whose email MIRRORS the current main's is
  // also excluded — that is the save-back slot a rotation just parked the
  // outgoing main creds into, and core refuses to ever rotate onto it
  // (core.ErrSaveBackIsTarget), so the button would be guaranteed to fail.
  const mainEmail = str(main?.email);
  const mirrorsMain =
    !!email && !!mainEmail && email.toLowerCase() === mainEmail.toLowerCase();
  const canMakeMain =
    rotationKnown && !a.current && !bool(a.no_creds) && !bool(a.reauth) && !mirrorsMain;

  return (
    <div className="u-account">
      <div className="u-account-head">
        <span className="u-slug">{slug}</span>
        {a.current ? (
          <span className="u-chip u-chip-current">current</span>
        ) : null}
        {email ? <span className="dim">{email}</span> : null}
        {tier ? <span className="u-chip">{tier}</span> : null}
        <Chips a={a} />
        <span className="spacer"></span>
        {canMakeMain ? (
          <button
            type="button"
            className="btn ghost mini u-make-main"
            data-slug={slug}
            data-email={email || slug}
            disabled={disabled}
            title={`switch the main account to ${slug}`}
            onClick={() => {
              if (
                !window.confirm(
                  `switch main to ${slug} (${email || slug})? every running session migrates at its next token refresh, and rotation pins to manual.`,
                )
              ) {
                return;
              }
              onMakeMain();
            }}
          >
            make main
          </button>
        ) : null}
        {a.current ? null : <CcpButton slug={slug} />}
      </div>
      {hasRows ? <div className="u-rows">{rows}</div> : null}
    </div>
  );
}

/**
 * "⧉ ccp" copies `ccp <slug>` to the clipboard.
 *
 * The execCommand fallback is load-bearing and not dead code: navigator
 * .clipboard is only available on a SECURE origin, and this board is routinely
 * opened over the tailnet on plain http — where the modern API is simply
 * absent.
 */
function CcpButton({ slug }: { slug: string }) {
  const [label, setLabel] = useState("⧉ ccp");
  const copy = useCallback(() => {
    const text = `ccp ${slug}`;
    const done = () => {
      setLabel("copied");
      window.setTimeout(() => setLabel("⧉ ccp"), 1200);
    };
    if (navigator.clipboard?.writeText) {
      navigator.clipboard.writeText(text).then(done, () => fallback(text, done));
      return;
    }
    fallback(text, done);
  }, [slug]);
  return (
    <button
      className="btn ghost mini u-ccp"
      data-slug={slug}
      title={`copy ccp ${slug}`}
      onClick={copy}
    >
      {label}
    </button>
  );
}

function fallback(text: string, done: () => void) {
  try {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    document.execCommand("copy");
    document.body.removeChild(ta);
    done();
  } catch {
    showToast("couldn't copy to the clipboard");
  }
}
