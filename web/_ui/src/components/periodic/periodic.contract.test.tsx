// periodic.contract.test.tsx — athena-logic's BLIND tests for 50-contract.md
// (kraken-periodic-jobs), Slice 5: the four client calls, periodicText's two
// labels, VIEWS, and PeriodicView's rendering, row buttons and add form.
//
// Written from the frozen contract alone; PeriodicView.tsx, periodicText.ts,
// api/client.ts, api/types.ts and hooks/useHashRoute.ts were never opened.
// Dimension: LOGIC (stated happy paths and invariants). Every module-scope
// identifier declared here is prefixed `contract`.
//
// Isolation: the client module's four periodic calls are mocked for the view
// tests; the client tests call the real module (vi.importActual) against a
// stubbed global fetch. Nothing reaches a live serve.

import { act, fireEvent, render, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../api/client.ts", async (importOriginal) => {
  const orig = (await importOriginal()) as Record<string, unknown>;
  return {
    ...orig,
    fetchPeriodic: vi.fn(),
    addPeriodic: vi.fn(),
    periodicOp: vi.fn(),
    runPeriodic: vi.fn(),
  };
});

import * as client from "../../api/client.ts";
import type { WebPeriodic } from "../../api/types.ts";
import { VIEWS } from "../../hooks/useHashRoute.ts";
import { PeriodicView } from "./PeriodicView.tsx";
import { lastFiringLabel, nextDueLabel } from "./periodicText.ts";

type contractFn = ReturnType<typeof vi.fn>;
const contractClient = client as unknown as {
  fetchPeriodic: contractFn;
  addPeriodic: contractFn;
  periodicOp: contractFn;
  runPeriodic: contractFn;
};

function contractRow(over: Partial<WebPeriodic>): WebPeriodic {
  return {
    name: "standup",
    prefix: "standup",
    at: "09:00",
    days: [],
    every: "",
    cadence: "daily 09:00",
    enabled: true,
    builtin: false,
    template: "---\nrepo: kraken\n---\nbody\n",
    last_date: "",
    last_slug: "",
    last_note: "",
    last_at: 0,
    next_due: 0,
    created_at: 1,
    ...over,
  };
}

// Rows whose nextDueLabel does not depend on the clock: a builtin dream row
// and an enabled row with next_due 0 ("none"), a paused row ("paused").
const contractDream = contractRow({
  name: "dream",
  prefix: "dream",
  at: "00:00",
  cadence: "daily 00:00",
  builtin: true,
  template: "",
  last_date: "2026-11-12",
  last_slug: "dream-2026-11-12",
});
const contractStandup = contractRow({
  name: "Standup Notes",
  prefix: "standup",
  at: "09:05",
  days: ["mon", "wed", "fri"],
  cadence: "mon,wed,fri 09:05",
  last_date: "2026-11-11",
  last_slug: "standup-2026-11-11",
});
const contractPaused = contractRow({
  name: "weekly review",
  prefix: "weekly",
  enabled: false,
  last_date: "2026-11-10",
  last_note: "start refused: no launcher",
});
const contractRows = [contractDream, contractStandup, contractPaused];

function contractJSONResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const contractSettle = () => new Promise((r) => setTimeout(r, 30));

function contractRowEl(name: string): HTMLElement {
  const el = document.querySelector<HTMLElement>(`[data-periodic="${CSS.escape(name)}"]`);
  if (!el) throw new Error(`no element carries data-periodic="${name}"`);
  return el;
}

async function contractRenderView(active = true) {
  const onToast = vi.fn();
  const utils = render(<PeriodicView active={active} onToast={onToast} />);
  if (active) {
    await waitFor(() => expect(contractRowEl("Standup Notes")).toBeTruthy());
  }
  return { ...utils, onToast };
}

// ---------------------------------------------------------------- VIEWS

describe("VIEWS (contract, hooks/useHashRoute.ts)", () => {
  // Contract: VIEWS is ["board", "memory", "dream", "periodic", "log"].
  it("lists periodic between dream and log", () => {
    expect([...VIEWS]).toEqual(["board", "memory", "dream", "periodic", "log"]);
  });
});

// ---------------------------------------------------------------- periodicText

