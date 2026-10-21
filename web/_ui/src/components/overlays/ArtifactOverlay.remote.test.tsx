import { fireEvent, render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { WebJob } from "../../api/types.ts";
import { ownerTurnLabel } from "../../lib/fleet.ts";
import { makeJob } from "../../test/fixtures.ts";
import { ArtifactOverlay } from "./ArtifactOverlay.tsx";
import { SteerOverlay, type SteerOverlayProps } from "./SteerOverlay.tsx";

// Regression pins for review findings on the remote surfaces: a sibling's
// scriptable page is framed with every sandbox restriction (a PDF with none,
// since the viewer refuses one), its card is never compiled into a 3D scene on
// this origin, a file dropped on a remote card's modal never uploads into the
// last LOCAL job opened, and a remote job's own turns are never labelled with
// this board's operator (ownerTurnLabel).

vi.mock("../../api/client.ts", () => ({
  createFeedback: vi.fn(() => Promise.resolve({ ok: true })),
  threadOp: vi.fn(() => Promise.resolve({ ok: true, tid: "t1" })),
  openInEditor: vi.fn(() => Promise.resolve({ ok: true })),
  fetchDraft: vi.fn(() => Promise.resolve({ slug: "j", text: "" })),
  fetchJobForm: vi.fn(() => Promise.resolve({ slug: "j", mtime: 0 })),
  saveDraft: vi.fn(() => Promise.resolve({ ok: true })),
  uploadImage: vi.fn(() => Promise.resolve({ path: "/tmp/x.png", name: "x.png" })),
  fetchNodeJobRaw: vi.fn(() => new Promise(() => {})),
  fetchNodeTranscript: vi.fn(() => new Promise(() => {})),
  fetchJobTranscriptTurns: vi.fn(() => new Promise(() => {})),
  fetchGoals: vi.fn(() => Promise.resolve({ goals: { week: [], day: [] } })),
  postJobGoal: vi.fn(() => Promise.resolve({ ok: true })),
}));
vi.mock("./Toast.tsx", () => ({ showToast: vi.fn(), Toast: () => null }));
// every .html card reads as compilable, so the 3D pins below test the REMOTE
// gate rather than whether a fixture happens to parse
vi.mock("../../lib/isoscene/index.ts", () => ({ cardCompilable: () => true }));

import { uploadImage } from "../../api/client.ts";

const mutate = async <T,>(op: () => Promise<T>) => op();

const noLibrary = {
  library: [],
  onSaveArtifact: async () => {},
  onRemoveSaved: async () => {},
};

function openArtifact(url: string, job: WebJob | undefined) {
  return render(
    <ArtifactOverlay
      open
      url={url}
      name="j/10-arch.html"
      job={job}
      onNavigate={() => {}}
      onClose={() => {}}
      onAskBranched={() => {}}
      mutate={mutate}
      {...noLibrary}
    />,
  );
}

const owner = { node_id: "n1", label: "mini", operator: "jess", online: true };

describe("ArtifactOverlay iframe sandbox", () => {
  it('frames a remote (proxied) html artifact with sandbox="" (no scripts)', () => {
    const url = "/api/nodes/artifact?node=n1&slug=j&name=10-arch.html";
    openArtifact(url, makeJob({ slug: "j", owner, artifacts: [{ name: "10-arch.html", url }] }));
    const frame = document.querySelector("#panel-body iframe");
    expect(frame?.hasAttribute("sandbox")).toBe(true);
    expect(frame?.getAttribute("sandbox")).toBe("");
  });

  it("sandboxes a proxied url even when its card has left the board", () => {
    openArtifact("/api/nodes/artifact?node=n1&slug=j&name=10-arch.html", undefined);
    const frame = document.querySelector("#panel-body iframe");
    expect(frame?.hasAttribute("sandbox")).toBe(true);
    expect(frame?.getAttribute("sandbox")).toBe("");
  });

  it("leaves a local html artifact's iframe with no sandbox attribute at all", () => {
    const url = "/api/artifact/j/artifacts/10-arch.html";
    openArtifact(url, makeJob({ slug: "j", artifacts: [{ name: "10-arch.html", url }] }));
    const frame = document.querySelector("#panel-body iframe");
    expect(frame).not.toBeNull();
    expect(frame?.hasAttribute("sandbox")).toBe(false);
  });

  it("frames a remote PDF with no sandbox attribute (the PDF viewer refuses one)", () => {
    const url = "/api/nodes/artifact?node=n1&slug=j&name=report.pdf";
    openArtifact(url, makeJob({ slug: "j", owner, artifacts: [{ name: "report.pdf", url }] }));
    const frame = document.querySelector("#panel-body iframe");
    expect(frame).not.toBeNull();
    expect(frame?.hasAttribute("sandbox")).toBe(false);
  });
});

describe("ArtifactOverlay 3D toggle", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve({ ok: true, text: () => Promise.resolve("<svg></svg>") })),
    );
  });
  afterEach(() => vi.unstubAllGlobals());

  it("is offered for a local compilable card (the control case)", async () => {
    const url = "/api/artifact/j/artifacts/10-arch.html";
    openArtifact(url, makeJob({ slug: "j", artifacts: [{ name: "10-arch.html", url }] }));
    await waitFor(() =>
      expect((document.getElementById("panel-iso") as HTMLButtonElement).hidden).toBe(false),
    );
  });

  it("is never offered for another node's card", async () => {
    const url = "/api/nodes/artifact?node=n1&slug=j&name=10-arch.html";
    openArtifact(url, makeJob({ slug: "j", owner, artifacts: [{ name: "10-arch.html", url }] }));
    // the .html text fetch compilable reads has run, and still no 3D
    await waitFor(() => expect(fetch).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 0));
    expect((document.getElementById("panel-iso") as HTMLButtonElement).hidden).toBe(true);
  });
});

