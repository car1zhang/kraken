import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";

import type { WebJob, WebNode } from "../../api/types.ts";
import { FleetPicker } from "./FleetPicker.tsx";

// FleetPicker.test.tsx — the machines dropdown's rows, ticks, picks, counts,
// dismissal and accessible names, from 50-components.md's FleetPicker
// obligations (job 634).

function node(node_id: string, label: string, operator: string, online = true): WebNode {
  return {
    node_id,
    label,
    operator,
    dns_name: `${label}.tail.ts.net`,
    ip: "100.70.0.2",
    port: 7777,
    build: "abc",
    contract: 7,
    online,
    last_ok: 1,
  };
}

function job(slug: string, owner?: WebNode): WebJob {
  return {
    slug,
    repo: "kraken",
    state: "working",
    live: true,
    mtime: 1,
    ...(owner
      ? { owner: { node_id: owner.node_id, label: owner.label, operator: owner.operator, online: owner.online } }
      : {}),
  } as WebJob;
}

const mj = node("nMJ", "MJ's MacBook Pro", "MJ");
const kitchen = node("nK", "kitchen-mac", "Sam", false);
const nodes = [mj, kitchen];
const jobs = [job("a"), job("b", mj), job("c"), job("d", mj), job("e", kitchen)];

/** The picker wired to real selection state, as App wires it. */
/** `null` for selfLabel/operator means "the server sent none" — an undefined prop would take the default. */
function Harness({ initial = [] as string[], onChange, selfLabel = "Mac Studio", operator = "carl" }: {
  initial?: string[];
  onChange?: (next: string[]) => void;
  selfLabel?: string | null;
  operator?: string | null;
}) {
  const [selected, setSelected] = useState<string[]>(initial);
  return (
    <FleetPicker
      nodes={nodes}
      selfLabel={selfLabel ?? undefined}
      operator={operator ?? undefined}
      jobs={jobs}
      selected={selected}
      onChange={(next) => {
        onChange?.(next);
        setSelected(next);
      }}
    />
  );
}

const button = () => screen.getByRole("button", { name: /^machines: / });
const rows = () => within(screen.getByRole("listbox")).getAllByRole("option");
const keys = () => rows().map((r) => r.getAttribute("data-node-key"));

describe("FleetPicker", () => {
  it("renders the button text and no menu at rest", () => {
    render(<Harness />);
    expect(button()).toHaveTextContent("all machines");
    expect(button()).toHaveAttribute("aria-haspopup", "listbox");
    expect(button()).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("lists reset, this Mac, then the registry in order, with counts", async () => {
    render(<Harness />);
    await userEvent.click(button());
    expect(button()).toHaveAttribute("aria-expanded", "true");
    const list = screen.getByRole("listbox");
    expect(list).toHaveAttribute("aria-multiselectable", "true");
    expect(keys()).toEqual(["__all__", "local", "nMJ", "nK"]);
    const [all, self, r1, r2] = rows();
    expect(all).toHaveTextContent("all machines");
    expect(self).toHaveTextContent("Mac Studio · carl");
    expect(self).toHaveTextContent("this Mac");
    expect(self).toHaveTextContent("2");
    expect(r1).toHaveTextContent("MJ's MacBook Pro · MJ");
    expect(r1).toHaveTextContent("2");
    expect(r2).toHaveTextContent("kitchen-mac · Sam");
    expect(r2).toHaveTextContent("1");
    expect(within(r2!).getByLabelText("offline")).toBeTruthy();
    expect(within(r1!).getByLabelText("online")).toBeTruthy();
  });

  it("says the bare words this Mac when the server sent no label", async () => {
    render(<Harness selfLabel={null} operator={null} />);
    await userEvent.click(button());
    expect(rows()[1]).toHaveTextContent("this Mac");
    expect(rows()[1]).not.toHaveTextContent("·");
  });

  it("ticks exactly the selected rows, never the reset row", async () => {
    render(<Harness initial={["nMJ"]} />);
    expect(button()).toHaveTextContent("MJ's MacBook Pro");
    await userEvent.click(button());
    expect(rows().map((r) => r.getAttribute("aria-selected"))).toEqual(["false", "false", "true", "false"]);
  });

  it("a pick toggles one key and leaves the menu open", async () => {
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);
    await userEvent.click(button());
    await userEvent.click(rows()[1]!);
    expect(onChange).toHaveBeenLastCalledWith(["local"]);
    expect(screen.getByRole("listbox")).toBeTruthy();
    expect(button()).toHaveTextContent("this Mac");
    await userEvent.click(rows()[2]!);
    expect(onChange).toHaveBeenLastCalledWith(["local", "nMJ"]);
    expect(button()).toHaveTextContent("2 of 3 machines");
    await userEvent.click(rows()[1]!);
    expect(onChange).toHaveBeenLastCalledWith(["nMJ"]);
  });

  it("the reset row empties the selection and leaves the menu open", async () => {
    const onChange = vi.fn();
    render(<Harness initial={["local", "nK"]} onChange={onChange} />);
    await userEvent.click(button());
    await userEvent.click(rows()[0]!);
    expect(onChange).toHaveBeenLastCalledWith([]);
    expect(screen.getByRole("listbox")).toBeTruthy();
    expect(button()).toHaveTextContent("all machines");
  });

  it("keeps a selected key the registry no longer names as a dimmed, ticked row", async () => {
    render(<Harness initial={["nGONE"]} />);
    expect(button()).toHaveTextContent("nGONE");
    await userEvent.click(button());
    expect(keys()).toEqual(["__all__", "local", "nMJ", "nK", "nGONE"]);
    const stale = rows()[4]!;
    expect(stale).toHaveAttribute("aria-selected", "true");
    expect(stale.className).toContain("fp-stale");
    await userEvent.click(stale);
    expect(button()).toHaveTextContent("all machines");
  });

  it("Escape closes the menu without reaching a document bubble listener", async () => {
    render(<Harness />);
    await userEvent.click(button());
    const seen = vi.fn();
    document.addEventListener("keydown", seen);
    fireEvent.keyDown(document.body, { key: "Escape" });
    document.removeEventListener("keydown", seen);
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(seen).not.toHaveBeenCalled();
  });

  it("a mousedown outside closes the menu; the button toggles it", async () => {
    render(<Harness />);
    await userEvent.click(button());
    fireEvent.mouseDown(document.body);
    expect(screen.queryByRole("listbox")).toBeNull();
    await userEvent.click(button());
    expect(screen.getByRole("listbox")).toBeTruthy();
    await userEvent.click(button());
    expect(screen.queryByRole("listbox")).toBeNull();
  });
});
