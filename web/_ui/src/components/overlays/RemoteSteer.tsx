import { useCallback, useEffect, useRef, useState } from "react";

import { fetchNodeJobRaw, postNodeAction, setJobVisibility } from "../../api/client.ts";
import type { WebJob } from "../../api/types.ts";
import { useHTML, useRenderedMD } from "../../hooks/useRenderedMD.ts";
import {
  ownerChipText,
  ownerTurnLabel,
  remoteMode,
  stripFrontmatter,
  VISIBILITIES,
  type Visibility,
} from "../../lib/fleet.ts";
import { loadROI } from "../../lib/format.ts";
import { jobKey } from "../../lib/jobKey.ts";
import { renderMD } from "../../lib/markdown.ts";
import { CrewChips, crewChipsVisible } from "../board/chips/CrewPills.tsx";
import { FeedbackPanel } from "../board/chips/FeedbackPanel.tsx";
import { PRChip } from "../board/chips/PRChips.tsx";
import { QuestionBlock } from "../board/chips/QuestionBlock.tsx";
import { BranchButton } from "../board/chips/BranchButton.tsx";
import { ThreadChips } from "../board/chips/ThreadChips.tsx";
import { DocQuestions } from "./DocQuestions.tsx";
import { FieldSelect } from "./FieldSelect.tsx";
import type { Mutate } from "./Overlay.tsx";
import { LoadDrill, steerHasTokens, ZeusTranscriptPanel } from "./SteerOverlay.tsx";
import { SwatchStrip } from "./SwatchStrip.tsx";
import { TokensPanel } from "./TokensPanel.tsx";

/**
 * The steer modal's fleet halves, kept beside SteerOverlay rather than inside
 * its 1600 lines: the LOCAL modal's three-way visibility control, and the
 * REMOTE body SteerOverlay renders instead of its own when the modal was
 * opened with an owner node (`props.node`). Both are SteerOverlay's — same overlay, same
 * escape priority, same App state — and neither is a second modal.
 */

// ---------------------------------------------------------------- local

const VIS_HINT: Record<Visibility, string> = {
  private: "never leaves this Mac",
  readonly: "every board on the net sees it, nobody else can touch it",
  public:
    "anyone on the net, person or agent, may steer, answer, stop, kill, mark done, submit, archive, recolour and edit it — each action arrives tagged with who sent it",
};

/**
 * A LOCAL job's visibility: a FieldSelect dropdown (the job-colour dropdown's
 * `.colorsel*` skin) offering private, readonly and public, with no empty
 * row. The id and `data-testid="visibility-control"` land on its button, the
 * interactive control; each value's explanation is its row's tooltip and the
 * button's. A pick of a DIFFERENT value posts the one-field route (usable on
 * a live cell, like colour) once through the shared mutation path; the value
 * shown while it is in flight is optimistic and falls back to whatever the
 * refetched board says. Re-picking the current value posts nothing.
 */
export function VisibilityControl({
  slug,
  visibility,
  mutate,
  id = "steer-visibility",
}: {
  slug: string;
  visibility: string | undefined;
  mutate: Mutate;
  /** DOM id — the steer modal's default, the editor passes its own */
  id?: string;
}) {
  const [pick, setPick] = useState<Visibility | null>(null);
  const known = (VISIBILITIES as readonly string[]).includes(visibility ?? "")
    ? (visibility as Visibility)
    : "readonly";
  const current = pick ?? known;
  const choose = async (v: string) => {
    if (v === current || !(VISIBILITIES as readonly string[]).includes(v)) return;
    setPick(v as Visibility);
    await mutate(() => setJobVisibility(slug, v as Visibility));
    setPick(null);
  };
  return (
    <FieldSelect
      id={id}
      testId="visibility-control"
      label="visibility"
      value={current}
      options={[...VISIBILITIES]}
      noEmpty
      titles={VIS_HINT}
      onPick={(v) => void choose(v)}
    />
  );
}

// ---------------------------------------------------------------- remote editor

export interface RemoteEditorBodyProps {
  node: string;
  slug: string;
  job: WebJob | undefined;
  mutate: Mutate;
  onClose: () => void;
}

/**
 * EditorOverlay's REMOTE mode: a remote job's whole file as raw text,
 * loaded through `/api/nodes/job/raw` and saved through the proxy's `save`
 * action with the load's mtime as the optimistic lock (the owner 409s a save
 * over a newer edit). The local editor's FORM is deliberately not reused: it
 * is built on `/api/job/form` and `/api/job/saveform`, neither of which the
 * owner exposes to other nodes — the raw text is the one shape both ends
 * speak. A readonly job's file loads too, but stays read-only: `ro` follows
 * `remoteMode`, and saving is offered for a public job only.
 */
