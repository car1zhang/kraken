---
name: dream
description: "KRAKEN-LEVEL copy (repo-owned, materialized into cell worktrees). Nightly corroboration pass: read the day's artifact feedback and Claude session transcripts, turn recurring signals into durable improvements to kraken's skills and code, park the rest as watched observations in the standalone watch store, and report back. Reads the operator's verdicts on watched observations and obeys them. Use when a job spec invokes /dream, or when a kraken dream job brief sends you here."
---

# Dream — The Nightly Corroboration Pass

> **Kraken-level copy** — loaded from `.claude/skills/dream/` in this worktree. If the harness
> header above says `Base directory ... ~/.claude/skills/dream`, a personal copy shadowed this one
> (personal beats project on a name collision).

Yesterday produced two kinds of evidence: what {{operator}} said about the artifacts (feedback rows) and
what the agents actually did (session transcripts). This pass turns the recurring parts of that
into durable change, and parks the rest where a later night can corroborate it.

The discipline is **corroboration**: one ambiguous signal is evidence, not an instruction.

## The one rule about where things go

Three outputs, three destinations. Getting this wrong is the only mistake here that is hard to undo.

| Output | Destination | May contain |
|---|---|---|
| A hardened change | a PR {{operator}} reviews | **the diff only** — never findings, evidence, quotes, or transcript excerpts |
| An observation not yet worth acting on | the standalone watch store (`kraken watchlist` verbs — its own `watch_items` table since v29) | anything useful; it is local and never committed |
| The night's report | `00-dream-report.md` in the artifacts dir | anything, including {{operator}}'s own words quoted back to them |

**Findings never enter git.** Not in a commit, not in a PR body, not in a file. The repo gets the
change; the machine keeps the reasoning. This is structural rather than a redaction rule, which is
why it is reliable: a commit contains a diff, and that is all it ever contains.

The leak this rule keeps failing at is **evidence used as illustration**: you hold all the
evidence, you write the diff, and when a change needs an example string the most natural example
to reach for is the real one — a pass wrote a real coworker's private DM ask, name and all, into
a Go example string, and only the style reviewer's last-gate block kept an identified human's
private message out of git history, the compiled binary, and every future transcript that binary
would emit it into. The next night's pass then wrote the SAME string into this very
paragraph while writing the rule against it, and was again saved only by its style reviewer.
Every example string in a diff is INVENTED — a made-up name, a made-up ask — and if a real
detail from tonight's evidence would make the example clearer, that is precisely the sign it
must not be there: the real thing belongs in `00-dream-report.md`, the side that never travels.

The corollary is freeing — because the local side never travels, the report and a watched fact can
hold the real thing: actual quotes, real session ids, the prompt text as written. That is what makes
the report worth reading.

## Order of operations

Read in this order. It is not a preference — proposing something {{operator}} already settled wastes the
night and teaches them the loop does not listen.

1. **{{operator}}'s verdicts and comments** on watched observations, and **open dream PRs**. Both are suppression:
   a rule already in flight must not be re-proposed, and an observation they have ruled on is already decided.
2. **The rest of the watch overlay** — what previous nights are still watching. A row whose state
   reads `shipped` is a CLAIM, not a settled fact: before letting it suppress a fresh sighting,
   verify the fix actually landed — a `pr_url` that points at a MERGED change covering the
   observation, or the change visible in the tree; an empty url fails the check and so does one
   whose PR closed unmerged. A shipped row with an EMPTY `pr_url` has covered a still-live defect
   while two scouts re-measured it the same night, because "shipped" read as closed and the
   residual went unrecorded. When scouts re-measure a shipped observation on evidence from AFTER
   its fix landed, that is a new sighting of a fix that did not take: re-add it with the SAME
   wording (folding keys on the wording — see the watch section below), and say so. Evidence that
   predates the fix proves nothing either way — the mining window always reaches back past a fix
   that landed inside it, so pre-fix sightings of a genuinely-fixed defect are expected, not a
   reopening.
3. **The evidence**: the feedback sweep, then the transcripts.

## Reading the operator's verdicts

Each watched observation can carry a verdict from {{operator}} and, optionally, a comment.

| Verdict | What it means | What you do |
|---|---|---|
| `fine` | Stop even watching this | Never propose it again. Softer than `declined`, which means "you proposed it and I said no". |
| `fix`, no comment | They want it done | Their asking **is** the explicit instruction — act on it now, no further corroboration needed. |
| `fix` + comment | They want it done a particular way | **The comment is the specification.** Follow it rather than re-deriving an approach, and say so in the PR body. |

You may also put a **suggested fix** on an observation you are not acting on — a concrete proposal for
{{operator}} to approve or push back on. Write it into the item's `suggested_fix`; do not act on your own
suggestion until they have turned it into a `fix`.

Never write `verdict`, `comment`, or `commented_at` yourself. Those are the operator's, exactly as
`answer`/`answered-at` are on questions.

## Evidence 1 — artifact feedback

`kraken feedback sweep --since <ts>` returns every feedback note across all jobs in the window, each
with the artifact it was left on and a URL for the **snapshot** — the artifact frozen as {{operator}} saw it.
Read the snapshot when the note is about something visual or structural: "edges run through boxes" is
unresolvable against an artifact that has since been fixed, or whose cell is long gone.

