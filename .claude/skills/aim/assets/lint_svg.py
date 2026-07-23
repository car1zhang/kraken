#!/usr/bin/env python3
"""Lint a hermes SVG diagram fragment against the style contract
(assets/hermes-contract.md, relative to this skill's own base directory).

Usage:
    python3 lint_svg.py <fragment.svg> [--marker-id arrow-<id>] [--prefix <p->]
    python3 lint_svg.py --selftest

Hard failures print as "line N: RULE_NAME: message" and cause a non-zero exit.
Warnings print the same way with a "(warning)" suffix and never affect the exit code.

stdlib only: re, sys, json, argparse, pathlib, xml.parsers.expat.
"""
import argparse
import json
import re
import sys
import xml.parsers.expat as expat
from pathlib import Path

ALLOWED_CLASSES = {
    "node", "accent", "group", "store", "lane",
    "edge", "dashed", "edgeLabel", "arrowhead", "flow-dot",
    "ok", "warn", "danger",
    "cell", "step",
    "pill", "decision", "actor", "badge", "bar", "axis", "region",
    "title", "note", "dim",
}

# Elements banned outright by the contract's ban list — they leak into or execute
# in the host document once flattened. (<style> has its own STYLE_BLOCK rule.)
BANNED_ELEMENTS = {"script", "foreignObject", "image"}

# Classes that ONLY ever belong on a <g> wrapping a box/container.
BOX_ONLY_CLASSES = {"node", "accent", "store", "group", "lane", "cell", "step",
                    "pill", "decision", "actor", "badge", "bar", "region"}

# Classes that ONLY ever belong on a <text> element.
TEXT_ONLY_CLASSES = {"edgeLabel", "title", "note"}

# Status classes are dual-purpose in the contract: a box variant on <g> (node ok/warn/danger)
# or an edge variant on <path> (edge ok/warn/danger) — valid host is <g> or <path>, never anything else.
STATUS_CLASSES = {"ok", "warn", "danger"}

# (?<!&) keeps XML numeric character references (&#8594; etc.) from matching as hex colors.
COLOR_LITERAL_RE = re.compile(r"(?<!&)#[0-9a-fA-F]{3,8}\b|\brgba?\(|\bhsla?\(")

_NUM = r"-?\d*\.?\d+(?:[eE][-+]?\d+)?"
_PATH_TOKEN_RE = re.compile(r"[MmLlHhVvCcSsQqTtAaZz]|" + _NUM)
_IS_CMD_RE = re.compile(r"^[MmLlHhVvCcSsQqTtAaZz]$")
_TIME_RE = re.compile(r"^(-?" + r"\d*\.?\d+" + r")\s*m?s?$")

# Argument count consumed per repetition of each path command (uppercase key).
_PARAM_COUNTS = {
    "M": 2, "L": 2, "T": 2,
    "H": 1, "V": 1,
    "S": 4, "Q": 4,
    "C": 6,
    "A": 7,
    "Z": 0,
}


class Node:
    """Minimal element record: tag, attrs, 1-indexed source line, accumulated text.
    ctx_cls carries the concatenated class strings of all ancestors, so heuristics
    (anchor/font-size in TEXT_OVERFLOW) see classes inherited from a wrapping <g>."""

    __slots__ = ("tag", "attrib", "line", "text", "ctx_cls")

    def __init__(self, tag, attrib, line, ctx_cls=""):
        self.tag = tag
        self.attrib = attrib
        self.line = line
        self.text = ""
        self.ctx_cls = ctx_cls


def parse_svg(svg_bytes):
    """Parse raw SVG source into (root, all_elements) using expat directly —
    this gives real, reliable line numbers via parser.CurrentLineNumber, which
    xml.etree.ElementTree does not expose portably."""
    elements = []
    stack = []
    root_holder = {}

    parser = expat.ParserCreate()

    def start(name, attrs):
        ctx = " ".join(n.attrib.get("class", "") for n in stack if n.attrib.get("class"))
        node = Node(name, dict(attrs), parser.CurrentLineNumber, ctx)
        elements.append(node)
        if not stack:
            root_holder["root"] = node
        stack.append(node)

    def end(name):
        stack.pop()

    def chardata(data):
        if stack:
            stack[-1].text += data

    parser.StartElementHandler = start
    parser.EndElementHandler = end
    parser.CharacterDataHandler = chardata
    parser.Parse(svg_bytes, True)
    return root_holder.get("root"), elements


