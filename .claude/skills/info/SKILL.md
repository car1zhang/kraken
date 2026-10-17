---
name: info
description: "KRAKEN-LEVEL copy (repo-owned, materialized into cell worktrees). Skill for explaining existing systems and answering deep questions about current behavior — the explain workflow. Researches deeply, then answers with ONE brief high-level overview and the minimum artifacts that answer the question; goes deeper only where the reader asks. Never opens open questions (that is /aim's job) and never proposes changes. Use when the user asks for /info, \"explain this system\", \"how does X work end to end\", \"document the current behavior of X\", \"investigate and write up X\", \"answer this question with a doc\", or when a kraken info-job brief invokes it. For proposed/future changes use /aim spec mode instead."
---

# Info — Explain An Existing System

> **Kraken-level copy** — loaded from `.claude/skills/info/` in this worktree. If the harness
> header above says `Base directory ... ~/.claude/skills/info`, the personal copy shadowed
> this one (personal beats project on a name collision).

Answer a question about a system that already exists. Research it deeply; explain it briefly.
The deliverable is a conversation, not a book: a short high-level `00-overview.md` first, then
whatever depth the reader actually asks for, one area at a time.

This is the read-only sibling of /aim — it never proposes changes — and it is deliberately
**lighter** than /aim: /info is not "/aim explain mode with a different name", and it must not
produce /aim's artifact set by default.

## The two rules that define this skill

**1. Deep research, brief output.** Depth of investigation and length of output are independent
knobs. Read the code until you actually understand it — entrypoints, call paths, the awkward
branches, the tests that pin behavior — then write the *short* version. Every claim traces to a
real path you read. Never pad the overview to look thorough; the research is what makes it
trustworthy, not the word count.

**2. No open questions. Ever.** /info never writes an open-questions record — not `kraken
questions add/set/update` in a cell, not `questions.yaml` standalone. Questions belong to /aim
(and, in a kraken cell, to a phase 2 {{operator}} opens after the explanation, which is `/do`'s and
follows its rules).
Uncertainty that survives research is one honest line inside the artifact it belongs to ("X is
not confirmed: <what you'd need to check>"), never a row and never a batch of prompts. If one
thing genuinely blocks the whole explanation, ask it directly in chat — a single question, not
a list — and keep going with a stated default while you wait.

## First pass: one brief overview

**Search {{operator}}'s mental model first.** Before writing a word of the overview, run `kraken mm get --q "<subject>"` (a case-insensitive substring over every live item's title, body and quote) for what {{operator}} already knows about the subject, then `kraken mm show <id>` for every hit that looks relevant — bias toward showing: an explanation written for {{operator}} is exactly where their own words on the subject matter most — and `kraken mm source <id>` only when the body is not enough; and should you notice an item contradicting another item or the current transcript, run `kraken mm flag --item <id> --note "<what contradicts what>"` and proceed (a teammate names the flag id in its report; zeus, the lead, is the only agent that raises or dismisses a flag, with `kraken mm flag raise|dismiss <id> --note …`, before its next stop, saying so in its final message): abstract what they know down to a clause, spend the page on what they are unfamiliar with, and carry the same "{{operator}} knows / is unfamiliar with" lines into any teammate brief that produces operator-facing prose. Mandated, not suggested — measured across 15 days, agents never pulled spontaneously. The per-file half is equally mandated: when explaining a file, citing a code ref, or assigning one to a diagram box, run `kraken mm get --file <path>` for that file first — the exact lines {{operator}} has read there decide whether the reference gets a clause or a walkthrough — and every hermes brief carries the same mandate for the files its assigned code refs name. Spell every one of these with the full `kraken` binary and run it as its own command, never inside a compound: a repo's own guardrail hook can block any command string containing a bare `k` (read as a kubectl alias) and kills everything bundled with it — a cell has lost its whole questions/feedback/mm compound to one `k mm get`, and the retry dropped the mental-model step entirely.

The default first deliverable is exactly one file: `00-overview.md`, and it stays short — a page
at most. Assume the reader wants orientation, not the complete tour. It covers, tersely:

