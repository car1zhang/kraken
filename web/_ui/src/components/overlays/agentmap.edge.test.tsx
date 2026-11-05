import { act, fireEvent, render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AgentEvent, WebJob } from "../../api/types.ts";
import { makeJob } from "../../test/fixtures.ts";
import { EventsPanel } from "./EventsPanel.tsx";

// athena-edge's blind suite for the steer modal's activity disclosure
// (50-contract.md W4 EventsPanel, as amended: #steer-activity with
// #steer-activity-toggle, collapsed by default, fetching only while expanded;
// plus fetchJobEvents): disabled panels, the collapsed summary with and without
// activity, empty answers, failed fetches, a slug that needs encoding, and the
// polling stopping on collapse or disable.

const T0 = 1_793_000_000_000;

let agentMapEdgeFetch: ReturnType<typeof vi.fn>;

function agentMapEdgeURL(input: RequestInfo | URL): string {
  if (typeof input === "string") return input;
  if (input instanceof URL) return input.toString();
  return input.url;
}

function agentMapEdgeStub(impl: (url: string) => Promise<Response>) {
  agentMapEdgeFetch = vi.fn((input: RequestInfo | URL) => impl(agentMapEdgeURL(input)));
  vi.stubGlobal("fetch", agentMapEdgeFetch);
}

function agentMapEdgeEventsCalls(): string[] {
  return agentMapEdgeFetch.mock.calls
    .map(([input]) => agentMapEdgeURL(input as RequestInfo))
    .filter((u) => u.includes("/api/job/events"));
}

function agentMapEdgeJSON(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

const agentMapEdgeFlush = () => act(async () => {
  for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 0));
});

const agentMapEdgeWait = (ms: number) => act(async () => {
  await new Promise((r) => setTimeout(r, ms));
});

function agentMapEdgeToggle(container: HTMLElement): HTMLElement {
  const t = container.querySelector<HTMLElement>("#steer-activity #steer-activity-toggle, #steer-activity-toggle");
  expect(t).not.toBeNull();
  return t!;
}

function agentMapEdgeExpand(container: HTMLElement) {
  fireEvent.click(agentMapEdgeToggle(container));
}

const agentMapEdgeBusy = (): WebJob =>
  makeJob({
    slug: "demo",
    state: "working",
    live: true,
    activity: {
      act: "edit",
      label: "Edit board.go",
      at: T0,
      events: [
        { id: "e1", t: T0 - 1000, act: "read", label: "Read x.go", dur_ms: 10 },
        { id: "e2", t: T0, act: "edit", label: "Edit board.go", open: true },
      ],
    },
  });

