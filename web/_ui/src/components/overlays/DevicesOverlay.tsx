import { useMemo, useState } from "react";

import {
  fetchNodeSelf,
  fetchPairDevices,
  revokePairDevice,
  setPairPassword,
} from "../../api/client.ts";
import type {
  NodeEnvelope,
  PairDevicesResponse,
  WebBoard,
} from "../../api/types.ts";
import { ESC_PRIORITY } from "../../hooks/useEscapeStack.ts";
import { usePoll } from "../../hooks/usePoll.ts";
import { age } from "../../lib/format.ts";
import { qrDataURL } from "../../lib/qr.ts";
import { Overlay } from "./Overlay.tsx";

/** The pairing password's floor — the server enforces the same (pairMinPassword). */
export const PAIR_MIN_PASSWORD = 12;
const DEVICES_POLL_MS = 5000;
const SELF_POLL_MS = 60_000;

/**
 * Client-side check of a new pairing password, or null when it may be sent.
 * Counted in RUNES (code points), as the server counts it, so an emoji
 * password is not refused for being "long" in UTF-16 units.
 */
export function pairPasswordProblem(pw: string, confirm: string): string | null {
  if ([...pw].length < PAIR_MIN_PASSWORD) {
    return `password must be at least ${PAIR_MIN_PASSWORD} characters`;
  }
  if (pw !== confirm) return "the two passwords do not match";
  return null;
}

/** The seed address a phone pairs from: this node's MagicDNS name + port. */
export function pairSeedURL(self: NodeEnvelope | undefined): string | null {
  if (!self) return null;
  const host = self.dns_name || self.ips[0];
  if (!host) return null;
  return `http://${host}:${self.port || 7777}`;
}

const ago = (ms: number) => (ms > 0 ? `${age(Math.floor(ms / 1000))} ago` : "never");

export interface DevicesOverlayProps {
  open: boolean;
  onClose: () => void;
  /** the board payload — its `nodes` registry and the owned cards per node */
  board: WebBoard | undefined;
  /** node ids hidden from this browser's board (kraken.hiddenNodes) */
  hiddenNodes: readonly string[];
  onToggleNode: (nodeId: string, hidden: boolean) => void;
}

/**
 * ◉ DEVICES — the fleet's one settings surface, modelled on the usage
 * overlay: a chrome button opens it, nothing configures the fleet itself
 * (discovery is automatic, there is nothing to join or approve).
 *
 * Five sections: this node's identity; the pairing password (set / change /
 * clear — any of which logs every phone out); the pairing QR plus the same
 * address as text; the paired phones with a Revoke each; and the registry's
 * nodes with a per-node HIDE checkbox, a finer cut than the board bar's
 * hide-everyone toggle. Every pairing route behind it is owner tier, so the
 * overlay is only useful on the Mac itself — which is the point.
 */
export function DevicesOverlay(props: DevicesOverlayProps) {
  return (
    <Overlay id="devices-overlay" open={props.open} priority={ESC_PRIORITY.usage} onClose={props.onClose}>
      {props.open ? <DevicesBody {...props} /> : null}
    </Overlay>
  );
}