def path_points(d):
    """Walk a path `d` string and return the absolute (x, y) endpoint reached after
    each command repetition. Bezier/arc control points are intentionally skipped —
    only on-curve endpoints are checked — to avoid false positives on tight curves."""
    tokens = _PATH_TOKEN_RE.findall(d)
    points = []
    i = 0
    n = len(tokens)
    cmd = None
    cx = cy = 0.0
    start_x = start_y = 0.0
    while i < n:
        tok = tokens[i]
        if _IS_CMD_RE.match(tok):
            cmd = tok
            i += 1
            if cmd in ("Z", "z"):
                cx, cy = start_x, start_y
                points.append((cx, cy))
                continue
        if cmd is None:
            i += 1
            continue
        letter = cmd.upper()
        relative = cmd.islower()
        count = _PARAM_COUNTS.get(letter, 0)
        if count == 0:
            continue
        if i + count > n:
            break
        try:
            args = [float(tokens[i + k]) for k in range(count)]
        except ValueError:
            break
        i += count
        if letter in ("M", "L", "T"):
            x, y = args
            if relative:
                x, y = cx + x, cy + y
            cx, cy = x, y
            points.append((cx, cy))
            if letter == "M":
                start_x, start_y = cx, cy
        elif letter == "H":
            (x,) = args
            cx = cx + x if relative else x
            points.append((cx, cy))
        elif letter == "V":
            (y,) = args
            cy = cy + y if relative else y
            points.append((cx, cy))
        elif letter in ("C", "S", "Q", "A"):
            x, y = args[-2], args[-1]
            if relative:
                x, y = cx + x, cy + y
            cx, cy = x, y
            points.append((cx, cy))
    return points


