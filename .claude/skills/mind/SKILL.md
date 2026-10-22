---
name: mind
description: "KRAKEN-LEVEL copy (repo-owned, materialized into cell worktrees). Nightly mental-model pass: read what the operator themselves said and wrote in the window — their typed prompts across every root transcript, their job specs, feedback notes and typed answers — resolve what each statement refers to, keep only what they plainly asserted and can be rooted, and propose it into their mental model in one batch for them to confirm on the Model pane. Use when a job spec invokes /mind, or when a kraken mind job brief sends you here."
---

# Mind — The Nightly Mental-Model Pass

> **Kraken-level copy** — loaded from `.claude/skills/mind/` in this worktree. If the harness header above says `Base directory ... ~/.claude/skills/mind`, a personal copy shadowed this one (personal beats project on a name collision).

Yesterday {{operator}} said things to agents — decisions, preferences, claims about how the code and the systems are — and almost none of it reached their mental model, because capture is a deliberate act and the day is not. This pass reads only {{operator}}'s OWN words from the window and proposes the few that are plainly asserted, resolvable and rooted as mental-model items.

The discipline is **precision over supply**: an item {{operator}} did not mean, or one that names a pronoun instead of a thing, is worse than no item — it is read by every agent that touches the directory until someone drops it. Three good items and twenty exclusions is a working night; twenty items and no exclusions is the alarm.

## What lands where

| Output | Destination | May contain |
|---|---|---|
| Admitted items | ONE `kraken mm propose` call, landing as `proposed` items with source `nightly` | {{operator}}'s quote verbatim, the resolved referent, the root |
| Contradictions with a live item | at most three `kraken mm flag --item <id> --note …` calls | what contradicts what, quoting both |
| The night's report | `00-mind-report.md` in the set root | everything, including every exclusion with its quote |

Proposed items are NOT in {{operator}}'s mental model yet. They are held out of the rendered rules files and out of `kraken mm get` until {{operator}} confirms each one on the board's Model pane — the night's single notification says how many are waiting — and only a confirmed item reaches agents. An item {{operator}} drops there is gone for good: its quote is suppressed forever, so the same sentence proposed on any later night comes back `suppressed` and writes nothing. Treat a suppressed result as {{operator}}'s verdict, never as something to reword around.

The propose call is accepted only from THIS job's own zeus session — the stamped lead session of the job the nightly watermark names. The gate cannot tell an in-process subagent from you (a subagent's Bash runs under your claude process and resolves your session), so the rule is procedural rather than enforced: you make the one call yourself, and no checker or other agent ever runs it.

Nothing here enters git. The quotes are {{operator}}'s private text: they live in kraken.db rows and in the report, both local.

## Order of operations

Read in this order. Judging a sentence before you know what is already known wastes the fold, and judging it before you know what it refers to is the main way this pass goes wrong.

1. **The existing mental model.** `kraken mm get --all` for every item, live, proposed and invalidated (read `kraken mm show <id>` for any you need whole). Live and proposed items are your FOLD targets (the same statement already held — no new row) and your CONTRADICTION targets; invalidated nightly items show what {{operator}} already dropped, and their quotes are suppressed.
2. **The two evidence readers, with the job body's window.** The body names both edges in unix millis and the exact commands; run them as written:
   - `kraken mine operator --since <since> --until <until>` — every prompt {{operator}} typed in the window across every root transcript on the machine. The reader removes kraken's own sends and other agents' records, and what survives is STILL read as text that may quote an agent — a passage {{operator}} forwarded, a question row they were answering — so you quote only the sentence {{operator}} typed, never the material they were relaying. Each record comes WITH its context: the reduced blocks before it (what the agent had just said or done) and after it (how the agent read the prompt). The envelope's `scanned`, `skipped` and `truncated` counts go into Coverage verbatim.
   - `kraken mm evidence --since <since> --until <until>` — the documents {{operator}} authored in the window: job specs (`jobs`), feedback notes (`feedback`), and question answers (`answers`). An answer carrying `accepted_rec: true` is {{operator}} accepting an agent's recommendation verbatim — an endorsement of agent text, not their words, and never a candidate. A job carrying `author: unknown` predates the author stamp (schema v37): its authorship is unverified, so read it with the same doubt as a pasted document — it may be agent-drafted text {{operator}} only submitted.
3. **Resolve, then judge** — every candidate, in that order, as below.

## Resolve every referent first

A sentence is read in its turn, and the turn is read in its conversation. "This boolean logic thing", "the mapping", "it" are a different thing in every session, and {{operator}} almost never names the thing they are talking about because the agent in front of them had just shown it. So before ruling on any record, read its attached `context.before` and `context.after` and settle what every "this", "that", "it" and "the X" names — the agent's preceding text, the tool call it had just run, the file it showed, its reply.

