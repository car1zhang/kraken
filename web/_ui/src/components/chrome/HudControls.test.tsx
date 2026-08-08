import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { resetSfxForTest, sfxEnabled } from "../../lib/sfx.ts";
import { SfxToggle } from "./HudControls.tsx";

// SfxToggle — the third HUD preference button. Unlike its two siblings it
// rides localStorage directly (no krakenHud, no pre-paint IIFE involvement),
// so the pins here are about the label, the persisted kraken-sfx flag, and
// the sfx module's runtime flag staying in step.

beforeEach(() => {
  localStorage.clear();
  resetSfxForTest();
});
afterEach(() => {
  cleanup();
  resetSfxForTest();
});

describe("SfxToggle", () => {
  it("defaults ON and toggles label + stored flag + module flag together", async () => {
    render(<SfxToggle />);
    const btn = screen.getByRole("button", { name: /SFX ON/ });
    await userEvent.click(btn);
    expect(btn).toHaveTextContent("SFX OFF");
    expect(localStorage.getItem("kraken-sfx")).toBe("0");
    expect(sfxEnabled()).toBe(false);
    await userEvent.click(btn);
    expect(btn).toHaveTextContent("SFX ON");
    expect(localStorage.getItem("kraken-sfx")).toBe("1");
    expect(sfxEnabled()).toBe(true);
  });

  it("mounts OFF from a persisted kraken-sfx=0", () => {
    localStorage.setItem("kraken-sfx", "0");
    render(<SfxToggle />);
    screen.getByRole("button", { name: /SFX OFF/ });
  });
});