def lint(path, marker_id=None, prefix=None):
    """Return (failures, warnings), each a list of (line, RULE, message)."""
    failures = []
    warnings = []
    raw = Path(path).read_bytes()

    try:
        root, elements = parse_svg(raw)
    except expat.ExpatError as e:
        line = getattr(e, "lineno", 1) or 1
        return [(line, "XML_PARSE", f"not well-formed XML: {e}")], []

    if root is None:
        return [(1, "XML_PARSE", "empty document")], []

    text = raw.decode("utf-8", errors="replace")

    # STYLE_BLOCK
    for el in elements:
        if el.tag == "style":
            failures.append((el.line, "STYLE_BLOCK",
                              "<style> element is banned; use the shared class vocabulary"))

    # BANNED_ELEMENT — <script>/<foreignObject>/<image> flatten into the host doc
    for el in elements:
        if el.tag in BANNED_ELEMENTS:
            failures.append((el.line, "BANNED_ELEMENT",
                              f"<{el.tag}> element is banned by the contract"))

    # STYLE_ATTR / PAINT_ATTR
    for el in elements:
        if "style" in el.attrib:
            failures.append((el.line, "STYLE_ATTR",
                              f'style="{el.attrib["style"]}" attribute is banned'))
        for attr in ("fill", "stroke"):
            if attr in el.attrib:
                failures.append((el.line, "PAINT_ATTR",
                                  f'{attr}="{el.attrib[attr]}" attribute is banned; '
                                  f"use a class from the parts kit"))

    # COLOR_LITERAL — scan raw source lines (also catches literals inside a
    # style="" value or a <style> block, on top of their own rule failures)
    for lineno, line_text in enumerate(text.splitlines(), start=1):
        if COLOR_LITERAL_RE.search(line_text):
            failures.append((lineno, "COLOR_LITERAL",
                              "hex/rgb/hsl color literal found; use a sanctioned class instead"))

    # UNKNOWN_CLASS / CLASS_PLACEMENT
    for el in elements:
        cls = el.attrib.get("class")
        if not cls:
            continue
        tokens = cls.split()
        for tok in tokens:
            if tok not in ALLOWED_CLASSES:
                failures.append((el.line, "UNKNOWN_CLASS",
                                  f'class "{tok}" is outside the sanctioned vocabulary'))
        box_tokens = [t for t in tokens if t in BOX_ONLY_CLASSES]
        if box_tokens and el.tag != "g":
            failures.append((el.line, "CLASS_PLACEMENT",
                              f"class {box_tokens} must be on <g>, found on <{el.tag}>"))
        status_tokens = [t for t in tokens if t in STATUS_CLASSES]
        if status_tokens and el.tag not in ("g", "path"):
            failures.append((el.line, "CLASS_PLACEMENT",
                              f"class {status_tokens} must be on <g> (box variant) or <path> "
                              f"(edge variant), found on <{el.tag}>"))
        if any(t in ("edge", "dashed") for t in tokens) and el.tag != "path":
            failures.append((el.line, "CLASS_PLACEMENT",
                              f'class "edge"/"dashed" must be on <path>, found on <{el.tag}>'))
        text_tokens = [t for t in tokens if t in TEXT_ONLY_CLASSES]
        if text_tokens and el.tag != "text":
            failures.append((el.line, "CLASS_PLACEMENT",
                              f"class {text_tokens} must be on <text>, found on <{el.tag}>"))
        if "axis" in tokens and el.tag != "path":
            failures.append((el.line, "CLASS_PLACEMENT",
                              f'class "axis" must be on <path>, found on <{el.tag}>'))
        if "flow-dot" in tokens and el.tag != "circle":
            failures.append((el.line, "CLASS_PLACEMENT",
                              f'class "flow-dot" must be on <circle>, found on <{el.tag}>'))
        if "arrowhead" in tokens and el.tag != "path":
            failures.append((el.line, "CLASS_PLACEMENT",
                              f'class "arrowhead" must be on <path>, found on <{el.tag}>'))

    # ROOT_SIZE + viewBox extraction
    vb_w = vb_h = None
    if root.tag != "svg":
        failures.append((root.line, "ROOT_SIZE", f"root element is <{root.tag}>, expected <svg>"))
    else:
        if "width" in root.attrib or "height" in root.attrib:
            failures.append((root.line, "ROOT_SIZE",
                              "width/height attributes are banned on the root <svg>"))
        vb = root.attrib.get("viewBox", "")
        m = re.match(r"^\s*0\s+0\s+860\s+(" + _NUM + r")\s*$", vb)
        if not m:
            failures.append((root.line, "ROOT_SIZE", f'viewBox must be "0 0 860 H", found "{vb}"'))
        else:
            vb_w, vb_h = 860.0, float(m.group(1))

    # COORD_BOUNDS
    if vb_w is not None:
        def check_point(ln, x, y, ctx):
            if not (0 <= x <= vb_w) or not (0 <= y <= vb_h):
                failures.append((ln, "COORD_BOUNDS",
                                  f"{ctx} point ({x:g},{y:g}) is outside viewBox 0 0 {vb_w:g} {vb_h:g}"))

        for el in elements:
            a = el.attrib
            try:
                if el.tag == "rect":
                    x, y = float(a.get("x", 0)), float(a.get("y", 0))
                    w, h = float(a.get("width", 0)), float(a.get("height", 0))
                    check_point(el.line, x, y, "<rect> top-left")
                    check_point(el.line, x + w, y + h, "<rect> bottom-right")
                elif el.tag in ("circle", "ellipse"):
                    cx, cy = float(a.get("cx", 0)), float(a.get("cy", 0))
                    check_point(el.line, cx, cy, f"<{el.tag}> center")
                elif el.tag == "line":
                    x1, y1 = float(a["x1"]), float(a["y1"])
                    x2, y2 = float(a["x2"]), float(a["y2"])
                    check_point(el.line, x1, y1, "<line> start")
                    check_point(el.line, x2, y2, "<line> end")
                elif el.tag in ("text", "tspan"):
                    if "x" in a and "y" in a:
                        x = float(a["x"].split(",")[0])
                        y = float(a["y"].split(",")[0])
                        check_point(el.line, x, y, f"<{el.tag}> anchor")
                elif el.tag == "path" and "d" in a:
                    for (x, y) in path_points(a["d"]):
                        check_point(el.line, x, y, "<path> d")
            except (KeyError, ValueError):
                continue

    # DUP_ID
    ids_seen = {}
    for el in elements:
        eid = el.attrib.get("id")
        if eid is None:
            continue
        if eid in ids_seen:
            failures.append((el.line, "DUP_ID",
                              f'id "{eid}" duplicates one first seen at line {ids_seen[eid]}'))
        else:
            ids_seen[eid] = el.line

    # TITLE_MISSING — the contract requires every diagram to open with one
    # <text class="title"> stating the figure's takeaway
    titles = [el for el in elements
              if el.tag == "text" and "title" in el.attrib.get("class", "").split()]
    if not titles:
        failures.append((root.line, "TITLE_MISSING",
                          'no <text class="title"> found; every diagram must state its '
                          "takeaway in a required title (top-left)"))

    # MARKER_ID
    if marker_id:
        markers = [el for el in elements if el.tag == "marker"]
        if not markers:
            failures.append((root.line, "MARKER_ID",
                              f'no <marker> element found; expected id="{marker_id}"'))
        else:
            for m in markers:
                mid = m.attrib.get("id")
                if mid != marker_id:
                    failures.append((m.line, "MARKER_ID",
                                      f'marker id "{mid}" does not match expected "{marker_id}"'))

    # ID_PREFIX
    if prefix:
        exempt = {marker_id} if marker_id else set()
        for eid, ln in ids_seen.items():
            if eid in exempt:
                continue
            if not eid.startswith(prefix):
                failures.append((ln, "ID_PREFIX", f'id "{eid}" does not start with prefix "{prefix}"'))

    # ANIM_BEGIN
    for el in elements:
        if el.tag not in ("animate", "animateMotion", "animateTransform"):
            continue
        begin = el.attrib.get("begin")
        if not begin:
            continue
        for part in begin.split(";"):
            part = part.strip()
            m = _TIME_RE.match(part)
            if m and float(m.group(1)) > 0:
                failures.append((el.line, "ANIM_BEGIN",
                                  f'begin="{begin}" has a positive delay; use "0s" or a negative value'))
                break

    # TEXT_OVERFLOW (warning only)
    if vb_w is not None:
        for el in elements:
            if el.tag not in ("text", "tspan"):
                continue
            content = el.text.strip()
            if not content:
                continue
            cls = (el.attrib.get("class", "") + " " + el.ctx_cls).strip()
            try:
                font_size = float(el.attrib["font-size"]) if "font-size" in el.attrib else None
            except ValueError:
                font_size = None
            if font_size is None:
                if "cell" in cls or "step" in cls:
                    font_size = 11.0
                elif "title" in cls:
                    font_size = 14.0
                elif "edgeLabel" in cls or "group" in cls or "note" in cls:
                    font_size = 12.0
                else:
                    font_size = 13.0
            char_w = font_size * (7.0 / 13.0)
            width = len(content) * char_w
            anchor = el.attrib.get("text-anchor")
            if not anchor:
                if "group" in cls or "note" in cls or "title" in cls or "region" in cls:
                    anchor = "start"
                else:
                    anchor = "middle" if cls else "start"
            if "x" not in el.attrib:
                continue
            try:
                x = float(el.attrib["x"].split(",")[0])
            except ValueError:
                continue
            if anchor == "middle":
                lo, hi = x - width / 2, x + width / 2
            elif anchor == "end":
                lo, hi = x - width, x
            else:
                lo, hi = x, x + width
            if lo < 16 or hi > (vb_w - 16):
                warnings.append((el.line, "TEXT_OVERFLOW",
                                  f"estimated text extent [{lo:.0f},{hi:.0f}] is within 16px "
                                  f"of viewBox 0..{vb_w:g}"))

    failures.sort(key=lambda f: f[0])
    warnings.sort(key=lambda w: w[0])
    return failures, warnings