When the attached window does not settle it, widen it: `kraken mine around <transcript_path> <uuid>` prints the record's surroundings (default 20 reduced blocks each side, the record marked `▶ `), and `kraken mine around <transcript_path> <uuid> --raw` adds the tool results the reduction drops — use `--raw` whenever the referent is inside a tool result (a diff, a log, a query's output). The reduction is a cost control, not an access control. A record flagged `pasted` holds a `<pasted_content` block whole: separate {{operator}}'s own prose from the pasted material before ruling — quoted lines, logs and tracebacks inside a paste are SOURCES, not claims.

A referent you still cannot resolve after widening excludes the candidate (rung 1b). Never write a title that still contains the deictic.

## The exclusion ladder

Walk every candidate down these rungs and stop at the first verdict. Each exclusion is recorded in the report with its rung, the quote and a half-sentence why.

| Rung | Test | Verdict |
|---|---|---|
| 1 Assertion | Does the sentence state something, or does it ask, acknowledge or react ("ok", "thanks", "hm", a question)? | not an assertion → **out** |
| 1b Referent | Can every "this", "that", "it", "the X" be resolved to a concrete thing from the context (above)? | unresolved → **out** as `unresolved referent`; resolved → the title names the THING, never the pronoun |
| 2 Hedge | Does it carry a marker of uncertainty — `i think`, `maybe`, `not sure`, `idk`, `might`, `probably`, `iirc`, `right?`, `actually works right?` — or does the surrounding turn show {{operator}} asking rather than telling? A trailing `?` is ALWAYS unsure. `ofc` and `obviously` read as SURE. The lexicon is guidance; the judgment is whether they were sure | **out** (hedge) |
| 3 One-off task | Is it an instruction to do something now with no lasting claim about how things are or should be ("kill the hermes", "run the tests again")? | **out** — a task is not knowledge |
| 4 Idea or decision | Is it a choice, a preference, a plan, a standing instruction, a design intent? | **in** as `decision` — rooted in {{operator}} by construction |
| 5 Factual claim | Does it assert how the world, the code, the data or a system IS? | a candidate `note` — **in** ONLY with a root (rung 6) |
| 6 Root | Can the claim be tied to one of: (a) a document {{operator}} pasted or attached in that turn; (b) the agent output they were replying to — the assistant text immediately before the prompt in the same session; (c) the code or config as it stood at the time; (d) a live mental-model item; (e) a spec or feedback note of {{operator}}'s? | no root → **out** as `unrooted`; a root → **in**, the root named in the body |
| 7 Already known / contradicted | Does a live or proposed item say the same thing? Does a live one say the opposite? | same → **fold** (no new item; the report names the id); opposite → **flag** |

**Doubt excludes.** A candidate you cannot place on rung 4 or rung 6 with confidence is excluded and listed under its nearest rung — never stored "to be safe". Storing is the unsafe direction: an exclusion costs one line of the report, a wrong item costs every agent that reads it.

**Rooting a claim in code (6c).** When a factual claim can only be rooted by looking the code up, spawn at most ONE read-only checker subagent for the whole night — unnamed, changing nothing in the repo, writing its findings to a scratch path you give it, every claim to check in its single brief with the session and timestamp each was made at — and wait for it as `.claude/skills/do/SKILL.md`'s `### 4. Plan The Implementation` step says, under its `**How to wait:` bullet. A claim the checker could not root is `unrooted`. `solo: true` on the job means no checker subagent at all: look up what you can yourself, and exclude the rest as `unrooted`.

**Never store a quote that looks like a credential** — a token, a key, a password, a cookie value, anything that would authenticate if pasted somewhere — whatever else the sentence says. Exclude it, and in the report name only the rung and that a credential-shaped value was withheld; never copy the value into the report either.

## Proposing — once, in one batch

Make ONE `kraken mm propose` call with every admitted item as a JSON array on stdin. One call, because the route notifies {{operator}} once per call and caps adds per call; and because the response is the only record of which proposals landed. Each item:

| Field | Rule |
|---|---|
| `kind` | `decision` (rung 4) or `note` (rungs 5 and 6). Never `reading` — this pass never reads code for {{operator}}, so it can never record that they read it; the route refuses it |
| `scope` | `repo` by default — with `repo` the repo the session worked in (the record's `repo`, or the interactive session's checkout) and `dir` the directory the claim names when it names one; `universal` only for a statement about {{operator}} themselves, their process, or how agents should treat them |
| `title` | at most 120 runes, a CLAIM in {{operator}}'s terms naming the resolved thing, never the pronoun and never a topic: "trade mappings belong in dispatch groups, not areas", not "the mapping" |
| `body` | at most 4 sentences: the claim with its referent spelled out, the root (the session and the agent output it replied to, the pasted traceback, `path:line` at a sha, the item id), and the rung |
| `quote` | {{operator}}'s sentence verbatim — the record text, pasted material trimmed to the lines that carry the claim. Never paraphrased: the quote is what folds and what suppression matches, byte for byte |
| `source` | the pointer: `{"kind": "transcript", "session": …, "uuid": …}` for a prompt; `{"kind": "job", "slug": …, "sha": <body_sha>}` for a spec; `{"kind": "feedback", "slug": …, "feedback_id": …}`; `{"kind": "question", "slug": …, "qid": …}` |
| `rung` | the rung that admitted it and the root letter, e.g. `4` or `6b` |

