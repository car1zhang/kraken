import { useCallback, useEffect, useRef, useState } from "react";

import {
  fetchNodeJobRaw,
  fetchNodeRead,
  postNodeAction,
  setJobVisibility,
} from "../../api/client.ts";
import type { NodeRead, WebJob } from "../../api/types.ts";
import { usePoll } from "../../hooks/usePoll.ts";
import {
  ownerChipText,
  remoteMode,
  VISIBILITIES,
  type Visibility,
} from "../../lib/fleet.ts";
import { PRChip } from "../board/chips/PRChips.tsx";
import { QuestionBlock } from "../board/chips/QuestionBlock.tsx";
import type { Mutate } from "./Overlay.tsx";
import { SwatchStrip } from "./SwatchStrip.tsx";

/**
 * The steer modal's fleet halves, kept beside SteerOverlay rather than inside
 * its 1600 lines: the LOCAL modal's three-way visibility control, and the
 * REMOTE body SteerOverlay renders instead of its own when the card it was
 * opened on carries an owner. Both are SteerOverlay's — same overlay, same
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
 * A LOCAL job's visibility: three buttons, the current one pressed. A pick
 * posts the one-field route (usable on a live cell, like colour) through the
 * shared mutation path; the pressed state shown while it is in flight is
 * optimistic and falls back to whatever the refetched board says.
 */
export function VisibilityControl({
  slug,
  visibility,
  mutate,
  id = "steer-visibility",
  compact = false,
}: {
  slug: string;
  visibility: string | undefined;
  mutate: Mutate;
  /** DOM id — the steer modal's default, the editor passes its own */
  id?: string;
  /** omit the explanatory line (the editor's one-line field row); the
   *  explanation stays on each option's tooltip */
  compact?: boolean;
}) {
  const [pick, setPick] = useState<Visibility | null>(null);
  const known = (VISIBILITIES as readonly string[]).includes(visibility ?? "")
    ? (visibility as Visibility)
    : "readonly";
  const current = pick ?? known;
  const choose = async (v: Visibility) => {
    if (v === current) return;
    setPick(v);
    await mutate(() => setJobVisibility(slug, v));
    setPick(null);
  };
  return (
    <div
      className={"steer-vis" + (compact ? " compact" : "")}
      id={id}
      data-testid="visibility-control"
    >
      <span className="vis-label">visibility</span>
      <span className="vis-seg" role="radiogroup" aria-label="visibility">
        {VISIBILITIES.map((v) => (
          <button
            key={v}
            type="button"
            role="radio"
            aria-checked={v === current}
            className={"vis-opt" + (v === current ? " on" : "")}
            data-visibility={v}
            title={VIS_HINT[v]}
            onClick={() => void choose(v)}
          >
            {v}
          </button>
        ))}
      </span>
      {compact ? null : <span className="vis-hint">{VIS_HINT[current]}</span>}
    </div>
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
 * EditorOverlay's REMOTE mode: a remote PUBLIC job's whole file as raw text,
 * loaded through `/api/nodes/job/raw` and saved through the proxy's `save`
 * action with the load's mtime as the optimistic lock (the owner 409s a save
 * over a newer edit). The local editor's FORM is deliberately not reused: it
 * is built on `/api/job/form` and `/api/job/saveform`, neither of which the
 * owner exposes to other nodes — the raw text is the one shape both ends
 * speak. A readonly job answers the load with 403, shown as the note.
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

const READ_POLL_MS = 5000;
const READ_BLOCKS = 5;

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
}

/**
 * A remote card's steer modal. Two modes, decided by lib/fleet.ts remoteMode:
 *
 *  - `readonly` (its owner has not opened it to the net): the card's state,
 *    detail and pending question are shown, and EVERY control
 *    is drawn disabled — so the modal says what could be done and that it may
 *    not, rather than hiding the controls and leaving it ambiguous.
 *  - `public`: the same controls, each one posting `/api/nodes/action`
 *    through this node's proxy, which the owner re-checks at request time.
 *
 * What a remote card can NOT offer at all, whatever its mode: the tmux pane
 * (on the owner's Mac), threads, feedback, uploads and the draft autosave
 * (all local stores), and artifacts: artifacts stay on the owner's board — a
 * remote card carries none.
 *
 * A relayed failure (a 502 such as "did not recognise this node" or
 * "answered non-JSON", or the owner's own 403/404) is an ordinary error: it
 * lands in the status corner through `mutate` and in this modal's note, like
 * any refused local action. There is no 401 branch here — the proxy never
 * relays a sibling's 401 as a 401.
 *
 * Remote text (detail, transcript) is rendered as PLAIN TEXT, never through
 * the markdown renderer's innerHTML: it is another machine's content.
 */
