import type { WebJob, ZeusStatus } from "../../../api/types.ts";

/**
 * zeusPillHTML (app.js:457): one god icon whose COLOR is the sole status
 * indicator — "running" green, "polling" cyan, "question" amber for a pending
 * AskUserQuestion; anything else (including "waiting") falls back to the muted
 * default rather than echoing an arbitrary status string verbatim. The name
 * survives only in the title attribute, for hover and assistive tech.
 */
const ZEUS_STATUS: Record<string, { cls: string; title: string }> = {
  running: { cls: "run", title: "zeus — running" },
  polling: { cls: "polling", title: "zeus — polling" },
  question: { cls: "question", title: "zeus — question" },
};

function ZeusPill({ status }: { status: ZeusStatus }) {
  const s = ZEUS_STATUS[status] ?? { cls: "", title: "zeus — waiting" };
  return (
    <span className={"crew-pill" + (s.cls ? " " + s.cls : "")} title={s.title}>
      ↯
    </span>
  );
}

const CREW_ROLES = ["hephaestus", "athena", "hermes", "other"] as const;
type CrewRole = (typeof CREW_ROLES)[number];

interface RoleCount {
  working: number;
  total: number;
}

// A crew role's god glyph, in its text presentation (VS15 forces monochrome
// rendering, matching the rest of the UI's icon glyphs — ● ⚑ ↻ ⧉ ↺; zeus' ↯
// has no emoji variant, so no VS15 is needed there). "other" has no god and
// keeps its plain text label.
const CREW_ICONS: Partial<Record<CrewRole, string>> = {
  hephaestus: "⚒︎",
  athena: "⚖︎",
  hermes: "✉︎",
};

/**
 * The crew line: a zeus status pill plus one working/total chip per subagent
 * role present in the roster. Port of crewHTML (app.js:475).
 *
 * Renders nothing for an old server payload (no zeus_status, no roster) so the
 * card looks exactly as it did before these fields existed.
 */
export function CrewPills({ job }: { job: WebJob }) {
  const roster = job.roster ?? [];
  if (!job.zeus_status && !roster.length) return null;

  const counts = new Map<CrewRole, RoleCount>();
  for (const r of roster) {
    const role = (CREW_ROLES as readonly string[]).includes(r.role)
      ? (r.role as CrewRole)
      : "other";
    const c = counts.get(role) ?? { working: 0, total: 0 };
    c.total++;
    if (r.working) c.working++;
    counts.set(role, c);
  }

  // Iterated as entries rather than `.filter(has).map(get!)`: a `.has()` filter
  // cannot narrow a later `.get()`, which is the only reason that shape needs a
  // non-null assertion.
  const present = CREW_ROLES.map((role) => [role, counts.get(role)] as const).filter(
    (pair): pair is readonly [CrewRole, RoleCount] => pair[1] !== undefined,
  );
  const chips = present.map(([role, c]) => {
    const icon = CREW_ICONS[role];
    return (
      <span
        key={role}
        className={"crew-chip" + (c.working > 0 ? " active" : "")}
        title={`${role} — ${c.working} of ${c.total} working`}
      >
        {icon ? (
          <>
            <span className="crew-ico">{icon}</span> {c.working}/{c.total}
          </>
        ) : (
          `${role} ${c.working}/${c.total}`
        )}
      </span>
    );
  });

  if (!job.zeus_status && !chips.length) return null;
  return (
    <div className="crew">
      {job.zeus_status ? <ZeusPill status={job.zeus_status} /> : null}
      {chips}
    </div>
  );
}