Before the call, fold near-duplicates yourself: two prompts saying the same thing become one proposal, and a candidate that restates a live or proposed item is a fold you report rather than a proposal (the store folds only byte-identical quotes). The response's `results` carry one outcome per item — `added`, `folded` (with the existing id), `suppressed` or `refused` (with a reason; `cap` past the per-call limit) — and its `added` count is N in the closing line. A 400 means one item failed validation and NOTHING was stored; its sentence names the item by its zero-based index in the array you sent (`item 0` is the first): fix that item and make the call again, since a refused batch is not a made call.

Then raise contradictions: at most THREE `kraken mm flag --item <id> --note "<what contradicts what>"` calls, the strongest first, each note quoting both the item and {{operator}}'s new sentence. You are the job's lead, so each is raised at once and {{operator}} resolves it on the Model pane. Every contradiction past the third is listed in the report and not flagged.

## The report

Write `00-mind-report.md` into the set root. Like every md artifact it opens with a one-liner stating its main message — the night's headline, not a table of contents. Keep it to about a page, and never hard-wrap its prose: one paragraph — and one bullet, and one table row — is ONE source line, however long, since a mid-sentence line break renders as a broken paragraph on the board. Every count in it is counted once, after the propose and flag calls have returned, from their responses — never from memory. Sections, in order, with "None" spelled out rather than a section dropped:

| Section | Content |
|---|---|
| Proposed | Each item the response reported `added`, by id: title, kind, scope, and the quote. Then, one line each, any proposal the response reported `suppressed` or `refused`, with the reason. Say once that these await {{operator}}'s confirm on the Model pane. |
| Folded | Each candidate folded onto an existing item — by you or by the store — naming the item id and the quote. |
| Excluded | Every excluded candidate, one line each: the rung, the quote, and a half-sentence why — "rung 2 hedge: «branching into headless actually works right?» — trailing question". A credential-shaped quote is listed by rung alone. This list is the pass' precision instrument; never trim it to look tidy. |
| Flagged | Each flag raised, by flag id and item id, and every contradiction past the third that was not flagged. |
| Coverage | The operator reader's `scanned`, `skipped` and `truncated` counts and its record count; the evidence counts (jobs, feedback notes, answers, and how many answers were `accepted_rec`); whether the window was clamped; and whether the checker ran. A non-zero `truncated` is said in words: a partial scan must never be silent. |

## Inside A Kraken Cell

When a kraken mind job brief sends you here, these deltas apply — the brief wins wherever it is more specific, and it is the only place the cell's real paths appear:

- **The report goes in the brief's SET ROOT** (`<cellDir>/artifacts/`) as `00-mind-report.md`. That directory is the only board-visible one, and the report is the deliverable alongside the proposed items.
- **Read the window off the job body** and run the two reader commands it names verbatim; the reader verbs (`kraken mine operator`, `kraken mine around`) read Claude-owned files directly and need no daemon, while `kraken mm evidence`, `kraken mm propose` and `kraken mm flag` go through `kraken serve`.
- **The checker is an unnamed subagent**, never a teammate: it reads and reports, and changes nothing in the repo. Nothing else is spawned.
- **Never `open`** (there is no browser in a cell). This pass opens no PR and writes nothing into the repo.
- **The phase ends with one line, then you stop.** Say exactly:

  `mind swept: <set root>/00-mind-report.md (N proposed, X excluded, F flagged, K prompts)`

  and STOP (end the turn). The four counts are DEFINED, not chosen per night: **N is the `added` count of the propose response** (0 when no call was made, which happens only when nothing was admitted), **X is the number of excluded candidates listed in the report's Excluded section**, **F is the number of flags raised** (at most 3), and **K is the operator reader's record count** — the length of its `records` — and when its `truncated` is non-zero, say so beside it (`K prompts, T partial`). Use `0` where a count is zero — never omit the clause. Kraken derives blocked from your silence and shows your last words on the board.
- Never declare the job done — accepting is {{operator}}'s move, never yours.
- `solo: true` on the job means no checker subagent: root what you can yourself, and say in Coverage that the checker was skipped.