export function RemoteSteerBody({
  node,
  slug,
  job,
  mutate,
  onClose,
  onOpenEditor,
}: RemoteSteerBodyProps) {
  const mode = job ? remoteMode(job) : "readonly";
  const ro = mode !== "public";
  const [text, setText] = useState("");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [colorPick, setColorPick] = useState<string | null>(null);

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

  // the transcript tail, public + live only — readonly answers 403 by design
  const read = usePoll<NodeRead>(
    (signal) => fetchNodeRead(node, slug, READ_BLOCKS, signal),
    READ_POLL_MS,
    { enabled: mode === "public" && isLive },
  );

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
  const disabledTitle = ro
    ? "this cell is readonly — its owner has not opened it to the net"
    : undefined;

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
        {ro ? (
          <button type="button" className="btn ghost" disabled title={disabledTitle}>
            colour
          </button>
        ) : (
          <span className="f-swatches compact" id="steer-swatches" title="job color">
            <SwatchStrip
              name="steer-color"
              value={colorPick ?? job?.color}
              onPick={(v) => void chooseColor(v)}
              compact
            />
          </span>
        )}
        <span className="spacer"></span>
        {working ? (
          <button
            type="button"
            className="btn"
            id="steer-stop"
            disabled={ro || busy}
            title={disabledTitle ?? "interrupt zeus' turn"}
            onClick={() => void act("stop")}
          >
            ■ stop
          </button>
        ) : null}
        {isLive ? (
          <button
            type="button"
            className="btn steer-done"
            id="steer-markdone"
            disabled={ro || busy}
            title={disabledTitle ?? "mark this job done"}
            onClick={() => void act("done").then((ok) => ok && onClose())}
          >
            ✓ mark done
          </button>
        ) : null}
        {isLive ? (
          <button
            type="button"
            className="btn ghost danger"
            id="steer-kill"
            disabled={ro || busy}
            title={disabledTitle}
            onClick={() => void kill()}
          >
            ✕ cancel job
          </button>
        ) : null}
        {isDraft ? (
          <button
            type="button"
            className="btn"
            id="steer-submit"
            disabled={ro || busy}
            title={disabledTitle ?? "start this job's cell on its owner's machine"}
            onClick={() => void act("submit").then((ok) => ok && onClose())}
          >
            ▶ submit
          </button>
        ) : null}
        {settled ? (
          <button
            type="button"
            className="btn"
            id="steer-archive"
            disabled={ro || busy}
            title={disabledTitle ?? "archive this job on its owner's machine"}
            onClick={() => void act("archive").then((ok) => ok && onClose())}
          >
            archive
          </button>
        ) : null}
        {settled ? (
          <button
            type="button"
            className="btn"
            id="steer-edit"
            disabled={ro || busy || !onOpenEditor}
            title={disabledTitle ?? "edit the job file on its owner's machine"}
            onClick={() => onOpenEditor?.(slug, node)}
          >
            ✎ edit job
          </button>
        ) : null}
      </div>
      <div className="panel-body answer-body remote-steer" data-remote-mode={mode}>
        {!job ? (
          <div className="answer-note">this card is no longer on the board</div>
        ) : null}
        <div className="steer-context" id="steer-context">
          <div className="remote-idrow">
            {job?.repo ? <span>{job.repo}</span> : null}
            {job?.cell ? <span>{job.cell}</span> : null}
            <span>{liveCol ?? job?.state ?? ""}</span>
          </div>
          {prs.length ? (
            <div className="prs" id="steer-prs">
              {prs.map((pr, i) => (
                <PRChip key={`${pr.url}|${i}`} pr={pr} />
              ))}
            </div>
          ) : null}
          {job?.detail ? (
            <div className="steer-detail remote-text" id="steer-detail">
              {job.detail}
            </div>
          ) : null}
          {job?.last_sent ? (
            <div className="last-sent" id="steer-lastsent">
              {/* the OWNER's last message to its zeus, not this viewer's */}
              owner→zeus: {job.last_sent}
            </div>
          ) : null}
          {read.data?.blocks?.length ? (
            <div className="remote-read" id="steer-remote-read">
              {read.data.blocks.map((b, i) => (
                <div key={i} className={"remote-block " + b.kind}>
                  <span className="who">{b.from || b.kind}</span>
                  <span className="remote-text">{b.text}</span>
                </div>
              ))}
            </div>
          ) : null}
        </div>

        {job && question ? (
          // The board's own QuestionBlock, answering through the proxy. A
          // disabled <fieldset> is what disables every option button for a
          // readonly card without a prop on a shared component.
          <fieldset
            className="steer-actions remote-question"
            id="steer-actions"
            disabled={ro}
            title={disabledTitle}
          >
            <QuestionBlock key={question.tool_use_id} job={job} onAnswerQuestion={answer} />
          </fieldset>
        ) : null}

        {isLive || ro ? (
          <>
            <textarea
              id="steer-text"
              className="remote-composer"
              rows={4}
              value={text}
              disabled={ro || !isLive}
              placeholder={
                ro
                  ? "this cell is readonly — its owner has not opened it to the net"
                  : `message to ${owner ? ownerChipText(owner) : node}'s zeus — arrives tagged with who sent it`
              }
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
              disabled={ro || !isLive || busy}
              title={disabledTitle}
              onClick={() => void send()}
            >
              send
            </button>
          </>
        ) : null}
        <div className="answer-note" id="steer-note">
          {note}
        </div>
      </div>
    </>
  );
}
