---
name: graph
description: "KRAKEN-LEVEL copy (repo-owned, materialized into cell worktrees). The knowledge graph's two agent-side jobs: ingest — drain pending episodes into entities and facts — and revision, where carl's instruction corrects what the graph believes. Use when a job spec invokes /graph, or when a kraken graph job brief sends you here."
---

# Graph — Ingest And Revision

> **Kraken-level copy** — loaded from `.claude/skills/graph/` in this worktree. If the harness
> header above says `Base directory ... ~/.claude/skills/graph`, a personal copy shadowed this one
> (personal beats project on a name collision).

Kraken remembers what it did. The daemon does the mechanical half — it sweeps transcripts, feedback
rows, job specs and artifacts into **episodes**, and it fills in embeddings and layout.
It does not understand any of it. That is this job: turn episodes into a graph of **entities** and
**facts**, or correct the graph when it has learned something wrong.

The graph is read back at every cell spawn (`graphContext`), so a wrong fact does not sit quietly in
a table — it is injected into the next agent's system prompt as something already known. Precision
beats recall here, always. **A fact you are not confident in is a fact you do not add.**

## The data model, in one table

| Thing | Is | Key property |
|---|---|---|
| **Episode** | one span of evidence: a transcript span, a feedback row, a job spec, an artifact — or the already-done episode a `kraken watchlist add` mints for itself, which is why watch observations never reach the pending queue | it is provenance — a fact SHOULD point back at the episode that produced it, but sweep edges may omit it and roughly one live edge in four currently does (measured 2026-08-03: 72 of 315), so a missing episode is a known state, never proof the fact is baseless |
| **Node** | one entity: a name, a type, and a **description** | the description is a short natural-language gloss of what the thing IS, independent of any edge |
| **Edge** | one fact: two endpoints, a predicate, and a **fact sentence** | the sentence IS the fact; the predicate is only how it is indexed |

Two time axes, and they mean different things. `valid_from`/`valid_until` are **world time** — when
the fact was true. `created_at`/`expired_at` are **system time** — when kraken learned and unlearned
it. Nothing is ever deleted: a fact that stops being true has its window closed, because "this was
true until July" is more useful than a silence.

## Modes

The job spec says which. **Ingest** drains the pending queue; **revision** carries carl's instruction
to correct what is already there. Both write only to `kraken.db`, and **neither opens a PR** — the
graph is not in git.

---

## Ingest mode

### 1. Take a batch

**The batch size is in the job spec**, on a line reading `Per-run episode cap: N`. Use that N:
`k graph pending --limit N` returns pending episodes as JSON: id, kind, ref, ts, and the reduced
content. The daemon sizes N from how deep the pending queue actually is, so a night with a backlog
gets a bigger batch than a quiet one — do not substitute a number of your own. The one exception is
`solo: true`, whose smaller-batch rule below still applies. **When the spec carries no such line,
use 25.**

Drain in the order given (oldest first) and stop at the end of one batch — the queue is long by
design and the nightly job is a drain, not a race.

For a `transcript` episode the content is a raw JSONL span and the ref is `path#start-end`. The span
is the evidence; when you need more than the span carries, `k mine reduce --since <ts> <path>` gives
the whole session as user prose + tool calls + assistant text, which is about an eighth of the bytes.
Do not read raw transcript JSONL by hand when `reduce` will answer — you will spend the batch's whole
budget on tool results nobody asked for.

### 2. Extract, as delimited tuples

Emit tuples, **never strict JSON**, while you are reading. This is the field consensus and the reason
is mechanical: JSON makes a model spend attention on brackets and escaping in the middle of the task
it is actually doing, and one malformed brace loses a whole batch. Convert to the add payload once,
at the end, when the thinking is done.

```
ENTITY|<name>|<type>|<one-sentence description of what this IS>
FACT|<source entity>|<predicate>|<target entity>|<the fact as a full sentence>|<strength 1-10>
```