| Part | Length | Content |
|---|---|---|
| The answer | 2-5 sentences | The direct answer to what was asked, up front. |
| Mental model | 3-6 sentences or a small table | The handful of moving parts and how they relate — the shape a reader needs to reason about the rest. |
| How it actually flows | 3-8 lines | One concrete pass through the system, real names and values, in order. |
| Where it lives | small table | The 3-8 files/functions that matter, one line each, so the reader can jump into source. |
| Not confirmed | 0-3 lines | Anything research could not settle. Omit the section entirely when there is nothing. |
| Go deeper | 3-6 bullets | Named areas you researched but deliberately did not expand, one line each — the menu the reader picks from. |

That last row is the point of the whole skill: it is how the reader steers the next round. Name
areas concretely ("the retry/backoff path in `daemon.go`", "how conflict state is resolved on
import"), never vaguely ("more detail on the backend").

**Diagrams in the first pass: almost always exactly one.** Ship `00-overview.md` with a single
`1N-<id>.html` diagram card unless you can say why a picture would add nothing — which is rare,
because an explanation is nearly always about how parts relate, and that is the thing prose is
worst at. The diagram carries the **mental model** row: it is the picture a reader would otherwise
have to build in their head from that table.

The bar is *replaces prose*, not *decorates it* — but assume it clears. Skipping is the exception
you justify, not the default you fall back to: an explanation whose answer is a single value, a
yes/no, or one linear sequence with no branching may genuinely have nothing to draw. If you do
skip, say so in one line in the "go deeper" menu so the reader can ask for it.

Still exactly ONE. Never ship the /aim two-core-diagram pair on a first pass — the cap is what
keeps a first pass a first pass; it is the *floor* that moved, not the ceiling.

**What the first pass does NOT include**, unless the request explicitly asks for it: a components
table, a code map beyond the small "where it lives" table, a testing/verification plan, an
assumptions doc, a scope doc, per-component test coverage, or any `5N` section doc at all. These
are /aim's required floor, not /info's. Shipping them unasked is the failure mode this skill
exists to prevent.

## Later passes: one area, one artifact

When the reader picks an area (or asks a follow-up), go as deep as that area needs and add **one**
artifact for it — a `50-<topic>.md` section doc (numbered sequentially from 50 as topics arrive)
or a `1N-<id>.html` diagram card. Keep `00-overview.md` as the index: add a one-line pointer to
the new artifact, and correct anything the deeper read proved wrong.

Do not retro-fit the rest of /aim's set because a second artifact now exists. Each round adds only
what was asked for. A five-round conversation should end with five focused artifacts, not a
generated spec.

## Explain-mode discipline

- **Describe, never prescribe.** No "should", no proposed refactors. A tempting improvement is
  one parenthetical observation at most, or the reader's cue to start an /aim spec.
- **Cite real code.** File paths (and functions) for every load-bearing claim, so any paragraph
  is jumpable into source. `file.go:120` style is ideal.
- **Verification over assertion.** For a key behavior, say how to SEE it: the test that covers it,
  the log line, the command to run, the fixture that encodes it. One pointer beats a paragraph.
- **Uncertainty is content, not a question.** Say what you could not confirm, in place, and move on.
- **Answer the question that was asked.** Scope creep into neighboring subsystems is what makes
  explanations unreadable; mention the neighbor in one line and put it on the "go deeper" menu.
- **Never hard-wrap prose in an artifact.** One paragraph — and one bullet, and one table row — is ONE source line, however long; wrapping is the renderer's job, never the author's. A mid-sentence line break renders as a broken paragraph on the board, and it is a repeat complaint from the operator ("please get rid of them and remember not to do"). This binds every markdown artifact the job ships, and every teammate brief that asks for one.
- **Every md artifact opens with a one-liner stating its main message.** The first line after the title, before any section: one sentence saying what the artifact TELLS {{operator}} — the thing it adds to or confirms in their mental model — never a description of what it contains. A reader who stops after that line should still leave with the point; if no such sentence exists, the artifact does not know what it is for yet. This binds every markdown artifact the job ships (the operator's standing steering: most generated text goes unread, so every generation must be impactful).
- **The set root is operator-facing ONLY.** Internal reference material — working notes, agent reports, scratch data, anything outside the numbered set (the numbered files themselves are set members) — never lands in the set root; scratch paths own those. An internal file there is noise on the board wearing an artifact's clothes.

## Diagrams, when a round calls for one

Diagrams follow /aim's mechanics exactly — read `.claude/skills/aim/SKILL.md` (paths relative to
the worktree root, your cwd in a cell, not this skill's base directory) for the full rules. The
short form, with /info's overrides:

- Each diagram is designed AND rendered by a TEAMMATE on **`model: opus`** (Claude Opus 5 — sonnet
  misplaces elements), writing one complete `<svg>` fragment to the set's fragments dir
  (`<cellDir>/doc/fragments/<id>.svg` in a cell). You never read or write SVG bodies. A teammate is
  an Agent-tool call that passes a **`name`** — use `hermes-<diagram-id>` (unique set-wide, since
  diagram ids are; keep ids lowercase `[a-z0-9-]` and ≤ 40 chars, since the id rides inside a
  schema-validated agent name) — which buys it its own pane and keeps it addressable for a later
  round; an unnamed in-process subagent is for read-only exploration, never for rendering.
- Every brief carries the **path** `.claude/skills/aim/assets/hermes-contract.md` with an
  instruction to Read it in full before writing any markup and to compose only from it — never its
  contents pasted inline, which is /aim's rule and not an /info override: a re-typed contract
  silently diverges from the file and from the other teammates' copies, and the file is already
  materialized in the teammate's own cwd. Then the diagram's exact content, a card-unique
  `arrow-<id>` marker id and path-id prefix, the fragment output path — with /aim's two
  fragment-write clauses: **Read an existing output file before writing it** (Write refuses
  otherwise), and on first authorship **write incrementally** (`Write` the opening section, `Edit`
  the rest in; composing a whole SVG in extended thinking has died at the output ceiling
  repeatedly, invisibly) — **its own scratch report
  path** (`/tmp/<slug>-hermes-<diagram-id>-r<N>.md`) and **the exact lint command with both flags
  already filled in** — hermes runs it and writes its report with the argv and the linter's complete
  output verbatim, and that report file, never the fragment, is what you wait on (per /aim's
  done-test bullet: a lead that waits on the fragment wakes before the lint has run, lints
  concurrently with the teammate's own run, and — measured in an /info round — gets an UNFLAGGED
  `clean` from a hermes whose brief never carried the flags, then TaskStops it mid-turn) — and the
  anti-artifact rules:
  no overlapping elements, text measured against its box (~7px/char at 13px) and wrapped with
  tspans before it can overflow, edges attached to box borders and routed around (never through)
  other boxes, every coordinate inside the viewBox, generous whitespace between groups.
- Boxes are `box → kind → code ref` triples using the sanctioned kind list in the contract's Shape
  Kit section; the teammate derives the silhouette from the kind — never dictate a shape class — and
  copies the code ref verbatim into a `data-code` attribute on that box's `<g>`, the one metadata
  attribute the contract sanctions and one hermes may place but never invent. **You assign every ref
  and you VERIFY it exists before the brief goes out** — one `rg` or `ls` per ref, the same shape as
  the colour rule below: a ref is a factual claim about where code lives, the renderer cannot check
  it, and a wrong one ships as a link that opens nothing. The value is `path:line` relative to the
  repo root (a line `web/_ui/src/lib/markdown.ts:109`; a range `internal/api/editor.go:176-265`; a
  position `web/_ui/src/lib/codeRefs.ts:76:17`) with a COLON, never a `#`: a hash is the wrong shape
  outright, and when 3-8 hex-alphabet characters follow it the linter's colour-literal scan fails the
  whole fragment on top, with a message about paint. An edge gets one too when
  you can name the contract or call site it runs through; it rides that edge's own `<path>`. A box
  with no honest code location carries none rather than a plausible-looking one, and the parts
  (`pill`, `decision`, `lane`, `region`, `cell`, `actor`) never carry one. When a figure's boxes are
  code, add `--code-refs` to the lint command you hand hermes: it warns on any component box that
  reached the fragment without the ref you assigned.
- **Colour states what is going on with the thing, never how to feel about it.** A treatment is a
  factual claim about the element it lands on — which means it can be WRONG, and only you know
  whether it is true, so the brief must assign the treatment AND name its glyph: hermes applies what
  you assign, invents neither, and cannot check whether the claim holds. Six treatments:
  - `grey` `∅` — missing · absent · removed (renders dotted; the one treatment that survives greyscale)
  - `red` `✗` — critically wrong: confirmed AND critical
  - `yellow` `⚠` — warning · likely wrong · less critically wrong
  - `green` `✓` — fixed · passing · working
  - `blue` `+` — new · added
  - `purple` `★` — emphasis · unorthodox (reach for it least often)
  - `dim` — out of this figure's story; no glyph

  Two decisive rules: **red vs yellow** — red needs confirmed AND critical, and hesitation itself
  means unconfirmed, so yellow; **blue vs green** — provenance vs state: new in this change → blue,
  existed and now works → green (new AND working is blue). **Most boxes carry no treatment at all**
  — a treated element is the exception. The full table, the three bans, the two exemptions, and the
  decision procedure live in `.claude/skills/aim/assets/hermes-contract.md` § Colour Semantics; read
  that before assigning, and LOOK at `.claude/skills/aim/assets/palette-reference.html` whenever a
  call is not obvious.
- The done-test is /aim's, not a lighter one: read hermes' REPORT (argv + verbatim linter output),
  then lint once yourself to confirm — with the same two flags — only AFTER that report arrives,
  never concurrently. `SendMessage` line-numbered violations back to that same `hermes-<diagram-id>`
  teammate — it still holds its fragment's context, so never spawn a fresh agent for a fix round
  (max 2 rounds, then debug the fragment source directly). A hermes is done when every fragment it
  owns lints CLEAN under the flagged command, or carries an accepted warning per /aim's done-test
  bullet — not when it reports the fragment written — and is stopped (TaskStop, bare name) only
  then. Only settled fragments get wrapped.
- Wrap per /aim's "Wrap, don't flatten" rule: copy `.claude/skills/aim/assets/diagram-card.html`
  to `<set-root>/10-<id>.html` (next free number in `10-49`, assigned sequentially; never 3-digit
  prefixes) and substitute all three placeholders with a python one-liner using `str.replace`
  (never `re.sub` — a fragment containing `\1` or `\g` crashes its replacement parsing), in one
  pass, never letting an earlier replacement introduce a later token. HTML-escape `&`, `<`, `>` in
  the title and caption; NEVER escape the fragment (it is markup). Fill `{{CARD_TITLE}}` (occurs
  twice) and `{{CAPTION}}` (occurs once) first, then replace the literal
  `<!-- DIAGRAM: {{DIAGRAM_ID}} -->` comment with the fragment's raw contents — e.g.
  `python3 -c "import pathlib, html; t = pathlib.Path('.claude/skills/aim/assets/diagram-card.html').read_text(); t =
  t.replace('{{CARD_TITLE}}', html.escape(TITLE)).replace('{{CAPTION}}', html.escape(CAPTION)).replace('<!-- DIAGRAM: {{DIAGRAM_ID}} -->',
  pathlib.Path(FRAG).read_text()); pathlib.Path(OUT).write_text(t)"` (adapt `TITLE`/`CAPTION`/`FRAG`/`OUT`).
  Verify the output contains `<svg` and contains none of `{{CARD_TITLE}}`, `{{CAPTION}}`,
  `<!-- DIAGRAM:` — if either check fails the wrap failed; do not ship the card.
- Diagram ids are unique across the whole set (an id names both the fragment file and the card
  file). Explain-mode architecture diagrams are **current state only** — there is no target panel,
  because nothing is being proposed.
- All diagrams are inline SVG — never Mermaid, never ASCII art.

## Workflow

1. **Frame the question** in 2-4 bullets: what the reader wants to understand, what is in scope,
   what depth is right, what is explicitly out of scope. Keep this in chat, not in an artifact.
2. **Research until you could write the long version** — read the relevant `CLAUDE.md` / `AGENTS.md`,
   trace entrypoints with `rg`, read enough implementation to cite real paths, types, and behaviors,
   and fan out parallel researcher subagents for broad sweeps. Every codebase-answerable question gets
   answered here; none of them become open questions. **Give each researcher an agent type that can
   write — `general-purpose`, never `Explore`, whose roster excludes `Write` and `Edit` — and name its
   output file in its brief**, one exact scratch path outside the repo, with the chat reply a short
   summary plus that path. Read-only means the REPO, not the filesystem: a researcher briefed
   "write your findings to `<path>`" but spawned as `Explore` cannot satisfy that brief, and it does
   not report the problem, it rationalises ("enforced read-only mode", "my configuration prohibits
   creating files") while you wait on a file that is never coming. Add the line *if you have no
   `Write` tool, write the file with a Bash heredoc (`cat > <path> <<'EOF'`)*. For how to WAIT on
   those files, follow `.claude/skills/do/SKILL.md` step 4's "How to wait" bullet and the
   sub-bullets under it — it is the one specification of the protocol and it applies here unchanged.
   Two more clauses every researcher brief carries, because phase-1 briefs are where they keep
   getting dropped (the zsh rules used to live only in the phase-2 skill, and one measured night had
   every Bash error across eleven sessions be the same hook block on `--include=*.py` from
   researcher briefs that never mentioned the shell). First, the dialect line, verbatim: *Shell is
   zsh on macOS: write `--include='*.py'` (quote every glob), `echo "==="` (quote any word starting
   with `=`), `timeout N cmd` does not exist (use a background watch), never name a loop/read
   variable `path` or `fpath` (`while read sid path` wipes PATH for the rest of the run), use
   absolute paths (an earlier call's `cd` may still be in effect, or the cwd may have reset — a
   relative path is a bet either way), and never end a `;`-compound with a grep/sed that may match
   nothing (close with `; true`).* Second, the file-output contract does not survive
   re-delegation on its own: a brief that permits its researcher to spawn further agents
   says the contract passes down VERBATIM —
   every spawned agent gets its own named output file under the same scratch namespace and the same
   repo bar — or forbids re-delegation outright. A researcher told "read-only" has re-briefed its
   own children with "do not write any files", stranding a fan-out's entire findings in chat
   replies that truncate and vanish. The wait protocol's load-bearing lines, inline because a
   pointer measurably does not deliver them (the same lead used `-f` in the phase that had only
   the pointer and `-s` in the phase with the text loaded): delete each report path before
   spawning, arm ONE background conjunctive until-loop over every path with `-s` (never `-f` —
   true at byte zero), end the turn, and treat expiry as "one of them is missing".
   Two evidence rules for what the research may CONCLUDE. A NEGATIVE result from an external
   API or service — a 404, an empty page, a zero-row answer — is not evidence until a known-good
   positive control has run in the same session/connection: one cell published four consecutive
   "X is impossible" artifacts off an endpoint that 404s every request after a session's first,
   and a single positive control overturned all four. And a conclusion relayed from another cell
   or a side thread — pasted into your prompt, quoted in a spec — is a HYPOTHESIS with a
   citation, never a result: re-derive or spot-check it before an artifact rests on it, because
   relay strips the caveats (the same cell adopted a pasted "no workaround exists" verdict whose
   underlying zero-row evidence was that same 404 artifact, and built three artifacts on it).
3. **Write the short version.** Create the set root and write `00-overview.md` per the table above,
   plus the one diagram card that carries its mental model. Skip the diagram only in the rare case
   above, and say in the "go deeper" menu that you did.
4. **Share and stop.** Give the `00-overview.md` path plus the set root (never run `open`), then
   state the "go deeper" menu in your final message so the reader can pick. Then stop — the next
   round is theirs to steer.
5. **Iterate on demand.** Each request → deeper research on that area → one new artifact + a
   one-line pointer added to the overview. Correct anything the deeper read disproved.

## Inside A Kraken Cell

When invoked by a kraken job brief (the brief will say so and give paths), apply these deltas —
the cell's brief wins wherever it is more specific:

- The set root is `<cellDir>/artifacts/`: write `00-overview.md` there, plus any later diagram
  cards (`1N-<id>.html`) and topic docs (`5N-<topic>.md`) as rounds are requested. Fragments stage
  at `<cellDir>/doc/fragments/<id>.svg`. `artifacts/` is the only board-visible directory and is
  the deliverable.
- **Before any research fan-out, check for sibling work — unconditionally, never only when the
  spec points at another job.** Finished jobs' artifacts survive their cells' teardown in kraken's
  store: run `kraken job list` (every active job, finished ones included until archived) for slugs
  sharing your slug's leading tokens or naming your system, then `kraken artifacts get <slug>` lists
  a hit's artifacts and `kraken artifacts get <slug> 00-overview.md` prints its overview — skim that
  first. {{operator}} files related jobs minutes apart without cross-references,
  and one cell re-derived from scratch a spec its sibling had preserved seventeen minutes
  earlier — hundreds of thousands of output tokens of pure re-derivation that one lookup would
  have prevented.
- **Never run `kraken questions` write verbs during the explanation rounds** — no `add`, `set`,
  `update`, or `remove`; the list stays empty while the job is explaining. Once a `/do` or a change
  instruction opens phase 2 (below), `.claude/skills/do/SKILL.md` governs, its question rows
  included. (`kraken feedback get <slug>` still applies:
  {{operator}}'s per-artifact feedback arrives as rows, each entry you have not already handled gets acted on
  in that round, and you say what you changed for it in the same final message, BEFORE the closing line
  below — but it is never MARKED addressed. That column is the nightly `/dream` pass' alone, so a note
  you fixed in place keeps reading as open, both to that pass and on your next resume. Never decide from
  the LIST, then: the artifact is what records the fix, so read it against the note — act if the fix is
  not there, say so if it is. Re-doing a fix costs a minute; skipping a note {{operator}} is waiting on costs
  the note.)
- Never run `open` — it has no display to land in; the board renders the artifacts. A browser may still be REACHABLE from a cell: the claude-in-chrome MCP tools drive {{operator}}'s own Chrome when its extension is connected, and a "not connected" result is often transient — re-try the same call once after a few minutes before reporting a browser gate as blocked (a cell wrote a full fallback runbook and a blocked load estimate for a disconnection that had healed by the time the operator re-asked, 8 minutes later, and the reconnected session then produced the job's best artifacts).
- **Each round ends with one line, then you stop.** Say exactly:

  `explanation ready: <set root>/00-overview.md (+N diagrams, M sections)`

  followed by the "go deeper" menu in the same line, and STOP (end the turn).
  Use `0` where a count is zero — never omit the clause. There is no questions clause, because there are
  never any questions. Kraken derives the blocked state from your silence and shows your last words
  on the board; {{operator}} reads the artifacts and messages back directly, and each reply is the next
  round. A decision that genuinely cannot be defaulted may use `AskUserQuestion` — kraken surfaces
  the options as buttons — but that is for blockers, not for the routine uncertainty that belongs
  inline. Never declare the job done; accepting is {{operator}}'s move.
- An /info job usually ends at its explanation and opens no PR — {{operator}} accepts the
  artifacts straight from the board instead. A `/do`, or a plain-language instruction to change
  what the explanation described, sent in your pane, is the same gate opening: read
  `.claude/skills/do/SKILL.md` by path and run phase 2 from the overview plus that message, contract
  and crew included. A question about the explanation ("can we…?", "do we…?") is
  answered with an offer, never built — while a direct ask to make the change ("change X to Y",
  "can you make it Y") is the instruction.
- `solo: true` on the job means no teammates and no subagents at all: prose only — no diagram cards
  in any round.
