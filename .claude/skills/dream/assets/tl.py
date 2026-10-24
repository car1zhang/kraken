#!/usr/bin/env python3
"""tl.py — one line per record of a Claude Code transcript, root or subagent sidecar.

Usage:
  python3 -I tl.py <transcript.jsonl> [--since TS] [--until TS] [--grep REGEX]
                   [--errors] [--stats] [--width N]
  python3 -I tl.py --selftest

TS is unix millis, unix seconds, or an ISO-8601 timestamp; an ISO value with no
`Z` or offset is read as UTC, the zone every printed timestamp uses. Records with
no parseable timestamp print with ts `-` and are kept, since about half of a
transcript's lines carry none; a window (--since/--until) drops them. A line
that is not a JSON object prints as `?` plus its first bytes (dropped under a
window, since it has no time). A field that is missing prints as `-`. Nothing
here raises on a record's shape; a flag given wrongly is a usage error (exit 2),
and a closed pipe (`| head`) ends the run quietly.

Which records count. Claude Code writes several record types per turn. The
timeline prints `user`, `assistant`, `system`, `queue-operation`, `attachment`
and the like, and SKIPS `api-request-blob` and `api-request-shape`: those
replay earlier messages' content into the file and would count every tool call
and result about twice. Tool calls are tallied from `assistant` records only,
results and is_error from `user` records only. A root transcript seeded by
`--fork-session` (a side thread) carries the parent's records with a snake-case
`session_id` naming the parent: those print with a `~` after the type and are
left out of every tally and sum, the same ownership rule kraken's token readers
apply. Sidecars carry no `session_id`, which reads as own.

Columns, tab-separated: ts, type, model, summary, usage.
  ts       UTC `YYYY-MM-DD HH:MM:SS.mmmZ`.
  type     the record's `type`; a user record made of tool_result blocks reads
           `result`, or `result!` when any block is is_error; `~` marks a
           replayed parent record (see above).
  model    assistant `message.model`; `<synthetic>` is Claude Code's own banner
           or null-turn record, not an API call.
  summary  tool_use → `Tool(head of command | file_path | pattern | prompt)`;
           text → its head; tool_result → `<- Tool` (`<- Tool!` on error) and
           the head of its content; queue-operation → the op and the head of
           its content.
  usage    `in=.. out=.. cw=.. cr=..` on assistant records that carry usage.

--errors keeps only is_error result lines. --grep keeps lines whose summary
matches. --width N truncates summaries (default 160; 0 or less means no
truncation). --stats appends: records by type, tool_use by name, is_error by
tool, synthetic records, models seen, and usage summed per `message.id` the way
kraken's `core.tokenGroup.commit` does it — input and both cache counters from a
message's FIRST record, output from its LAST (a message is written as one record
per content block; summing every record over-counts about 2x).
"""

import argparse
import json
import os
import re
import sys
import tempfile
from collections import Counter, OrderedDict
from datetime import datetime, timezone

SKIPPED_TYPES = {"api-request-blob", "api-request-shape"}


def parse_ts(s):
    """Unix millis, unix seconds, or ISO-8601 (naive read as UTC) → float seconds, or None."""
    if s is None:
        return None
    if isinstance(s, (int, float)):
        v = float(s)
        return v / 1000.0 if v > 1e12 else v
    s = str(s).strip()
    if not s:
        return None
    if re.fullmatch(r"\d+(\.\d+)?", s):
        v = float(s)
        return v / 1000.0 if v > 1e12 else v
    try:
        dt = datetime.fromisoformat(s.replace("Z", "+00:00"))
    except ValueError:
        return None
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt.timestamp()


def fmt_ts(t):
    if t is None:
        return "-"
    return datetime.fromtimestamp(t, timezone.utc).strftime("%Y-%m-%d %H:%M:%S.%f")[:-3] + "Z"


def head(s, width):
    s = " ".join(str(s).split())
    if width <= 0 or len(s) <= width:
        return s
    return s[: width - 1] + "…"


def to_int(v):
    try:
        return int(v or 0)
    except (TypeError, ValueError):
        return 0


