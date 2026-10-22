import type { StackLink, WebJob } from "../../../api/types.ts";

/**
 * Stack chips: one linked badge per manual-test URL the cell registered with
 * `kraken stack add` (internal/api/jobstack.go). The sibling of PRChips in the
 * card's band 5 — a PR is the job's output, a stack link is the thing to go
 * and TRY — and the two rows read the same way: label says WHICH, colour says
 * its state.
 */

/** `:5173` from the url, or "" when it names no port and no scheme default applies cleanly. */
function portOf(url: string): string {
  try {
    const u = new URL(url);
    if (u.port) return `:${u.port}`;
    return u.protocol === "https:" ? ":443" : u.protocol === "http:" ? ":80" : "";
  } catch {
    return "";
  }
}

function hostOf(url: string): string {
  try {
    return new URL(url).hostname;
  } catch {
    return url;
  }
}

/**
 * `⇱ board :7878` — the label the cell gave it plus the port, which is the
 * identifying half a label cannot be trusted to carry. No label → `⇱ :5173`;
 * a url with no usable port → the host.
 */
export function stackLabel(l: StackLink): string {
  const port = portOf(l.url);
  const label = (l.label || "").trim();
  if (label && port) return `⇱ ${label} ${port}`;
  if (label) return `⇱ ${label}`;
  if (port) return `⇱ ${port}`;
  return `⇱ ${hostOf(l.url)}`;
}

function stackClass(l: StackLink): string {
  const s = l.status === "up" ? "stack-up" : l.status === "down" ? "stack-down" : "stack-unknown";
  return `stack-chip ${s}`;
}

/** The tooltip: the full url, then what the probe last said about it. */
export function stackTitle(l: StackLink): string {
  const port = portOf(l.url);
  if (l.status === "down") return `${l.url} · ${port || "port"} not listening`;
  if (l.status === "up") return `${l.url} · up`;
  return `${l.url} · not probed`;
}

/**
 * One chip. Two guards turn the anchor into an inert span: a non-http(s)
 * url (the server refuses them, but the row is a stored string and JSX will
 * render `href="javascript:…"` as happily as anything else) and a REMOTE
 * card (`remote` — a sibling node's job), because `localhost` on another
 * machine is not this stack. A `down` link stays a link: the probe can be a
 * tick stale after a restart, and the dimmed class already says what it knows.
 */
export function StackChip({ link, remote = false }: { link: StackLink; remote?: boolean }) {
  const cls = stackClass(link) + (remote ? " remote" : "");
  const title = stackTitle(link);
  if (remote || !/^https?:\/\//i.test(link.url || "")) {
    return (
      <span className={cls} title={title}>
        {stackLabel(link)}
      </span>
    );
  }
  return (
    // stopPropagation: the chip sits inside the card's whole-card click
    // target — without it a click opens the link AND the drilldown.
    <a
      className={cls}
      href={link.url}
      title={title}
      target="_blank"
      rel="noreferrer"
      onClick={(e) => e.stopPropagation()}
    >
      {stackLabel(link)}
    </a>
  );
}

/** The card's `.stack` row (band 5, under `.prs`). Renders nothing when the job has no links. */
export function StackChips({ job }: { job: WebJob }) {
  const list = job.stack ?? [];
  if (!list.length) return null;
  const remote = job.owner !== undefined;
  return (
    <div className="stack">
      {list.map((l, i) => (
        <StackChip key={`${l.url}|${i}`} link={l} remote={remote} />
      ))}
    </div>
  );
}
