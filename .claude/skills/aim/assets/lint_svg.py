#!/usr/bin/env python3
"""Lint a hermes SVG diagram fragment against the style contract
(assets/hermes-contract.md, relative to this skill's own base directory).

Usage:
    python3 lint_svg.py <fragment.svg> [--marker-id arrow-<id>] [--prefix <p->]
    python3 lint_svg.py --selftest

Hard failures print as "line N: RULE_NAME: message" and cause a non-zero exit.
Warnings print the same way with a "(warning)" suffix and never affect the exit code.

Warn-tier rules, the complete list: TEXT_OVERFLOW, SHAPE_TEXT_FIT, NEST_WELL,
HOLDS_MISSING, HOLDS_EMPTY, GEOM_UNPARSEABLE. Every other rule is a hard failure.
Anything this linter cannot parse or read degrades to a finding — it never raises.

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
    # ---- shape kit: the 17 silhouettes ----
    "proc", "cli", "thread", "fn", "agent", "llm", "timer",
    "iface", "event", "ui",
    "table", "file", "queue", "type",
    "pkg", "machine", "cloud",
    # ---- shape-kit modifiers + the sparkle paint class ----
    "head", "holds", "spark",
}

# Elements banned outright by the contract's ban list — they leak into or execute
# in the host document once flattened. (<style> has its own STYLE_BLOCK rule.)
BANNED_ELEMENTS = {"script", "foreignObject", "image"}

# Classes that name a silhouette. Exactly one of these may appear on an element:
# a box is one kind of thing. Modifiers (accent/ok/warn/danger/dim/head/holds/dashed)
# and non-silhouette parts (edge/edgeLabel/arrowhead/flow-dot/axis/title/note/spark)
# are deliberately absent, so they compose with any shape.
SHAPE_CLASSES = {
    "node", "store", "group", "lane", "pill", "decision", "actor", "cell",
    "region", "step", "badge", "bar",
    "proc", "cli", "thread", "fn", "agent", "llm", "timer",
    "iface", "event", "ui",
    "table", "file", "queue", "type",
    "pkg", "machine", "cloud",
}

# Classes that ONLY ever belong on a <g> wrapping a box/container.
BOX_ONLY_CLASSES = {"node", "accent", "store", "group", "lane", "cell", "step",
                    "pill", "decision", "actor", "badge", "bar", "region",
                    # the 17 silhouettes always wrap their geometry in a <g>
                    "proc", "cli", "thread", "fn", "agent", "llm", "timer",
                    "iface", "event", "ui", "table", "file", "queue", "type",
                    "pkg", "machine", "cloud",
                    # modifiers ride along on the same <g>
                    "head", "holds"}

# Usable interior of each silhouette, a verbatim copy of the width table in the
# contract's Shape Kit. THE SURFACE MATTERS: every number here is measured against the
# silhouette's full box `W` — the box hermes sizes, the one the contract's table is
# written against — NOT against whatever <rect> the shape happens to draw. Two shapes
# deliberately draw a body rect smaller than the box (see SHAPE_RECT_INSET), and their
# allowance already pays for that; subtracting it from the rect width too double-counts
# the offset and warns on a box that is sized exactly right. silhouette_box() is the one
# place that reconstructs the box, so every rule reads the same `W`.
#
# `cloud` is measured, not skipped. Its outline is arcs that bulge past their own endpoints
# and path_points sees endpoints only, so a cloud's measured HEIGHT understates what it
# draws — but the contract's recipe walks x from `X` to `X+W` in three arc endpoints, so
# its measured WIDTH is exactly `W`, and width is all this rule reads.
SHAPE_WIDTH_ALLOWANCE = {
    "node": 24, "proc": 24, "cli": 24, "thread": 24, "ui": 24,
    "table": 24, "file": 24, "queue": 24, "type": 24,
    "fn": 40, "pkg": 36, "iface": 40, "event": 44, "agent": 46,
    "machine": 36, "store": 40, "decision": 60, "cloud": 32,
}

# Padding (left, top, right, bottom) between each shape's own body <rect> and the box
# `W`×`H` hermes reserved for it, so a measured rect can be grown back to that box.
# `iface` draws `x="X+16" width="W-16"` (the lollipop owns the leftmost 16px);
# `machine` draws `x="X" y="Y+12" width="W-12" height="H-12"` (the top and right faces
# own the remaining 12px each). Every other silhouette draws its body at the full box,
# so it has no entry and gets (0, 0, 0, 0).
SHAPE_RECT_INSET = {"iface": (16, 0, 0, 0), "machine": (0, 12, 12, 0)}

# Silhouettes the contract draws with no body <rect> at all — an outline path, or a path
# plus a cap ellipse. Only these fall back to measuring their own outline geometry: for
# the 24 rect-drawn silhouettes an absent <rect> means malformed markup, and measuring
# whatever decorative paths remain yields a "box" that is not the box (a `node` drawing
# only two sparkle glyphs would report a 49px interior it never had).
PATH_DRAWN_SHAPES = {"store", "file", "event", "decision", "cloud"}

# Silhouettes whose label is drawn outside the shape, so no interior to fit it in.
TEXT_FIT_EXEMPT_SHAPES = {"llm", "timer"}

# Nestable ⟺ the Shape Kit gives the silhouette a content well. `node` belongs here:
# it is a plain rounded rect with a real rectangular interior, and it is the fallback
# for a kind absent from the lookup table — the fallback must not be less capable than
# the silhouettes it stands in for, so an unclassifiable component can still hold parts.
#
# `lane` is deliberately NOT here, and it is the one exclusion worth stating: in this kit
# a `lane` is a swimlane *header* — a small box at the top of the diagram with a lifeline
# dropping BELOW it — not a full-height container. The contract says so twice ("Never a
# parent", and again in the depth carve-out), gives it no row in the content-well table,
# and the stylesheet centers a `lane` label in its own box. A participant's boxes hang off
# the lifeline as siblings of the header, never inside it.
NESTABLE_SHAPES = {"node", "proc", "agent", "machine", "store", "pkg", "ui", "cli",
                   "group", "region"}

# The NEST_PARENT predicate: a silhouette that may not hold a *component* child at
# all, because a child crammed into one clips the very angles the shape exists for.
# Derived by subtraction so it can never disagree with NESTABLE_SHAPES, which is the
# single source of truth. As of this writing that yields exactly:
#
#     actor badge bar cell cloud decision event file fn iface lane llm pill queue
#     step table thread timer type
#
# — 19 shapes: the contract's 18 "Never a parent" silhouettes, plus `table`, whose
# well row reads "its own `cell` children only" and is reproduced here by the blanket
# `cell` exemption in NEST_PARENT rather than by a per-parent carve-out. Note the
# default this encodes: a
# NEW shape token added to SHAPE_CLASSES without a NESTABLE_SHAPES entry becomes a
# hard-failing parent with no override. That is deliberate — the kit's shapes are
# leaves far more often than not — but it means every shape added here must be
# checked against the contract's well table in the same edit.
LEAF_SHAPES = SHAPE_CLASSES - NESTABLE_SHAPES

# Content wells: (dx, dy, dw, dh) insets applied to the parent's silhouette box — the
# box hermes reserved, reconstructed by silhouette_box() — to get the rectangle its
# children may occupy. Every row below is the contract's well-table row transcribed:
# `X+14, Y+26, W-28, H-38` becomes (14, 26, -28, -38), so the two can be diffed by eye.
#
# `store` is the ONE row that is not a straight transcription, because it is the one
# parent whose box is measured rather than reconstructed from a <rect>: its bottom cap
# arc bulges 10px below its own endpoints and path_points cannot see that, so a
# contract-conformant cylinder measures (X, Y, W, H-10). Its `dh` is therefore 10 less
# negative than the contract's `H-46`, which lands the well on exactly the contract's
# rectangle. Verify a change to this row by computing the resulting rectangle, not by
# comparing the four numbers to the table.
#
# `region` is the one nestable shape with no entry, on purpose: the contract's row reads
# "anywhere inside" — it is a background tint parts already sit on top of.
NESTABLE_WELLS = {
    "node": (14, 26, -28, -38),
    "proc": (16, 16, -32, -32),
    "agent": (34, 26, -46, -38),
    "machine": (12, 26, -36, -38),
    "store": (10, 26, -20, -36),
    "pkg": (14, 26, -26, -38),
    "ui": (8, 26, -16, -34),
    "cli": (8, 24, -16, -32),
    "group": (14, 30, -28, -44),
}

# Boundaries rather than components: they wrap other things by definition, so they do not
# consume one of the two allowed levels of component nesting (NEST_DEPTH elides them).
#
# `group` and `region` are also in NESTABLE_SHAPES and are exempt from HOLDS_MISSING —
# the stylesheet start-anchors both their labels, so neither ever needs `holds` to get
# clear of its children. `lane` is NOT in NESTABLE_SHAPES (see there) and its label is
# centered, not top-left; it is listed here only because the contract counts it the same
# way "for consistency, though it is never a parent". Since a `lane` can never legally be
# an ancestor, its depth exemption is unreachable by design, and its HOLDS_MISSING
# exemption is redundant with the NESTABLE_SHAPES gate that already excludes it.
CONTAINER_SHAPES = {"group", "region", "lane"}

# Attributes each element type carries plain user-space numbers in. Every geometry
# rule reads these, and silently skips an element whose numbers do not parse — which
# is what GEOM_UNPARSEABLE exists to make visible. <text>/<tspan> `font-size` is swept
# by the same rule but is not listed here: its x/y are allowed to be coordinate lists,
# so the tag needs its own narrower check (see the GEOM_UNPARSEABLE block).
GEOM_ATTRS = {
    "rect": ("x", "y", "width", "height"),
    "circle": ("cx", "cy", "r"),
    "ellipse": ("cx", "cy", "rx", "ry"),
    "line": ("x1", "y1", "x2", "y2"),
}

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
    (anchor/font-size in TEXT_OVERFLOW) see classes inherited from a wrapping <g>.
    parent/children give the shape and nesting rules the actual tree — ctx_cls
    flattens it, so it cannot tell "my parent is a store" from "some ancestor is"."""

    __slots__ = ("tag", "attrib", "line", "text", "ctx_cls", "parent", "children")

    def __init__(self, tag, attrib, line, ctx_cls="", parent=None):
        self.tag = tag
        self.attrib = attrib
        self.line = line
        self.text = ""
        self.ctx_cls = ctx_cls
        self.parent = parent
        self.children = []


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
        parent = stack[-1] if stack else None
        node = Node(name, dict(attrs), parser.CurrentLineNumber, ctx, parent)
        elements.append(node)
        if parent is not None:
            parent.children.append(node)
        else:
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


