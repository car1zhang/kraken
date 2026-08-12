import type { CSSProperties } from "react";

import type { WebJob } from "../../api/types.ts";
import { age, jobColorVar } from "../../lib/format.ts";
import { jobKey } from "../../lib/jobKey.ts";
import { ArtifactCountChip, artifactCountVisible } from "./chips/ArtifactChips.tsx";
import { FeedbackChip, feedbackChipVisible } from "./chips/FeedbackChip.tsx";
import type { BoardActions } from "./BoardView.tsx";

export interface ArchivedCardProps {
  job: WebJob;
  actions: BoardActions;
  /** @deprecated Dead — see JobCardProps.expanded. Kept so App.tsx compiles. */
  expanded?: boolean;
  /** @deprecated See `expanded`. */
  onToggleExpanded?(key: string): void;
}

/**
 * One archived job's card, inside the drawer under DONE. Port of
 * archivedCardHTML (app.js).
 *
 * It follows the same bands as a live card, minus the ones that cannot arise:
 * band 1 identity, band 2 counts, band 3's outcome line, and band 6's
 * unarchive/delete pair. There is no band 5 — an archived job's PRs are not on
 * the wire — and no band 4.
 *
 * `data-slug` is the jobKey, not the bare archive name: nothing reads an
 * archived card's data-slug as a server key (unarchive/delete carry their own,
 * `job.slug`, which is what the server resolves), but a bare slug would make
 * this card and an active job that reused the freed slug collide.
 */
export function ArchivedCard({ job, actions }: ArchivedCardProps) {
  const label = job.display_slug || job.slug;
  const renamed = !!job.display_slug && job.display_slug !== job.slug;
  const key = jobKey(job);
  // band 2 — the same two counts an active card carries, in the same place and
  // for the same reason: both open in the job editor, which is what a click on
  // an archived card already opens, and which renders the artifact list itself.
  // Nothing about an archived job is worth naming on the card that isn't worth
  // naming on a live one.
  const hasChips = artifactCountVisible(job) || feedbackChipVisible(job);
  // The colour SURVIVES archiving. The card is dimmed as a whole by
  // .card.archived's opacity, so the accent bar reads as muted without needing
  // a treatment of its own — same all-or-nothing emission as the live card: no
  // --job-color means the bar falls through to --text-2.
  const jc = jobColorVar(job);

  const openDrilldown = () => {
    // A click that ends a text selection is not a click on the card.
    if (window.getSelection()?.toString()) return;
    // The jobKey, exactly as the vanilla handler passed the card's data-slug:
    // an archived job's key is the synthetic "archive:<slug>" (there is no such
    // job file), and the editor is expected to recognise it and skip the form
    // load — the panels ARE the whole modal for it.
    actions.onOpenEditor(key);
  };

  return (
    <div
      className="card archived"
      style={
        { "--state": "var(--text-2)", ...(jc ? { "--job-color": jc } : {}) } as CSSProperties
      }
      data-slug={key}
      // data-editable="false" is what makes the card CLICKABLE. The open-card
      // route matches on PRESENCE of the attribute and reads the value only to
      // decide whether the editor renders read-only — so "false" still opens.
      //
      // An archived card carried no such attribute at all and so had never been
      // clickable, which was harmless only while it rendered real `.chip`
      // buttons the artifact handler could open directly. It carries a bare ▤
      // COUNT now (ArtifactCountChip), whose entire affordance is "click the
      // card", so without this the archive's artifacts would have no path in
      // the UI at all — 69 archived jobs' worth. Caught in review before it
      // shipped; web/jobkey_test.go's TestArchivedCardIsClickable pins the
      // vanilla end and ArchivedCard.test.tsx pins this one.
      data-editable="false"
      onClick={openDrilldown}
    >
      {/* ---- band 1: IDENTITY */}
      <div className="card-top">
        <span className="slug">{label}</span>
        {renamed ? <span className="state">as {job.slug}</span> : null}
        {/* `■` rather than the old `●`: it pairs with the one glyph the live
            cards still carry (`✓ done`) as a mark for something
            SETTLED — the live active/blocked tokens went bare-word at carl's
            post-gate note, so the square no longer echoes a "blocked" square
            anywhere. It stays in .card-top rather than moving to .meta as the
            live card's did — an archived card has no state to report, only a
            fact about the card itself. */}
        <span className="state">■ archived</span>
      </div>
      <div className="meta">
        <span>{job.repo}</span>
        <span data-age={job.mtime}>{age(job.mtime)}</span>
      </div>

      {/* ---- band 2: CHIPS — counts, not lists */}
      {hasChips ? (
        <div className="chips">
          <ArtifactCountChip job={job} />
          <FeedbackChip job={job} />
        </div>
      ) : null}

      {/* ---- band 3: STATUS (the outcome line is all that can survive archiving) */}
      {job.outcome ? <div className="outcome">→ {job.outcome}</div> : null}

      {/* ---- band 6: ACTIONS */}
      <div className="card-actions">
        <button
          className="btn unarchive"
          data-slug={job.slug}
          onClick={(ev) => {
            ev.stopPropagation();
            actions.onUnarchive(job.slug);
          }}
        >
          ⬆ unarchive
        </button>
        <button
          className="btn danger delete"
          data-slug={job.slug}
          onClick={(ev) => {
            ev.stopPropagation();
            if (
              !window.confirm(
                `delete ${job.slug} forever? the archived job file and its artifact copies are permanently removed.`,
              )
            ) {
              return;
            }
            actions.onDeleteForever(job.slug);
          }}
        >
          ✕ delete forever
        </button>
      </div>
    </div>
  );
}