def block_summary(block, width):
    if not isinstance(block, dict):
        return head(block, width)
    kind = block.get("type")
    if kind == "tool_use":
        inp = block.get("input")
        if not isinstance(inp, dict):
            inp = {}
        arg = (
            inp.get("command")
            or inp.get("file_path")
            or inp.get("pattern")
            or inp.get("prompt")
            or inp.get("description")
            or ""
        )
        return f"{block.get('name', '-')}({head(arg, width)})"
    if kind == "tool_result":
        content = block.get("content")
        if isinstance(content, list):
            content = " ".join(
                c.get("text", "") if isinstance(c, dict) else str(c) for c in content
            )
        return head(content if content is not None else "", width)
    if kind == "text":
        return head(block.get("text", ""), width)
    if kind == "thinking":
        return "[thinking]"
    return f"[{kind}]"


def own_session(path):
    """A root transcript's own session id is its basename stem; a sidecar (agent-*.jsonl) has none."""
    stem = os.path.splitext(os.path.basename(path))[0]
    if stem.startswith("agent-"):
        return None
    return stem


def run(path, since=None, until=None, grep=None, errors_only=False, stats=False, width=160, out=None):
    out = out or sys.stdout
    own = own_session(path)
    types = Counter()
    tools = Counter()
    errors_by_tool = Counter()
    models = Counter()
    synthetic = 0
    replayed = 0
    usage = OrderedDict()  # message.id -> [in, out, cw, cr]
    tool_names = {}  # tool_use id -> name, registered before any window filter
    windowed = since is not None or until is not None

    with open(path, "r", errors="replace") as f:
        for raw in f:
            raw = raw.rstrip("\n")
            if not raw.strip():
                continue
            try:
                rec = json.loads(raw)
            except ValueError:
                rec = None
            if not isinstance(rec, dict):
                if not windowed and not errors_only and grep is None:
                    out.write(f"-\t?\t-\t{head(raw, width)}\t\n")
                continue
            rtype = str(rec.get("type", "-"))
            if rtype in SKIPPED_TYPES:
                continue
            msg = rec.get("message") if isinstance(rec.get("message"), dict) else {}
            content = msg.get("content")
            if content is None and rtype == "queue-operation":
                content = rec.get("content")
            # Register tool ids from every assistant record BEFORE the window
            # filter, so a result inside the window still names a call just
            # before it.
            if rtype == "assistant" and isinstance(content, list):
                for b in content:
                    if isinstance(b, dict) and b.get("type") == "tool_use":
                        tool_names[b.get("id")] = b.get("name", "-")
            t = parse_ts(rec.get("timestamp"))
            if since is not None and (t is None or t < since):
                continue
            if until is not None and (t is None or t > until):
                continue
            sid = rec.get("session_id")
            foreign = bool(own and sid and sid != own)
            model = msg.get("model") or "-"
            mid = msg.get("id")
            u = msg.get("usage") if isinstance(msg.get("usage"), dict) else None
            usage_s = ""
            if foreign:
                replayed += 1
            else:
                types[rtype] += 1
            if rtype == "assistant":
                if not foreign:
                    models[model] += 1
                    if model == "<synthetic>":
                        synthetic += 1
                    if u and mid:
                        vals = [
                            to_int(u.get("input_tokens")),
                            to_int(u.get("output_tokens")),
                            to_int(u.get("cache_creation_input_tokens")),
                            to_int(u.get("cache_read_input_tokens")),
                        ]
                        if mid in usage:
                            usage[mid][1] = vals[1]  # output: the last record wins
                        else:
                            usage[mid] = vals  # input and cache counters: the first record
                if u:
                    usage_s = (
                        f"in={to_int(u.get('input_tokens'))} out={to_int(u.get('output_tokens'))} "
                        f"cw={to_int(u.get('cache_creation_input_tokens'))} cr={to_int(u.get('cache_read_input_tokens'))}"
                    )

            lines = []
            is_err = False
            if isinstance(content, list):
                for b in content:
                    if not isinstance(b, dict):
                        lines.append((head(b, width), False))
                        continue
                    kind = b.get("type")
                    if kind == "tool_use":
                        if rtype == "assistant" and not foreign:
                            tools[b.get("name", "-")] += 1
                        lines.append((block_summary(b, width), False))
                    elif kind == "tool_result":
                        name = tool_names.get(b.get("tool_use_id"), "-")
                        err = bool(b.get("is_error"))
                        if err:
                            is_err = True
                            if rtype == "user" and not foreign:
                                errors_by_tool[name] += 1
                        lines.append((f"<- {name}{'!' if err else ''} {block_summary(b, width)}", err))
                    else:
                        lines.append((block_summary(b, width), False))
            elif rtype == "queue-operation":
                lines.append((f"{rec.get('operation', '-')} {head(content if content is not None else '', width)}".rstrip(), False))
            elif content is not None:
                lines.append((head(content, width), False))
            else:
                lines.append(("", False))
            shown = rtype
            if rtype == "user" and isinstance(content, list) and any(
                isinstance(b, dict) and b.get("type") == "tool_result" for b in content
            ):
                shown = "result!" if is_err else "result"
            if foreign:
                shown += "~"
            for s, err in lines:
                if errors_only and not err:
                    continue
                if grep is not None and not grep.search(s):
                    continue
                out.write(f"{fmt_ts(t)}\t{shown}\t{model}\t{s}\t{usage_s}\n")

    if stats:
        tot = [0, 0, 0, 0]
        for v in usage.values():
            for k in range(4):
                tot[k] += v[k]
        out.write("\n# stats\n")
        out.write("records by type: " + (", ".join(f"{k}={v}" for k, v in types.most_common()) or "none") + "\n")
        out.write("tool_use by name: " + (", ".join(f"{k}={v}" for k, v in tools.most_common()) or "none") + "\n")
        out.write("is_error by tool: " + (", ".join(f"{k}={v}" for k, v in errors_by_tool.most_common()) or "none") + "\n")
        out.write(f"synthetic assistant records: {synthetic}\n")
        out.write("models: " + (", ".join(f"{k}={v}" for k, v in models.most_common()) or "none") + "\n")
        out.write(f"replayed parent records (excluded above): {replayed}\n")
        out.write(
            f"usage per message.id (in/cw/cr first record, out last), {len(usage)} messages: "
            f"in={tot[0]} out={tot[1]} cw={tot[2]} cr={tot[3]}\n"
        )
    return {
        "types": types, "tools": tools, "errors": errors_by_tool, "synthetic": synthetic,
        "models": models, "replayed": replayed, "usage": dict(usage),
    }