def parse_number(raw):
    """`raw` as a finite float, or None. NaN and ±inf parse successfully in Python but
    every comparison against a NaN is False and every comparison against an inf is
    trivially true, so letting one through silently switches a geometry or text rule off
    (NaN) or floods it (inf). They are rejected here, once, for every rule that reads a
    number — GEOM_UNPARSEABLE then reports them the same way it reports `100px`."""
    try:
        v = float(raw)
    except (TypeError, ValueError):
        return None
    if v != v or v == float("inf") or v == float("-inf"):
        return None
    return v


def path_points(d):
    """Walk a path `d` string and return the absolute (x, y) endpoint reached after
    each command repetition. Bezier/arc control points are intentionally skipped —
    only on-curve endpoints are checked — to avoid false positives on tight curves.

    Nothing is emitted before the first `M`/`m`. A `d` that opens with any other command
    is malformed (SVG requires a moveto first), and its implicit current point is the
    ORIGIN — so measuring it would drag the element's box to (0,0) and report a box the
    author never drew. A leading or stray `Z` is the same case."""
    tokens = _PATH_TOKEN_RE.findall(d)
    points = []
    i = 0
    n = len(tokens)
    cmd = None
    started = False
    cx = cy = 0.0
    start_x = start_y = 0.0
    while i < n:
        tok = tokens[i]
        if _IS_CMD_RE.match(tok):
            cmd = tok
            i += 1
            if cmd in ("Z", "z"):
                if not started:
                    continue
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
        args = [parse_number(tokens[i + k]) for k in range(count)]
        if any(a is None for a in args):
            break
        i += count
        if letter == "M":
            started = True
        elif not started:
            continue
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


