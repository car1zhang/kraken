import type { WebArtifact } from "../../../api/types.ts";

export interface ArtifactChipsProps {
  artifacts: WebArtifact[] | undefined;
  /**
   * The job's jobKey() — NEVER a bare slug, or an archived job and an active
   * one that reused its freed slug would share one expand/collapse toggle.
   */
  jobKey: string;
  /** Prefixes each chip's name; differs from the slug for a renamed archive. */
  label: string;
  expanded: boolean;
  onToggleExpanded(jobKey: string): void;
  onOpenArtifact(url: string, name: string): void;
}

/**
 * A job's artifact chips, collapsing to the first 3 plus a "+N more" toggle.
 * Port of chipsHTML (app.js:428).
 *
 * The toggle carries `.chip chip-more` — both classes, because it needs
 * `.chip`'s size/padding — which in the vanilla delegated handler meant its
 * branch had to run BEFORE the generic `.chip` branch or it would try to open
 * an artifact with an undefined url. Here each button owns its own handler, so
 * the ordering hazard is gone; the classes stay for the paint.
 */
export function ArtifactChips({
  artifacts,
  jobKey,
  label,
  expanded,
  onToggleExpanded,
  onOpenArtifact,
}: ArtifactChipsProps) {
  const list = artifacts ?? [];
  if (!list.length) return null;
  const shown = expanded ? list : list.slice(0, 3);
  return (
    <>
      {shown.map((a) => (
        <button
          key={a.name}
          className="chip"
          data-url={a.url}
          data-name={`${label}/${a.name}`}
          onClick={(ev) => {
            ev.stopPropagation();
            onOpenArtifact(a.url, `${label}/${a.name}`);
          }}
        >
          ▤ {a.name}
        </button>
      ))}
      {list.length > 3 ? (
        <button
          className="chip chip-more"
          data-key={jobKey}
          onClick={(ev) => {
            ev.stopPropagation();
            onToggleExpanded(jobKey);
          }}
        >
          {expanded ? "less" : `+${list.length - 3} more`}
        </button>
      ) : null}
    </>
  );
}
