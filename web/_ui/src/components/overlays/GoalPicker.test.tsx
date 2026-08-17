// GoalPicker's own behavior — label building, the pick, the unlink, the
// off-period round-trip, the archived fetch gate — pinned by BARE-mounting
// the component, unlike RenameControl's host-driven suite: the picker takes
// its whole world through props (slug, goal, mutate), so a host would add
// wiring without adding coverage. What a host DOES add — that each overlay
// actually mounts the picker where its head/form says it does — is asserted
// in SteerOverlay.test.tsx's head test, not here.
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { GoalPicker } from "./GoalPicker.tsx";

vi.mock("../../api/client.ts", () => ({
  fetchGoals: vi.fn(() =>
    Promise.resolve({
      goals: {
        week: [{ id: 1, scope: "week", period: "2026-08-10", text: "ship v3", done: false, position: 0 }],
        day: [{ id: 2, scope: "day", period: "2026-08-17", text: "fix intake", done: false, position: 0 }],
      },
    }),
  ),
  postJobGoal: vi.fn(() => Promise.resolve({ ok: true })),
}));

import { fetchGoals, postJobGoal } from "../../api/client.ts";

const mutate = async <T,>(op: () => Promise<T>) => op();

describe("GoalPicker", () => {
  beforeEach(() => vi.clearAllMocks());

  it("lists the current periods with scope prefixes and posts the picked id", async () => {
    const user = userEvent.setup();
    render(<GoalPicker id="p" slug="j" goal={undefined} mutate={mutate} />);
    await waitFor(() => expect(fetchGoals).toHaveBeenCalled());
    await user.click(document.getElementById("p") as HTMLButtonElement);
    await user.click(screen.getByText("wk ship v3"));
    expect(postJobGoal).toHaveBeenCalledWith("j", 1);
    // day goals ride the same list, with their own prefix
    await user.click(document.getElementById("p") as HTMLButtonElement);
    expect(screen.getByText("day fix intake")).toBeInTheDocument();
  });

  it("posts 0 on the — row: the unlink", async () => {
    const user = userEvent.setup();
    render(
      <GoalPicker
        id="p"
        slug="j"
        goal={{ id: 1, text: "ship v3", done: false }}
        mutate={mutate}
      />,
    );
    await waitFor(() => expect(fetchGoals).toHaveBeenCalled());
    await user.click(document.getElementById("p") as HTMLButtonElement);
    await user.click(screen.getByRole("option", { name: "—" }));
    expect(postJobGoal).toHaveBeenCalledWith("j", 0);
  });

  it("appends an off-period linked goal so the link round-trips", async () => {
    render(
      <GoalPicker
        id="p"
        slug="j"
        goal={{ id: 9, text: "last week's push", done: false }}
        mutate={mutate}
      />,
    );
    await waitFor(() => expect(fetchGoals).toHaveBeenCalled());
    // selected value shows on the button, without a scope prefix (the scope
    // is not on the wire for a linked goal)
    const btn = document.getElementById("p") as HTMLButtonElement;
    await waitFor(() => expect(btn).toHaveTextContent("last week's push"));
  });

  it("skips the fetch when inactive (archived host)", () => {
    render(<GoalPicker id="p" slug="j" goal={undefined} mutate={mutate} active={false} />);
    expect(fetchGoals).not.toHaveBeenCalled();
  });
});