def class_tokens(el):
    return el.attrib.get("class", "").split()


def shape_tokens(el):
    """Shape-class tokens on this element, in source order."""
    return [t for t in class_tokens(el) if t in SHAPE_CLASSES]


def shape_of(el):
    """The silhouette this element names, or None. On a SHAPE_COMBO element the
    first token wins so downstream rules still have something to report against."""
    toks = shape_tokens(el)
    return toks[0] if toks else None


def rect_box(el):
    """(x, y, w, h) of a <rect>, or None when a coordinate is unparseable."""
    vals = [parse_number(el.attrib.get(a, 0)) for a in ("x", "y", "width", "height")]
    if any(v is None for v in vals):
        return None
    return tuple(vals)


def own_geometry(el):
    """This shape's own geometry elements: its direct <rect>/<path>/<ellipse>/<circle>
    children, plus those inside any wrapping <g> that names no silhouette of its own.

    The walk stops at the next shape-carrying <g>, so a child silhouette's geometry is
    never counted as the parent's. It exists because hermes wraps geometry in a bare <g>
    often enough that reading direct children only let an unclassed wrapper switch
    NEST_WELL and SHAPE_TEXT_FIT off silently — with no GEOM_UNPARSEABLE to show for it,
    since every number parsed fine. Iterative rather than recursive: a pathological
    fragment can be 20 000 <g>s deep."""
    out = []
    stack = list(reversed(el.children))
    while stack:
        c = stack.pop()
        if c.tag == "g":
            if shape_of(c) is None:
                stack.extend(reversed(c.children))
            continue
        out.append(c)
    return out


