import { render, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AgentEvent, WebJob } from "../../api/types.ts";
import { makeJob } from "../../test/fixtures.ts";
import { EventsPanel } from "./EventsPanel.tsx";

// agentmap.contract.test.tsx — athena-logic's blind tests for EventsPanel
// (50-contract.md W4, amended): while enabled, one disclosure #steer-activity
// with a toggle #steer-activity-toggle (aria-expanded false by default).
// Collapsed it shows the job's ActivityStrip and fetches nothing; expanded it
// polls fetchJobEvents(slug, 60) and renders #steer-events newest first, each
// row showing activityName(act) and the label, and no #steer-events while
// there are no events. Not enabled it renders and fetches nothing.

const T0 = 1_792_000_000_000;

function agentMapContractStubEvents(events: AgentEvent[]) {
  const fetchMock = vi.fn((_input: RequestInfo | URL, _init?: RequestInit) => {
    const body = { events };
    return Promise.resolve({
      ok: true,
      status: 200,
      statusText: "OK",
      headers: new Headers({ "Content-Type": "application/json" }),
      json: () => Promise.resolve(body),
      text: () => Promise.resolve(JSON.stringify(body)),
    } as unknown as Response);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function agentMapContractEventCalls(fetchMock: ReturnType<typeof agentMapContractStubEvents>) {
  return fetchMock.mock.calls
    .map(([u]) => new URL(String(u), "http://board.local"))
    .filter((u) => u.pathname === "/api/job/events");
}

const agentMapContractSettle = () => new Promise((r) => setTimeout(r, 30));

const agentMapContractEvents: AgentEvent[] = [
  { id: "e1", t: T0, act: "data", label: "sqlite3", dur_ms: 40 },
  { id: "e2", t: T0 + 1000, act: "test", label: "go test", dur_ms: 900 },
  { id: "e3", t: T0 + 2000, act: "web", label: "WebFetch", open: true },
];

function agentMapContractBusyJob(): WebJob {
  return makeJob({
    slug: "agent-map",
    state: "working",
    live: true,
    activity: { act: "web", label: "WebFetch", at: T0 + 2000, events: agentMapContractEvents },
  });
}

beforeEach(() => vi.unstubAllGlobals());
afterEach(() => vi.unstubAllGlobals());

describe("EventsPanel", () => {
  it("is collapsed by default, shows the job's activity strip, and fetches nothing", async () => {
    const f = agentMapContractStubEvents(agentMapContractEvents);
    const { container } = render(<EventsPanel slug="agent-map" enabled job={agentMapContractBusyJob()} />);
    const panel = container.querySelector("#steer-activity");
    expect(panel).not.toBeNull();
    const toggle = container.querySelector("#steer-activity-toggle");
    expect(toggle).not.toBeNull();
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(panel!.querySelector(".act-now")).not.toBeNull();
    expect(panel!.querySelector(".act-ribbon")).not.toBeNull();
    await agentMapContractSettle();
    expect(agentMapContractEventCalls(f)).toHaveLength(0);
    expect(container.querySelector("#steer-events")).toBeNull();
  });

  it("shows nothing but the toggle while collapsed for a job with no activity", async () => {
    const f = agentMapContractStubEvents(agentMapContractEvents);
    const { container } = render(
      <EventsPanel slug="agent-map" enabled job={makeJob({ slug: "agent-map", state: "working", live: true })} />,
    );
    expect(container.querySelector("#steer-activity-toggle")).not.toBeNull();
    expect(container.querySelector(".act-now")).toBeNull();
    expect(container.querySelector(".act-ribbon")).toBeNull();
    await agentMapContractSettle();
    expect(agentMapContractEventCalls(f)).toHaveLength(0);
  });

  it("expanded, fetches the slug's 60 newest events and lists them newest first", async () => {
    const f = agentMapContractStubEvents(agentMapContractEvents);
    const { container } = render(<EventsPanel slug="agent-map" enabled job={agentMapContractBusyJob()} />);
    await userEvent.click(container.querySelector("#steer-activity-toggle")!);
    expect(container.querySelector("#steer-activity-toggle")).toHaveAttribute("aria-expanded", "true");

    await waitFor(() => expect(container.querySelector("#steer-events")).not.toBeNull());
    const calls = agentMapContractEventCalls(f);
    expect(calls.length).toBeGreaterThan(0);
    expect(calls[0]!.searchParams.get("slug")).toBe("agent-map");
    expect(calls[0]!.searchParams.get("n")).toBe("60");

    await waitFor(() => expect(container.querySelector("#steer-events")!.textContent).toContain("WebFetch"));
    const text = container.querySelector("#steer-events")!.textContent!;
    expect(text).toContain("fetch");
    expect(text).toContain("query");
    const pos = (s: string) => text.indexOf(s);
    expect(pos("WebFetch")).toBeLessThan(pos("go test"));
    expect(pos("go test")).toBeLessThan(pos("sqlite3"));
  });

  it("expanded, renders no #steer-events while there are no events", async () => {
    const f = agentMapContractStubEvents([]);
    const { container } = render(<EventsPanel slug="agent-map" enabled />);
    await userEvent.click(container.querySelector("#steer-activity-toggle")!);
    await waitFor(() => expect(agentMapContractEventCalls(f).length).toBeGreaterThan(0));
    await agentMapContractSettle();
    expect(container.querySelector("#steer-events")).toBeNull();
  });

  it("renders and fetches nothing while not enabled", async () => {
    const f = agentMapContractStubEvents(agentMapContractEvents);
    const { container } = render(<EventsPanel slug="agent-map" enabled={false} job={agentMapContractBusyJob()} />);
    await agentMapContractSettle();
    expect(agentMapContractEventCalls(f)).toHaveLength(0);
    expect(container.innerHTML).toBe("");
  });
});