**The seed types**, which cover nearly everything in this corpus:

`person` · `convention` · `component` · `file` · `job` · `decision` · `failure-mode` · `tool` ·
`external-system`

The list is **open**. When something genuinely is not one of these, name a new type in the same
lowercase-hyphenated style and use it consistently across the batch — a type invented once and never
reused is noise, so prefer the closest seed type unless the new one will recur.

Five rules about what a good tuple looks like:

- **The fact sentence must stand alone.** "It is skipped under mirror" is unreadable a month later;
  "tokensPass is skipped under --mirror because mirror runs only state derivation and the block-event
  artifact sync" is a fact. It will be read cold, in another job's system prompt, by an agent with
  none of this context.
- **Prefer durable over incidental.** "carl prefers stdlib-only Go" is worth a row. "the build failed
  at 14:02" is not. The test is whether it would still be true, and still useful, in three months.
- **Strength is your confidence that this is a durable, correct fact**, 1-10 — not how important it
  feels. An explicit instruction from carl is a 9; something you inferred from one agent's behaviour
  is a 3, and a 3 is usually a sign you should not add it at all. It is **required** on every edge:
  an absent or out-of-range strength is a 400, deliberately, because a default invented on your
  behalf would record a confidence nobody asserted. Omitting it fails the whole batch, not one edge.
