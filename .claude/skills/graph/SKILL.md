---
name: graph
description: "KRAKEN-LEVEL copy (repo-owned, materialized into cell worktrees). The knowledge graph's two agent-side jobs: ingest — drain pending episodes into entities and facts — and revision, where carl's instruction corrects what the graph believes. Use when a job spec invokes /graph, or when a kraken graph job brief sends you here."
---

# Graph — Ingest And Revision

> **Kraken-level copy** — loaded from `.claude/skills/graph/` in this worktree. If the harness
> header above says `Base directory ... ~/.claude/skills/graph`, a personal copy shadowed this one
> (personal beats project on a name collision).

Kraken remembers what it did. The daemon does the mechanical half — it sweeps transcripts, feedback,
watchlist rows, job specs and artifacts into **episodes**, and it fills in embeddings and layout.
It does not understand any of it. That is this job: turn episodes into a graph of **entities** and
**facts**, or correct the graph when it has learned something wrong.

The graph is read back at every cell spawn (`graphContext`), so a wrong fact does not sit quietly in
a table — it is injected into the next agent's system prompt as something already known. Precision
beats recall here, always. **A fact you are not confident in is a fact you do not add.**

## The data model, in one table

| Thing | Is | Key property |
|---|---|---|
| **Episode** | one span of evidence: a transcript span, a feedback row, a job spec, an artifact, a watchlist row | it is provenance — every fact points back at the episode that produced it |
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

`k graph pending --limit 25` returns pending episodes as JSON: id, kind, ref, ts, and the reduced
content. Drain in the order given (oldest first) and stop at the end of one batch — the queue is
long by design and the nightly job is a drain, not a race.

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

Four rules about what a good tuple looks like:

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
- **One gleaning pass.** After the first pass over an episode, ask once: what did I miss? Then stop.
  There is no second gleaning and no community-summary layer; both cost far more than they return at
  this graph's scale.

### 3. Canonicalize AFTER extraction, never during

Extract with the names the episode actually uses. Only once the batch is extracted, resolve those
names against what already exists — `k graph search "<name>"` and the resolve that `k graph add`
performs for you. Canonicalizing while reading makes you bend the evidence toward names you already
know, which is how a graph converges on its own first guesses.

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
again rather than skipping it because it is already there.

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

**Re-invalidating an already-closed edge is an error, not a no-op** — `invalidate` matches live rows
only, so a re-run of a partially-completed batch will fail on the edges the first run already closed.
That error means the edge is **already closed**, never that it is gone. Drop those entries and
re-send the rest rather than treating the batch as unwritten.

### 6. Close the episode

`k graph done <episode id>` for every episode you processed, **including one you extracted nothing
from** — an episode with no durable facts in it is a processed episode, and leaving it pending means
the next run pays for it again. Say how many you skipped and why in the report.

---

## Revision mode

The job spec carries carl's instruction verbatim. It is the specification; follow it rather than
re-deriving an approach, exactly as a watchlist `fix` comment is followed. Four shapes:

| Instruction | What you do |
|---|---|
| **Merge** two nodes that are one thing | Keep the better-described node; add the other's name (and its aliases) as aliases of the keeper, and fold anything its gloss knew into the keeper's `description`. The duplicate's facts STAY WHERE THEY ARE — there is deliberately no verb that re-points an edge — and that is enough: every future mention resolves through the alias to the keeper, and search still finds the old facts by their sentences. Only when an old fact is actively misleading do you invalidate it and re-state it against the keeper (which resets that fact's sighting count to 1 — say so in the report when you do it). |
| **Split** one node that is two things | Create the second node with its own `description`; then, for each fact that belongs to it, add the fact against the new node and invalidate the misattributed original. Each moved fact restarts at 1 sighting — the corroboration belonged to the conflated node, and carrying it over would launder it. Facts that genuinely described the original stay untouched. |
| **Re-describe** a node whose gloss is wrong or thin | Rewrite the `description`. It re-embeds automatically — the stored vector is cleared when the description changes. |
| **Invalidate** a fact that is wrong or stale | Close its window. **Never delete.** If it was wrong from the start rather than newly false, say so in the report; the row still stands as a record of what kraken believed. |

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

- **The report goes in the brief's SET ROOT** (`<cellDir>/artifacts/`) as `00-graph-report.md`. Keep
  it to a page: what was ingested (or revised), the judgment calls, what you refused to add and why,
  and any episode you closed without extracting from. That last section is the one that tells carl
  whether a quiet run was a clean one or a blind one.
- **Read and write the graph with `k graph pending|search|add|done|stats`.** Never open `kraken.db`
  directly — `kraken serve` is its single writer, and a second handle would run migrations.
- **This job opens no PR.** The graph lives in `kraken.db`; nothing here belongs in git. If you find a
  real bug in kraken while doing this, say so in the report and leave it — that is a job of its own.
- **Never `open`** (there is no browser in a cell).
- **The phase ends with one line, then you stop.** Say exactly:

  `graph updated: <set root>/00-graph-report.md (N episodes, M nodes, K facts)`

  and STOP (end the turn). Use `0` where a count is zero — never omit the clause. Kraken derives
  blocked from your silence and shows your last words on the board.
- Never declare the job done — accepting is carl's move, never yours.