def print_results(path, failures, warnings):
    for ln, rule, msg in failures:
        print(f"line {ln}: {rule}: {msg}")
    for ln, rule, msg in warnings:
        print(f"line {ln}: {rule}: {msg} (warning)")
    if not failures and not warnings:
        print(f"{path}: clean")


def selftest():
    fixtures_dir = Path(__file__).resolve().parent / "lint-fixtures"
    manifest = json.loads((fixtures_dir / "manifest.json").read_text())
    all_ok = True
    for entry in manifest:
        fpath = fixtures_dir / entry["file"]
        failures, warnings = lint(fpath, marker_id=entry.get("marker_id"), prefix=entry.get("prefix"))
        got = "fail" if failures else "pass"
        expected = entry["expect"]
        ok = got == expected
        all_ok = all_ok and ok
        print(f"[{'OK' if ok else 'MISMATCH'}] {entry['file']}: expected {expected}, got {got} "
              f"({len(failures)} failures, {len(warnings)} warnings)")
        for ln, rule, msg in failures:
            print(f"    line {ln}: {rule}: {msg}")
    print()
    print("selftest: " + ("ALL GREEN" if all_ok else "FAILURES PRESENT"))
    return 0 if all_ok else 1


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("fragment", nargs="?", help="path to the .svg fragment to lint")
    ap.add_argument("--marker-id", help="expected arrow marker id, e.g. arrow-my-diagram")
    ap.add_argument("--prefix", help="required id prefix for every other id in the fragment")
    ap.add_argument("--selftest", action="store_true",
                     help="run against the bundled lint-fixtures/ and report pass/fail")
    args = ap.parse_args()

    if args.selftest:
        return selftest()

    if not args.fragment:
        ap.error("fragment path is required unless --selftest is given")

    failures, warnings = lint(args.fragment, marker_id=args.marker_id, prefix=args.prefix)
    print_results(args.fragment, failures, warnings)
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
