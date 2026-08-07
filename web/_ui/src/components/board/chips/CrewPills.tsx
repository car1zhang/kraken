import type { WebJob, ZeusStatus } from "../../../api/types.ts";

/**
 * zeusPillHTML (app.js): one god icon whose COLOR is the sole status
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
  names: string[];
}

// A crew role's icon, in its text presentation (VS15 forces monochrome
// rendering, matching the rest of the UI's icon glyphs — ● ⚑ ↻ ⧉ ↺; zeus' ↯
// has no emoji variant, so no VS15 is needed there). "other" takes a plain
// `?` rather than a god glyph (carl's adjustment round — it used to print
// the word "other", the row's one text label): an unknown role is a
// question, not a deity. The role name still travels in the chip's title.
const CREW_ICONS: Record<CrewRole, string> = {
  hephaestus: "⚒︎",
  athena: "⚖︎",
  hermes: "✉︎",
  other: "?",
};

// Most teammate names a crew chip's tooltip lists before it summarises the
// remainder — see the cap's reasoning in CrewChips.
const CREW_TITLE_MAX = 12;

/** Whether band 2 gets any crew tokens at all — see CrewChips. */
export function crewChipsVisible(job: WebJob): boolean {
  return !!job.zeus_status || (job.roster ?? []).length > 0;
}

/**
 * The crew tokens: a zeus status pill plus one working/total chip per subagent
 * role present in the roster, each chip led by its god's icon in place of the
 * role name (the name moves to the title). Port of crewChipsHTML (app.js).
 *
 * Bare chips with NO wrapper, so the same markup fills both the card's unified
 * band-2 `.chips` row and the modal's static `#steer-crew` — which carries
 * `.crew` itself, exactly as `#steer-threads` carries `.threads`. There is no
 * `.crew` div here any more: it only ever existed to inject a wrapper into a
 * container that nothing styled, and on a card the tokens render bare among
 * the branches and the counts.
 *
 * Renders nothing for an old server payload (no zeus_status, no roster) so the
 * card looks exactly as it did before these fields existed.
 *
 * The chip's title also NAMES every agent counted in it. That used to be a
 * second block on the card — a `.roster` row per teammate restating what the
 * chips had just counted, the card's only outright duplication. The chips are
 * the version that survives: they fit on one line whatever the crew size, and
 * nowhere else ever showed the names, so a hover keeps them reachable.
 */
export function CrewChips({ job }: { job: WebJob }) {
  const roster = job.roster ?? [];
  if (!job.zeus_status && !roster.length) return null;

  const counts = new Map<CrewRole, RoleCount>();
  for (const r of roster) {
    const role = (CREW_ROLES as readonly string[]).includes(r.role)
      ? (r.role as CrewRole)
      : "other";
    const c = counts.get(role) ?? { working: 0, total: 0, names: [] };
    c.total++;
    if (r.working) c.working++;
    // `?? ""` per FIELD, not on the joined string: every other field the card
    // renders degrades a missing value to blank, and composing first would make
    // this the one place that prints the literal "undefined". The wire says both
    // are always present (board.go carries no omitempty on them) — this is what
    // that contract slipping looks like if it ever does.
    c.names.push(`${r.working ? "●" : "○"} ${r.name ?? ""} (${r.model ?? ""})`);
    counts.set(role, c);
  }

  // Iterated as entries rather than `.filter(has).map(get!)`: a `.has()` filter
  // cannot narrow a later `.get()`, which is the only reason that shape needs a
  // non-null assertion.
  const present = CREW_ROLES.map((role) => [role, counts.get(role)] as const).filter(
    (pair): pair is readonly [CrewRole, RoleCount] => pair[1] !== undefined,
  );
  const chips = present.map(([role, c]) => {
    // Capped: a native tooltip does not scroll, so a large hephaestus fleet
    // would render a name list taller than the window with no way to see its
    // end. The on-card `.roster` block this replaced was unbounded too, but it
    // at least wrapped inside the column.
    const shown = c.names.slice(0, CREW_TITLE_MAX);
    const rest = c.names.length - shown.length;
    const body = rest > 0 ? [...shown, `…and ${rest} more`] : shown;
    return (
      <span
        key={role}
        className={"crew-chip" + (c.working > 0 ? " active" : "")}
        title={`${role} — ${c.working} of ${c.total} working\n${body.join("\n")}`}
      >
        {/* per-role class so the sheet can size each glyph individually —
            ⚖︎ and ✉︎ draw visibly smaller than ⚒︎ inside one em-box, so a
            single font-size cannot make the row read as one icon set */}
        <span className={`crew-ico crew-ico-${role}`}>{CREW_ICONS[role]}</span>{" "}
        {c.working}/{c.total}
      </span>
    );
  });

  if (!job.zeus_status && !chips.length) return null;
  return (
    <>
      {job.zeus_status ? <ZeusPill status={job.zeus_status} /> : null}
      {chips}
    </>
  );
}