export function RemoteEditorBody({ node, slug, job, mutate, onClose }: RemoteEditorBodyProps) {
  const [content, setContent] = useState<string | null>(null);
  const [mtime, setMtime] = useState<number | string>(0);
  const [editable, setEditable] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [gen, setGen] = useState(0);

  useEffect(() => {
    const ac = new AbortController();
    fetchNodeJobRaw(node, slug, ac.signal)
      .then((r) => {
        if (ac.signal.aborted) return;
        setContent(r.content);
        setMtime(r.mtime);
        setEditable(!!r.editable);
        setDirty(false);
      })
      .catch((e: unknown) => {
        if (ac.signal.aborted) return;
        setNote("could not load: " + (e instanceof Error ? e.message : String(e)));
      });
    return () => ac.abort();
  }, [node, slug, gen]);

  const save = async () => {
    if (content === null) return;
    setBusy(true);
    // the save payload is `{content, mtime}` — /api/job/save's own keys,
    // which the owner's proxy hands it verbatim.
    const res = await mutate(() => postNodeAction(node, slug, "save", { content, mtime }));
    setBusy(false);
    if (res === null) {
      setNote("save failed — see status bar (a newer edit on its owner's machine refuses an older save)");
      return;
    }
    setNote("saved");
    setGen((g) => g + 1); // reload for the fresh mtime
  };

  const owner = job?.owner;
  const ro = !editable || (job ? remoteMode(job) !== "public" : true);

  return (
    <>
      <div className="panel-head remote-head">
        <span className="panel-title" id="editor-title">
          {slug}
        </span>
        {owner ? (
          <span className="ow-chip" data-testid="owner-chip">
            {ownerChipText(owner)}
          </span>
        ) : null}
        <span className="spacer"></span>
        <button
          type="button"
          className="btn"
          id="editor-save"
          disabled={ro || busy || !dirty || content === null}
          onClick={() => void save()}
        >
          save
        </button>
        <button type="button" className="btn ghost" onClick={onClose}>
          close
        </button>
      </div>
      <div className="panel-body remote-editor">
        <textarea
          id="editor-raw"
          className="remote-raw"
          spellCheck={false}
          value={content ?? ""}
          readOnly={ro}
          placeholder={content === null ? "loading…" : ""}
          onChange={(e) => {
            setContent(e.target.value);
            setDirty(true);
          }}
        />
        <div className="answer-note" id="editor-note">
          {ro && content !== null && !note
            ? "read-only — the job has a cell in flight, or its owner has not made it public"
            : note}
        </div>
      </div>
    </>
  );
}

// ---------------------------------------------------------------- remote

export interface RemoteSteerBodyProps {
  /** the owner node's id — the route's `node` */
  node: string;
  slug: string;
  /** the card's freshest board entry; undefined once it left the board */
  job: WebJob | undefined;
  mutate: Mutate;
  onClose: () => void;
  /** opens EditorOverlay in its remote mode on this card */
  onOpenEditor?: (slug: string, node: string) => void;
  /** opens an artifact chip in the viewer — SteerOverlayProps.onOpenArtifact */
  onOpenArtifact(url: string, name: string, jobKey: string): void;
  /**
   * opens one of the job's threads, read through the owner node; `tid` null
   * opens the remote new-thread dialog (the ⑂ ask button)
   */
  onOpenThread(slug: string, tid: string | null, node: string): void;
  /**
   * `board.operator` — the transcript panel's and load drill's label only for
   * a card with no owner; an owned card uses the owner's (ownerTurnLabel)
   */
  operator?: string;
  /** `board.node_id` — this node's own id, for the thread chips' origin badges */
  boardNodeID?: string;
}

