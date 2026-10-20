// App.test.tsx — wiring pins for App's overlay hand-offs. The components on
// either side of a hand-off have their own suites (SteerOverlay.test.tsx,
// EditorOverlay.test.tsx); what none of them can see is the OPTIONS App passes
// between them, which is exactly where the editor→reactivate regression lived:
// App passed `requireText: true` on the editor's ↺ hand-off from the React
// port onward, so one of the two paths to reactivate a done job — click the
// card, click ↺, click send — silently dead-ended on "message is empty" while
// the composer's own placeholder said "leave empty to just wake it". Every
// component test passed throughout, because every component behaved exactly
// as told.
//
// The card's click now opens the steer modal in VIEW mode rather than the
// editor (a done job reads like a blocked one; carl, 2026-10-20), and view's
// own ↺ is a second App hand-off of the same shape — `openSteer(slug,
// "reactivate")` with no requireText — so this is the path the pin follows.
// The editor's ↺ keeps its own component test; from the board it is no
// longer a done card's route.

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

// Always-mounted sibling views, stubbed: the log/dream views poll endpoints of
// their own, and the memory view only renders App's library/mm polls. None is
// part of the wiring under test.
vi.mock("./components/memory/MemoryView.tsx", () => ({
  MemoryView: () => null,
  default: () => null,
}));
vi.mock("./components/dream/DreamView.tsx", () => ({ DreamView: () => null }));
vi.mock("./components/log/LogView.tsx", () => ({ LogView: () => null }));

// The client is mocked wholesale, every function export replaced — App mounts
// the entire component tree, so hand-listing the fetchers in use would break
// silently each time any child gains one. The board fetch returns one DONE,
// reactivatable job; everything else resolves to an empty object.
vi.mock("./api/client.ts", async (importOriginal) => {
  const real = await importOriginal<typeof import("./api/client.ts")>();
  const { makeBoard, makeJob } = await import("./test/fixtures.ts");
  const mocked: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(real)) {
    mocked[k] = typeof v === "function" ? vi.fn(() => Promise.resolve({})) : v;
  }
  mocked.fetchBoard = vi.fn(() =>
    Promise.resolve(
      makeBoard({
        jobs: [
          makeJob({ slug: "settled", state: "done", reactivatable: true }),
        ],
      }),
    ),
  );
  mocked.fetchDraft = vi.fn(() =>
    Promise.resolve({ slug: "settled", text: "" }),
  );
  return mocked;
});

import { reactivateJob } from "./api/client.ts";
import { App } from "./App.tsx";

describe("App: view → reactivate hand-off", () => {
  beforeEach(() => vi.clearAllMocks());

  it("reactivates a done job with an empty message via card → view → ↺ → send", async () => {
    const user = userEvent.setup();
    const { container } = render(<App />);

    // A done card's plain click opens the steer modal in view mode — the
    // read-only drilldown, which has no composer of its own.
    await user.click(await screen.findByText("settled"));
    expect(document.getElementById("steer-title")?.textContent).toBe("settled — done");
    expect(document.getElementById("steer-text")).toBeNull();

    // View's ↺ — not the card's, which shares the label. Per SteerOverlay's
    // mode table this opens reactivate WITHOUT requireText: only a
    // done→working drag sets that. The button is ALWAYS rendered and gated
    // only by `hidden` (SteerOverlay's showReactivate), and user-event clicks
    // straight through a hidden node in jsdom — so the visibility assertion
    // is what makes this fixture honest, not the click.
    const viewReactivate = container.querySelector("#steer-reactivate");
    expect(viewReactivate).not.toBeNull();
    expect(viewReactivate).not.toHaveAttribute("hidden");
    await user.click(viewReactivate as HTMLElement);

    // Send with the composer untouched. The empty message must go through —
    // the placeholder says "leave empty to just wake it", and an empty-body
    // reactivate is the wire's "resume without steering".
    await user.click(await screen.findByText("reactivate ⌘⏎"));
    await waitFor(() => expect(reactivateJob).toHaveBeenCalledWith("settled", ""));

    // The success half of send(): a delivered reactivate closes the modal.
    // (A "note did not fire" assertion here would be vacuous — the refusal
    // branch returns before reactivateJob is called, so any world where the
    // note fires is one the waitFor above already failed in, and on this path
    // the modal's body is unmounted and the note element gone.)
    await waitFor(() =>
      expect(document.getElementById("steer-overlay")).toHaveAttribute("hidden"),
    );
  });
});