describe("lastFiringLabel (contract)", () => {
  // Contract: "never when last_date is empty."
  it("is never when last_date is empty", () => {
    expect(lastFiringLabel(contractRow({ last_date: "" }))).toBe("never");
  });

  // Contract: "Otherwise it starts with last_date, contains last_slug when that
  // is non-empty, and contains last_note when that is non-empty."
  it("starts with last_date and carries the slug and the note", () => {
    const withSlug = lastFiringLabel(contractRow({ last_date: "2026-11-12", last_slug: "standup-2026-11-12" }));
    expect(withSlug.startsWith("2026-11-12")).toBe(true);
    expect(withSlug).toContain("standup-2026-11-12");

    const withNote = lastFiringLabel(contractRow({ last_date: "2026-11-13", last_note: "standup-2026-11-13 is taken" }));
    expect(withNote.startsWith("2026-11-13")).toBe(true);
    expect(withNote).toContain("standup-2026-11-13 is taken");

    const both = lastFiringLabel(
      contractRow({ last_date: "2026-11-14", last_slug: "standup-2026-11-14", last_note: "start refused" }),
    );
    expect(both.startsWith("2026-11-14")).toBe(true);
    expect(both).toContain("standup-2026-11-14");
    expect(both).toContain("start refused");
  });
});

describe("nextDueLabel (contract)", () => {
  // Built in the local zone on both sides, so the assertion holds in any TZ.
  const now = new Date(2026, 10, 13, 8, 0, 0).getTime();

  // Contract: "paused when !p.enabled."
  it("is paused for a disabled row, whatever next_due says", () => {
    expect(nextDueLabel(contractRow({ enabled: false, next_due: 0 }), now)).toBe("paused");
    expect(nextDueLabel(contractRow({ enabled: false, next_due: now + 60_000 }), now)).toBe("paused");
  });

  // Contract: "none when enabled and next_due is 0."
  it("is none when enabled with next_due 0", () => {
    expect(nextDueLabel(contractRow({ enabled: true, next_due: 0 }), now)).toBe("none");
  });

  // Contract: "due now when next_due is at or before nowMs."
  it("is due now at or before now", () => {
    expect(nextDueLabel(contractRow({ next_due: now }), now)).toBe("due now");
    expect(nextDueLabel(contractRow({ next_due: now - 3_600_000 }), now)).toBe("due now");
  });

  // Contract: "Otherwise the local time of next_due as zero-padded HH:MM,
  // prefixed today when it falls on nowMs's local date and by the local
  // YYYY-MM-DD and a space when it does not."
  it("is today HH:MM later the same local day", () => {
    expect(nextDueLabel(contractRow({ next_due: new Date(2026, 10, 13, 9, 5).getTime() }), now)).toBe("today 09:05");
    expect(nextDueLabel(contractRow({ next_due: new Date(2026, 10, 13, 23, 59).getTime() }), now)).toBe("today 23:59");
  });

  it("is YYYY-MM-DD HH:MM on a later local day", () => {
    expect(nextDueLabel(contractRow({ next_due: new Date(2026, 10, 14, 7, 3).getTime() }), now)).toBe(
      "2026-11-14 07:03",
    );
    expect(nextDueLabel(contractRow({ next_due: new Date(2027, 0, 2, 0, 0).getTime() }), now)).toBe(
      "2027-01-02 00:00",
    );
  });
});

// ---------------------------------------------------------------- client