def own_rect_boxes(el):
    return [b for b in (rect_box(c) for c in own_geometry(el) if c.tag == "rect") if b]


def widest_own_rect(el):
    """The parent's own silhouette rect: the widest of the <rect>s it draws.
    proc/pkg/queue/machine all draw detail rects alongside the body, and the body
    is always the widest of them."""
    boxes = own_rect_boxes(el)
    return max(boxes, key=lambda b: b[2]) if boxes else None


def geom_extent(el, skip_spark=False):
    """Bounding box (x0, y0, x1, y1) over the geometry an element draws itself (see
    own_geometry) — <rect> corners, <path> on-curve endpoints, <ellipse>/<circle>
    bounds — or None when it draws none. Bezier control points and the bulge of an arc
    past its own endpoints are not measured (see path_points), so a silhouette whose
    outline bows outward between endpoints measures narrower than it draws; `store`'s
    bottom cap and `cloud`'s shoulders are the two places in the kit where that bites,
    and both are handled where they are used.

    skip_spark drops `spark` paths: the sparkle is a mark riding on a silhouette,
    never part of its outline."""
    xs, ys = [], []
    for c in own_geometry(el):
        if skip_spark and "spark" in class_tokens(c):
            continue
        if c.tag == "rect":
            b = rect_box(c)
            if b:
                xs.extend((b[0], b[0] + b[2]))
                ys.extend((b[1], b[1] + b[3]))
        elif c.tag == "path" and "d" in c.attrib:
            for (x, y) in path_points(c.attrib["d"]):
                xs.append(x)
                ys.append(y)
        elif c.tag in ("ellipse", "circle"):
            cx = parse_number(c.attrib.get("cx", 0))
            cy = parse_number(c.attrib.get("cy", 0))
            rx = parse_number(c.attrib.get("rx", c.attrib.get("r", 0)))
            ry = parse_number(c.attrib.get("ry", c.attrib.get("r", 0)))
            if None in (cx, cy, rx, ry):
                continue
            xs.extend((cx - rx, cx + rx))
            ys.extend((cy - ry, cy + ry))
    if not xs:
        return None
    return (min(xs), min(ys), max(xs), max(ys))


def silhouette_box(el, shape):
    """(x, y, w, h) of the BOX hermes reserved for this shape <g> — the `W`×`H` every
    contract table is written against, not whatever the shape happened to draw.

    Two sources, and the distinction is the point:

    - A shape that draws a <rect> is measured from its widest one (the body, so detail
      rects — proc's inset, pkg's tabs, queue's offsets — never win) and then grown back
      to the box by SHAPE_RECT_INSET. `iface` and `machine` draw their body smaller than
      their box, and their width/well allowances already pay for that; measuring the rect
      alone made them pay twice.
    - A shape in PATH_DRAWN_SHAPES draws no body rect at all, so its outline geometry is
      the box. Note this measures on-curve endpoints, so a `store`'s cap and floor and a
      `cloud`'s humps understate its HEIGHT; both callers read width only, except
      NESTABLE_WELLS["store"], whose `dh` is written against the measured height.

    Anything else with no <rect> returns None: for a rect-drawn silhouette an absent rect
    is malformed markup, and measuring the decorations that remain would invent a box."""
    r = widest_own_rect(el)
    if r is not None:
        left, top, right, bottom = SHAPE_RECT_INSET.get(shape, (0, 0, 0, 0))
        return (r[0] - left, r[1] - top, r[2] + left + right, r[3] + top + bottom)
    if shape not in PATH_DRAWN_SHAPES:
        return None
    ext = geom_extent(el, skip_spark=True)
    if ext is None:
        return None
    return (ext[0], ext[1], ext[2] - ext[0], ext[3] - ext[1])


def holds_label(el):
    """True when this <text>/<tspan> is a `holds` parent's OWN label. The contract's
    `svg .holds > text` rule restyles those to 12px and start-anchored, so the text
    estimators have to model them that way. Deliberately a direct-parent walk, not a
    ctx_cls test: the CSS uses the direct-child combinator, so a label nested deeper
    inside a `holds` box keeps its own shape's styling."""
    p = el.parent
    while p is not None and p.tag in ("text", "tspan"):
        p = p.parent
    return p is not None and p.tag == "g" and "holds" in class_tokens(p)