/**
 * A remote card's steer modal. It shows what the local modal shows — crew,
 * PRs, threads, zeus' detail and last message, the transcript, the load
 * estimate, the artifacts, the doc questions, the feedback notes, the token
 * spend and the spec — and differs only in what it lets you DO, decided by
 * lib/fleet.ts remoteMode:
 *
 *  - `readonly` (its owner has not opened it to the net): no control but the
 *    threads row's ⑂ ask. Absent rather than disabled, so nothing on screen
 *    offers an action the owner would refuse; the one exception is the
 *    pending question, drawn inside a disabled fieldset so its options stay
 *    readable.
 *  - `public`: the controls (colour, stop, mark done, cancel, submit,
 *    archive, edit, the composer and the question's options), each posting
 *    `/api/nodes/action` through this node's proxy, which the owner re-checks
 *    at request time.
 *
 * In both modes, while the card reports its cell live, the threads row ends
 * in ⑂ ask, which opens ThreadOverlay's remote new-thread dialog (the
 * `thread-ask` action, admitted on readonly and public cards alike — it opens
 * a HEADLESS thread on the owner, never a pane). The panels whose writes are
 * the owner's alone (doc-question answers, feedback notes) render read-only
 * in both modes, and nothing here stamps
 * a `data-mind-*` source: the mental model is this machine's, and this is
 * another machine's content. The account binding is not shown — it names the
 * owner's pool login, and the local panel reads THIS machine's usage.
 *
 * A relayed failure (a 502 such as "did not recognise this node" or
 * "answered non-JSON", or the owner's own 403/404) is an ordinary error: it
 * lands in the status corner through `mutate` and in this modal's note, like
 * any refused local action. There is no 401 branch here — the proxy never
 * relays a sibling's 401 as a 401.
 *
 * Remote markdown (detail, spec) goes through renderMD, which escapes before
 * it transforms — the same argument the local body makes for agent text.
 */
