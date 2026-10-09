import { useCallback, type ReactNode, type RefObject } from "react";
import {
  CMD_ACTIVE_RE,
  CMD_CHIPS,
  insertIntoTextarea,
  useCmdHighlight,
} from "../../hooks/useCmdHighlight.ts";

// The composer shared by the steer modal and the job editor: the one-tap
// /info /aim /do /pr /handoff chip row above a text-transparent <textarea>
// layered over its mirrored highlight backdrop.
//
// One component rather than two copies because style.css unions the two id
// pairs (#steer-text/#steer-text-backdrop, #editor-text/#editor-text-backdrop)
// for exactly the same reason: splitting the metrics is what lets the two box
// models drift apart and ghosts the glyphs. Same argument one level up — two
// hand-written copies of this JSX would drift the same way.
//
// The ids are props, not derived, because they are the CSS contract.

export interface CmdComposerProps {
  /** e.g. "steer-text" — the wrap/backdrop ids are this + "-wrap"/"-backdrop". */
  id: string;
  /** The chip row's own id, e.g. "steer-cmd-chips". */
  chipsId: string;
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  rows?: number;
  readOnly?: boolean;
  disabled?: boolean;
  spellCheck?: boolean;
  /** ⌘/Ctrl+Enter — send in steer, queue in the editor. */
  onSubmit?: () => void;
  /** Hides the chip row entirely (the editor does this when read-only). */
  hideChips?: boolean;
  /** Wraps the textarea, e.g. the editor's <label>. */
  wrapper?: (composer: ReactNode) => ReactNode;
  /**
   * Mirror of the internal textarea ref, for the ONE caller that has to splice
   * text in from outside a keystroke: the editor's image upload, which lands
   * `[image: <path>]` at the cursor when it resolves.
   */
  textareaRef?: RefObject<HTMLTextAreaElement | null>;
}

export function CmdComposer({
  id,
  chipsId,
  value,
  onChange,
  placeholder,
  rows,
  readOnly,
  disabled,
  spellCheck,
  onSubmit,
  hideChips,
  wrapper,
  textareaRef,
}: CmdComposerProps) {
  const hl = useCmdHighlight(value);

  const setTextarea = useCallback(
    (el: HTMLTextAreaElement | null) => {
      hl.textareaRef.current = el;
      if (textareaRef) textareaRef.current = el;
    },
    [hl.textareaRef, textareaRef],
  );

  const insertCmd = useCallback(
    (cmd: string) => {
      const ta = hl.textareaRef.current;
      if (!ta || ta.disabled || ta.readOnly) return;
      insertIntoTextarea(ta, cmd + " ", onChange, { inlineGap: true });
      ta.focus();
    },
    [hl.textareaRef, onChange],
  );

  const composer = (
    <div className={hl.wrapClassName} id={`${id}-wrap`}>
      <div
        className="cmd-hl-backdrop"
        id={`${id}-backdrop`}
        aria-hidden="true"
        ref={hl.backdropRef}
        dangerouslySetInnerHTML={{ __html: hl.backdropHTML }}
      />
      <textarea
        id={id}
        ref={setTextarea}
        value={value}
        rows={rows}
        placeholder={placeholder}
        readOnly={readOnly}
        disabled={disabled}
        spellCheck={spellCheck}
        onChange={(e) => onChange(e.target.value)}
        onScroll={hl.onScroll}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
            e.preventDefault();
            // stopPropagation, not just preventDefault: the editor wraps this
            // composer in a #editor-form div carrying its own ⌘⏎ handler (so
            // the shortcut works from the repo/base/model fields too), and a
            // bubbled ⌘⏎ ran BOTH — two doSubmit calls in one dispatch, both
            // seeing the same stale `busy`, so every keyboard start posted
            // /api/queue twice and the loser's 409 painted a false "start
            // refused" over a cell that was in fact coming up.
            e.stopPropagation();
            onSubmit?.();
          }
        }}
      />
    </div>
  );

  return (
    <>
      <div className="cmd-chips" id={chipsId} hidden={hideChips}>
        {CMD_CHIPS.map((cmd) => {
          const active = CMD_ACTIVE_RE[cmd].test(value);
          return (
            <button
              key={cmd}
              type="button"
              className={
                `cmd-chip cmd-chip-${cmd.slice(1)}` + (active ? " active" : "")
              }
              // Never let the button take focus. On mobile that dismisses and
              // then reopens the keyboard as focus bounces button → textarea;
              // preventing the default mousedown keeps the caret and the
              // selection exactly where they were the whole time.
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => insertCmd(cmd)}
            >
              {cmd}
            </button>
          );
        })}
      </div>
      {wrapper ? wrapper(composer) : composer}
    </>
  );
}