**You are the only writer of `addressed` anywhere in kraken.** Every other cell is told to act on its
feedback, fix what the note asks for, and leave the column alone — so an OPEN note does not mean nobody
handled it. The owning job's zeus very probably did, in place, the same day. Read every note as evidence
on its own terms; the column tells you nothing about whether the artifact got fixed, and it is not meant
to.

That is the whole meaning the column now carries: **`addressed` says a DURABLE fix shipped** — a skill
sentence, a brief, real code — never that one artifact was patched. Which is exactly the distinction this
pass exists to draw, and the reason the write belongs here: a per-artifact fix is invisible to the next
job that hits the same problem, and the recurrence is the finding.

**So mark a note addressed only when you shipped a change for it**, and name the PR when you do. A note
you merely READ stays open.

The case that rule exists for is the reverse one. A note you acted on would otherwise sit open
**forever**: the job that owns it is usually done, blocked, or torn down by the time you read it, so
{{operator}} is left looking at a note whose fix has already merged, with no way to tell it from one nobody
touched.

The test is authorship of the FIX, not of the note: if this pass shipped something because of it, close it
and say which PR. Otherwise leave it alone.

Three properties of the window are worth holding onto while you read the sweep, because each changes what
a row in front of you means.

**A row reaches you ONCE — plan as if this is your only look at it.** The window keys on each row's
immutable CREATION time and the floor only moves forwards, so a note you leave open is not deferred to
tomorrow: it is out of the evidence stream, still visible to {{operator}} on the board but never re-read here.
There is no second look to defer a judgment to.

**"Once" is the rule, not a guarantee, and it fails in both directions.** After a long `kraken serve`
outage the window CLAMPS (7 days) and everything older is swept zero times — the night's job body says so
when it happens, and those rows can now never be closed by anyone, so name them in the report's coverage
section. At the other seam, the floor is stamped when the night's job is CREATED and you run the sweep
some time after that, so a note {{operator}} writes in between falls in both tonight's window and tomorrow's.
That is the one case where a row comes back. **A row you have seen before is the SAME sighting, never a
second one** — corroboration means two different sources, and one row read twice is one source.

**Which is why an `addressed` note naming a PR is worth reading rather than ignoring.** On a re-swept row
that text is *yours*, from a previous night — the one reliable "already shipped for this" signal you get.
No other cell writes this column, so it cannot be anyone else's.

## Evidence 2 — transcripts

Two passes, and the first is what makes the second affordable.

**The mechanical pass** is `kraken mine digest --since <ts>`. Its JSON goes to stdout and its
human summary to STDERR, so stage it as `> digest.json 2>digest.err` — never `2>&1`, which
interleaves the summary into the JSON and hands every downstream reader a corrupt file. The
envelope's top-level keys are `since`, `scanned`, `skipped`, `truncated`, `tokens`, and `digests` —
the per-session rows are under `digests` (`null`, not `[]`, when nothing was scanned), and two
consecutive passes have burned calls guessing
`.Sessions` first, because the row fields are documented everywhere and the wrapper nowhere. It reads
every transcript in the window and counts what is countable: tool calls, `is_error` clusters by
tool, interrupts (the operator
pressing escape), `<synthetic>` API failures, output-ceiling deaths (`MaxTokensStops` — messages cut
at stop_reason max_tokens, the failure that burns a whole budget while reading as a clean session),
turn durations, literally repeated calls, and the four
**token counters** (`In`, `Out`, `CacheWrite`, `CacheRead`) per session plus a roll-up across the
whole sweep. Three counter caveats, all measured, for anything you build on those numbers:
`ErrorCounts` includes the benign tail-exit-1 shape (a `;`-compound whose last command is a grep
that matched nothing — every wanted byte arrived, exit 1 anyway) while missing aborts that exit 0,
so read an error cluster as a hypothesis about command SHAPES rather than as N failures;
`RepeatedCalls` keys `Read`/`Edit` on file path alone, so careful paged reads of one large file and
distinct edits to one file both present as repetition; and `SyntheticErrors` counts every
`<synthetic>` assistant record, which on this machine is mostly USAGE-LIMIT BANNERS and null turns
rather than transport failures — three scouts in one night independently re-derived this after
their briefs relayed the counter as "API failures", so brief it as "session-interrupting records"
and let the scout classify them. Each row also says WHOSE session it is and how it sits in the
window — `AgentName` (the head agentName: a teammate's `athena-…`/`hephaestus-…`, the lead's
`zeus-…`), `Delegated` (the token splitter's lead-vs-delegated file classification), and
`FileFirstTS`/`FileLastTS`, the file's whole dated span beside the windowed `FirstTS`/`LastTS` —
read them BEFORE calling a row anomalous: a wide FileFirstTS-to-FirstTS gap means the row is an
old session's TAIL, not a newborn (three ~23-minute reviewers whose last dozen records crossed a
floor once digested as three anomalous midnight sessions, and the false premise went into a scout
brief). `kraken mine reduce --since <ts> <path>` then emits the **reduced view** of one session
— user prose, tool calls and assistant text, with thinking, tool results and attachments dropped,
which is about an eighth of the bytes. The operator's MID-TURN messages — typed while a turn was running,
the course corrections that matter most to the prompting-feedback output — are stored as queue
records rather than user records and emit as `[user mid-turn]`; kraken's own daemon bumps queue
through the same channel and read the same way, so judge by the text.