describe("periodic client calls (contract, api/client.ts)", () => {
  let actual: typeof import("../../api/client.ts");
  let fetchMock: ReturnType<typeof vi.fn>;

  function contractStubFetch(status: number, body: unknown) {
    fetchMock = vi.fn(async () => contractJSONResponse(status, body));
    vi.stubGlobal("fetch", fetchMock);
  }

  function contractCall(i = 0): { path: string; method: string; body: unknown } {
    const [u, init] = fetchMock.mock.calls[i]! as [unknown, RequestInit | undefined];
    const raw = init?.body;
    return {
      path: new URL(String(u), "http://board.local").pathname,
      method: (init?.method ?? "GET").toUpperCase(),
      body: typeof raw === "string" ? JSON.parse(raw) : raw,
    };
  }

  beforeEach(async () => {
    actual = await vi.importActual<typeof import("../../api/client.ts")>("../../api/client.ts");
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  // Contract: fetchPeriodic is "GET /api/periodic" returning a PeriodicList.
  it("fetchPeriodic GETs /api/periodic and resolves the list", async () => {
    contractStubFetch(200, { periodic: contractRows });
    const got = await actual.fetchPeriodic();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const c = contractCall();
    expect(c.path).toBe("/api/periodic");
    expect(c.method).toBe("GET");
    expect(got).toEqual({ periodic: contractRows });
  });

  // Contract: addPeriodic is "POST /api/periodic with the JSON body {op: "add",
  // ...fields}".
  it("addPeriodic POSTs op add with the five fields", async () => {
    contractStubFetch(200, { ok: true, periodic: contractStandup });
    const fields = { name: "Standup Notes", prefix: "standup", at: "09:05", days: "mon,wed,fri", every: "", template: "t {{slug}}" };
    await actual.addPeriodic(fields);
    const c = contractCall();
    expect(c.path).toBe("/api/periodic");
    expect(c.method).toBe("POST");
    expect(c.body).toEqual({ op: "add", ...fields });
  });

  // Contract: periodicOp is "POST /api/periodic with the JSON body {op, name}".
  it.each(["pause", "resume", "remove"] as const)("periodicOp(%s) POSTs {op, name}", async (op) => {
    contractStubFetch(200, op === "remove" ? { ok: true } : { ok: true, periodic: contractStandup });
    await actual.periodicOp(op, "Standup Notes");
    const c = contractCall();
    expect(c.path).toBe("/api/periodic");
    expect(c.method).toBe("POST");
    expect(c.body).toEqual({ op, name: "Standup Notes" });
  });

  // Contract: runPeriodic is "POST /api/periodic with the JSON body {op: "run",
  // name}" resolving a PeriodicRunResult.
  it("runPeriodic POSTs op run and resolves the run result", async () => {
    const answer = { ok: true, slug: "standup-2026-11-13-run", started: false, note: "refused" };
    contractStubFetch(200, answer);
    const got = await actual.runPeriodic("standup");
    const c = contractCall();
    expect(c.path).toBe("/api/periodic");
    expect(c.method).toBe("POST");
    expect(c.body).toEqual({ op: "run", name: "standup" });
    expect(got).toEqual(answer);
  });

  // Contract: each write "Rejects with an Error carrying the server's error
  // text on a non-2xx answer."
  it("each write rejects with an Error carrying the server's error text", async () => {
    contractStubFetch(409, { error: "name standup is taken" });
    await expect(
      actual.addPeriodic({ name: "standup", prefix: "standup", at: "09:00", days: "", every: "", template: "t" }),
    ).rejects.toThrow("name standup is taken");

    contractStubFetch(404, { error: "no periodic job named ghost" });
    await expect(actual.periodicOp("pause", "ghost")).rejects.toThrow("no periodic job named ghost");

    contractStubFetch(409, { error: "dream is built in" });
    await expect(actual.runPeriodic("dream")).rejects.toThrow("dream is built in");
  });
});

// ---------------------------------------------------------------- PeriodicView

describe("PeriodicView (contract)", () => {
  beforeEach(() => {
    contractClient.fetchPeriodic.mockReset();
    contractClient.addPeriodic.mockReset();
    contractClient.periodicOp.mockReset();
    contractClient.runPeriodic.mockReset();
    contractClient.fetchPeriodic.mockResolvedValue({ periodic: contractRows });
    contractClient.addPeriodic.mockResolvedValue({ ok: true });
    contractClient.periodicOp.mockResolvedValue({ ok: true });
    contractClient.runPeriodic.mockResolvedValue({ ok: true, slug: "x", started: true, note: "" });
  });

  // Contract: "Renders <main id="periodic">, with the hidden attribute exactly
  // when !active. It calls fetchPeriodic while active."
  it("renders main#periodic visible and fetches while active", async () => {
    await contractRenderView(true);
    const main = document.querySelector("main#periodic");
    expect(main).not.toBeNull();
    expect(main!.hasAttribute("hidden")).toBe(false);
    expect(contractClient.fetchPeriodic).toHaveBeenCalled();
  });

  it("renders main#periodic hidden while inactive", async () => {
    await contractRenderView(false);
    await act(contractSettle);
    const main = document.querySelector("main#periodic");
    expect(main).not.toBeNull();
    expect(main!.hasAttribute("hidden")).toBe(true);
  });

  // Contract: "Each row of the answer renders one element carrying
  // data-periodic="<name>" whose text includes the row's name, prefix,
  // cadence, lastFiringLabel and nextDueLabel."
  it("renders one data-periodic element per row with its labels", async () => {
    await contractRenderView(true);
    for (const row of contractRows) {
      const els = document.querySelectorAll(`[data-periodic="${CSS.escape(row.name)}"]`);
      expect(els.length).toBe(1);
      const text = els[0]!.textContent ?? "";
      expect(text).toContain(row.name);
      expect(text).toContain(row.prefix);
      expect(text).toContain(row.cadence);
      expect(text).toContain(lastFiringLabel(row));
      // these three rows' labels are clock-independent: none / none / paused
      expect(text).toContain(nextDueLabel(row, Date.now()));
    }
    expect(contractRowEl("weekly review").textContent).toContain("paused");
  });

  // Contract: "A row with builtin: false has three buttons inside that element
  // ...: pause when enabled or resume when not, run now, and delete. A
  // builtin: true row has none of them."
  // Amended in revision 2: the dream row is a stored row that pauses and
  // resumes, so it has that one button and still no run now or delete.
  it("gives stored rows pause|resume, run now and delete, and the dream row pause only", async () => {
    await contractRenderView(true);
    const standup = within(contractRowEl("Standup Notes"));
    expect(standup.getByRole("button", { name: "pause" })).toBeTruthy();
    expect(standup.queryByRole("button", { name: "resume" })).toBeNull();
    expect(standup.getByRole("button", { name: "run now" })).toBeTruthy();
    expect(standup.getByRole("button", { name: "delete" })).toBeTruthy();

    const paused = within(contractRowEl("weekly review"));
    expect(paused.getByRole("button", { name: "resume" })).toBeTruthy();
    expect(paused.queryByRole("button", { name: "pause" })).toBeNull();
    expect(paused.getByRole("button", { name: "run now" })).toBeTruthy();
    expect(paused.getByRole("button", { name: "delete" })).toBeTruthy();

    const dream = within(contractRowEl("dream"));
    expect(dream.getByRole("button", { name: "pause" })).toBeTruthy();
    for (const name of ["resume", "run now", "delete", "confirm delete"]) {
      expect(dream.queryByRole("button", { name })).toBeNull();
    }
    await userEvent.click(dream.getByRole("button", { name: "pause" }));
    await waitFor(() => expect(contractClient.periodicOp).toHaveBeenCalledWith("pause", "dream"));
  });

  // Contract: "Clicking pause or resume calls periodicOp with that op and the
  // row's name ... After any op succeeds the list is fetched again."
  it("pause and resume call periodicOp and refetch", async () => {
    await contractRenderView(true);
    const before = contractClient.fetchPeriodic.mock.calls.length;
    await userEvent.click(within(contractRowEl("Standup Notes")).getByRole("button", { name: "pause" }));
    await waitFor(() => expect(contractClient.periodicOp).toHaveBeenCalledWith("pause", "Standup Notes"));
    await waitFor(() => expect(contractClient.fetchPeriodic.mock.calls.length).toBeGreaterThan(before));

    const mid = contractClient.fetchPeriodic.mock.calls.length;
    await userEvent.click(within(contractRowEl("weekly review")).getByRole("button", { name: "resume" }));
    await waitFor(() => expect(contractClient.periodicOp).toHaveBeenCalledWith("resume", "weekly review"));
    await waitFor(() => expect(contractClient.fetchPeriodic.mock.calls.length).toBeGreaterThan(mid));
    expect(contractClient.periodicOp).toHaveBeenCalledTimes(2);
  });

  // Contract: "Clicking run now calls runPeriodic and, on success, calls
  // onToast with a message containing the returned slug."
  it("run now calls runPeriodic and toasts the slug", async () => {
    contractClient.runPeriodic.mockResolvedValue({
      ok: true,
      slug: "standup-2026-11-13-run-2",
      started: true,
      note: "",
    });
    const { onToast } = await contractRenderView(true);
    const before = contractClient.fetchPeriodic.mock.calls.length;
    await userEvent.click(within(contractRowEl("Standup Notes")).getByRole("button", { name: "run now" }));
    await waitFor(() => expect(contractClient.runPeriodic).toHaveBeenCalledWith("Standup Notes"));
    await waitFor(() =>
      expect(onToast.mock.calls.some(([m]) => String(m).includes("standup-2026-11-13-run-2"))).toBe(true),
    );
    await waitFor(() => expect(contractClient.fetchPeriodic.mock.calls.length).toBeGreaterThan(before));
  });

  // Contract: "The first click on delete only renames that button to confirm
  // delete; clicking confirm delete calls periodicOp("remove", name)."
  it("delete needs a confirming second click", async () => {
    await contractRenderView(true);
    const row = within(contractRowEl("Standup Notes"));
    await userEvent.click(row.getByRole("button", { name: "delete" }));
    await act(contractSettle);
    expect(contractClient.periodicOp).not.toHaveBeenCalled();
    expect(row.queryByRole("button", { name: "delete" })).toBeNull();
    const before = contractClient.fetchPeriodic.mock.calls.length;
    await userEvent.click(row.getByRole("button", { name: "confirm delete" }));
    await waitFor(() => expect(contractClient.periodicOp).toHaveBeenCalledWith("remove", "Standup Notes"));
    expect(contractClient.periodicOp).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(contractClient.fetchPeriodic.mock.calls.length).toBeGreaterThan(before));
  });

  // Contract: "An op that rejects calls onToast with a message containing the
  // error's text."
  it("toasts the error text when an op rejects", async () => {
    contractClient.periodicOp.mockRejectedValue(new Error("no periodic job named Standup Notes"));
    contractClient.runPeriodic.mockRejectedValue(new Error("cannot create job: repo gone"));
    const { onToast } = await contractRenderView(true);
    await userEvent.click(within(contractRowEl("Standup Notes")).getByRole("button", { name: "pause" }));
    await waitFor(() =>
      expect(onToast.mock.calls.some(([m]) => String(m).includes("no periodic job named Standup Notes"))).toBe(true),
    );
    await userEvent.click(within(contractRowEl("Standup Notes")).getByRole("button", { name: "run now" }));
    await waitFor(() =>
      expect(onToast.mock.calls.some(([m]) => String(m).includes("cannot create job: repo gone"))).toBe(true),
    );
  });

  // Contract: "The view also renders <form id="periodic-add"> holding controls
  // with the name attributes name, prefix, at, days and template (the last a
  // textarea) and a submit button named add; submitting calls addPeriodic with
  // the five current values, and on success empties the controls and fetches
  // the list again."
  function contractFormControls() {
    const form = document.querySelector<HTMLFormElement>("form#periodic-add");
    if (!form) throw new Error("no form#periodic-add");
    const ctl = (n: string) => {
      const el = form.querySelector<HTMLInputElement | HTMLTextAreaElement>(`[name="${n}"]`);
      if (!el) throw new Error(`no control named ${n}`);
      return el;
    };
    return {
      form,
      name: ctl("name"),
      prefix: ctl("prefix"),
      at: ctl("at"),
      days: ctl("days"),
      template: ctl("template"),
      submit: within(form).getByRole("button", { name: "add" }),
    };
  }

  const contractFormValues = {
    name: "Nightly Notes",
    prefix: "nightly",
    at: "23:30",
    days: "mon,fri",
    every: "",
    template: "---\nrepo: kraken\n---\nNotes for {{date}}.\n",
  };

  function contractFill(c: ReturnType<typeof contractFormControls>) {
    for (const key of ["name", "prefix", "at", "days", "template"] as const) {
      fireEvent.change(c[key], { target: { value: contractFormValues[key] } });
    }
  }

  it("renders the add form with its five controls", async () => {
    await contractRenderView(true);
    const c = contractFormControls();
    expect(c.template.tagName).toBe("TEXTAREA");
    expect(c.submit).toBeTruthy();
  });

  it("submits the five values, then empties the controls and refetches", async () => {
    await contractRenderView(true);
    const c = contractFormControls();
    contractFill(c);
    const before = contractClient.fetchPeriodic.mock.calls.length;
    await userEvent.click(c.submit);
    await waitFor(() => expect(contractClient.addPeriodic).toHaveBeenCalledTimes(1));
    expect(contractClient.addPeriodic.mock.calls[0]![0]).toEqual(contractFormValues);
    await waitFor(() => {
      const after = contractFormControls();
      for (const key of ["name", "prefix", "at", "days", "template"] as const) {
        expect(after[key].value).toBe("");
      }
    });
    await waitFor(() => expect(contractClient.fetchPeriodic.mock.calls.length).toBeGreaterThan(before));
  });

  // Contract: "a rejection calls onToast and keeps the values". (Whether the
  // add form's toast must carry the error text is not stated for the form, so
  // only the call is asserted.)
  it("keeps the values and toasts when add rejects", async () => {
    contractClient.addPeriodic.mockRejectedValue(new Error("prefix nightly is taken"));
    const { onToast } = await contractRenderView(true);
    const c = contractFormControls();
    contractFill(c);
    await userEvent.click(c.submit);
    await waitFor(() => expect(onToast).toHaveBeenCalled());
    const after = contractFormControls();
    for (const key of ["name", "prefix", "at", "days", "template"] as const) {
      expect(after[key].value).toBe(contractFormValues[key]);
    }
  });
});
