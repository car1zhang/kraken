import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { WebJob } from "../../../api/types.ts";
import { makeJob } from "../../../test/fixtures.ts";
import { CodexChip, codexChipVisible } from "./CodexChip.tsx";

const job = (over: Partial<WebJob> = {}): WebJob => makeJob({ ...over });

function renderChip(j: WebJob) {
  const { container } = render(<CodexChip job={j} />);
  return container.querySelector<HTMLElement>(".cx-chip");
}

describe("CodexChip", () => {
  it("renders `◇ codex` for a codex job", () => {
    expect(renderChip(job({ agent: "codex" }))?.textContent).toBe("◇ codex");
  });

  it("renders nothing for a claude job (absent agent, the default)", () => {
    expect(renderChip(job({}))).toBeNull();
    expect(renderChip(job({ agent: "claude" }))).toBeNull();
  });

  it("is a cx-chip, not a chip (which is the artifact click target)", () => {
    const el = renderChip(job({ agent: "codex" }));
    expect(el).not.toBeNull();
    expect(el?.classList.contains("chip")).toBe(false);
  });

  describe("codexChipVisible", () => {
    it("is true only when agent is exactly codex, regardless of state", () => {
      expect(codexChipVisible(job({ agent: "codex", state: "draft", live: false }))).toBe(true);
      expect(codexChipVisible(job({ agent: "codex", state: "done", live: false }))).toBe(true);
      expect(codexChipVisible(job({ agent: "claude" }))).toBe(false);
      expect(codexChipVisible(job({}))).toBe(false);
    });
  });
});