- **A description states only what the source states — the refusal filter covers node glosses, not
  just edges.** This bites hardest on personal material (the corpus now includes carl's own notes):
  one ingest correctly refused every speculative EDGE and then shipped a node description carrying
  the exact inference its own report said the notes never make ("likely his partner"), which
  `/api/graph` then serves to the board and iOS graph views and `k graph search` returns. A
  relationship, diagnosis, or private circumstance the source does not state does not go in a
  gloss either — describe the person by what the notes actually say, and when the honest gloss
  feels thin, thin is correct.
- **One gleaning pass.** After the first pass over an episode, ask once: what did I miss? Then stop.
  There is no second gleaning and no community-summary layer; both cost far more than they return at
  this graph's scale.

### 3. Canonicalize AFTER extraction, never during

Extract with the names the episode actually uses. Only once the batch is extracted, resolve those
names against what already exists. **The existing vocabulary comes from ONE full-graph read** — the
same `/api/graph` pull the sweep below spells out — whose `nodes` list is the complete inventory of
canonical NAMES. Aliases are deliberately not joined on that route (they are visible one node at a
time via `/api/graph/node?id=N`), so a name absent from the inventory may still be an alias of an
existing node: let the resolve that `k graph add` performs catch those, and send a `description`
only for a node you are confident is genuinely new — an alias-resolved write OVERWRITES the
existing node's gloss and re-queues its embedding. `k graph search` cannot supply the vocabulary:
it is ranked retrieval with no way to enumerate (a degree-zero node is structurally absent from its
results, since the route builds its node list from returned edges) — two extraction teammates
briefed with only `search` burned ~80 probe calls rebuilding the node list by union, recovered two
DIFFERENT partial vocabularies, and canonicalized the two halves of one batch against different
worlds. That incident is the Delegation rule broken, not a capability to brief out: canonicalization
is the LEAD's step, done once across the whole batch against one inventory pull — teammates extract
with source names, never receive the inventory, and never resolve their own slices, because
canonicalizing while reading makes you bend the evidence toward names you already know, which is
how a graph converges on its own first guesses.

Resolution is **resolve-then-add**: an incoming name that matches an existing node's name or alias
merges into it, and a genuinely new name creates a node. When two names are the same thing, add the
loser as an **alias** rather than a second node.

**An LLM judge is a merge VETO, never a merge PROPOSER.** Use judgment to REFUSE a merge the string
match proposed — `kraken serve` and `kraken cutover` are not one component, `athena-logic` and
`athena-style` are not one agent. Never use it to invent a merge nothing else suggested: a
model asked "are these the same?" says yes far too often, and a wrong merge is the one mistake here
that is genuinely hard to undo, since it destroys the distinction that would have let anyone notice.

### 4. Write

```
k graph add --episode <episode id> --json <path to payload>
```

The payload names entities by NAME; the server resolves them:

```json
{
  "nodes": [
    {"name": "tokensPass", "type": "component",
     "description": "The daemon pass that keeps each job's job_tokens row current.",
     "aliases": ["token pass"]}
  ],
  "edges": [
    {"src": "tokensPass", "dst": "kraken serve", "predicate": "runs-inside",
     "fact": "tokensPass runs inside kraken serve's daemon tick and is skipped under --mirror.",
     "strength": 8}
  ],
  "invalidate": [
    {"edge_id": 412, "valid_until": 1753900000000}
  ]
}
```

A repeat of a fact that already exists is **not** a duplicate row: the store folds it into the
existing edge, bumping its `occurrences`. That count is what `graphContext` renders as `seen Nx`, and
it is the whole reason independent corroboration is worth recording — so state a fact you have seen
again rather than skipping it because it is already there. **But the fold keys on the EXACT
sentence** — `InsertEdge` matches `src + predicate + dst + fact`, whitespace runs collapsed on the
way in and everything else exact to the byte, case included — and a "fuller" restatement lands as a
second live edge beside the original, not a fold (the store's own comment calls near-duplicate
merging the revision job's call, not its own). So a re-sighting you want counted is sent with the
existing edge's `fact` field VERBATIM — the full-graph pull you already hold has the text — and a
wording you genuinely want improved is an `invalidate` of the old edge plus the reworded fact as a
new one, never a better sentence beside the old; note in the report that the fresh edge restarts
`occurrences` at 1, so the old corroboration count is not silently lost. This is the rule the
"must stand alone" bullet above pushes against, and obeying that bullet on a re-sighting is how one
night's ingest turned 7 corroborations into 6 duplicate live edges: measured 2026-08-03, the graph
held 32 (src, predicate, dst) groups with more than one live edge — 36 redundant edges, 11.4% of the
graph — while the occurrences histogram read `{1: 313, 2: 2}`, the corroboration signal dead
precisely because every sighting arrived freshly worded.

### 5. Contradictions close windows; they do not overwrite

When an episode says something that contradicts a live edge, put the old edge's id in `invalidate`
with the `valid_until` millis at which it stopped being true, and add the new fact as its own edge.
Do not edit the old one and do not silently add a second live edge saying the opposite — a graph
holding both is a graph that will hand a future agent whichever one ranks higher that day.

Only genuine contradiction. A fact that is merely more specific than an existing one is an addition,
not a correction.

**A fact that simply stopped being true is not a contradiction either.** An edge carries its own
`valid_until`, and that is **world time only** — "kraken used a bare `wt` worktree name until the
cutover" is one edge kraken still believes; the world moved on. An `invalidate` entry additionally
stamps system time, because it records kraken *unlearning* something it had asserted. Reach for the
edge's own `valid_until` when the world changed, and for `invalidate` when the graph was wrong.
And when ONE batch carries both a design and the evidence that superseded it, the superseded fact is
written with its `valid_until` already set — to the past millis the world moved on, since a future
value deliberately stays live to `graphContext` — keeping the history a bi-temporal graph exists to
hold rather than dropping the superseded fact entirely. "Nothing prior to invalidate" does not
license shipping the stale design open-ended: an ingest that reasoned exactly that way handed
`graphContext` a superseded read-path design as currently true, in the same batch as its own
supersession edge. (The world-time filter is `graphContext`'s; search and the board still show a
world-closed edge as live, which is correct — it is history, not error.)

**Re-invalidating an already-closed edge is an error, not a no-op** — `invalidate` matches live rows
only, so a re-run of a partially-completed batch will fail on the edges the first run already closed.
That error means the edge is **already closed**, never that it is gone. Drop those entries and
re-send the rest rather than treating the batch as unwritten.

### 6. Close the episode

`k graph done <episode id>` for every episode you processed, **including one you extracted nothing
from** — an episode with no durable facts in it is a processed episode, and leaving it pending means
the next run pays for it again. Say how many you skipped and why in the report.

### 7. Sweep for connectivity

Extraction produces spokes. Each episode's facts point at the entities that episode mentions —
usually a hub plus a handful of leaves — and nothing ties one batch's leaves to another's. Left
alone the graph converges on hub-and-spoke: the first sweep (2026-08-01) found the whole
pre-existing kraken cluster — zeus, athena, the web board, the pipeline skills — connected to carl
and never to the `kraken` node itself, and `ServiceTitan` sitting at degree zero. Sweep once per
run, after the batch is written and its episodes closed — and a run with no pending episodes still
sweeps, because the sweep walks the graph, not the batch.

- Pull the whole graph in one read, with this command exactly (kept on ONE line because it is
  copied verbatim, and a wrapped copy runs `curl` with no URL — both halves measured):

  ```
  ADDR="${KRAKEN_SERVE_ADDR:-127.0.0.1:7777}"; curl -sf --max-time 10 "http://${ADDR#http*://}/api/graph"
  ```

  That is the board's own read route, on the same address knob
  every `k` verb resolves through (the knob may carry a scheme, so strip one rather than double
  it; and still never kraken.db directly). Empty output is a failed read, never an empty graph —
  and output that does not parse as JSON is a failed read too (a trailing slash on the knob
  builds `//api/graph`, which answers 301 with a small HTML body that `curl -sf` does not treat
  as failure). The route returns
  invalidated edges too (the viewer draws history struck through), so compute each node's degree
  over `expired_at == 0` edges only — count a dead fact and the freshly-orphaned node it left
  behind is exactly the one the walk skips. Do the counting in `python3` (the interpreter a cell
  is guaranteed to have); read prose only for the nodes you actually walk.
- Walk the lowest-degree nodes first (0, then 1), plus the nodes this batch created, capped at
  ~25 nodes including this batch's — the walk otherwise grows with the graph, and it is bounded
  independently of the episode cap for that reason: a big-backlog night drains more episodes
  without buying a proportionally longer walk. **Reserve at least a third of the cap for nodes the
  batch did NOT create**: a typical batch mints ~20 nodes and would otherwise consume nearly every
  slot itself while replenishing the low-degree pool it was meant to drain (measured: one run
  walked 21 of its own nodes and 4 of a 46-deep standing backlog — the backlog can only grow under
  that split), and a backlog-scaled batch (up to 150 episodes) mints proportionally more, so the
  share rule binds hardest on exactly the nights the queue is deepest. When the batch overflows its
  two-thirds share, its YOUNGEST nodes wait — they arrive carrying their batch edges, so next
  night's walk sees them as ordinary low-degree nodes; nothing is lost, only deferred. A node a
  previous report already recorded as refused (its
  `## Sweep refusals` section — the format the cell section below specifies) gives
  its slot to one not yet considered, so a standing backlog cannot pin the head of the queue. For each, ask what existing node it is genuinely related to, and whether that
  relation stands alone as a fact sentence supported by a node's description, one of this batch's
  episodes, or a live fact the graph already holds. A world-closed edge (`valid_until` set, still
  live) counts toward degree, but is not evidence for a new fact — the world moved past it.
- Write the passing edges through `k graph add`, strengths by the same rules as extraction, with
  **one call per distinct provenance**: `episode_id` is a field of the whole request, so edges
  evidenced by a batch episode go in a call carrying `--episode <that id>`, while edges supported
  only by what the graph already holds go in a call that OMITS `--episode` entirely — the flag
  takes positive ids only, and the payload's absent `episode_id` writes the store's 0, its marker
  for a row with no source episode. That is a real exception to the data model's
  every-fact-points-back rule, tolerable here because the sweep's evidence IS the graph. Say in
  the report how many edges carry it — and derive the count from the `/api/graph` payload you
  already hold rather than trusting a stamped figure, because it moves nightly: the hand-planted
  backfills (the pluto-notes ingest and the watchlist migration) wrote episode-0 edges wholesale,
  making provenance-free edges the MAJORITY of the graph at last measurement (181 of 298 live
  edges, 2026-08-02), with episode-backed ingest closing the gap each night. The operative rule is
  therefore not "keep it the minority" — the graph is not in that state — but the per-edge one
  above: an edge a batch episode DOES evidence always carries that episode's id, and episode 0 is
  only ever the honest marker for an edge whose evidence is the graph itself.
- **A sweep adds edges, never nodes** — and the server will not hold that line for you: an edge
  endpoint that resolves to nothing silently creates a bare node with no type and no description,
  and the response's `nodes` list does not mention it. So endpoint names are copied verbatim from
  the `/api/graph` payload's `name` field, the sweep payload sends no `nodes` array at all, and a
  name missing from that node list is a bug in the sweep, never a new entity.
- **One pass, same rule as gleaning: sweep once and stop.** Connectivity is a shape, not a quota —
  an edge invented to raise a degree number is exactly the wrong-fact-in-a-brief failure this
  skill exists to prevent. An edge whose result comes back `added: false` folded into a fact
  already there — report it as already known, not as a new link, and never restate a fact whose
  only evidence is that same fact, which counts the graph as its own witness. And like
  canonicalization, the sweep needs the whole graph at once: never hand slices of it to a crew.

---

## Revision mode

The job spec carries carl's instruction verbatim. It is the specification; follow it rather than
re-deriving an approach, exactly as carl's `fix` comment on a watched observation is followed. Four shapes:

| Instruction | What you do |
|---|---|
| **Merge** two nodes that are one thing | Keep the better-described node; add the other's name (and its aliases) as aliases of the keeper, and fold anything its gloss knew into the keeper's `description`. The duplicate's facts STAY WHERE THEY ARE — there is deliberately no verb that re-points an edge — and that is enough: every future mention resolves through the alias to the keeper, and search still finds the old facts by their sentences. Only when an old fact is actively misleading do you invalidate it and re-state it against the keeper (which resets that fact's sighting count to 1 — say so in the report when you do it). |
| **Split** one node that is two things | Create the second node with its own `description`; then, for each fact that belongs to it, add the fact against the new node and invalidate the misattributed original. Each moved fact restarts at 1 sighting — the corroboration belonged to the conflated node, and carrying it over would launder it. Facts that genuinely described the original stay untouched. |
| **Re-describe** a node whose gloss is wrong or thin | Rewrite the `description`. It re-embeds automatically — the stored vector is cleared when the description changes. |
| **Invalidate** a fact that is wrong or stale | Close its window. **Never delete.** If it was wrong from the start rather than newly false, say so in the report; the row still stands as a record of what kraken believed. |

**Some edges are watched, and the overlay rides the edge.** The dream loop's observations are ordinary facts wearing a thin watch overlay, so revising one reaches past the graph: invalidating a watched edge takes that observation out of the board's DREAM view entirely, and folding two near-duplicate watch facts into one keeps only the surviving edge's overlay. The agent-owned half of the retired overlay — sighting count, evidence, suggested fix — you can re-supply by hand with `kraken watchlist add|update` against the survivor; **carl's verdict cannot be carried across**, because no verb you can reach writes it (a fresh overlay always starts unjudged, and the verdict route is his alone). So a merge that retires a JUDGED watch fact silently discards a human ruling: name it in the report so carl can re-judge the survivor, and never perform one as a side effect of a cleanup.

Change only what the instruction reaches. A revision job is not an excuse for a general cleanup —
carl asked about one thing, and a diff he did not ask for is a diff he has no way to review.

---

## Delegation

Extraction over a batch splits cleanly by episode, so it is real work for a crew: give each
**hephaestus** teammate a disjoint slice of the batch and the tuple format above, and have each write
its tuples to a scratch path outside the repo (`/tmp/…`, never a worktree-relative path) — a report
that exists only as a chat reply is one that gets lost. Then YOU do step 3 across all of it:
canonicalization is the one step that must see the whole batch at once, and a crew that each resolves
its own slice produces four graphs that happen to share a database.

Any agent that only READS — surveying what the graph already holds, say — is an unnamed subagent and
must be spawned `general-purpose` or another type whose roster includes `Write`; **never `Explore`**,
which has no `Write` tool at all, so a brief naming a report path is unsatisfiable by construction and
the agent rationalises rather than saying so, while you wait on a file that is never coming.

For how to WAIT on several report files, follow `.claude/skills/do/SKILL.md` step 4's "How to wait"
bullet and the sub-bullets under it; it is the one specification of the protocol and it applies here
unchanged.

`solo: true` on the job means no subagents at all: take a smaller batch and do it yourself.

## Inside A Kraken Cell

When a kraken job brief sends you here, these deltas apply — the brief wins wherever it is more
specific, and it is the only place the cell's real paths appear:

- **The report goes in the brief's SET ROOT** (`<cellDir>/artifacts/`) as `00-graph-report.md`. Like
  every md artifact, it opens with a one-liner stating its main message — the run's headline. Keep
  it to a page: what was ingested (or revised), the judgment calls, what you refused to add and why,
  what the connectivity sweep linked and what it walked but refused (ingest mode only; sweep edges
  count toward the closing line's fact count), and any episode you closed without extracting from.
  That last section is the one that tells carl whether a quiet run was a clean one or a blind one.
  Previous nights' reports survive as flat copies at `~/Documents/pluto/<slug>-<id>.00-graph-report.md`
  (moved into `archive/` when their job is archived); the sweep reads the most recent few for the
  nodes already recorded as refused — the only cross-run state it has. So the sweep's refusals are
  written machine-readably: a `## Sweep refusals` section — heading always present, bare node
  names exactly as `/api/graph` spells them, one per line, no bullets and no prose; a run with
  zero refusals leaves the heading with no lines under it (never `None`, which a later run would
  read as a node name). It is distinct from the prose "what you refused to add" section above,
  whose subject is extraction content and whose category headings ("ephemeral work state") a
  later run globbing for refused nodes would mis-parse; reports from before this format simply
  lack the section, which reads as zero recorded refusals, not as an error.
- **Read and write the graph with `k graph pending|search|add|done|stats`.** Never open `kraken.db`
  directly — `kraken serve` is its single writer, and a second handle would run migrations.
- **This job opens no PR.** The graph lives in `kraken.db`; nothing here belongs in git. If you find a
  real bug in kraken while doing this, say so in the report and leave it — that is a job of its own.
  The one exception is carl steering this job into a repo change ("bake that into the skill"): his
  steer overrides the no-PR default, but it does not override the procedure — a repo change is
  phase-2 work and `.claude/skills/do/SKILL.md` governs it end to end (contract, review round,
  build report, the manual-test gate, and only then `pr/SKILL.md`), whatever skill the job
  started under. One job took the side door here:
  it read only the pr skill, so a merged PR shipped with no contract, no review-round close
  discipline, and no build report, and nothing anywhere noticed the gates were skipped.
- **Never `open`** (there is no browser in a cell).
- **The phase ends with one line, then you stop.** Say exactly:

  `graph updated: <set root>/00-graph-report.md (N episodes, M nodes, K facts)`

  and STOP (end the turn). Use `0` where a count is zero — never omit the clause. Kraken derives
  blocked from your silence and shows your last words on the board.
- Never declare the job done — accepting is carl's move, never yours.