def font_size_of(el):
    """Font size for a <text>/<tspan>: an explicit attribute when it is a finite plain
    number, otherwise the stylesheet's class-driven default for that context. `NaN` and
    `12pt` both fall through to the default rather than disabling the text rules — and
    GEOM_UNPARSEABLE reports the attribute so the fallback is never silent."""
    if "font-size" in el.attrib:
        v = parse_number(el.attrib["font-size"])
        if v is not None:
            return v
    if holds_label(el):
        return 12.0
    cls = (el.attrib.get("class", "") + " " + el.ctx_cls).strip()
    if "cell" in cls or "step" in cls:
        return 11.0
    if "title" in cls:
        return 14.0
    if "edgeLabel" in cls or "group" in cls or "note" in cls:
        return 12.0
    return 13.0


def estimated_text_width(el):
    """Rough rendered width of one element's own text run, at the contract's
    ~0.54em average glyph. TEXT_OVERFLOW and SHAPE_TEXT_FIT both call this so the
    heuristic can only ever be tuned in one place."""
    return len(el.text.strip()) * font_size_of(el) * (7.0 / 13.0)


def nearest_shape_g(el):
    """(<g> element, its shape class) for the closest shape-carrying <g> ancestor."""
    p = el.parent
    while p is not None:
        if p.tag == "g":
            s = shape_of(p)
            if s:
                return p, s
        p = p.parent
    return None, None


def has_shape_descendant(el):
    """True when any element below this one is a <g> naming a silhouette, however deep
    and through any number of unclassed wrappers. Iterative for the same reason
    own_geometry is."""
    stack = list(el.children)
    while stack:
        c = stack.pop()
        if c.tag == "g" and shape_of(c) is not None:
            return True
        stack.extend(c.children)
    return False


def shape_g_ancestors(el):
    """Every shape-carrying <g> ancestor's class, nearest first."""
    out = []
    p = el.parent
    while p is not None:
        if p.tag == "g":
            s = shape_of(p)
            if s:
                out.append(s)
        p = p.parent
    return out