def selftest():
    """A synthetic root transcript pinning the counting rules; prints OK or the failed check."""
    own = "11111111-2222-3333-4444-555555555555"
    msg1 = {"id": "m1", "model": "claude-opus-5-5", "usage": {"input_tokens": 3, "output_tokens": 2, "cache_creation_input_tokens": 100, "cache_read_input_tokens": 50}}
    msg1b = dict(msg1, usage=dict(msg1["usage"], output_tokens=40))
    recs = [
        {"type": "user", "timestamp": "2026-01-01T00:00:00Z", "message": {"content": "hello"}},
        {"type": "assistant", "timestamp": "2026-01-01T00:00:01Z", "message": dict(msg1, content=[{"type": "thinking"}])},
        {"type": "assistant", "timestamp": "2026-01-01T00:00:02Z", "message": dict(msg1b, content=[{"type": "tool_use", "id": "t1", "name": "Bash", "input": {"command": "ls"}}])},
        {"type": "api-request-blob", "message": {"content": [{"type": "tool_use", "id": "t1", "name": "Bash", "input": {"command": "ls"}}]}},
        {"type": "user", "timestamp": "2026-01-01T00:00:03Z", "message": {"content": [{"type": "tool_result", "tool_use_id": "t1", "is_error": True, "content": "boom"}]}},
        {"type": "assistant", "timestamp": "2026-01-01T00:00:04Z", "session_id": "99999999-0000-0000-0000-000000000000",
         "message": {"id": "p1", "model": "claude-opus-5-5", "usage": {"input_tokens": 1, "output_tokens": 500, "cache_creation_input_tokens": 9000, "cache_read_input_tokens": 0},
                     "content": [{"type": "tool_use", "id": "p-t", "name": "Read", "input": {"file_path": "/x"}}]}},
        {"type": "queue-operation", "timestamp": "2026-01-01T00:00:05Z", "operation": "dequeue"},
        {"type": "assistant", "timestamp": "2026-01-01T00:00:06Z", "message": {"id": "m2", "model": "<synthetic>", "content": [{"type": "text", "text": "You've hit your session limit"}], "usage": {"input_tokens": "abc"}}},
    ]
    d = tempfile.mkdtemp(prefix="tl-selftest-")
    p = os.path.join(d, own + ".jsonl")
    with open(p, "w") as f:
        f.write("not json\n")
        for r in recs:
            f.write(json.dumps(r) + "\n")
        f.write("[1]\n")

    class Sink:
        def __init__(self):
            self.lines = []

        def write(self, s):
            self.lines.extend(x for x in s.split("\n") if x)

    sink = Sink()
    r = run(p, stats=True, out=sink)
    checks = [
        ("tool_use counted once, blob skipped", r["tools"] == Counter({"Bash": 1})),
        ("is_error named by tool", r["errors"] == Counter({"Bash": 1})),
        ("usage: first for in/cw/cr, last for out, fork excluded", r["usage"].get("m1") == [3, 40, 100, 50] and "p1" not in r["usage"]),
        ("usage: a garbage counter reads as zero, not a crash", r["usage"].get("m2") == [0, 0, 0, 0]),
        ("replayed parent record counted as replayed", r["replayed"] == 1),
        ("synthetic counted, bad usage counter tolerated", r["synthetic"] == 1),
        ("non-object json prints as ?", sum(1 for ln in sink.lines if ln.split("\t")[1:2] == ["?"]) == 2),
        ("replayed record printed with ~", any("\tassistant~\t" in ln for ln in sink.lines)),
        ("dequeue prints its op", any("\tqueue-operation\t-\tdequeue\t" in ln for ln in sink.lines)),
    ]
    sink2 = Sink()
    run(p, since=parse_ts("2026-01-01T00:00:03"), until=parse_ts("2026-01-01T00:00:03Z"), errors_only=True, out=sink2)
    checks.append(("naive --since read as UTC, result in window names its tool", len(sink2.lines) == 1 and "<- Bash! boom" in sink2.lines[0]))
    failed = [name for name, ok in checks if not ok]
    for fn in os.listdir(d):
        os.unlink(os.path.join(d, fn))
    os.rmdir(d)
    if failed:
        print("SELFTEST FAILED: " + "; ".join(failed), file=sys.stderr)
        return 1
    print(f"OK ({len(checks)} checks)")
    return 0