function DevicesBody({ board, hiddenNodes, onToggleNode }: DevicesOverlayProps) {
  const self = usePoll<NodeEnvelope>((s) => fetchNodeSelf(s), SELF_POLL_MS);
  const devices = usePoll<PairDevicesResponse>((s) => fetchPairDevices(s), DEVICES_POLL_MS);

  const [pw, setPw] = useState("");
  const [confirm, setConfirm] = useState("");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);

  const seed = pairSeedURL(self.data);
  const qr = useMemo(() => (seed ? qrDataURL(seed) : null), [seed]);

  const nodes = board?.nodes ?? [];
  const jobsByNode = useMemo(() => {
    const m = new Map<string, number>();
    for (const j of board?.jobs ?? []) {
      if (j.owner) m.set(j.owner.node_id, (m.get(j.owner.node_id) ?? 0) + 1);
    }
    return m;
  }, [board?.jobs]);

  const writePassword = async (value: string) => {
    setBusy(true);
    try {
      const r = await setPairPassword(value);
      setPw("");
      setConfirm("");
      setNote(
        (value ? "password set" : "password cleared") +
          (r.revoked ? ` — ${r.revoked} paired phone${r.revoked === 1 ? "" : "s"} logged out` : ""),
      );
      await devices.refetch();
    } catch (e) {
      setNote("✕ " + (e instanceof Error ? e.message : String(e)));
    } finally {
      setBusy(false);
    }
  };

  const submit = () => {
    const problem = pairPasswordProblem(pw, confirm);
    if (problem) {
      setNote(problem);
      return;
    }
    void writePassword(pw);
  };

  const clear = () => {
    if (!window.confirm("clear the pairing password? every paired phone is logged out, and no phone can pair until a new one is set.")) {
      return;
    }
    void writePassword("");
  };

  const revoke = async (id: number, label: string) => {
    if (!window.confirm(`revoke ${label}? that phone is logged out and must pair again.`)) return;
    try {
      await revokePairDevice(id);
      await devices.refetch();
    } catch (e) {
      setNote("✕ " + (e instanceof Error ? e.message : String(e)));
    }
  };

  const s = self.data;
  const list = devices.data?.devices ?? [];

  return (
    <>
      <div className="panel-head">
        <span className="panel-title">◉ DEVICES{s?.label ? ` — ${s.label}` : ""}</span>
        <span className="spacer"></span>
      </div>
      <div className="panel-body devices-body" id="devices-body">
        <div className="dv-id">
          {s ? (
            <>
              <span title="tailscale stable id">node {s.node_id || "—"}</span>
              {seed ? <span>{seed.replace(/^http:\/\//, "")}</span> : null}
              {s.ips[0] ? <span>{s.ips[0]}</span> : null}
              {s.operator ? <span>operator {s.operator}</span> : null}
            </>
          ) : (
            <span className="dim">{self.error ? "this node's identity is unavailable" : "loading…"}</span>
          )}
        </div>

        <div className="u-sect">pairing password</div>
        <form
          className="dv-pw"
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
        >
          <input
            type="password"
            id="pair-password"
            autoComplete="new-password"
            placeholder={`new password (${PAIR_MIN_PASSWORD}+ characters)`}
            aria-label="new pairing password"
            value={pw}
            onChange={(e) => setPw(e.target.value)}
          />
          <input
            type="password"
            id="pair-password-confirm"
            autoComplete="new-password"
            placeholder="confirm"
            aria-label="confirm pairing password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
          />
          <button type="submit" className="btn" id="pair-set" disabled={busy}>
            set
          </button>
          <button type="button" className="btn ghost danger" id="pair-clear" disabled={busy} onClick={clear}>
            clear
          </button>
        </form>
        <div className="dv-warn">changing or clearing the password logs every paired phone out.</div>
        {note ? (
          <div className="answer-note" id="pair-note">
            {note}
          </div>
        ) : null}

        <div className="u-sect">pair a phone</div>
        <div className="dv-qr">
          {qr ? <img src={qr} alt={`pairing code for ${seed}`} width={132} height={132} /> : null}
          <div>
            <div className="dim">
              {qr
                ? "scan from the iOS app's pair screen, or type the seed address by hand:"
                : "type this seed address into the iOS app's pair screen:"}
            </div>
            <code className="dv-seed" id="pair-seed">
              {seed ?? "—"}
            </code>
          </div>
        </div>

        <div className="u-sect">paired devices</div>
        {list.length ? (
          <table className="dv-table" id="pair-devices">
            <thead>
              <tr>
                <th>label</th>
                <th>tailscale device</th>
                <th>paired</th>
                <th>last seen</th>
                <th>last ip</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {list.map((d) => (
                <tr key={d.id} data-device-id={d.id}>
                  <td>{d.label || "—"}</td>
                  <td>{d.tailnet_name || "—"}</td>
                  <td>{ago(d.created_at)}</td>
                  <td>{ago(d.last_seen_at)}</td>
                  <td>{d.last_ip || "—"}</td>
                  <td>
                    <button
                      type="button"
                      className="btn ghost danger"
                      onClick={() => void revoke(d.id, d.label || `device ${d.id}`)}
                    >
                      revoke
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <div className="empty">{devices.error ? "✕ " + devices.error.message : "no paired phones"}</div>
        )}

        <div className="u-sect">nodes on the net</div>
        {nodes.length ? (
          <table className="dv-table" id="fleet-nodes">
            <thead>
              <tr>
                <th></th>
                <th>node</th>
                <th>operator</th>
                <th>dns</th>
                <th>last poll</th>
                <th>jobs</th>
                <th>hide</th>
              </tr>
            </thead>
            <tbody>
              {nodes.map((n) => {
                const hidden = hiddenNodes.includes(n.node_id);
                return (
                  <tr key={n.node_id} data-node-id={n.node_id} title={n.last_err || undefined}>
                    <td>
                      <span className={"dv-dot" + (n.online ? " on" : "")} aria-label={n.online ? "online" : "offline"} />
                    </td>
                    <td>{n.label || n.node_id}</td>
                    <td>{n.operator || "—"}</td>
                    <td>{n.dns_name || n.ip}</td>
                    <td>{n.online ? ago(n.last_ok) : `offline · ${ago(n.last_ok)}`}</td>
                    <td>{jobsByNode.get(n.node_id) ?? 0}</td>
                    <td>
                      <input
                        type="checkbox"
                        aria-label={`hide ${n.label || n.node_id}'s jobs`}
                        checked={hidden}
                        onChange={(e) => onToggleNode(n.node_id, e.target.checked)}
                      />
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        ) : (
          <div className="empty">no other krakens on the tailnet</div>
        )}
        <div className="dim dv-foot">
          reachable from this Mac only. the hide column is this browser's preference — finer than the board bar's toggle.
        </div>
      </div>
    </>
  );
}