def lint(path, marker_id=None, prefix=None):
    """Return (failures, warnings), each a list of (line, RULE, message)."""
    failures = []
    warnings = []
    # A missing path, a directory, a permissions problem: all findings, never a
    # traceback. Being handed a fragment that has not been written yet is routine.
    try:
        raw = Path(path).read_bytes()
    except OSError as e:
        return [(1, "FILE_READ",
                 f"cannot read {path}: {e.strerror or e}")], []

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
        # SHAPE_COMBO — a box is one kind of thing, so exactly one silhouette per element.
        # This is what retires the legacy `node store` / `cell store` combos. Tokens are
        # deduped first: `class="proc proc"` names exactly one silhouette, which is what
        # the rule asks for, and reporting it as "proc and proc" is unactionable.
        shape_toks = []
        for t in shape_tokens(el):
            if t not in shape_toks:
                shape_toks.append(t)
        if len(shape_toks) > 1:
            # The cell-header hint is only relevant when a `cell` is one of the offenders;
            # on `proc iface` it sends a fix round hunting for a cell that is not there.
            hint = (' (header cells are "cell head", not "cell store")'
                    if "cell" in shape_toks else "")
            failures.append((el.line, "SHAPE_COMBO",
                              f'classes "{shape_toks[0]}" and "{shape_toks[1]}" are both shape '
                              f'classes ({" + ".join(shape_toks)}); exactly one silhouette per '
                              f"<g> — pick the single shape for this box's kind from the "
                              f"kind→shape table{hint}"))
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
        if "spark" in tokens and el.tag != "path":
            failures.append((el.line, "CLASS_PLACEMENT",
                              f'class "spark" must be on <path>, found on <{el.tag}>'))

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

    # GEOM_UNPARSEABLE (warning) — a coordinate the geometry rules cannot read.
    # COORD_BOUNDS, NEST_WELL and SHAPE_TEXT_FIT all skip an element whose numbers do
    # not parse, so without this rule a fragment can print "clean" while three checks
    # are silently switched off for that box. Warn-tier on purpose: `width="100px"` is
    # legal SVG that renders exactly as intended, it is just unmeasurable here.
    for el in elements:
        for attr in GEOM_ATTRS.get(el.tag, ()):
            if attr not in el.attrib:
                continue
            if parse_number(el.attrib[attr]) is None:
                warnings.append((el.line, "GEOM_UNPARSEABLE",
                                  f'<{el.tag}> {attr}="{el.attrib[attr]}" is not a finite plain '
                                  f"number, so every geometry check skips this element; write "
                                  f"unitless user-space coordinates"))
        # font-size gets the same treatment for a different reason: it does not disable
        # the text rules, it makes them measure the stylesheet default instead of what
        # the browser will render. `NaN` is the case worth naming — it parses as a float
        # and then every comparison against it is False.
        if el.tag in ("text", "tspan") and "font-size" in el.attrib:
            if parse_number(el.attrib["font-size"]) is None:
                warnings.append((el.line, "GEOM_UNPARSEABLE",
                                  f'<{el.tag}> font-size="{el.attrib["font-size"]}" is not a '
                                  f"finite plain number, so this label is measured at the "
                                  f"stylesheet's default size instead; write a unitless size"))

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
            width = estimated_text_width(el)
            anchor = el.attrib.get("text-anchor")
            if not anchor:
                if (holds_label(el) or "group" in cls or "note" in cls
                        or "title" in cls or "region" in cls):
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

    # ---- shape-kit containment rules ----
    # Only <g> elements are considered: a shape class anywhere else is already a
    # CLASS_PLACEMENT failure, and treating it as a silhouette would double-report.
    shape_gs = [(el, shape_of(el)) for el in elements
                if el.tag == "g" and shape_of(el) is not None]

    # Every shape <g> attributed to its NEAREST shape-<g> ancestor, so an unclassed
    # <g> sitting between a parent and its child cannot hide the relationship from
    # HOLDS_MISSING while NEST_PARENT/NEST_DEPTH/NEST_WELL all still see through it.
    kids_of = {}
    for el, shape in shape_gs:
        parent_g, _ = nearest_shape_g(el)
        if parent_g is not None:
            kids_of.setdefault(id(parent_g), []).append(shape)

    for el, shape in shape_gs:
        parent_g, parent_shape = nearest_shape_g(el)

        # NEST_PARENT — only a silhouette the Shape Kit gives a content well may hold a
        # *component*. A `cell` is not a component, it is data: Q11 makes a type's
        # fields a cell stack and a queue's items likewise, and a table's body is
        # nothing but cells. So a `cell` child is legal inside any silhouette, and no
        # per-parent carve-out is needed.
        if shape != "cell" and parent_shape in LEAF_SHAPES:
            failures.append((el.line, "NEST_PARENT",
                              f'<g class="{shape}"> is nested inside a "{parent_shape}" '
                              f"(line {parent_g.line}), which is leaf-only in the Shape Kit "
                              f"(no content well); move it inside one of "
                              f"{sorted(NESTABLE_SHAPES)}, wrap the pair in a dashed group, or "
                              f"expand the parent in its own tunnel card. A <g class=\"cell\"> "
                              f"is data, not a component, and may sit inside any silhouette"))

        # NEST_DEPTH — two levels of *component* nesting. group/region/lane are
        # boundaries, not components, so they do not consume a level. `cell` is data,
        # not a component: it never fails a depth check itself and never adds a level
        # to one, which is what keeps `ui > table > cell` legal per the contract.
        ancestors = shape_g_ancestors(el)
        if shape != "cell":
            components = [s for s in ancestors
                          if s not in CONTAINER_SHAPES and s != "cell"]
            if len(components) >= 2:
                links = list(reversed(components)) + [shape]
                # A pathological fragment can be hundreds deep; the useful part of the
                # chain is the outermost box and the last few before the offender, so
                # elide the middle rather than printing a 300-token line per failure.
                if len(links) > 6:
                    chain = " > ".join([links[0], f"… {len(links) - 4} more …"] + links[-3:])
                else:
                    chain = " > ".join(links)
                failures.append((el.line, "NEST_DEPTH",
                                  f'component "{shape}" sits {len(components) + 1} levels deep '
                                  f"({chain}); the limit is 2 — expand the parent in its own "
                                  f"tunnel card instead of nesting deeper"))

        # NEST_WELL (warning) — the child's box must land inside the parent's well.
        # Both sides are measured with the full geometry walk, not rects alone, so a
        # path-drawn parent (`store`) or child (`store`/`file`/`event`/`decision`) and
        # a child `iface`'s protruding lollipop knob are all visible to the check.
        if parent_shape in NESTABLE_WELLS:
            pbox = silhouette_box(parent_g, parent_shape)
            cbox = geom_extent(el)
            if pbox and cbox:
                dx, dy, dw, dh = NESTABLE_WELLS[parent_shape]
                wx, wy = pbox[0] + dx, pbox[1] + dy
                wx2, wy2 = wx + pbox[2] + dw, wy + pbox[3] + dh
                cx0, cy0, cx1, cy1 = cbox
                # A parent smaller than its own insets has an inverted well, which every
                # child "falls outside" — a line that reads as a linter bug rather than a
                # layout problem. The parent being too small to hold anything is a real
                # defect, but it is not the one this rule can describe.
                if wx2 <= wx or wy2 <= wy:
                    pass
                elif cx0 < wx or cy0 < wy or cx1 > wx2 or cy1 > wy2:
                    warnings.append((el.line, "NEST_WELL",
                                      f'<g class="{shape}"> box [{cx0:g},{cy0:g} '
                                      f"{cx1:g},{cy1:g}] falls outside the \"{parent_shape}\" "
                                      f"content well [{wx:g},{wy:g} {wx2:g},{wy2:g}]"))

        # HOLDS_MISSING (warning) — `holds` is the modifier that moves a parent's own
        # label into a top-left band clear of its children. (Its mirror, HOLDS_EMPTY,
        # runs in its own sweep below: that one has to reach a <g> with no shape class.)
        #
        # HOLDS_MISSING is gated on the parent actually being ALLOWED to hold: asking a
        # leaf to add `holds` contradicts the NEST_PARENT failure it just got in the same
        # output, and a fix round reading both with no other context takes the cheap,
        # wrong one. Boundary containers already label top-left, and `cell` children are
        # data whose parent's label never lands on them.
        components = [k for k in kids_of.get(id(el), []) if k != "cell"]
        if (components and "holds" not in class_tokens(el)
                and shape in NESTABLE_SHAPES and shape not in CONTAINER_SHAPES):
            warnings.append((el.line, "HOLDS_MISSING",
                              f'<g class="{shape}"> contains a nested {components[0]} but does '
                              f'not carry "holds"; add it to this <g> so the parent\'s own label '
                              f"moves to the top-left band instead of sharing space with its "
                              f"children"))

    # HOLDS_EMPTY (warning) — the mirror of HOLDS_MISSING. Swept over EVERY <g> carrying
    # `holds`, not just the shape-carrying ones: `svg .holds > text` restyles a label by
    # class alone, so `<g class="holds">` with no silhouette gets the same muted 12px band
    # and the same silent visual regression. Emptiness is a descendant question, so a
    # child parked under an unclassed wrapper still counts as held.
    for el in elements:
        if el.tag != "g" or "holds" not in class_tokens(el):
            continue
        if has_shape_descendant(el):
            continue
        shape = shape_of(el)
        named = f"{shape} holds" if shape else "holds"
        warnings.append((el.line, "HOLDS_EMPTY",
                          f'<g class="{named}"> carries "holds" but contains no nested shape '
                          f'<g>; "holds" restyles this box\'s own label to a muted 12px '
                          f'top-left band, so drop it unless the box really holds children'))

    # SHAPE_TEXT_FIT (warning only) — a label wider than its silhouette's interior.
    for el in elements:
        if el.tag not in ("text", "tspan"):
            continue
        content = el.text.strip()
        if not content:
            continue
        parent_g, shape = nearest_shape_g(el)
        if shape is None or shape in TEXT_FIT_EXEMPT_SHAPES:
            continue
        allowance = SHAPE_WIDTH_ALLOWANCE.get(shape)
        if allowance is None:
            continue
        # silhouette_box already reports the box `W` that SHAPE_WIDTH_ALLOWANCE is
        # written against, reconstructed from the body rect where the shape draws one
        # narrower than its box. It returns None when there is nothing trustworthy to
        # measure — a rect-drawn silhouette that drew no rect — and then the rule stays
        # quiet rather than warning about a box it invented.
        pbox = silhouette_box(parent_g, shape)
        if pbox is None:
            continue
        box_w = pbox[2]
        usable = box_w - allowance
        if box_w <= 0 or usable <= 0:
            continue  # degenerate or unmeasurable box: not a label-fit problem
        width = estimated_text_width(el)
        if width > usable:
            warnings.append((el.line, "SHAPE_TEXT_FIT",
                              f'estimated label width {width:.0f}px exceeds the "{shape}" '
                              f"usable interior {usable:.0f}px (W-{allowance} of a {box_w:g}px "
                              f"box); widen the box or shorten the label"))

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
    """Lint every fixture named by lint-fixtures/manifest.json and check it against
    that entry's expectations.

    A manifest entry is {"file", "expect": "pass"|"fail", "warnings": int, "note", and
    optionally "marker_id"/"prefix"}. `warnings` is an exact count and is OPTIONAL by
    design — absent means "unchecked" — but every entry in the shipped manifest sets it
    on purpose, so a fixture that starts emitting a new warning can never slip through
    silently. It only ever makes the check stricter: a fail/pass mismatch is a mismatch
    regardless of the warning count.

    A manifest that is malformed, or that names a fixture that is not on disk, is a
    reported mismatch — never a traceback. Every field is type-checked before use, so a
    wrong-typed value (`"file": 5`, `"warnings": null`) is a `[BAD ENTRY]` line rather
    than an exception or a silently unpinned fixture."""
    fixtures_dir = Path(__file__).resolve().parent / "lint-fixtures"
    manifest_path = fixtures_dir / "manifest.json"
    try:
        manifest = json.loads(manifest_path.read_text())
    except OSError as e:
        print(f"selftest: cannot read {manifest_path}: {e.strerror or e}")
        return 1
    except ValueError as e:  # json.JSONDecodeError subclasses ValueError
        print(f"selftest: {manifest_path} is not valid JSON: {e}")
        return 1
    if not isinstance(manifest, list):
        print(f"selftest: {manifest_path} must be a JSON array of fixture entries, "
              f"found {type(manifest).__name__}")
        return 1
    all_ok = True
    for entry in manifest:
        # Every value is type-checked before it is used, not just tested for
        # truthiness: `{"file": 5}` is valid JSON, and `fixtures_dir / 5` raises.
        problem = None
        if not isinstance(entry, dict):
            problem = "must be a JSON object"
        else:
            name = entry.get("file")
            expected = entry.get("expect")
            if not isinstance(name, str) or not name:
                problem = 'needs "file": a fixture filename'
            elif "/" in name or name in (".", ".."):
                problem = ('"file" must name a fixture in lint-fixtures/, not a path '
                           "into another directory")
            elif expected not in ("pass", "fail"):
                problem = 'needs "expect": "pass" | "fail"'
            elif not isinstance(entry.get("marker_id", ""), str):
                problem = '"marker_id" must be a string'
            elif not isinstance(entry.get("prefix", ""), str):
                problem = '"prefix" must be a string'
            elif "warnings" in entry and (not isinstance(entry["warnings"], int)
                                          or isinstance(entry["warnings"], bool)
                                          or entry["warnings"] < 0):
                # Distinguished from absent on purpose: `null`, `"0"` and `-1` would all
                # otherwise read as "unchecked" or mismatch with a line that looks like
                # a pass. Absent is the only way to opt out of the count.
                problem = '"warnings" must be a non-negative integer, or absent'
        if problem is not None:
            print(f"[BAD ENTRY] {entry!r}: {problem}")
            all_ok = False
            continue
        fpath = fixtures_dir / name
        if not fpath.is_file():
            print(f"[MISSING] {name}: named by manifest.json but not present in "
                  f"{fixtures_dir.name}/")
            all_ok = False
            continue
        failures, warnings = lint(fpath, marker_id=entry.get("marker_id"), prefix=entry.get("prefix"))
        got = "fail" if failures else "pass"
        ok = got == expected
        expected_warnings = entry.get("warnings")
        warn_ok = expected_warnings is None or len(warnings) == expected_warnings
        ok = ok and warn_ok
        all_ok = all_ok and ok
        suffix = "" if expected_warnings is None else f", expected {expected_warnings} warnings"
        print(f"[{'OK' if ok else 'MISMATCH'}] {name}: expected {expected}, got {got} "
              f"({len(failures)} failures, {len(warnings)} warnings{suffix})")
        for ln, rule, msg in failures:
            print(f"    line {ln}: {rule}: {msg}")
        if not warn_ok:
            for ln, rule, msg in warnings:
                print(f"    line {ln}: {rule}: {msg} (warning)")
    print()
    print("selftest: " + ("ALL GREEN" if all_ok else "FAILURES PRESENT"))
    return 0 if all_ok else 1


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("fragment", nargs="?", help="path to the .svg fragment to lint")
    ap.add_argument("--marker-id", help="expected arrow marker id, e.g. arrow-my-diagram")
    ap.add_argument("--prefix", help="required id prefix for every other id in the fragment")
    ap.add_argument("--selftest", action="store_true",
                     help="lint the bundled lint-fixtures/ and check each one against its "
                          "manifest entry's expected verdict and warning count")
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