def main(argv):
    ap = argparse.ArgumentParser(prog="tl.py", description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("transcript", nargs="?", help="path to a Claude Code transcript (.jsonl)")
    ap.add_argument("--since", help="unix millis, unix seconds, or ISO-8601 (naive = UTC)")
    ap.add_argument("--until", help="same forms as --since")
    ap.add_argument("--grep", help="regex a summary must match")
    ap.add_argument("--errors", action="store_true", help="only is_error result lines")
    ap.add_argument("--stats", action="store_true", help="append the counts and usage sums")
    ap.add_argument("--width", type=int, default=160, help="summary width; 0 or less means unlimited")
    ap.add_argument("--selftest", action="store_true", help="run the built-in fixture and exit")
    a = ap.parse_args(argv[1:])
    if a.selftest:
        return selftest()
    if not a.transcript:
        ap.error("a transcript path is required (or --selftest)")
    since = until = None
    if a.since is not None:
        since = parse_ts(a.since)
        if since is None:
            ap.error(f"--since {a.since!r} is not a timestamp")
    if a.until is not None:
        until = parse_ts(a.until)
        if until is None:
            ap.error(f"--until {a.until!r} is not a timestamp")
    grep = None
    if a.grep is not None:
        try:
            grep = re.compile(a.grep)
        except re.error as e:
            ap.error(f"--grep: {e}")
    if not os.path.isfile(a.transcript):
        print(f"tl.py: no such file: {a.transcript}", file=sys.stderr)
        return 1
    run(a.transcript, since, until, grep, a.errors, a.stats, a.width)
    return 0


if __name__ == "__main__":
    try:
        code = main(sys.argv)
        sys.stdout.flush()
    except BrokenPipeError:
        # `| head` closed the pipe: leave quietly, and keep the interpreter's
        # own flush-at-exit from printing a second traceback.
        try:
            sys.stdout = open(os.devnull, "w")
        except OSError:
            pass
        code = 0
    sys.exit(code)