**Rank by spend, not by tool-call count — and rank CELLS, scouting each one whole.** A per-session
ranking selects agent SHAPE rather than anomaly: a diagram renderer whose brief forbids emitting
SVG into its reply structurally tops output-per-line, and a subagent sidecar structurally bottoms
it, so session-ranked scout slots go to agent kinds, not problems. Group each cell's sessions into
one scout assignment and rank the cells. The token roll-up is the cheapest correct answer to
"where did last night actually go", and it does not agree with the tool-call counts: a session that
polls a pane four hundred times looks busy and costs little, while one that re-renders the same
figure three times looks quiet and costs a lot. Sort the digests by cost, and read the top of that
list first — the expensive sessions are where the waste is, and the correlation is usually blunt
enough to name the cell before a scout has read a line. Note `Truncated`: a partial scan makes every
count, tokens included, a FLOOR rather than a total. And when a corpus measurement includes the cell
you are running in, exclude it or label it as MOVING — your own session is being appended to while you
read it, so its number is the one figure in the sweep that will not reproduce, and two passes have now
headlined a ratio for a cell they were executing inside. Per-job totals are also on the board (each
job's `tokens`), which is the same number aggregated the other way — by job rather than by night.

**The reduction is a cost control, not an access control.** Say so in the scout brief, and hand over
the raw transcript's path alongside the reduced one. Thinking, tool results and attachments are
exactly what `reduce` drops, so a question about where output tokens went, what a command actually
returned, or what {{operator}} steered mid-turn CANNOT be answered from the artifact the scout was given — and
every scout worth reading last night was one that ignored a "sparingly" and went to the raw JSONL
anyway. Seven of them, independently; four of the night's best findings exist only because of it.
A brief must not send a subagent to an artifact that cannot contain the answer. One more thing the
raw file cannot show a scout: an inbound `SendMessage` leaves ZERO trace in a subagent sidecar —
the sidecar records the effect and never the cause — so a sidecar-heavy assignment's brief says to
cross-check the parent session's `SendMessage`/`Agent` calls before attributing a mid-run pivot to
the agent's own judgment. Three more lines the brief owes every scout. A reduced view's LINES are
unbounded — a single line can be an entire inlined skill body — so a page size that worked on page
one can blow the Read tool's token ceiling on page two: say to page large views in modest chunks
(a few hundred lines) and to shrink the page on a ceiling error rather than switching to grep.
**Shapes, never values**: a scout scanning for where a credential or a person's identifier landed
searches by SHAPE (a prefix, a length, a regex) and COUNTS or LOCATES rather than prints — `grep -c`,
`grep -l`, `grep -n … | cut -d: -f1` (the file name over several files, the line number in one);
never `grep -o`, `grep -ob` or a bare grep that echoes the matching line, since a tool result is a
transcript record too, and paging a view with Read shows whatever is in it — and reports the class, the file and
the record timestamp (or path and line for a file that is not a transcript); it never types a value
it found into a command, a notes line or its report. A scout that built a python dict of the
identifiers and the credential it was tracing copied all of them into a new transcript, from where
the next night's `reduce` carried them into that pass's staged views: the dream pass had become the
propagator its own evidence rule warns about, and nothing in the brief had said otherwise. And
the brief carries a short **already-established list** — the known cross-cutting costs and facts
from the watch store and {{operator}}'s mental model (`kraken mm get --q "<subject>"`, then `kraken mm show <id>`) that touch this scout's assignment — marked "measure, don't
re-derive": six scouts in one night each independently re-derived from raw a cost kraken's memory
already held, because nothing in any brief said what was already known.

**The scouts** are one subagent per transcript, each reading that session's reduced view plus its
digest. They are read-only exploration — they change nothing in the repo and judge nothing — so they are
unnamed subagents, not teammates. Give each one a scratch path **outside the repo** (`/tmp/…`, never
a worktree-relative path) and require its findings there: a scout's report is long, and the reply
channel truncates. Each brief also names that scout's OWN scratch subdirectory for working files
(staged reduced views, notes) — two scouts staging into one shared directory have collided mid-run,
and the collision recovery costs more than the namespace would have. Name those directories with
SHORT slice handles under one root you create first — a short `/tmp/<handle>` root, never under
the session-uuid scratchpad, or every path built on it re-carries the identifier — and **never
hand-transcribe an opaque
identifier — a session uuid, a 36-char random string — into a brief**: one brief carried five
successive wrong transcriptions of its work-dir uuid, the attempt labelled "FINAL, copy this
literally" among them, then cut off before its report rules; the scout burned four calls and a
duplicated full reduce in the garbled dir, and the failure is silent to the lead. The paths that
cannot avoid identifiers — the assigned transcript paths above all — go in a staged assignment
file (copied bytes, not typed ones) that the brief names by its short path, exactly as zeus' own
brief rides in a file rather than on the tmux command line. Tell each scout to stage
running notes there AS IT READS: the one-shot report rule concentrates the run's entire value into
the final call, and a scout that died on a connection error at the literal write boundary — its last words were an
announcement that it had everything and was writing the report — cost ~30K output tokens of
finished analysis with zero salvage, where its staged views survived and were the only thing the
respawn could reuse (that pass did not yet know the transport-resume rule below). Notes are working
files, not the report; the one-shot rule binds the report alone. And the shell-dialect rule from
`.claude/skills/do/SKILL.md` step 4 binds YOU from your own first command, then goes into every
scout brief — framed only as brief material, it was broken by the pass's own lead (a bare `===`
echo aborting its second analysis command) minutes before the lead wrote it into five briefs, and
by the next night's lead again; scouts hit the same aborts on their first command. Spawn them in
waves rather than all at once; a few at a time, with the remainder running as slots free. Every
transcript gets read; the pass simply takes as long as it takes. **A scout dead on a TRANSPORT
error is resumed, not replaced**: a connection-closed or ENOTFOUND death leaves the subagent
resumable, and `SendMessage` to its agent id (from the Agent call's spawn result, or `ListAgents`)
continues it with its context intact and nothing re-paid — so fetch the messaging tools at the TOP
of the fan-out, not mid-crisis, because a lead that has not loaded `SendMessage` has only
"replace" available and cannot discover the cheaper move while stalled itself: one lead respawned
two just-started scouts at full brief re-payment (~123K cache-write) before its first
`ToolSearch`, then resumed every one of six later deaths at no loss. Any OTHER death — an output
ceiling, a resume that does not take — is a respawn under the rule that follows (a hook-blocked
report write is not a death: that case is specified below — report the refusal and stop). **A
respawn — however the scout died — gets a FRESH report path** (`<name>-B.md`, then `-C` and so
on), and you check
the original path first — a killed scout may have finished its report before dying, and a respawn's
heredoc fallback overwrites without the Write tool's read-first guard, so handing it the same path
destroys the one copy of a finished report (a finished 22 KB report survived exactly this only
because `Write` happened to refuse). "Finished" means it ends with its Coverage section — a scout
killed mid-write leaves a non-empty, truncated report, the same on-disk shape as a complete one,
so non-empty is not the test. For how
to WAIT on that many report files, follow `.claude/skills/do/SKILL.md` step 4's "How to wait" bullet
and the sub-bullets under it; it is the one specification of the protocol and it applies here
unchanged. (Outside a cell that file may not exist. The protocol in one line: fetch `Monitor` and
`SendMessage` in one `ToolSearch`, arm ONE background until-loop over every report path using `-s`
not `-f`, end the turn, and treat an expiry as "one of them is missing" rather than as a reason to
re-arm.)

**A scout needs an agent type that can write, and it must not be able to write the repo.** Spawn it
`general-purpose` — or any type whose roster includes `Write`; never `Explore`, which has no `Write`
tool at all, so a brief naming a report path is unsatisfiable by construction and the agent
rationalises ("enforced read-only mode", "my configuration prohibits creating files") rather than
saying so, while you wait on a file that is never coming. But `general-purpose` can write anything,
including this cell's worktree, and this pass's one hard rule is that findings never enter git — so
the guarantee that used to be structural is now a sentence in the brief, and it has to actually be
there: *write nothing under the repo; "read-only" bars the repo, not your own report, which goes at
the scratch path above.* When the brief mentions the heredoc report fallback, it says FALLBACK —
"if `Write` is refused" and nothing wider: briefed with "or any other refusal, use a heredoc",
scouts with a working `Write` tool have shipped 20 KB reports through `cat <<EOF` (three of five in
one measured wave), and a heredoc routes report PROSE through Bash, where a text-matching guardrail
hook can block the whole write because the findings quote a banned command. `do/SKILL.md` names the
same failure for review fleets; it applies to scout briefs identically. And the brief says what to
do when BOTH paths are closed — `Write` refused and the heredoc blocked by a hook: report the
refusal in the reply and stop, never trim or reword the findings until the hook passes. One more
brief clause, and it binds you as much as any scout: `kraken.db` is read through the verbs —
`kraken watchlist get`, `kraken feedback sweep`, `kraken mm get` — never by opening the file
(`kraken serve` is its single writer, and a second handle would run migrations; what follows is
this pass's one narrower carve-out). Where a read genuinely has no verb, open it READ-ONLY and never
otherwise — `sqlite3 -readonly`, or Python's `sqlite3.connect('file:<path>?mode=ro', uri=True)`,
where `uri=True` is what guarantees the URI is PARSED rather than taken as a filename: sqlite
honours `file:` URIs only when URI handling is on, and whether it is on by default depends on how
that interpreter's sqlite was built, so a connection that opened read-only on one machine can open
read-write on the next — because a read-write handle CAN write: a mistyped statement, or a checkpoint on
close, into a file only `kraken serve` may write; four scouts in one night opened it three different
ways, one of them plain read-write. Never copy the database and query the copy — without its
`-wal` the copy is a stale snapshot whose counts are wrong with nothing to signal it. The repo
clause cannot cover any of this, because the database is not in a repo.

**Hand a scout an observation, never a conclusion — and never a number you have not checked.** This is
the single most-measured defect in the loop's own history: ten scouts in one night were each briefed
with a derived figure stated as established fact, phrased so the answer was presupposed ("session [3]
made 94 near-identical repeated calls — work out what that repetition was doing"; "Out=1,682 — were 32
Bash calls earned"). Every one of those premises was false, several of them artifacts of counters this
very file warns scouts about, and each cost its scout calls to disprove before it could start. The
authority is what makes it expensive: a scout distrusts the digest because the brief told it to, and
trusts the BRIEF, which nothing told it to distrust. Two rules follow. Every counter you quote into a
brief is a hypothesis — write it as "the digest reports X; verify before building on it". One
counter has now minted the same false finding two nights running: `Lines` counts TIMESTAMPED
records inside the window — roughly half of a transcript's physical lines carry no parseable
timestamp and are excluded — so quoted into a brief as "N lines" it reads as stale the moment the
scout line-counts the file, and a 719-vs-1,112 "staleness" diagnosis was corroborated and shipped
as a finding while the refutation sat in the field's own doc comment. Brief it as "N timestamped
in-window records — about half a transcript's lines carry no timestamp, so `wc -l` reads at least
double, and more when the window starts inside the file", never "N lines": a label that drops
TIMESTAMPED sends the verifying scout to a record
count that is ~2N even when the window covers the whole file, which re-derives the exact false
finding. And phrase
an anomaly as a question, not as a claim: "say what that shape means" got a scout to reject the
implied reading and find the real cause, where "confirm that X" would have got X. The same sentence
pasted into every brief is its own defect — when you suspect a cross-cutting cause, investigate it
once yourself or name ONE scout its owner, rather than making N scouts rediscover it in parallel.
And the hypothesis rule covers MECHANISMS, not only counters: a failure mode a brief calls "known"
must cite where it is written down — a skill line, a watch row id — or be phrased as a possibility
to check. A lead told its scout that reduce returning zero bytes on sidecars was a known defect
when no skill and no watch row states any such thing (all eight sidecars reduced cleanly),
pre-authorised the raw-JSONL fallback on it, and steered that scout into filing eight READ rows
over staged views it never opened — the exact mislabeling that pass then diagnosed as the scout's
own drift, with the fabricated mechanism appearing nowhere in its report. A number in a brief at
least names a source a scout can go check; an invented mechanism has no referent at all, and the
scout has no move against it.

**Require a per-transcript coverage line, as a filled slot rather than a habit — and Coverage is
the report's FINAL section**, which is what makes "a finished report ends with its Coverage
section" a checkable test on a killed scout's file. A scout that
`sed`-peeked twelve of seventeen files and closed with "17 sessions read" is not lying; it is
answering a template that never asked. Put READ or SAMPLED per assigned transcript in the report
format, and bar a "clean" verdict on a SAMPLED one — a template slot gets filled, a good intention
does not. This costs one line and it is the difference between a quiet night and a blind one: the
sharpest catch in the corpus was a scout that read the SOURCE rather than the relay and found a
"138 tests, 0 failures" verdict that had dropped an explicit "but the UI tests fail, flagged so
nobody records iOS green unqualified" — dropped once by a lead, then again by the pass whose whole
job is catching exactly that.

**Then read the reports.** The file contract is justified by "a report that exists only as a chat
reply is one that gets lost" — and the writing half is the half that keeps getting enforced. One
pass commissioned 385 KB of scout reports, `ls`'d the directory twice, opened nothing, and
synthesized the night from the task-notification summaries alone: **18% of what it paid for**. A
summary is a scout's own compression of its findings, made before it knew what the other scouts
found; the corroboration this pass runs on lives in the parts it dropped. If the reports are too
large to hold at once, that is itself a job for one more subagent under exactly the scout rules above
— `general-purpose`, scratch path outside the repo, file written in one shot, and not at all under
`solo: true` — handed the findings directory and asked for the cross-cutting matrix. But "I read the
notifications" is not reading them.
The checkable form of this rule lives in the report's COVERAGE section, as a filled slot rather
than a promise — a citation format alone is satisfiable by grepping a closed report for quotable
lines, and one pass did exactly that: wrote the report first, then back-filled its citations with
nine `grep | head` calls in 27 seconds, three reports never opened. So Coverage lists **every scout
report file by name with its size and READ IN FULL or PARTIAL (saying what was skipped)** — and a
finding may only cite a report listed READ IN FULL, or a PARTIAL one when the finding comes from a
section the skipped-note names as read (a legitimately partial read of an oversized report must not
push the pass into mislabelling it FULL). List the reports as you finish reading them,
before any finding is written; the order is the honest part. **A scout's own READ rows get the same
treatment: they are claims, not facts, and the aggregate you print inherits whatever they
inflated.** Three scouts in one measured night claimed "reduced in full" on transcripts their
sidecars show were touched only by `grep '^\[assistant\]' -A4 | head -80` — twelve files, eighty
grep'd lines, twelve rows saying READ — and the lead's "97 READ / 9 SAMPLED" aggregate was exact
arithmetic over false inputs, while its one verification habit checked that the LEAD had read its
eleven reports, the link that was never the problem. So before trusting the aggregate, spot-check
the scouts of at least the two LARGEST assignments against their own transcripts, and say in
Coverage which scouts were spot-checked. The spot-check needs a DENOMINATOR: compare the scout's
read offsets against the staged file's own line count (a `wc -l` you run yourself), because offsets
alone prove contiguity, not completion — a lead extracted both scouts' offset lists, never fetched
either file's length, and certified "coverage spot-checks passed" on an instrument structurally
unable to tell a scout that finished from one that stopped a page short (both happened to have
finished; the check could not have known). Two axes decide a row's label, and they answer
different questions. PAGE COVERAGE — not tool choice — separates READ from SAMPLED: READ means
the reads collectively reach the staged file's LAST line, so a `Read` capped
at `limit=60` on a 202-line file and a `head -20` are SAMPLED whatever the row says — a brief
that banned "a capped grep" while blessing "paged reads" was violated twice in one night by
capped Reads and a `head`, forms that sentence literally permits, one row even stating the true
line count its reads never reached. Write that rule into scout briefs as coverage ("a row claiming
full means your pages reached the last line"), never as a list of banned tools. And give the
Coverage format a THIRD label, because the binary one manufactures false rows: a scout that did its
real work in the raw JSONL — exactly what the brief encourages — has nothing honest to write in a
format that only certifies the reduced view, and two such scouts (three nights running) resolved
the tension by writing "READ in full" about reduced files they never opened, one enumerating its
own 82-line hole inside the sentence claiming completeness. The labels are: READ (reduced pages
reached the last line), SAMPLED (say what was skipped), and RAW (targeted) — covered primarily by
raw-JSONL queries, saying which queries; a RAW row is honest coverage, not a downgrade, but like
SAMPLED it cannot carry a "clean, nothing found" verdict on its own. DELIVERY MECHANISM is the
second axis, and it separates READ from RAW: a READ row's pages entered your context through
`Read` or a plain `cat` of the staged view itself — content you met only as extraction-script
output or filtered greps is RAW whatever file the script opened, however completely it covered
it (an honest, often better way to work; just not that label). This axis goes into scout briefs
too, as its own coverage sentence — "a row claiming READ means the staged view's own pages
entered your context, not a script's output over it" — since it is the axis that produced the
inflated rows. A scout that staged reduced
views, worked entirely from its own extracts, and filed every row READ has now happened on
consecutive nights, including by a scout that was itself policing a sibling's inflated
rows. Alongside it:
**every finding cites its source, and a finding that came from a scout cites that scout's file and quotes one verbatim
line out of it — name the scout file even when you also cite the underlying transcript, because a
report whose findings cite only cells and transcripts is indistinguishable, on this format's own
check, from one synthesized from task notifications.** A finding
from anywhere else cites what it actually came from — the digest metric, the feedback row and its
snapshot, the watched fact's edge id — and a `solo: true` night says so once at the top and cites what it read
directly. A night synthesized from notifications cannot produce the scout citations, so it fails its
own report format rather than merely disobeying a paragraph. Cite the source you HAVE; a citation
invented to satisfy the format is worse than none, in the one document whose whole value is that its
evidence is real.

Scouts exist to find what counting cannot:

| | |
|---|---|
| **Semantic repetition** | The same mistake made three different ways, so no counter catches it. |
| **A misleading instruction** | A skill sentence several agents read the same wrong way. That several of them did is the evidence. |
| **Confident wrongness** | An agent proceeding smoothly in the wrong direction. Mechanically it looks like a clean session. |
| **Prompt-induced detours** | A run that went sideways because of how it was asked, not what was asked. |

That last one is its own output — see Prompting feedback below.

## Deciding: fix now, or watch

The default bar is **two independent sightings, or one explicitly general instruction from {{operator}}**.
You may go below it on judgment, but only in two cases, and you must name it when you do:

- the finding is **mechanically unambiguous** (a counter, an error, a measured duration), or
- the change has **no behavioural blast radius** — skill wording, a comment, a doc line.

Anything touching Go, orchestration, or a brief needs the full bar regardless of how obvious it
feels. Every sub-default ship goes in the report's **judgment calls** section, saying why.

Everything else becomes a **watched observation in the standalone watch store** (`watch_items` — since the v29 cutover of job 349 this is the dream loop's OWN table, and since the graph's removal in job 542 the only memory kraken keeps beside the operator's mental model; kraken's self-observations never enter the mental model). A row is the observation sentence plus this loop's bookkeeping (class, state, sightings, evidence, suggested fix, and the operator's verdict trio). Add it with `kraken watchlist add`, whose item is `observation`, `class`, and optionally `src`, `dst`, `predicate` (default `concerns`) — plain-text context labels now, no longer graph nodes — plus the optional `state`, `evidence`, `suggested_fix`, `pr_url` (`strength` is retired with the edge model; a still-sent value is ignored). **Folding is automatic**: re-adding the same wording (byte-identical after one-line normalization) bumps the existing row's sighting count server-side and replaces its evidence with what you sent, so there is no find-then-bump dance and never a second row for the same thing. To REWORD an observation, remove it and add it again — `update` changes bookkeeping fields only, never the sentence. Two things a fold does NOT do on its own, and both are yours when re-adding a SHIPPED observation whose fix did not take: it does not reset `state` unless you SEND one — a re-sighted row left `shipped` drops out of the DREAM view and tomorrow's pass reads it as settled, so include `state: watching` in the re-add itself (add forwards a non-empty state on a fold), or follow with `kraken watchlist update` — and `pr_url` is single-valued, so before a second ship overwrites it, write the earlier PR into the `evidence` text you send with the fold; nine sightings and one url is a shape that cannot express "fixed, recurred, fixed again", and the report is the only other place that history survives.

`src` and `dst`, when you send them, name the two real things the observation is ABOUT — a component, an agent, a tool, a convention, a failure mode — not a restatement of the sentence. "Scouts keep going to the raw transcript after being handed the reduced view" concerns `dream scout` and `kraken mine reduce`; it does not concern `observation 1` and `the loop`. They are plain-text labels on the watch row: they buy scan-ability in the DREAM view and consistency across nights (reuse the spelling earlier rows used — `kraken watchlist get` shows them), and nothing else — no retrieval leg anchors on them, no future cell is injected with them, and a row with imperfect endpoints costs a shrug rather than unreachability. The OBSERVATION SENTENCE is what must carry the finding on its own, since the sentence and the report are the only places a future pass meets it.

The move to the standalone store cuts the other way too: a watched observation is NOT a mental-model item — it is not returned by `kraken mm get`, never injected into a cell's spawn context, and invisible to the Memory view's Model pane. It lives exactly as long as this loop cares, in the DREAM view and `kraken watchlist get`. `kraken watchlist remove` **closes the row** (expiry, not deletion): the observation stops being watched and stays readable by id, with the operator's verdict trio preserved on it. If an observation deserves to become durable knowledge — a fact future agents should be handed — that is a decision for {{operator}}'s mental model (`kraken mm note` is theirs, never yours) or for a shipped fix whose skill text carries it; the dream loop itself never writes the mental model.

## What you may change

Anything in the kraken repo. But reach for **skill text first**: most findings are about how an
agent was instructed, and a sentence is cheaper to get wrong than a function. Larger changes are
allowed and sometimes right — a subagent definition, a brief, orchestration, real Go — and they
carry the same obligations any other change would: tests, and the review the cell's brief specifies.

Two hard fences: never `.claude/settings.json` (per-cell, gitignored, kraken-written — never a
durable improvement), and never anything outside the kraken repo.

**One concern per PR.** A PR mixing a skill tweak with a refactor cannot be partially rejected, and
{{operator}}'s review is the only thing standing between a judgment call and a merged mistake. The cell
branch recycles after each merge, so several narrow PRs from one night cost nothing.

## Prompting feedback

The scouts also answer a question pointed the other way: **where did {{operator}}'s own input make the work
harder than it needed to be?** An instruction two agents read two ways. A detail supplied late that
invalidated finished work. A prompt that never said what "done" meant. A correction they have now had
to issue twice.

This goes in the report, where it may quote them verbatim — advice like "one of your prompts was
unclear" is worthless without the prompt. It also accumulates: each distinct observation is a
watched fact with class `prompting`, folded on repeat like any other, because one ambiguous prompt
is noise and the same one three weeks running is a habit worth naming.

It is what makes a quiet night productive. A night with no corroborated rule and three honest notes
about prompting still produced something worth reading.

## The report

Write `00-dream-report.md` into the artifacts dir. Like every md artifact, it opens with a one-liner stating its main message — the night's headline, not a table of contents. Keep it to a page, and never hard-wrap its prose: one paragraph — and one bullet, and one table row — is ONE source line, however long, since a mid-sentence line break renders as a broken paragraph on the board (a repeat complaint from the operator). One evidence bar binds every factual claim in it, and hardest the claims about THE OPERATOR'S OWN ACTIONS: this is the one output of the night that passes through no review gate (the athenas see the git diff; the report never enters git), so you are its only checker — a report has told {{operator}} they performed a recovery action twice, hours from when they did, when the raw showed exactly one, and nothing between author and reader could catch it. A count, a time, or an attribution goes in only re-verified against the raw in the same session you write the sentence. The pass's numbers about ITSELF sit under the same bar and are the ones measured failing: the first pass to run under the defined closing counts got every one of its own wrong (118 reported vs 127 READ, 8 vs 44 RAW-supplemented, 45 vs 44 watch rows — 21 folds + 1 reword + 16 new + 7 shipped-state, the reword's own row already among the 16), so the report's Watching count, the Coverage aggregate, and the closing line's M — the same number as the Watching count; the cell section below defines it — are COUNTED, once, in one order: finish every watchlist write, count, write the report, then emit the closing line, and recount the watch number if any watchlist write lands after the count. M is the count of DISTINCT row ids the ADD and UPDATE verbs echoed tonight (`watchlist add|update ok (id N)`, on stderr — an unwatch echoes an id too and is still cited in Owed, but it is not a write M counts; keep every id as you go, deduped, and when an echo arrives id-less — the id print is best-effort — recover that row's id from `kraken watchlist get` before counting; that list's length is M and its members are the citations), with the new-vs-fold split read by diffing those ids against your opening UNFILTERED `kraken watchlist get` (no state argument — a watching-only baseline misreads every fold onto a shipped or declined row as a new add), since the CLI prints the id but not the route's folded flag — never a sum over a category breakdown you have not deduped, and never memory. Every watchlist write is yours — never delegate one to a scout: an id echoed into a subagent's terminal never reaches you, and M cannot be reconstructed without it. Any script you write to count lives at a scratch path outside the worktree: a file naming tonight's row ids is a finding, and findings never enter git. And every Watching, Seen or Owed claim that a row was added, folded, updated or unwatched cites the id the verb echoed: a report has said "watched with a suggested fix" over a row that was never touched, and the id is the one form of the claim a later pass can check — for an unwatched row the only one, since the overlay it named is gone. Sections, in order, with "None" spelled out rather than a section dropped:

| Section | Content |
|---|---|
| Shipped | Each change, the evidence behind it, and its PR. Also every edit applied after a verdict on that change, each one either recorded here under `do/SKILL.md`'s non-blocking carve-out — by file and by the reviewer finding it answered, with your own wording quoted wherever the reviewer offered none — or named as the edit that reopened the round, and the change's round count: a dream cell writes no `90-build-report.md`, so that carve-out's record has nowhere else to go, and one pass applied fifty-odd diff lines of comment and prose after its final CLEAR (one of them a comment correcting a misstated behaviour) while its report said only that nothing was committed before the CLEAR. |
| Judgment calls | Everything shipped below the two-sighting default, and why. An empty section on a night that shipped several changes means you are not naming your own judgment. |
| Watching | Newly watched facts, and what changed on existing ones — the watch overlay is not committed, so this is the only place its movement is visible. |
| Owed human actions | Every action only {{operator}} can take — a credential rotation, an external revoke — still outstanding, each with its watch row id, re-raised EVERY night until it is done. Done means {{operator}}'s `fine` verdict on that row, or the row's evidence recording who performed the action — then unwatch it; never unwatch a row while its action is still owed, and never write the verdict yourself. The watch row is what makes an owed action durable; this report and the closing line are the only channel that re-SURFACES it, and one live credential's rotation, named in a closing line but not re-raised by the next pass, ceased to exist for two nights while the value stayed live. |
| Your prompting | The prompting feedback, quoted. |
| Seen, not actionable | One line each, capped around ten. It is what distinguishes a quiet night from a blind one. |
| Coverage | Transcripts read, feedback notes swept, and anything skipped — including the aggregate READ vs SAMPLED split from the scouts' own coverage tables ("272/272 digested" over a 30%-sampled corpus reads as full coverage and is not), and the per-report READ IN FULL / PARTIAL list from Evidence 2. Also the pass's OWN stalls: limit banners, dead stretches, and operator interventions in your own pane tonight — your session is tomorrow's sweep evidence, so tonight's report is the only one that can carry them, and one pass reported four cells' outages while omitting its own 10h37m, 2.5× the four combined. |

## Inside A Kraken Cell

When a kraken job brief sends you here, these deltas apply — the brief wins wherever it is more
specific, and it is the only place the cell's real paths appear:

- **The report goes in the brief's SET ROOT** (`<cellDir>/artifacts/`) as `00-dream-report.md`. That
  directory is the only board-visible one, and the report is the deliverable; it is also what the
  board's `DREAM` view renders, so a pass that ships changes but writes no report is invisible.
- **Read and write watched observations with `kraken watchlist get|add|update|remove`** — the same four
  verbs, backed by the standalone `watch_items` table: `add` folds server-side on a repeat
  wording, and `remove` closes the row without deleting it. Sweep feedback with
  `kraken feedback sweep --since <ts>`. Run `kraken feedback addressed <slug> <id> <text>` **only for
  a note you shipped a change for**, naming the PR in the text — see Evidence 1. **No other cell runs
  that verb at all**, so nothing closes a note if you do not. The slug is the one the note was left on,
  which is usually another job's — though not always, since {{operator}} leaves notes on dream reports too. Two
  failures to expect rather than debug: the write 404s on a note whose owning job was hard-deleted (the
  row survives its job and the sweep still hands it to you, but there is nothing left to scope it to) —
  record that in the report instead. And `--clear` reopens an entry: use it if you closed one in error
  while it is still in front of you, since once the window passes nobody can reach that row again.
- **Scouts are unnamed subagents**, never teammates: they read and report, and change nothing in the
  repo — they still write their own findings to the scratch path you give them. The crew rules in
  the brief govern any agent that changes the repo.
- **Never `open`** (there is no browser in a cell).
- **The phase ends with one line, then you stop.** Say exactly:

  `dream swept: <set root>/00-dream-report.md (N shipped, M watching, K transcripts)`

  followed by the PR urls if any, then one clause per still-owed human action from the report's Owed
  section, and STOP (end the turn). The three counts are DEFINED, not chosen per night — the retired
  /graph job's closing line ran three conventions in three nights before its counts were pinned, and this line had
  started the same drift (one pass counted only its new rows as "watching"): **N is PRs this pass
  OPENED** (opened, not merged), **M is watch rows this pass WROTE** — new adds, folds onto existing
  rows, and overlay updates, each row counted once — and **K is the digest envelope's `scanned`
  count**, minus any sessions you set aside as wholly post-ceiling (name that subtraction in
  Coverage when it is non-zero) — and when the envelope's `truncated` is non-zero, say so beside it
  (`K transcripts, T partial`): a partial scan must never be silent.
  Use `0` where a count is zero — never omit the clause. Kraken derives blocked from your
  silence and shows your last words on the board.
- Never declare the job done — accepting is {{operator}}'s move, never yours.
- `solo: true` on the job means no subagents at all: run the mechanical pass and read what you can
  yourself, and say in the report that the semantic pass was skipped.