export function RemoteSteerBody({
  node,
  slug,
  job,
  mutate,
  onClose,
  onOpenEditor,
  onOpenArtifact,
  onOpenThread,
  operator,
  boardNodeID,
}: RemoteSteerBodyProps) {
  const mode = job ? remoteMode(job) : "readonly";
  const ro = mode !== "public";
  const [text, setText] = useState("");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [colorPick, setColorPick] = useState<string | null>(null);
  const [specOpen, setSpecOpen] = useState(false);
  // the owner's raw job file, for the spec drawer; null until it lands and
  // for good when it fails (no drawer at all then)
  const [spec, setSpec] = useState<string | null>(null);

  // a card that has left the board renders only its note and asks the owner
  // for nothing
  const present = !!job;
  useEffect(() => {
    if (!present) return;
    const ac = new AbortController();
    fetchNodeJobRaw(node, slug, ac.signal)
      .then((r) => {
        if (!ac.signal.aborted) setSpec(stripFrontmatter(r.content ?? ""));
      })
      .catch(() => {
        // a 403/404/502 renders no drawer; the status corner is not told,
        // since nothing the reader did failed
      });
    return () => ac.abort();
  }, [node, slug, present]);

  // The local modal's force-close rule, copied: the card changing state under
  // the modal (a done on the owner's board, a kill) makes every control here
  // describe a job that no longer exists in that shape.
  const openState = useRef<string | undefined>(undefined);
  const captured = useRef(false);
  useEffect(() => {
    if (!captured.current) {
      if (!job) return;
      captured.current = true;
      openState.current = job.state;
      return;
    }
    if (!job || job.state !== openState.current) onClose();
  }, [job, onClose]);

  /**
   * Every control's one path. Resolves true on success; a refusal (the owner
   * made it readonly since the last poll, the node is down) lands in the
   * status corner via `mutate` and in this modal's note.
   */
  const act = useCallback(
    async (action: string, payload: Record<string, unknown> = {}): Promise<boolean> => {
      setBusy(true);
      const res = await mutate(() => postNodeAction(node, slug, action, payload));
      setBusy(false);
      if (res === null) {
        setNote(`${action} failed — see status bar`);
        return false;
      }
      setNote("");
      return true;
    },
    [mutate, node, slug],
  );

  const live = !!job?.live;
  const liveCol = live ? (job?.health ? "blocked" : job?.state) : null;
  const working = liveCol === "working";
  const isLive = liveCol === "working" || liveCol === "blocked";
  const isDraft = !live && job?.state === "draft";
  const settled = !live;

  const send = async () => {
    const body = text.trim();
    if (!body) {
      setNote("message is empty");
      return;
    }
    if (await act("steer", { text: body })) {
      setText("");
      onClose();
    }
  };

  const kill = async () => {
    if (
      !window.confirm(
        `kill ${slug} on ${job?.owner ? ownerChipText(job.owner) : node}? its cell is torn down and the job returns to draft.`,
      )
    ) {
      return;
    }
    if (await act("kill")) onClose();
  };

  // QuestionBlock's contract: resolve to the failure (it re-enables its
  // options) or null. `text` is the 1-based option index, as the local route.
  const answer = async (_s: string, optionIndex: number): Promise<Error | null> => {
    if (ro) return new Error("readonly");
    const ok = await act("question", { text: String(optionIndex) });
    return ok ? null : new Error("answer failed");
  };

  const chooseColor = async (v: string) => {
    setColorPick(v);
    await act("color", { color: v });
    setColorPick(null);
  };

  const owner = job?.owner;
  const prs = job?.prs ?? [];
  const question = job?.question;
  const artifacts = job?.artifacts ?? [];
  const threads = job?.threads ?? [];
  const detailHTML = useRenderedMD(job?.detail);
  const specHTML = useHTML(spec !== null ? renderMD(spec) : "");
  const loadDerived = loadROI(job?.load);
  const key = job ? jobKey(job) : node + "/" + slug;
  const label = job ? job.display_slug || job.slug : slug;
  // who the owner's own turns are labelled as — see ownerTurnLabel
  const ownerLabel = ownerTurnLabel(job?.owner, operator);
  const disabledTitle = ro
    ? "this cell is readonly — its owner has not opened it to the net"
    : undefined;

  if (!job) {
    return (
      <div className="panel-body answer-body remote-steer" data-remote-mode={mode}>
        <div className="answer-note">this card is no longer on the board</div>
      </div>
    );
  }

  return (
    <>
      <div className="panel-head remote-head" data-remote-mode={mode}>
        <span className="panel-title" id="steer-title">
          {slug}
        </span>
        {owner ? (
          <span
            className={"ow-chip" + (owner.online ? "" : " offline")}
            data-testid="owner-chip"
            title={owner.online ? "runs on " + ownerChipText(owner) : "owner offline — last known state"}
          >
            {ownerChipText(owner)}
          </span>
        ) : null}
        {mode === "public" ? <span className="pub-chip">public</span> : null}
        {ro ? null : (
          <span className="f-swatches compact" id="steer-swatches" title="job color">
            <SwatchStrip
              name="steer-color"
              value={colorPick ?? job.color}
              onPick={(v) => void chooseColor(v)}
              compact
            />
          </span>
        )}
        <span className="spacer"></span>
        {/* every control below is public-only: a readonly card offers none */}
        {!ro && working ? (
          <button
            type="button"
            className="btn"
            id="steer-stop"
            disabled={busy}
            title="interrupt zeus' turn"
            onClick={() => void act("stop")}
          >
            ■ stop
          </button>
        ) : null}
        {!ro && isLive ? (
          <button
            type="button"
            className="btn steer-done"
            id="steer-markdone"
            disabled={busy}
            title="mark this job done"
            onClick={() => void act("done").then((ok) => ok && onClose())}
          >
            ✓ mark done
          </button>
        ) : null}
        {!ro && isLive ? (
          <button
            type="button"
            className="btn ghost danger"
            id="steer-kill"
            disabled={busy}
            onClick={() => void kill()}
          >
            ✕ cancel job
          </button>
        ) : null}
        {!ro && isDraft ? (
          <button
            type="button"
            className="btn"
            id="steer-submit"
            disabled={busy}
            title="start this job's cell on its owner's machine"
            onClick={() => void act("submit").then((ok) => ok && onClose())}
          >
            ▶ submit
          </button>
        ) : null}
        {!ro && settled ? (
          <button
            type="button"
            className="btn"
            id="steer-archive"
            disabled={busy}
            title="archive this job on its owner's machine"
            onClick={() => void act("archive").then((ok) => ok && onClose())}
          >
            archive
          </button>
        ) : null}
        {!ro && settled ? (
          <button
            type="button"
            className="btn"
            id="steer-edit"
            disabled={busy || !onOpenEditor}
            title="edit the job file on its owner's machine"
            onClick={() => onOpenEditor?.(slug, node)}
          >
            ✎ edit job
          </button>
        ) : null}
      </div>
      <div className="panel-body answer-body remote-steer" data-remote-mode={mode}>
        {/* The local body's containers, in its order and with its ids and
            classes, so style.css dresses both alike — but each is MOUNTED
            only when it has something to show, rather than hidden. */}
        <div className="steer-context" id="steer-context">
          {crewChipsVisible(job) ? (
            <div className="crew" id="steer-crew">
              <CrewChips job={job} />
            </div>
          ) : null}
          {prs.length ? (
            <div className="prs" id="steer-prs">
              {prs.map((pr, i) => (
                <PRChip key={`${pr.url}|${i}`} pr={pr} />
              ))}
            </div>
          ) : null}
          {threads.length ? (
            // ThreadChips appends ⑂ ask only while job.live
            <div className="threads" id="steer-threads">
              <ThreadChips
                job={job}
                withBranch={!!job.live}
                branchLabel="⑂ ask"
                boardNodeID={boardNodeID}
                onOpenThread={(s, tid) => onOpenThread(s, tid, node)}
              />
            </div>
          ) : job.live ? (
            // a live card with no threads yet: ⑂ ask alone, in its own row,
            // since #steer-threads is mounted only when there are threads
            <div className="threads" id="steer-ask">
              <BranchButton
                job={job}
                label="⑂ ask"
                onOpenThread={(s, tid) => onOpenThread(s, tid, node)}
              />
            </div>
          ) : null}
          {job.detail ? (
            <div
              className="steer-detail"
              id="steer-detail"
              // renderMD escapes before transforming, so the owner's text is inert
              dangerouslySetInnerHTML={detailHTML}
            />
          ) : null}
          {job.last_sent ? (
            <div className="last-sent" id="steer-lastsent">
              {/* the OWNER's last message to its zeus, not this viewer's */}
              owner→zeus: {job.last_sent}
            </div>
          ) : null}
          {/* the prompts in this transcript were typed on the OWNER's machine,
              so they are labelled with its operator (see ownerLabel) */}
          <ZeusTranscriptPanel
            slug={slug}
            operator={ownerLabel}
            node={node}
          />
          {job.load && loadDerived ? (
            <LoadDrill
              load={job.load}
              roi={loadDerived.roi}
              band={loadDerived.band}
              // the owner's operator is who would answer this block
              operator={ownerLabel}
            />
          ) : null}
          {artifacts.length ? (
            // the urls are this node's proxy urls (the server rewrote them),
            // and `key` is the owner-qualified jobKey the pager resolves by
            <div className="chips" id="steer-chips">
              {artifacts.map((a) => {
                const name = `${label}/${a.name}`;
                return (
                  <button
                    key={a.url}
                    className="chip"
                    data-url={a.url}
                    data-key={key}
                    data-name={name}
                    onClick={() => onOpenArtifact(a.url, name, key)}
                  >
                    ▤ {a.name}
                  </button>
                );
              })}
            </div>
          ) : null}
          {job.doc_questions?.length ? (
            <div className="steer-docq" id="steer-docq">
              <DocQuestions job={job} mutate={mutate} readOnly />
            </div>
          ) : null}
          {job.feedback?.length ? (
            <div className="steer-feedback" id="steer-feedback">
              <FeedbackPanel
                job={job}
                readOnly
                onSaveFeedback={async () => new Error("read-only")}
                onDeleteFeedback={() => {}}
              />
            </div>
          ) : null}
          {steerHasTokens(job) ? (
            <div className="steer-tokens" id="steer-tokens">
              <TokensPanel job={job} />
            </div>
          ) : null}
        </div>

        {question ? (
          // The board's own QuestionBlock, answering through the proxy. A
          // disabled <fieldset> is what disables every option button for a
          // readonly card without a prop on a shared component — the one
          // place a readonly card draws a control, so the options stay legible.
          <fieldset
            className="steer-actions remote-question"
            id="steer-actions"
            disabled={ro}
            title={disabledTitle}
          >
            <QuestionBlock key={question.tool_use_id} job={job} onAnswerQuestion={answer} />
          </fieldset>
        ) : null}

        {/* absent too for a frontmatter-only job: a drawer that unfolds to
            nothing reads as a failed load */}
        {spec !== null && spec.trim() !== "" ? (
          <div className="steer-spec" id="steer-spec">
            <button
              className="steer-spec-head"
              id="steer-spec-toggle"
              onClick={() => setSpecOpen((v) => !v)}
            >
              <span className="tri">{specOpen ? "▾" : "▸"}</span> spec
            </button>
            <div
              className="steer-spec-body"
              id="steer-spec-body"
              hidden={!specOpen}
              dangerouslySetInnerHTML={specHTML}
            />
          </div>
        ) : null}

        {!ro && isLive ? (
          <>
            <textarea
              id="steer-text"
              className="remote-composer"
              rows={4}
              value={text}
              placeholder={`message to ${owner ? ownerChipText(owner) : node}'s zeus — arrives tagged with who sent it`}
              onChange={(e) => setText(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                  e.preventDefault();
                  void send();
                }
              }}
            />
            <button
              type="button"
              className="btn btn-big"
              id="steer-send"
              disabled={busy}
              onClick={() => void send()}
            >
              send
            </button>
          </>
        ) : null}
        {ro ? null : (
          <div className="answer-note" id="steer-note">
            {note}
          </div>
        )}
      </div>
    </>
  );
}
