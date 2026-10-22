import type { SpawnLink, WebJob } from "../../../api/types.ts";
import type { SteerMode } from "../../../lib/moves.ts";

/**
 * Spawn chips: one chip per cell this job's cell FILED (`↳ <slug>`) and one
 * for the cell that filed this one (`↰ <slug>`), from `webJob.spawned` and
 * `webJob.spawned_by` (internal/api/board.go's spawnIndex). The third row of
 * band 5, under the PRs and the stack links, and the first that names another
 * JOB rather than an external thing — so a chip opens that job the way its
 * card would, instead of a URL.
 *
 * Colour says the other job's STATE, in the board's own vocabulary: working
 * takes --ok, blocked --warn, done the merged-PR tint, draft the neutral
 * border, archived muted. An archived job and a remote card's chips are
 * inert spans: there is no card to open for either.
 */

/** How a click opens the other job: the card's own mapping (JobCard.tsx). */
export interface SpawnOpener {
  onOpenSteer(slug: string, mode: SteerMode): void;
  onOpenEditor(slug: string): void;
}

/**
 * Opens `slug` as clicking its card would, keyed on the state the LINK
 * carries (the board's display state): working → steer, blocked → unblock,
 * done → view, anything else still openable (a draft) → the editor. Exported
 * so App.tsx and JobCard.tsx route through one mapping.
 */
export function openSpawned(slug: string, state: string, o: SpawnOpener): void {
  if (state === "working") o.onOpenSteer(slug, "steer");
  else if (state === "blocked") o.onOpenSteer(slug, "unblock");
  else if (state === "done") o.onOpenSteer(slug, "view");
  else o.onOpenEditor(slug);
}

/** A link whose job can still be opened on this board. */
export function spawnOpenable(l: SpawnLink): boolean {
  return l.state !== "archived";
}

export type SpawnDirection = "child" | "parent";

export function spawnLabel(l: SpawnLink, dir: SpawnDirection): string {
  return `${dir === "child" ? "↳" : "↰"} ${l.slug}`;
}

export function spawnTitle(l: SpawnLink, dir: SpawnDirection): string {
  const what = dir === "child" ? "spawned by this cell" : "spawned this cell";
  return `${l.slug} · ${what} · ${l.state}`;
}

function spawnClass(l: SpawnLink): string {
  const s = ["working", "blocked", "done", "draft", "archived"].includes(l.state) ? l.state : "draft";
  return `spawn-chip spawn-${s}`;
}

/**
 * One chip. `onOpen` is what a click does; without it (a remote card, where
 * the slug names a job on another node's board, or an archived link) the chip
 * is a span that still says which job and in what state.
 */
export function SpawnChip({
  link,
  dir,
  onOpen,
}: {
  link: SpawnLink;
  dir: SpawnDirection;
  onOpen?: (slug: string, state: string) => void;
}) {
  const cls = spawnClass(link);
  const title = spawnTitle(link, dir);
  if (!onOpen || !spawnOpenable(link)) {
    return (
      <span className={`${cls} inert`} title={title}>
        {spawnLabel(link, dir)}
      </span>
    );
  }
  return (
    // stopPropagation: on the card the chip sits inside the whole-card click
    // target — without it a click opens the child AND the parent's drilldown.
    <button
      type="button"
      className={cls}
      title={title}
      onClick={(e) => {
        e.stopPropagation();
        onOpen(link.slug, link.state);
      }}
    >
      {spawnLabel(link, dir)}
    </button>
  );
}

/**
 * The chips of one job, parent first then children, with no wrapper — the
 * caller supplies the `.spawned` row so the card and the steer modal each
 * own exactly one.
 */
export function SpawnChipList({
  job,
  onOpen,
}: {
  job: WebJob;
  onOpen?: (slug: string, state: string) => void;
}) {
  const open = job.owner === undefined ? onOpen : undefined;
  return (
    <>
      {job.spawned_by ? <SpawnChip link={job.spawned_by} dir="parent" onOpen={open} /> : null}
      {(job.spawned ?? []).map((l, i) => (
        <SpawnChip key={`${l.slug}|${i}`} link={l} dir="child" onOpen={open} />
      ))}
    </>
  );
}

/** True when the job carries either key — what decides whether the row exists. */
export function spawnChipsVisible(job: WebJob): boolean {
  return job.spawned_by !== undefined || (job.spawned?.length ?? 0) > 0;
}

/** The card's `.spawned` row (band 5, under `.stack`). Nothing when there are no links. */
export function SpawnChips({ job, opener }: { job: WebJob; opener?: SpawnOpener }) {
  if (!spawnChipsVisible(job)) return null;
  const onOpen = opener ? (slug: string, state: string) => openSpawned(slug, state, opener) : undefined;
  return (
    <div className="spawned">
      <SpawnChipList job={job} onOpen={onOpen} />
    </div>
  );
}