describe("steer modal file-drop sink", () => {
  beforeEach(() => vi.clearAllMocks());

  const local = makeJob({ slug: "loc", state: "working", live: true });
  const remote = makeJob({ slug: "rem", state: "working", live: true, owner, visibility: "public" });

  function props(
    over: Pick<SteerOverlayProps, "slug" | "job"> & Partial<SteerOverlayProps>,
  ): SteerOverlayProps {
    return {
      open: true,
      mode: "steer" as const,
      onClose: () => {},
      mutate,
      onOpenArtifact: () => {},
      onOpenThread: () => {},
      ...over,
    };
  }

  function drop() {
    const panel = document.querySelector("#steer-overlay .panel") as HTMLElement;
    const file = new File(["x"], "x.png", { type: "image/png" });
    fireEvent.drop(panel, { dataTransfer: { files: [file], types: ["Files"] } });
  }

  it("a drop on a remote modal opened after a local one uploads nothing", () => {
    const { rerender } = render(<SteerOverlay {...props({ slug: "loc", job: local })} />);
    rerender(<SteerOverlay {...props({ slug: "rem", job: remote, node: "n1" })} />);
    drop();
    expect(uploadImage).not.toHaveBeenCalled();
  });

  it("a local modal remounted for another job keeps that job's sink", () => {
    const other = makeJob({ slug: "loc2", state: "working", live: true });
    const { rerender } = render(<SteerOverlay {...props({ slug: "loc", job: local })} />);
    rerender(<SteerOverlay {...props({ slug: "loc2", job: other })} />);
    drop();
    expect(uploadImage).toHaveBeenCalledWith("loc2", expect.any(File));
  });
});

describe("owner turn label", () => {
  const bare = { node_id: "n9", label: " ", operator: "  ", online: true };

  it("falls back operator → label → node_id, never this board's operator", () => {
    expect(ownerTurnLabel({ ...bare, operator: "jess" }, "carl")).toBe("jess");
    expect(ownerTurnLabel({ ...bare, label: "mini" }, "carl")).toBe("mini");
    expect(ownerTurnLabel(bare, "carl")).toBe("n9");
    expect(ownerTurnLabel(undefined, "carl")).toBe("carl");
  });

  it("the remote modal's load drill names the owner's node id, not this board's operator", () => {
    const job = makeJob({
      slug: "rem",
      state: "working",
      live: true,
      owner: bare,
      load: { unblock: 2, scope: 8, note: "", at: Date.now() },
    });
    render(
      <SteerOverlay
        open
        slug="rem"
        mode="steer"
        job={job}
        node="n9"
        operator="carl"
        onClose={() => {}}
        mutate={mutate}
        onOpenArtifact={() => {}}
        onOpenThread={() => {}}
      />,
    );
    const drill = document.querySelector('[data-testid="load-drill"]');
    expect(drill?.getAttribute("aria-label")).toContain("costs n9 ");
    expect(drill?.getAttribute("aria-label")).not.toContain("carl");
  });
});