beforeEach(() => {
  agentMapEdgeStub(async () => agentMapEdgeJSON({ events: [] }));
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("EventsPanel — disabled", () => {
  it("renders nothing and fetches nothing, with or without a job", async () => {
    for (const job of [undefined, agentMapEdgeBusy()]) {
      const { container, unmount } = render(<EventsPanel slug="demo" enabled={false} job={job} />);
      await agentMapEdgeWait(300);
      expect(container.querySelector("#steer-activity")).toBeNull();
      expect(container.querySelector("#steer-activity-toggle")).toBeNull();
      expect(container.querySelector("#steer-events")).toBeNull();
      expect(container.querySelector(".act-now, .act-ribbon")).toBeNull();
      unmount();
    }
    expect(agentMapEdgeEventsCalls()).toHaveLength(0);
  });
});

describe("EventsPanel — collapsed (the default)", () => {
  it("starts collapsed and fetches nothing while collapsed", async () => {
    const { container } = render(<EventsPanel slug="demo" enabled={true} job={agentMapEdgeBusy()} />);
    expect(container.querySelector("#steer-activity")).not.toBeNull();
    expect(agentMapEdgeToggle(container).getAttribute("aria-expanded")).toBe("false");
    await agentMapEdgeWait(300);
    expect(agentMapEdgeEventsCalls()).toHaveLength(0);
    expect(container.querySelector("#steer-events")).toBeNull();
  });

  it("with no job, shows nothing but the toggle", async () => {
    const { container } = render(<EventsPanel slug="demo" enabled={true} />);
    await agentMapEdgeFlush();
    expect(container.querySelector("#steer-activity-toggle")).not.toBeNull();
    expect(container.querySelector(".act-now, .act-ribbon")).toBeNull();
    expect(container.querySelector("#steer-events")).toBeNull();
  });

  it("with a job that has no activity, shows nothing but the toggle", async () => {
    const { container } = render(<EventsPanel slug="demo" enabled={true} job={makeJob({ slug: "demo", state: "working", live: true })} />);
    await agentMapEdgeFlush();
    expect(container.querySelector("#steer-activity-toggle")).not.toBeNull();
    expect(container.querySelector(".act-now, .act-ribbon")).toBeNull();
  });

  it("with activity, shows the strip inside the disclosure", async () => {
    const { container } = render(<EventsPanel slug="demo" enabled={true} job={agentMapEdgeBusy()} />);
    await agentMapEdgeFlush();
    expect(container.querySelector("#steer-activity .act-now")).not.toBeNull();
    expect(container.querySelectorAll("#steer-activity .act-ribbon i")).toHaveLength(2);
  });
});

describe("EventsPanel — expanded", () => {
  it("expanding flips aria-expanded and asks for 60 events of the slug", async () => {
    const { container } = render(<EventsPanel slug="demo-job" enabled={true} />);
    agentMapEdgeExpand(container);
    expect(agentMapEdgeToggle(container).getAttribute("aria-expanded")).toBe("true");
    await waitFor(() => expect(agentMapEdgeEventsCalls().length).toBeGreaterThan(0));
    const url = new URL(agentMapEdgeEventsCalls()[0]!, "http://x");
    expect(url.pathname).toBe("/api/job/events");
    expect(url.searchParams.get("slug")).toBe("demo-job");
    expect(url.searchParams.get("n")).toBe("60");
  });

  it("an empty events answer renders no #steer-events", async () => {
    const { container } = render(<EventsPanel slug="demo" enabled={true} />);
    agentMapEdgeExpand(container);
    await waitFor(() => expect(agentMapEdgeEventsCalls().length).toBeGreaterThan(0));
    await agentMapEdgeFlush();
    expect(container.querySelector("#steer-events")).toBeNull();
    expect(container.querySelector("#steer-activity-toggle")).not.toBeNull();
  });

  it("a rejected fetch renders no #steer-events and does not throw", async () => {
    agentMapEdgeStub(async () => {
      throw new TypeError("network down");
    });
    const { container } = render(<EventsPanel slug="demo" enabled={true} />);
    agentMapEdgeExpand(container);
    await waitFor(() => expect(agentMapEdgeEventsCalls().length).toBeGreaterThan(0));
    await agentMapEdgeFlush();
    expect(container.querySelector("#steer-events")).toBeNull();
    expect(container.querySelector("#steer-activity")).not.toBeNull();
  });

  it("a 500 or a 404 renders no #steer-events and does not throw", async () => {
    for (const status of [500, 404]) {
      agentMapEdgeStub(async () => new Response("nope", { status }));
      const { container, unmount } = render(<EventsPanel slug="demo" enabled={true} />);
      agentMapEdgeExpand(container);
      await waitFor(() => expect(agentMapEdgeEventsCalls().length).toBeGreaterThan(0));
      await agentMapEdgeFlush();
      expect(container.querySelector("#steer-events")).toBeNull();
      unmount();
    }
  });

  it("a slug with reserved characters arrives intact", async () => {
    const { container } = render(<EventsPanel slug="a&b=c d" enabled={true} />);
    agentMapEdgeExpand(container);
    await waitFor(() => expect(agentMapEdgeEventsCalls().length).toBeGreaterThan(0));
    const url = new URL(agentMapEdgeEventsCalls()[0]!, "http://x");
    expect(url.searchParams.get("slug")).toBe("a&b=c d");
    expect(url.searchParams.get("n")).toBe("60");
  });

  it("a single hear event is a row with its display name and label", async () => {
    const events: AgentEvent[] = [{ id: "h1", t: T0, act: "hear", label: "from cell other-job", from: "cell", who: "other-job" }];
    agentMapEdgeStub(async () => agentMapEdgeJSON({ events }));
    const { container } = render(<EventsPanel slug="demo" enabled={true} />);
    agentMapEdgeExpand(container);
    await waitFor(() => expect(container.querySelector("#steer-events")).not.toBeNull());
    const text = container.querySelector("#steer-events")!.textContent ?? "";
    expect(text).toContain("hear");
    expect(text).toContain("from cell other-job");
  });
});

describe("EventsPanel — polling stops", () => {
  it("collapsing again stops the polling", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { container } = render(<EventsPanel slug="demo" enabled={true} />);
    agentMapEdgeExpand(container);
    await vi.waitFor(() => expect(agentMapEdgeEventsCalls().length).toBeGreaterThan(0));
    fireEvent.click(agentMapEdgeToggle(container));
    expect(agentMapEdgeToggle(container).getAttribute("aria-expanded")).toBe("false");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10);
    });
    const before = agentMapEdgeEventsCalls().length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5 * 60_000);
    });
    expect(agentMapEdgeEventsCalls().length).toBe(before);
    expect(container.querySelector("#steer-events")).toBeNull();
  });

  it("going from enabled to disabled stops the polling and removes the disclosure", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { container, rerender } = render(<EventsPanel slug="demo" enabled={true} />);
    agentMapEdgeExpand(container);
    await vi.waitFor(() => expect(agentMapEdgeEventsCalls().length).toBeGreaterThan(0));
    rerender(<EventsPanel slug="demo" enabled={false} />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10);
    });
    const before = agentMapEdgeEventsCalls().length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5 * 60_000);
    });
    expect(agentMapEdgeEventsCalls().length).toBe(before);
    expect(container.querySelector("#steer-activity")).toBeNull();
  });
});
