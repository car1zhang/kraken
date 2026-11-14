// The PERIODIC view: the rows the daemon fires once a day, each creating and
// starting a job from its stored template, plus the form that adds one.
//
// Built on the DREAM view's recipe (DreamView.tsx): it stays mounted across
// view switches with `hidden`, so a half-typed template in the add form
// survives a trip to the board, and its one poll runs only while the view is
// the active one. Every write goes straight to its client function rather
// than through the board's shared `mutate`, because a refusal here (a taken
// prefix, a template that does not parse) is a sentence carl has to read and
// act on, which belongs in a toast.

import { useState, type ChangeEvent, type FormEvent } from "react";

import {
  addPeriodic,
  fetchPeriodic,
  periodicOp,
  runPeriodic,
} from "../../api/client.ts";
import type { PeriodicAddFields, PeriodicList, WebPeriodic } from "../../api/types.ts";
import { usePoll } from "../../hooks/usePoll.ts";
import { lastFiringLabel, nextDueLabel } from "./periodicText.ts";

const PERIODIC_POLL_MS = 5000;

const EMPTY_FORM: PeriodicAddFields = {
  name: "",
  prefix: "",
  at: "",
  days: "",
  every: "",
  template: "",
};

const TEMPLATE_PLACEHOLDER =
  "---\nrepo: kraken\n---\n" +
  "What the job should do, written like any job spec.\n" +
  "{{date}} becomes the day it fires, {{slug}} the job's name.";

export interface PeriodicViewProps {
  /** Whether PERIODIC is the visible view: drives `hidden` and gates the poll. */
  active: boolean;
  onToast(msg: string): void;
}

const errText = (e: unknown) => (e instanceof Error ? e.message : String(e));

function PeriodicRow({
  row,
  nowMs,
  busy,
  onPause,
  onRun,
  onRemove,
}: {
  row: WebPeriodic;
  nowMs: number;
  busy: boolean;
  onPause(): void;
  onRun(): void;
  onRemove(): void;
}) {
  // Delete is two clicks on the same button. The first only arms it, so a
  // stray click on a row cannot drop it; nothing disarms it on a timer, since
  // the armed label is plain about what the next click does.
  const [armed, setArmed] = useState(false);
  return (
    <div
      className={"pd" + (row.enabled ? "" : " off") + (row.builtin ? " builtin" : "")}
      data-periodic={row.name}
    >
      <div className="pd-h">
        <span className="pd-name">{row.name}</span>
        <span className={"pd-state" + (row.enabled ? " on" : "")}>
          {row.enabled ? "on" : "paused"}
        </span>
        {row.builtin ? <span className="pd-tag">kraken's own</span> : null}
        <span className="spacer" />
        <span
          className="pd-cadence"
          title="when it fires: a local time on certain days, or a fixed interval"
        >
          {row.cadence}
        </span>
      </div>
      <div className="pd-meta">
        <span className="k">job names</span>
        <span className="v">
          <span className="pd-prefix">{row.prefix}</span>
          {row.every ? "-YYYY-MM-DD-HHMM" : "-YYYY-MM-DD"}
        </span>
        <span className="k">last fired</span>
        <span className="v">{lastFiringLabel(row)}</span>
        <span className="k">next</span>
        <span className="v">{nextDueLabel(row, nowMs)}</span>
      </div>
      {row.builtin ? (
        <>
          <div className="pd-note">
            The nightly dream pass. Kraken writes its job text itself, so it can be
            paused and resumed here but not run on demand or deleted.
          </div>
          <div className="pd-ctrl">
            <button type="button" className="wl-btn" disabled={busy} onClick={onPause}>
              {row.enabled ? "pause" : "resume"}
            </button>
          </div>
        </>
      ) : (
        <>
          <div className="pd-ctrl">
            <button type="button" className="wl-btn" disabled={busy} onClick={onPause}>
              {row.enabled ? "pause" : "resume"}
            </button>
            <button
              type="button"
              className="wl-btn"
              disabled={busy}
              onClick={onRun}
              title="create and start one job from the template now, without waiting for the schedule"
            >
              run now
            </button>
            <button
              type="button"
              className={"wl-btn pd-del" + (armed ? " armed" : "")}
              disabled={busy}
              onClick={() => {
                if (!armed) {
                  setArmed(true);
                  return;
                }
                setArmed(false);
                onRemove();
              }}
              title="removes the schedule; jobs it already created stay on the board"
            >
              {armed ? "confirm delete" : "delete"}
            </button>
          </div>
          <details className="pd-tpl">
            <summary>template</summary>
            <pre>{row.template}</pre>
          </details>
        </>
      )}
    </div>
  );
}

export function PeriodicView({ active, onToast }: PeriodicViewProps) {
  const poll = usePoll<PeriodicList>(
    (signal) => fetchPeriodic(signal),
    PERIODIC_POLL_MS,
    { enabled: active },
  );
  // A failed fetch empties the list and says why, as the DREAM panes do:
  // showing the last good rows under an error would claim they are current.
  const rows = poll.error ? [] : (poll.data?.periodic ?? []);
  const loadNote = poll.error ? "couldn't load periodic jobs: " + poll.error.message : "";

  // Names with a write in flight, so a double click on `run now` cannot
  // create two jobs. The refetch after a write is not awaited inside it: the
  // buttons come back as soon as the server has answered the write itself.
  const [busy, setBusy] = useState<Set<string>>(() => new Set());
  const [form, setForm] = useState<PeriodicAddFields>(EMPTY_FORM);
  const [adding, setAdding] = useState(false);

  const withBusy = async (name: string, op: () => Promise<void>) => {
    setBusy((prev) => new Set(prev).add(name));
    try {
      await op();
    } finally {
      setBusy((prev) => {
        const next = new Set(prev);
        next.delete(name);
        return next;
      });
    }
  };

  const runOp = (name: string, op: "pause" | "resume" | "remove") =>
    withBusy(name, async () => {
      try {
        await periodicOp(op, name);
      } catch (e) {
        onToast(`✕ ${op} ${name}: ${errText(e)}`);
        return;
      }
      void poll.refetch();
    });

  const runNow = (name: string) =>
    withBusy(name, async () => {
      try {
        const res = await runPeriodic(name);
        onToast(
          res.started
            ? `started ${res.slug}`
            : `created ${res.slug} as a draft, but it did not start: ${res.note}`,
        );
      } catch (e) {
        onToast(`✕ run ${name}: ${errText(e)}`);
        return;
      }
      void poll.refetch();
    });

  const onSubmit = async (ev: FormEvent<HTMLFormElement>) => {
    ev.preventDefault();
    if (adding) return;
    setAdding(true);
    try {
      await addPeriodic({ ...form });
    } catch (e) {
      // keep what was typed: the usual refusal is one field to fix
      onToast(`✕ add: ${errText(e)}`);
      return;
    } finally {
      setAdding(false);
    }
    setForm(EMPTY_FORM);
    void poll.refetch();
  };

  const field =
    (key: keyof PeriodicAddFields) =>
    (ev: ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => {
      const value = ev.target.value;
      setForm((prev) => ({ ...prev, [key]: value }));
    };

  const nowMs = Date.now();

  return (
    <main className="periodicview" id="periodic" hidden={!active}>
      <section className="dream-pane">
        <div className="dream-panehdr">
          periodic jobs{" "}
          <span className="ct">{rows.length ? `· ${rows.length}` : ""}</span>
        </div>
        <div className="dream-scroll">
          {rows.length ? (
            rows.map((r) => (
              <PeriodicRow
                key={r.name}
                row={r}
                nowMs={nowMs}
                busy={busy.has(r.name)}
                onPause={() => void runOp(r.name, r.enabled ? "pause" : "resume")}
                onRun={() => void runNow(r.name)}
                onRemove={() => void runOp(r.name, "remove")}
              />
            ))
          ) : (
            <div className="dream-empty">
              {loadNote || (poll.data ? "no periodic jobs yet" : "loading…")}
            </div>
          )}
        </div>
      </section>
      <section className="dream-pane">
        <div className="dream-panehdr">add a periodic job</div>
        <form id="periodic-add" className="dream-scroll pd-form" onSubmit={onSubmit}>
          <p className="pd-help">
            Kraken creates a job from the template and starts it on a schedule:
            either at a time of day (this Mac's local time) on the days you pick, or
            every fixed interval. Fill in time and days, or the interval.
          </p>
          <label>
            <span>name</span>
            <input
              name="name"
              value={form.name}
              onChange={field("name")}
              placeholder="nightly-deps"
              autoComplete="off"
            />
          </label>
          <label>
            <span>job name prefix</span>
            <input
              name="prefix"
              value={form.prefix}
              onChange={field("prefix")}
              placeholder="deps (jobs are named deps-2026-11-13)"
              autoComplete="off"
            />
          </label>
          <label>
            <span>time</span>
            <input
              name="at"
              value={form.at}
              onChange={field("at")}
              placeholder="09:00 (24-hour)"
              autoComplete="off"
            />
          </label>
          <label>
            <span>days</span>
            <input
              name="days"
              value={form.days}
              onChange={field("days")}
              placeholder="blank for every day, or weekdays, or mon,wed,fri"
              autoComplete="off"
            />
          </label>
          <label>
            <span>or every</span>
            <input
              name="every"
              value={form.every}
              onChange={field("every")}
              placeholder="30m, 2h, 3d or 1w (leave time and days blank)"
              autoComplete="off"
            />
          </label>
          <label className="tpl">
            <span>template (the job's text)</span>
            <textarea
              name="template"
              value={form.template}
              onChange={field("template")}
              placeholder={TEMPLATE_PLACEHOLDER}
              rows={10}
              spellCheck={false}
            />
          </label>
          <div className="pd-ctrl">
            <button type="submit" className="wl-btn" disabled={adding}>
              add
            </button>
          </div>
        </form>
      </section>
    </main>
  );
}
