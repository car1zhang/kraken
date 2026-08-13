# kraken web API — iOS wire contract

Canonical reference for every JSON endpoint the iOS app (`ios/Kraken/`)
consumes. Source of truth is the Go code — the handlers and wire types in
`internal/api/` (`board.go`, `peers.go`, `update.go`, `shipios.go`, …),
`web/serve.go`, and the model layers they read
(`internal/jobs/{daemon,job,status}.go`, `internal/core/peer.go`); this doc
transcribes it, it does not define it independently.

**Current contract**: `apiContract = 6` (internal/api/board.go — the Go
constant + `contract_test.go` bump lands in the same commit as this doc, per
CLAUDE.md's API-contract rule), mirrored by `APIClient.expectedContract`
(APIClient.swift). `/api/health` and
`/api/board` both serve `contract` (int) + `stale` (bool). v3 bumped when
the auto-approve feature (and `auto_approve` on the job form GET/POST) was
removed. v4 bumped when the job-level `type` (`build`|`info`) concept was
deleted (Q14) — `type` removed from `/api/board`'s job objects and from the
job form GET/POST shapes; the pipeline distinction now lives in `/aim` vs
`/info` skill choice plus carl's `/do` steering, never a wire field. v5
bumped when `skip_doc` was removed from the job form GET/POST shapes (the
doc phase now runs purely from zeus' own read of the job spec text, never a
stored flag) and `doc_questions_err` was removed from `/api/board`'s job
objects (questions moved from a strictly-parsed `questions.yaml` file to
relational rows in `kraken.db` — there is nothing left that can fail to
parse). v6 bumped when the **queued status was retired**: `ready` and
`prereqs` were removed from the job form GET/POST shapes, and `start_error`
and `prereqs` from `/api/board`'s job objects. Submitting a job now starts its
cell inside the request (`POST /api/queue`, URL kept — see below), so there is
no flag to set, no dependency gate to configure, and no background failure to
report out of band. `state`'s value domain lost `queued` with them:
draft · working · blocked · done (+ `archived`).

**Bump policy**: bump `apiContract` ONLY on a breaking change — a JSON key
retyped, renamed, or removed. Additive keys never bump it: old clients
already ignore fields they don't recognize, and `Decodable` optionals
absorb a key's absence.

**Change rule**: any change to a JSON shape served to or accepted from the
app/web UI lands in the same commit as: the Go change (`internal/api/*.go`,
`web/serve.go`, `internal/jobs/{daemon,job}.go`), this doc,
`ios/Kraken/Models.swift`, and `internal/api/contract_test.go`. A Swift
model should not be the only thing pinning a shape: give every response the
app decodes a spec in `contract_test.go`. The carve-out is the handful of
endpoints whose success path needs real tmux panes to reach —
`/api/peers/{spawn,send,kill,capture}`, listed in `contract_test.go`'s own
exclusion comment — so `SpawnResponse` and `CaptureResponse` (APIClient.swift)
are pinned by this doc and their Go structs alone. See also the "API
contract" note in the root `CLAUDE.md`.

**omitempty → Optional rule** (from Models.swift's header comment): a Go
struct field tagged `,omitempty` becomes a Swift `Optional` (the key may be
entirely absent; for slices, Go's nil-vs-empty distinction can even surface
as JSON `null`, which `Decodable` treats the same as an absent key for an
Optional property). A field with no `omitempty` tag is always present on
the wire and modeled as non-optional in Swift. Map-literal responses (the
job-editor endpoints, built as `map[string]any` rather than a tagged
struct) always emit every key — even ones a Swift model marks Optional for
other reasons (nil-vs-empty on the Go side, not the wire's absence).

The rule reads one way only: `omitempty` ⇒ Optional, but Optional does not
imply `omitempty`. Two kinds of Swift Optional exist without one.
**Absence on an older server** — `contract`, `stale`, `WebBoard.update`,
`WebBoard.shipIOS`: all always present on a current server, missing from
builds that predate them, where the absence is itself what the app checks
for. **Blast-radius containment** — every field `WebUpdate` and
`WebShipIOS` model, all of them always present on the wire, made Optional
(with a hand-written `init(from:)`) purely so a drifted sub-key costs the
⟳ / ⇧ control rather than the entire `/api/board` decode; see those structs'
header comments in Models.swift. (`restarted` is the one wire key WebUpdate
deliberately does not model — see its row below.)

Error responses: any non-2xx status returns `{"error": string}`. Board
action endpoints (`webAction`-wrapped) additionally return `{"ok": true}`
on success. `mtime` is always a JSON **number** (unix milliseconds); the
job-editor POST endpoints accept a number *or* a legacy numeric string on
the way in (`mtimeToken`), for old clients — but always emit a number.

---

## Board & actions

### `GET /api/board` → `webBoard` (web.go)

| key | type | optionality | notes |
|---|---|---|---|
| `daemon` | object (`daemonStatus`) | always | see below |
| `daemon_ok` | bool | always | |
| `cell_cap` | number | always | **VESTIGIAL since v6** — nothing enforces a cap any more (the daemon's queue pass was its only reader and went with the queued status). Held at the value shipped clients have always seen rather than zeroed, because `cellCap` is a NON-OPTIONAL member of the iOS `Board` model and a 0 renders as "a cap of none". Do not add new readers; it goes when the existing ones stop |
| `live_cells` | number | always | |
| `jobs` | array (`webJob`) | always (`[]` not `null`) | within a display state, jobs render in the manual reorder's order (see `POST /api/reorder` below) — no separate order field, the array position IS the display order |
| `contract` | number | always | == `apiContract` |
| `stale` | bool | always | this `k web` process' self-check |
| `update` | object (`webUpdate`) | always | the self-update button's state (see `POST /api/update` under Misc). Added additively, no contract bump. Modeled in the iOS app as `WebUpdate` (Models.swift), **Optional** on `WebBoard` — always present on any server new enough to have the button, absent on one older than it |
| `ship_ios` | object (`webShipIOS`) | always | the TestFlight ship button's state (see `POST /api/ship-ios` under Misc). Added additively, no contract bump. Modeled in the iOS app as `WebShipIOS` (Models.swift), **Optional** on `WebBoard` for the same reason `update` is |

`webUpdate` (api/update.go) — assembled by one function (`currentUpdate`)
shared with `GET /api/update/status`, so the board key and the poller's
endpoint can never disagree. No field is `omitempty`: every key is always
present, zeroed when it doesn't apply.

| key | type | optionality | notes |
|---|---|---|---|
| `phase` | string | always | `idle`\|`preflight`\|`fetching`\|`merging`\|`building`\|`installing`\|`restarting`\|`mac app`\|`done`\|`failed`\|`uptodate`. `mac app` is the `make install-mac` step, deliberately after the restart so an Xcode failure finds the runtime already updated. `idle` when no run has ever happened (missing status file). Render unknown phases generically — the list is additive |
| `from_sha` | string | always | the commit the in-flight/last run started from |
| `to_sha` | string | always | the commit it moved to; `""` until the merge lands |
| `behind` | number | always | commits `HEAD..origin/main`, from the daemon's 10-minute availability check |
| `error` | string | always | the RUN's error only — a failed availability check is never surfaced here |
| `started_at` | number | always | unix millis; `0` = never run |
| `updated_at` | number | always | unix millis of the last phase transition |
| `build_sha` | string | always | the running binary's ldflags-stamped short sha, `"dev"` unstamped |
| `checked_at` | number | always | unix millis of the last availability check; `0` = never checked |
| `restarted` | bool | always | this run replaced the binary and kickstarted the daemon (set from the `restarting` phase on) — a `failed` record with it true is a mac-app-phase failure whose runtime half already landed, and the web board still reloads on it. Not modeled in Models.swift: its two consumers are the SPA's embedded-bundle reload and the server's own behind-suppression, and the native app has neither an embedded bundle nor that count to correct |

`webShipIOS` (api/shipios.go) — assembled by one function (`currentShipIOS`)
shared with `GET /api/ship-ios/status`, so the board key and the poller's
endpoint can never disagree. No field is `omitempty`: every key is always
present, zeroed when it doesn't apply.

| key | type | optionality | notes |
|---|---|---|---|
| `phase` | string | always | `idle`\|`preflight`\|`generating`\|`archiving`\|`exporting`\|`uploading`\|`done`\|`failed`. `idle` when no run has ever happened (missing status file) |
| `build` | number | always | the `CFBundleVersion` this run is uploading (`git rev-list --count HEAD`); `0` until the shipper's `KRAKEN-BUILD:` line is seen. App Store Connect burns a build number permanently, so this is the first thing needed after a failure |
| `error` | string | always | the run's error; `""` otherwise |
| `started_at` | number | always | unix millis; `0` = never run |
| `updated_at` | number | always | unix millis of the last phase transition |

**`done` means uploaded, not installed.** The phase reaches `done` when the
`.ipa` has been accepted by App Store Connect. TestFlight then processes it
(minutes), and the build still has to be installed by hand from the
TestFlight app. Both clients are worded around this — the control reads
"uploaded", never "shipped" — because a green ✓ that implied "the phone is on
the new build" would be false at exactly the moment it is shown.

`daemonStatus` (daemon.go):

| key | type | optionality |
|---|---|---|
| `pid` | number | always |
| `started_at` | number | always |
| `last_tick` | number | always |
| `last_error` | string | omitempty |
| `cells` | number | always |
| `stale` | bool | omitempty |

`last_error` is **VESTIGIAL since v6** — the same call `cell_cap` gets, for the
same reason. It has no writer left: the tick returns no error now that the
queue pass is gone, and the one start it still makes (a dream or graph job it
created itself) reports its failure per job, to the event log and a
notification, rather than through one global string no client renders per job.
The key stays because omitting one is a breaking change and removing it buys
nothing — being `omitempty`, it was already absent from every healthy tick's
payload, so a client sees exactly what it has always seen.

`webJob` (web.go):

| key | type | optionality | notes |
|---|---|---|---|
| `slug` | string | always | archived jobs: numbered archive file base. **Not unique across `jobs`** — the archive numbers a name against its own contents only, so a slug freed by archiving and then reused by a new active job appears TWICE (archived entries come last). Key by `(slug, archived)`, never bare `slug` |
| `repo` | string | always | |
| `state` | string | always | draft/working/blocked/done/archived. `queued` was removed in v6 with the status itself |
| `err` | string | omitempty | |
| `outcome` | string | omitempty | |
| `cell` | string | omitempty | tmux session name |
| `live` | bool | always | |
| `artifacts` | array (`webArtifact`) | omitempty | |
| `detail` | string | omitempty | zeus' last assistant text, full multi-line text capped at 4000 runes (not bytes) |
| `question` | object (`webQuestion`) | omitempty (null-able) | |
| `zeus_status` | string | omitempty | zeus' own refined status (running/polling/waiting/question); live started, non-done cells only, omitted when zeus isn't resolvable |
| `wakeup` | object (`webWakeup`) | omitempty (null-able) | pending `ScheduleWakeup` from zeus' transcript — cell is on a poll timer, still counts as `working`; render a countdown chip. Additive, no contract bump |
| `roster` | array (`webAgent`) | omitempty | live crew (nested subagents), live started non-done cells only |
| `health` | string | omitempty | "" (absent) = healthy |
| `mtime` | number | always | unix millis |
| `archived` | bool | omitempty | |
| `display_slug` | string | omitempty | original slug, archived jobs only |
| `cell_gone` | bool | omitempty | started + non-done + tmux session vanished; worktree (incl. uncommitted WIP) still on disk — reactivate preserves it, done/kill discard it |
| `reactivatable` | bool | omitempty | has a recorded zeus session, and either done or `cell_gone` |
| `last_sent` | string | omitempty | last real user message from zeus' session transcript, any send channel (board steer, `k s`, direct tmux typing); omitted when no transcript is resolvable or it has no user turn yet; one-line, capped 4000 runes (not bytes). Question-answer digit keypresses never appear (they're tool_results, not user text); started + non-done jobs (live or cell_gone) only |
| `prs` | array (`jobPR`) | omitempty | every PR shipped from the cell branch (daemon-tracked history, never cleared); render as linked state chips |
| `doc_questions` | array (`webDocQuestion`) | omitempty | zeus' open doc questions (answered or not), served straight from `questions` rows in `kraken.db` — no file, no parse step, so there is no more cell-gone-read-only or malformed-file case; never set for archived jobs. Additive, no contract bump |
| `feedback` | array (`webFeedback`) | omitempty | per-job feedback entries (open + addressed), served straight from `feedback` rows in `kraken.db`; unlike `doc_questions`, IS populated for archived jobs (rows outlive the cell). Additive, no contract bump |
| `color` | string | omitempty | the job's color, one of `red` \| `blue` \| `green` \| `yellow` \| `purple` \| `orange` \| `pink` \| `cyan` (the names Claude Code's `/color` accepts, in that order); absent or `""` means the job has NO color and must render exactly as an uncolored job always has — no swatch, no reserved space. Clients degrade an unrecognized name to "no color" rather than failing. Additive, no contract bump |
| `threads` | array (`webThread`) | omitempty | this job's side threads — every fork of zeus' session, live or closed, served straight from `threads` rows in `kraken.db`; a closed thread outlives its cell (its transcript is still readable), so the array survives teardown. Additive, no contract bump |
| `tokens` | object (`webTokens`) | omitempty | this job's summed token spend, straight from its `job_tokens` row. **Absent means NEVER MEASURED, which is not the same as zero** — a job that was measured and cost nothing serves the object with all-zero counters. Present for done and archived jobs too (the row outlives the cell). Additive, no contract bump |
| `load` | object (`webJobLoad`) | omitempty | zeus' two 1–10 triage estimates for this job — how expensive carl's unblock is, and how big the claude work it buys. **Absent means "no estimate to act on", which is not the same as an estimate of zero**, and unlike `tokens` it covers THREE cases: never estimated, cleared (a clear zeroes `at`, and a zero `at` serves no object), and archived. **Withheld on archived jobs regardless of whether one exists** — the four columns survive archival and the key returns on unarchive; the archived loop drops it because a card that cannot be blocked cannot pose the question the badge answers. Served on every active job whatever its display state, so the key never flickers as a card moves column — the blocked-only rule is a RENDERING rule the clients apply, not a payload rule. The ROI the badge renders is DERIVED by the client (`scope / unblock`) and deliberately never on the wire. Additive, no contract bump |

`start_error` and `prereqs` were REMOVED from this shape in v6. `start_error`
was a side channel for a failure the board could not otherwise see: the daemon
started queued jobs in the background, so the reason one would not start
reached nobody. A start is a foreground HTTP request now — the reason comes
back as `POST /api/queue`'s own 409 body — and a key that can only ever be
empty is one a client keeps rendering affordances for. `prereqs` went with the
feature.

`jobPR`: `url` (string, always), `state` (string, always: `open` \| `merged` \| `closed`). The PR number for display derives from the url's trailing `/pull/<n>`.

`webArtifact`: `name` (string, always — in store mode this may contain `/`
for a nested path, e.g. `icon-variations/v2.png`), `url` (string, always).
The `artifacts` array is **sorted by `name`**, lexicographically over the full
path, however many sources the server unioned to build it. That is what makes
the `00-`/`10-`/`50-`/`90-` filename prefixes read as an order rather than a
convention, and clients rely on it: both the web board and the app page
through this array with next/prev and neither re-sorts it. Nested paths sort
as full paths, so a subdirectory's files cluster together. Ordering is not
part of the wire *shape*, so this is no contract bump.

`webAgent` (roster entry): `name` (string, always), `model` (string, always),
`role` (string, always — `hephaestus`/`athena`/`hermes`/`other`), `status`
(string, always — the entry's refined status, e.g. `running`/`waiting`/
`question`/`in-process`), `working` (bool, always — no `omitempty`, so
`false` still rides the wire; tmux-backed entries are working iff their pane
reports `running`, in-process entries iff their team-config `isActive` flag
is true).

`webQuestion`: `tool_use_id` (string, always), `questions` (array of `webQuestionItem`, always).

`webQuestionItem`: `question` (string, always), `header` (string, always),
`multi_select` (bool, always — note: no `omitempty` tag, so `false` still
rides on the wire), `options` (array of `webQuestionOption`, always).

`webQuestionOption`: `label` (string, always), `description` (string, always).

`webWakeup`: `reason` (string, always), `fires_at` (number, always — unix millis). Distinct from `Wakeup` on the peer wire shape only in that it drops `delay_seconds` — the client only needs `fires_at` for its countdown.

`webDocQuestion` (one `questions` row, `store.DocQuestion`'s wire twin — same
field set, same json tags as the old `questions.yaml` record it replaces):

| key | type | optionality | notes |
|---|---|---|---|
| `id` | string | always | `Q<digits>`, e.g. `Q1` |
| `text` | string | always | the question itself |
| `context` | string | omitempty | |
| `importance` | string | always | `P0` \| `P1` \| `P2` |
| `recommendation` | string | omitempty | zeus' suggested answer |
| `answer` | string | omitempty | carl's answer once posted; absent means still open |
| `answered_at` | string | omitempty | RFC3339 timestamp, set alongside `answer` |

`webFeedback` (one `feedback` row, `store.Feedback`'s wire twin):

| key | type | optionality | notes |
|---|---|---|---|
| `id` | number | always | row id — the target for the edit/delete/addressed ops below |
| `artifact` | string | always | artifact path/name the feedback is attached to |
| `note` | string | always | latest note text — note is latest-only, an edit overwrites rather than appending |
| `at` | number | always | unix millis the feedback was submitted (`ts` column) |
| `addressed` | string | omitempty | the nightly dream pass' free-text account of the DURABLE fix it shipped for this note, naming the PR (prose, not a boolean). Written by that pass alone — an ordinary cell acts on its feedback and leaves this empty by design — so absent means "no durable fix has shipped", NOT "nobody handled it" |
| `addressed_at` | string | omitempty | RFC3339 timestamp, server-stamped alongside `addressed` |

`webThread` (one `threads` row, `store.Thread`'s board-facing subset — a side
thread is a fork of zeus' session running as a second claude in its own tmux
window of the same cell):

| key | type | optionality | notes |
|---|---|---|---|
| `tid` | string | always | `t<digits>` counting from `t1`, unique per job and never recycled; the target every op below takes |
| `title` | string | omitempty | carl's name for the thread; absent for one branched without a title — clients fall back to the tid |
| `state` | string | always | `live` \| `closed`. `closed` = the tmux window is gone; the row (and the transcript it points at) survives, and `revive` resumes the same claude session |
| `pane` | string | omitempty | the live thread's tmux pane, e.g. `%42` — usable with `GET /api/peers/capture`. Cleared on close (tmux recycles pane ids), so it is absent for a closed thread rather than stale |
| `created_at` | number | always | unix millis the thread was branched |
| `closed_at` | number | omitempty | unix millis the thread was closed; absent (0) while live |

The provenance columns — `session_id`, `parent_session`, `fork_uuid`, `seed`,
`cwd` — and the tmux window target deliberately never ride the board poll;
`fork_uuid` is served by `GET /api/thread/read` instead, where the split it
anchors is actually used.

`webTokens` (one `job_tokens` row — the job's token spend, summed from its own
Claude Code transcripts by the daemon and only READ by the API):

| key | type | optionality | notes |
|---|---|---|---|
| `in` | number | always | `input_tokens` — uncached input |
| `out` | number | always | `output_tokens` — generated |
| `cache_write` | number | always | `cache_creation_input_tokens` |
| `cache_read` | number | always | `cache_read_input_tokens` |
| `sessions` | number | always | how many transcript FILES were summed (root transcripts + subagent sidecars) — what tells "this job was cheap" apart from a row of zeros meaning something else |
| `updated_at` | number | always | unix millis, when a refresh last found new spend |
| `zeus_in` | number | always | the part of `in` the cell's LEAD agent spent. Additive key, no contract bump |
| `zeus_out` | number | always | the part of `out` the lead spent |
| `zeus_cache_write` | number | always | the part of `cache_write` the lead spent |
| `zeus_cache_read` | number | always | the part of `cache_read` the lead spent |

Four rules for any client rendering this:

- **The four counters ride RAW, with no weighted total, and none is ever
  added.** They are billed at wildly different rates and no server-side "cost"
  number exists on purpose — one would bake a price ratio into the wire and go
  stale the moment pricing moves. A client that wants a single number weights
  them itself and owns that choice.
- **No key inside the object is `omitempty`** — a measured zero is a real
  answer and still rides the wire. Absence is expressed one level up: `tokens`
  itself is missing from the job. Never render an absent `tokens` as `0`.
- **A row may be a FLOOR rather than an exact total.** A transcript that could
  only be scanned part way still contributes what was read (a failed read is
  not a zero), and nothing on the wire marks which rows those are — so present
  the number as spend-so-far, never as exact.
- **The four `zeus_*` keys are a SUBSET of the four totals, not a sibling of
  them, and SUBAGENT spend has no key of its own.** `in` already includes
  `zeus_in`; a client that adds the two doubles the lead's spend. What the job
  delegated — teammates (`hephaestus`, `athena-*`, `hermes-*`) plus in-process
  subagents — is `in - zeus_in`, `out - zeus_out`, and so on, computed by the
  client. Only one half is ever stored or served, because two halves on the
  wire can arrive disagreeing with the total they are supposed to add up to
  and a derived half cannot. A cell's SIDE THREADS count as the lead: a thread
  is a fork of zeus' own session that speaks in its place, not work zeus handed
  out. kraken decides which half a transcript belongs to from its own head
  records — an in-process subagent's sidecar path, Claude Code's `agent-setting`
  record, or an `agentName` that is not one of the two kraken mints for a lead
  (`zeus-…`, `thread-…`). A transcript carrying none of those reads as the lead;
  on this machine that is 7 of 1,588 and all seven really are leads (claudes
  started in a cell worktree by hand), so no transcript is currently booked to
  the wrong half. Either way the split can only ever move spend between the two
  halves — the four totals are unaffected.

`webJobLoad` (the job's `load_*` columns — zeus' two triage estimates for a
BLOCKED job: how much effort carl must spend to unblock it, and how big the
claude work that reply buys):

| key | type | optionality | notes |
|---|---|---|---|
| `unblock` | number | always | 1..10 — carl's cost to unblock. 1 = a mechanical action; 5 = moderate thought; 10 = deep thought, many decisions, lots of context |
| `scope` | number | always | 1..10 — the size of the claude work that unblock releases. 1 = a one-message reply; 5 = a moderate implementation slice; 10 = many subagents and a massive scoped implementation |
| `note` | string | always | zeus' one-line rationale; may be `""`. One-lined and capped at 200 **runes** (not bytes) server-side, so it is safe to render on one line without re-truncating |
| `at` | number | always | unix millis the estimate was written; always > 0 while the object is present. Stamped SERVER-SIDE — a client never sends it |

Three rules for any client rendering this:

- **Absence is the "no estimate" answer, and it is not zero.** No key inside
  the object is `omitempty`, so a present object always carries all four;
  absence is expressed one level up, by `load` itself being missing from the
  job — the same arrangement `tokens` uses. Never render an absent `load` as
  `0`. **There is exactly ONE spelling of "no estimate" on the wire, and it is
  the absent key.** A both-zero object never arrives: the clear writes a zero
  row, `at` is zeroed with it, and the server serves no object for a zero `at`.
  Both-zero is a STORAGE state only. A half-estimate — one of the two at zero —
  is a 400 on the way in and likewise never arrives. Both clients nonetheless
  refuse to draw a badge for either shape, since neither has an ROI and a
  hand-edited row is the one way one could appear.
- **roi is DERIVED, never on the wire.** It is `scope / unblock` as a float,
  computed at render on each client. Storing or serving it would put a third
  number beside the two it divides, free to drift out of agreement with them —
  the same reasoning that keeps `webTokens` serving one half of its split and
  subtracting the other.
- **The badge is a BLOCKED-card control.** Both clients render it only where a
  card reads as blocked, and only when the object is present. The question it
  answers — "is this one worth unblocking right now" — is not one a working,
  draft, done or archived card poses, and an estimate left over from an
  earlier block reads as a current fact on a running card.

The band the badge is coloured by is derived from that float, closed-open, and
is identical in the web CSS and the iOS theme: `hot` at `roi >= 3.0`, `warm`
over `1.5 ..< 3.0`, `even` over `0.75 ..< 1.5`, `cold` below `0.75`. Badge text
is `⚡ <roi>× <unblock>→<scope>` with roi to one decimal place (`⚡ 4.0× 2→8`);
its tooltip / accessibility label is
`unblock effort <unblock>/10 · next step <scope>/10 · roi <roi>× · estimated <age> ago`,
with ` — <note>` appended when the note is non-empty.

The **age clause is required**, and it is the only thing either client renders
`at` for. Without it a stale estimate is indistinguishable from a fresh one on
exactly the card the feature exists for: zeus estimates, carl answers, the cell
later dies, the daemon's `health` files the card back under BLOCKED — and with
zeus gone nothing rewrites the estimate, so the badge keeps describing a
question answered hours ago. Each client formats the age with its own existing
relative-time helper; `3h` vs `3 h` between the two boards is acceptable drift,
the clause and its position are not.

The age also needs a **visible** surface on a client with no hover. On the web
board the `title` tooltip is enough — a pointer reaches it. iOS has neither
hover nor a tap target here, and its accessibility label REPLACES the visible
text rather than supplementing it, so an age living only there would be
reachable through VoiceOver alone. The app therefore renders it as a small
muted `<age> ago` label BESIDE the badge; the badge text itself is pinned
identical across clients and never absorbs it.

### `POST /api/queue` `/api/done` `/api/retry` `/api/kill` `/api/archive` `/api/unarchive` `/api/delete`

Request: `webReq` — `slug` (string, required), `text` (string, omitempty, unused by these).
Response: `{"ok": true}` on success; non-2xx `{"error": string}` on failure (409 for validation conflicts).

**`/api/queue` is the SUBMIT** — the draft→working move, and the one start
surface in the whole system. The URL is deliberately unchanged (shipped iOS
builds post to it for what they call the draft→queued drag), but what it does
is not: it starts the job's cell inside this request and answers only once the
cell is up. A `200` means the cell is live and the job reads `working`; a `409`
carries the exact reason and leaves the job a draft. It refuses, in order, a
job that is unparseable, done, already started, has an empty spec, or whose
`repo:` does not resolve — one refusal set (`jobs.SubmitJob`), shared with the
TUI's `s` key and the daemon's own dream/graph starts.

Clients that used to render an in-flight "queued" column should render the
pending POST on the dragged card and return it to draft on a 409: the request
is now as long as a cell start takes.

**Raise the request timeout for this call.** A cell start cuts a git worktree,
opens a tmux session and spawns claude; it routinely outruns a default
per-request timeout. The iOS app's shared `URLSession` is configured at
`timeoutIntervalForRequest = 5` (APIClient.swift), which this call cannot ride
— it uses a dedicated session with a longer cap instead. A client-side timeout
here is **not a failed start**: the server does not cancel on disconnect, so
the cell comes up anyway and the next `/api/board` poll shows the job
`working`. A client that renders the timeout as a failure and puts the card
back in draft is telling the user the opposite of what happened.

`/api/dequeue` and `/api/start` are **GONE** (404). `dequeue` was the
queued→draft reverse move and `start` the manual start for a job the queue
pass would not take; neither addresses a state that exists.

### `POST /api/question`

Request: `webReq` — `slug` (string), `text` (string: a single digit `"1"`–`"9"`, the 1-based option index).
Response: same as above.

### `POST /api/doc-question`

Request: `webReq` — `slug` (string, required), `id` (string, required: the
question id, e.g. `"Q1"`), `text` (string, required non-empty: the answer,
capped at 4000 **runes** (not bytes) after trim — same cap convention as
`last_sent`).
Response: `{"ok": true}` on success; non-2xx `{"error": string}` on failure
(409 for validation conflicts, same convention as the other board actions).
Additive request field (`id`); no contract bump.

Answers (or **re-answers**) one open doc question with a plain row UPDATE of
`answer`/`answered_at` on the matching `questions` row in `kraken.db` —
replaces the old `jobs.AnswerDocQuestion` surgical `questions.yaml` file
edit. No tmux/pane interaction, and critically **no liveness requirement**:
the row is the record, so this lands whether zeus is live, dead, mid-recovery,
or the job is done — there is no more "job's cell is gone — questions are
read-only" case, since rows have no cell dir to lose. Editability is free
(Q13): a re-answer simply overwrites the prior `answer`/`answered_at`, so the
old single-use-per-question restriction is gone too. Refused (409) when:
`id` or `text` is empty, or the question id doesn't exist for that job.

### `POST /api/feedback`

One endpoint, three carl-facing forms distinguished by which fields are
present (plus a fourth, zeus-facing form below) — `feedbackReq`:

| key | type | optionality | notes |
|---|---|---|---|
| `slug` | string | required | |
| `home` | string | required for create | artifact home, same set as `/api/artifact/<slug>/<home>/<name>` (`cell`\|`pluto`\|`archive`) |
| `name` | string | required for create | artifact name/path the feedback is attached to |
| `id` | number | required for edit/delete | the feedback row id |
| `feedback` | string | required for create/edit | the note text, capped at 8000 **runes** (not bytes) after trim — refused (400) over that, since the note rides every `/api/board` poll for as long as the card is visible |
| `delete` | bool | set `true` for the delete form | |

- **Create** — `{slug, home, name, feedback}`: resolves the artifact the
  same way the artifact route does, INSERTs a row (snapshot of the artifact
  as currently served, note, carl's session, submit timestamp). Works for
  done/archived jobs too (Q2) — feedback rows are the only store, so there
  is no live/dead split.
- **Edit** — `{slug, id, feedback}`: UPDATEs the note and re-freezes the
  snapshot from the artifact as currently served (Q4: snapshot always
  re-freezes on edit). Refused (404) unless `slug` resolves to the SAME job
  `id`'s row belongs to — an id alone is never enough to mutate another
  job's feedback.
- **Delete** — `{slug, id, delete: true}`: DELETEs the row (Q9). Same
  slug-ownership check as edit.

Response: `{"ok": true}` on success — no row echoed back; the board's next
`/api/board` poll picks up the updated `feedback` array. Non-2xx
`{"error": string}` on failure (404 for an unknown or not-yours row, 413 for
an oversize request body).

Not modeled: the fourth form, `{slug, id, addressed}` (UPDATEs only the
`addressed`/`addressed_at` columns), is reached through the `kraken feedback
addressed` cell verb by the NIGHTLY DREAM PASS alone — an ordinary cell reads
its feedback and acts on it while deliberately leaving the column empty — and
never by the iOS app —
same convention as `/api/pr-attach`/`/api/logevent` above, so it is not in
Models.swift/APIClient.swift. A non-empty `addressed` is capped at the same
8000 **runes** after trim as `feedback` (refused 400 over that — `addressed`
also rides every `/api/board` poll, and its writer is an agent, not a human).
An
explicitly empty `addressed` (`{slug, id, addressed: ""}`, as opposed to the
key being absent) clears a prior addressed note back to open; the cell verb
reaches that form as `kraken feedback addressed --clear <slug> <id>`.
Sending `addressed` and `feedback` together is refused (400): they are two
different forms, and the addressed branch wins the dispatch, so honoring the
request would silently drop the note edit.

### `GET /api/feedback?slug=`

zeus-side read: every feedback entry for a job, closed and not, re-read on
every resume via the `kraken feedback get` cell verb. Not modeled in
Models.swift/APIClient.swift — the iOS app reads feedback from the per-job
`feedback` array on `/api/board` instead, never this endpoint directly.

Response: `{"feedback": [webFeedback]}` (same `webFeedback` shape as the
board's per-job array above). Always an array, never `null` — a job with no
feedback yet serves `[]`.

### `GET /api/feedback/sweep?since=<unix-millis>`

The dream loop's CROSS-JOB read: every feedback note created at or after the
floor, across all jobs, oldest first (`ts`, then `id`). Reached by the
nightly pass through the `kraken feedback sweep` cell verb. **Additive, no
contract bump.** Distinct from `/api/feedback?slug=` in two ways that matter:
it spans jobs (so each row names its own), and each row carries a
`snapshot_url`.

`since` is REQUIRED and must parse as an integer — a missing or non-numeric
value is a 400, never a silent "everything" or a default window. The default
window lives in the CLI verb instead (last 24h), where it is visible in a
usage line. The floor is inclusive (`ts >= since`), so a caller sweeping
consecutive windows should advance past the last row it consumed.

Response: `{"feedback": [webSweepFeedback]}` — always an array, never `null`.

| key | type | optionality | notes |
|---|---|---|---|
| `id` | number | always | the feedback row id |
| `job` | string | always | the owning job's slug (an archived job's numbered archive name) — this read spans jobs, so the row names its own |
| `artifact` | string | always | artifact name/path the note was left on |
| `note` | string | always | carl's note text |
| `at` | number | always | unix millis, submit time (immutable — an edit does not restamp it) |
| `addressed` | string | omitempty | the nightly dream pass' account of the durable fix it shipped for the note — that pass is its only writer, so absent means "no durable fix has shipped", not "nobody handled it" |
| `addressed_at` | string | omitempty | RFC3339 |
| `snapshot_url` | string | always | `/api/feedback/snapshot?id=<id>` — always present; a row whose frozen blob is empty 404s when fetched (the sweep excludes blobs, so it cannot know which) |

Errors: 400 (missing/non-numeric `since`), 500 `no store wired`. A GET is a
read, so it stays reachable under `--mirror`.

### `GET /api/feedback/snapshot?id=<row id>`

Not JSON — serves the raw bytes a feedback row froze at submit (or last edit)
time: the artifact exactly as carl was looking at it when he wrote the note.
The first read path this column has ever had. **Additive, no contract bump.**

The bytes are served INERT, and deliberately so: a snapshot is untrusted
content (an agent wrote it, months ago) replayed from the board's own origin.
`Content-Type` is derived from the row's stored `snapshot_ext` by the same
extension map the artifact route uses, EXCEPT that `text/html` and
`image/svg+xml` are downgraded to `text/plain; charset=utf-8` — both are
script-executing document types. Alongside it: `Content-Disposition:
attachment` (never a top-level document), `X-Content-Type-Options: nosniff`,
`Content-Security-Policy: sandbox; default-src 'none'`, and `Cache-Control:
no-store` (an edit re-freezes the blob under the same id).

Errors: 400 (missing/non-numeric `id`), 404 (unknown id, **or** a row whose
blob is empty — "no frozen copy" is not "an empty frozen copy"), 405
(non-GET), 500 `no store wired`.

### `GET /api/watchlist[?state=]`

The dream loop's watchlist: observations the nightly pass is tracking, newest
observation first (`last_seen` DESC, id DESC). **Additive, no contract bump.**
Unlike questions and feedback, watch items are GLOBAL — they belong to no job,
so there is no `slug` on this route or either of its writes.

**A watch item is a knowledge-graph edge.** The observation IS the edge's fact
sentence (`kg_edges.fact`); a thin overlay row in `kg_watch`, keyed by edge id,
carries the dream-loop workflow and carl's judgement. Two consequences for a
client:

- **`id` is an EDGE id** — the same id `GET /api/graph` and
  `POST /api/graph/search` print for that fact. It is still opaque to the app,
  and still the id both writes below take.
- **An invalidated fact leaves the list.** The join is against LIVE edges only,
  so the graph's revision pass striking a fact through is also the DREAM view
  dropping it. To a client that reads as an item disappearing — the same thing
  a removal has always looked like.

Nothing else moved: the keys, their json names, their types and their
optionality are byte-for-byte what they were before the move, so the DREAM view
and the iOS app decode this unchanged. `src`/`dst`/`predicate` are deliberately
NOT on this wire — adding a key to an existing shape is a contract bump, and
the graph routes already expose linkage.

`state` narrows to one of `watching`|`shipped`|`declined`; omitted or empty
means every item. An unrecognized state is a 400, not an empty list — a typo'd
filter and an empty watchlist are different answers.

Response: `{"watchlist": [webWatchItem]}` — always an array, never `null`.
Modeled in the iOS app (`WebWatchItem`/`WatchlistResponse` in Models.swift,
`fetchWatchlist` in APIClient.swift) — the DREAM screen's watchlist pane,
which reads `?state=watching`. Note `class` is a Swift keyword, so the Swift
property is `watchClass` behind an explicit CodingKey.

| key | type | optionality | notes |
|---|---|---|---|
| `id` | number | always | the watched edge's id |
| `observation` | string | always | what the pass noticed — the edge's fact sentence, whitespace-collapsed to one line |
| `class` | string | always | `feedback`\|`correction`\|`repetition`\|`waste`\|`prompting` |
| `state` | string | always | `watching`\|`shipped`\|`declined` |
| `sightings` | number | always | a repeat folds into the existing item and increments this, rather than adding a second one. Distinct from the edge's own `occurrences` (episode corroboration) — the two are deliberately not kept in sync |
| `first_seen` | number | always | unix millis |
| `last_seen` | number | always | unix millis |
| `evidence` | string | omitempty | JSON sample of the most recent occurrence, replaced wholesale on each sighting. Local-only — it never enters git |
| `suggested_fix` | string | omitempty | the agent's proposed correction |
| `pr_url` | string | omitempty | |
| `updated_at` | number | always | unix millis — the AGENT's last write; a verdict deliberately does not bump it |
| `verdict` | string | omitempty | carl-owned: `fine`\|`fix` |
| `comment` | string | omitempty | carl-owned |
| `commented_at` | string | omitempty | carl-owned, RFC3339, stamped server-side |

The overlay's columns have two owners, and the two writes below are split to
match — the store enforces it column by column (`UpdateWatchItem` never
writes carl's trio; `SetWatchVerdict` never writes anything else).

### `POST /api/watchlist`

**Carl's write** — the board's DREAM view judging an item. Web-UI/iOS-facing.
Nothing else: it cannot express an observation edit, and a request that even
MENTIONS an agent-owned key (`observation`, `class`, `state`, `sightings`,
`first_seen`, `last_seen`, `evidence`, `suggested_fix`, `pr_url`,
`updated_at`, `src`, `dst`, `predicate`, `strength`) is refused with a 400
rather than having it silently dropped.

| key | type | optionality | notes |
|---|---|---|---|
| `id` | number | required | the watched edge's id |
| `verdict` | string | required | `fine`\|`fix`, or `""` to CLEAR the judgement |
| `comment` | string | omitempty | trimmed |

`commented_at` is stamped server-side in RFC3339 and is never accepted from
the client. An empty `verdict` **and** an empty `comment` clears all three
columns (reopening the item) — the documented undo.

Response: `{"ok": true}`. Errors: 400 (missing/zero `id`, a verdict outside
the set, an agent-owned key), 404 (unknown item — no overlay row, **or** an
overlay whose edge has been invalidated), 413 (body over 1MB), 500. Gated
under `--mirror` with the standard plain-text 403.

Modeled in the iOS app (`WatchVerdictRequest` in Models.swift,
`setWatchVerdict` in APIClient.swift) — the DREAM screen's fine/fix control
and its comment box. The request DTO carries exactly `{id, verdict, comment}`
and always sends `comment`, even empty: `""` is the documented clear, not an
absence.

### `POST /api/watchlist/item`

**The agent's write** — add/update/remove of the observation itself, reached
only through the `kraken watchlist add|update|remove` cell verb; never called
by the iOS app or the web UI, so it is not modeled in
Models.swift/APIClient.swift (same convention as `/api/pr-attach`).

`verdict`/`comment`/`commented_at` are refused with a 400 here — at the
request root AND inside `item` — rather than dropped: the store preserves
them across every agent write, so honoring them is impossible, and an agent
that echoed a whole row back from the GET needs to be told.

| key | type | optionality | notes |
|---|---|---|---|
| `op` | string | required | `add`\|`update`\|`remove` |
| `item` | object | required for add/update | a `watchItemWire` (below) |
| `id` | number | required for remove | the watched edge's id |

`watchItemWire` — every key optional on the wire; the handler validates per op:

| key | type | notes |
|---|---|---|
| `id` | number | required on `update`, ignored on `add` |
| `observation` | string | `add` only, required, non-blank after whitespace collapse. **Refused on `update`** |
| `class` | string | required on `add`, one of the five classes above; optional on `update` |
| `src` / `dst` | string | `add` only, required when the observation is genuinely new. Entity NAMES — a name that resolves merges into the existing node, a new one creates a bare node. **Refused on `update`**, ignored on a fold |
| `predicate` | string | `add` only, optional, defaults to `concerns`. **Refused on `update`** |
| `strength` | number | `add` only, 1-10, required when the observation is genuinely new — an absent strength is an unstated confidence, not a default. **Refused on `update`**, ignored on a fold |
| `state` | string | one of the three states; blank defaults to `watching` on add, and leaves the current state alone on update |
| `sightings` | number | blank/zero on update leaves the current count alone |
| `first_seen` / `last_seen` | number | unix millis; blank/zero leaves the current values alone — the store's update is partial per column, so a restatement of one field never zeroes the rest |
| `evidence` | string | JSON sample, replaced wholesale |
| `suggested_fix` | string | |
| `pr_url` | string | |

**`add` is find-or-create, in three steps.** (1) If a WATCHED live edge already
states this observation — matched case-insensitively on the whitespace-collapsed
sentence — the add FOLDS: `sightings` increments, `evidence` is replaced, and
`class`/`state`/`suggested_fix`/`pr_url` are applied when sent. `src`/`dst`/
`predicate`/`strength` are ignored: the observation already has endpoints.
(2) Else if an UNWATCHED live edge states it — a fact some transcript or
feedback note already contributed — that edge is PROMOTED: the overlay attaches
to it rather than minting a second copy of the sentence. (3) Else the endpoints
are upserted, an episode is created (kind `watchlist`, marked done immediately —
extracting it later would only mint a near-duplicate of the edge just written),
the edge is inserted, and the overlay attaches.

**`update` writes the overlay only.** The fact sentence and its endpoints are
refused with a 400 rather than ignored: `kg_edges` is insert-only, so no verb
could honor a reword — remove and re-add to reword.

**`remove` UNWATCHES.** The overlay row goes; the edge and its episode survive
untouched, and the fact stays searchable. Unlearning the fact itself is
`POST /api/graph/add`'s `invalidate` list.

Responses:

| op | body |
|---|---|
| `add` | `{"ok": true, "id": number, "folded": bool}` — `id` is the edge id, whether created, promoted or folded into; `folded` tells the caller its observation was a repeat |
| `update` | `{"ok": true, "id": number}` |
| `remove` | `{"ok": true, "unwatched": true}` — spelled out because "remove" is narrower than it reads: the fact survives |

The id on `add`/`update` is why those aren't the standard `{"ok": true}`: a
caller that just watched something has no other way to learn its id. These
three shapes are CLI-facing only (the iOS app never calls this route), so the
added keys are not a contract bump.

Errors: 400 (unknown op, missing item, blank observation, bad class or state,
missing `src`/`dst` or an out-of-range `strength` on a create, an immutable key
on update, missing id on update/remove, a carl-owned key), 404 (unknown id on
update/remove), 413 (body over 1MB), 500. Gated under `--mirror`.

### `GET /api/dream`

The board's DREAM view: the night reports, newest first. **Additive, no
contract bump.** A report is the `00-dream-report.md` artifact of a job whose
slug starts `dream-`, resolved through the same cell ∪ store ∪ pluto artifact
plumbing every other artifact read uses — so a report survives its cell's
teardown with no second persistence path. Both the active and archived job
namespaces are swept.

A dream job that has not written its report yet (its cell is still running)
is simply absent, rather than present with an empty `body`.

Response: `{"reports": [webDreamReport]}` — always an array, never `null`.
Modeled in the iOS app (`WebDreamReport`/`DreamReportsResponse` in
Models.swift, `fetchDreamReports` in APIClient.swift) — the DREAM screen's
report list, which renders `body` through the app's own markdown renderer
(MarkdownText.swift) rather than a WKWebView.

| key | type | optionality | notes |
|---|---|---|---|
| `date` | string | always | `YYYY-MM-DD`, taken from the job's own slug (`dream-<date>`) |
| `slug` | string | always | the dream job's slug |
| `body` | string | always | the report's raw markdown, served inline — a report is one short page and the view renders every visible one, so a URL per report would be N round trips for nothing |

Errors: 405 (non-GET). A GET is a read, so it stays reachable under
`--mirror`.

### `GET /api/questions?slug=`

zeus-side read: every doc-question row for a job, re-read on every resume via
the `kraken questions get` cell verb. Not modeled in
Models.swift/APIClient.swift — the iOS app reads questions from the per-job
`doc_questions` array on `/api/board` instead, never this endpoint directly.

Response: `{"questions": [questionWire]}` — always an array, never `null`.
`questionWire` is the same shape as the board's `doc_questions` entries:

| key | type | optionality | notes |
|---|---|---|---|
| `id` | string | always | `Q<digits>` |
| `text` | string | always | |
| `context` | string | omitempty | |
| `importance` | string | always | `P0`\|`P1`\|`P2` |
| `recommendation` | string | omitempty | |
| `answer` | string | omitempty | kraken-owned — written only by `/api/doc-question` |
| `answered_at` | string | omitempty | RFC3339, kraken-owned |

### `POST /api/questions`

zeus-side write, reached through the `kraken questions set|add|update|remove`
cell verbs; never called by the iOS app. Three forms, distinguished by which
fields are present:

- **Bulk set** — `{slug, questions: [questionWire]}`: upserts each entry by
  id, deletes rows whose id is absent from the list, and PRESERVES the
  kraken-owned `answer`/`answered_at` on every surviving row. Position is
  assigned from each entry's position in the POSTED ARRAY (1-based), never
  from a `position` field in the request body — array order IS the order,
  matching `webDocQuestion`'s own wire contract (position never rides the
  wire on read, so there's nothing for a caller to echo back on write
  either). An empty or absent `questions` array is REFUSED (400) rather than
  treated as "delete everything" — clearing the set must go through explicit
  `remove` ops, so a malformed bulk write can never silently wipe answered
  questions.
- **Per-question add/update** — `{slug, op: "add"|"update", question:
  questionWire}`: `add` with no (or zero) position appends after the current
  last question, and is refused (409) if the id already exists — use
  `update` to edit an existing question instead; `update` never reassigns
  position (a text-only update leaves the question exactly where it was).
- **Remove** — `{slug, op: "remove", qid}`.

Validation is loud: a bad question id (not `Q<digits>`), a bad importance, or
a duplicate id within one bulk set is a 400 whose `error` names the problem —
the cell verbs surface it on stderr with a non-zero exit. An oversize request
body (either endpoint) is a 413, not a 400.

Response: `{"ok": true}` on success, `{"error": string}` otherwise.

### `GET /api/threads?slug=`

One job's side threads, the same array the board's per-job `threads` key
carries. **Additive, no contract bump.**

Response: `{"threads": [webThread]}` — always an array, never `null`; a job
with no threads yet serves `[]`. Ordered oldest-first (`t1`, `t2`, … `t10` —
creation order, which is numeric tid order since tids only count upward).

Errors: 400 `missing slug`, 404 for an unknown slug, 500 `no store wired`
(side threads live ONLY in `kraken.db` — a file-mode process says so rather
than serving an empty list that would read as "this job has no threads").
A GET is a read, so it stays reachable under `--mirror`.

### `POST /api/threads`

Every side-thread mutation, one body shape across all five ops
(`ThreadActionRequest` in Models.swift). **Additive, no contract bump.**

| key | type | optionality | notes |
|---|---|---|---|
| `slug` | string | required | the job the thread hangs off |
| `op` | string | required | `branch` \| `kill` \| `revive` \| `rename` \| `send` |
| `tid` | string | required for `kill`/`revive`/`rename`/`send` | |
| `title` | string | required for `branch`/`rename` | |
| `text` | string | required for `send`; optional for `branch` | |

- **`branch`** — `{slug, op:"branch", title, text?}`: forks zeus' session into
  a new claude in a fresh window of the same cell, in the same worktree, under
  a brand-new session id. `text` is the seed prompt and is OPTIONAL — branching
  with nothing to say yet is legitimate, and the thread comes up idle. This is
  the only op whose success body carries the new `tid`.
- **`kill`** — `{slug, op:"kill", tid}`: kills the thread's window and marks
  the row closed. The ROW SURVIVES: its session id still locates the
  transcript, so the conversation stays readable and revivable.
- **`revive`** — `{slug, op:"revive", tid}`: resumes the SAME claude session
  in a fresh window (no re-fork). Refused if the thread is already live, or
  if the job's cell is gone — reactivate the job first.
- **`rename`** — `{slug, op:"rename", tid, title}`: retitles the row; never
  touches the pane, so it works on closed threads too.
- **`send`** — `{slug, op:"send", tid, text}`: types a message into the live
  thread's pane, through the same verify-before-acting guard `k s` uses.

Response: `{"ok": true}`, plus `"tid"` on a `branch`. Errors: 400 for a
missing required field or an unrecognized `op` (the message names every valid
form), 404 for an unknown slug or an unknown `tid`, 409 for a refusal about
live state (no live cell to branch, a zeus that can't be forked, a pane that
no longer holds the thread, a tid already taken) — the `error` string is jobs'
own message verbatim, and it already names the recovery, so a 409 always means
"change something and retry". 413 for an oversize body, 500 `no store wired`
or a database failure — that one is the server's fault, not the request's, and
is retryable as-is. Every op but `rename` drives a real tmux window, and
`rename` rides along under the same "a mirror board is read-only, full stop"
policy, so the whole POST is refused with 403 `disabled in mirror mode` under
`--mirror`.

### `GET /api/thread/read?slug=&tid=&n=`

One thread's conversation, read from its OWN forked transcript. `n` is the
block count (default 50, clamped to 1…150; the web board omits it and takes
the default). Both bounds sit near what the transcript reader's first tail
window serves — a fork's transcript opens with zeus' whole history copied in,
so a larger `n` buys latency, not blocks, and a tail shorter than `n` is a
normal answer rather than an error. **Additive, no contract bump.**

Response:

| key | type | optionality | notes |
|---|---|---|---|
| `blocks` | array | always | the tail blocks, oldest-first; `[]`, never `null` |
| `counted` | number | always | non-tool blocks counted toward `n` (same meaning as `/api/peers/read`) |
| `live` | bool | always | the row is `live` AND still has a pane — i.e. there is somewhere to send to; gate the composer on this |
| `pane` | string | always | the live thread's pane (`""` when closed) — feed it to `/api/peers/capture` to mirror the window |
| `fork_uuid` | string | always | the transcript uuid the fork hangs off (`""` when none was recorded) |
| `source` | string | always | `transcript` when the fork's transcript was readable, `none` when there is nothing to read yet |

Each block is `/api/peers/read`'s shape — `kind` (string, always:
`user`\|`text`\|`thinking`\|`tool`\|`teammate`), `text` (string, always),
`from` (string, omitempty — teammate blocks only) — plus:

| key | type | optionality | notes |
|---|---|---|---|
| `inherited` | bool | omitempty | `true` on the pre-fork turns this thread inherited from zeus' session |

A fork's transcript opens with zeus' whole history copied in, and forks
preserve message uuids — so every block whose source record is at or before
`fork_uuid` is inherited history, and the first block WITHOUT `inherited` is
where the thread's own conversation begins. The anchor kraken records at
branch time is the last message a READER would see, never a bookkeeping
record (`system`, `attachment`, subagent/sidechain traffic, a content-less
turn), so whenever it falls inside the window read it names one of the blocks
in this very response. That is where clients draw the
"⑂ branched here" divider, dimming everything above it. When `fork_uuid` is
empty or isn't in the window read (an anchor older than `n` blocks, or one
never recorded), NOTHING is marked inherited and no divider is drawn — the
graceful path both clients already handle.

A missing session id, a missing transcript, or an unreadable one is NOT an
error: it answers 200 with `source: "none"`, `blocks: []` and `counted: 0`
(the repo rule for files another program owns — a freshly branched thread
that hasn't taken a turn has nothing to read). Real errors are 400 for a
missing `slug`/`tid`, 404 for an unknown slug or `tid`, 500 `no store wired`.
A read stays reachable under `--mirror`.

### `POST /api/steer`

Request: `webReq` — `slug` (string), `text` (string: free-text message, required non-empty).
Response: same as above.

`text` is delivered to zeus' pane verbatim — no `"from carl: "` prefix — so
a message starting with `/` invokes a real slash command in zeus' Claude
session (e.g. `/do`) rather than being typed as plain conversation text.

### `POST /api/reactivate`

Request: `webReq` — `slug` (string), `text` (string, omitempty: optional steering message folded into the resume prompt).
Response: same as above.

### `POST /api/start` — REMOVED in v6

The route is unregistered and answers 404. It was the manual start for a job
the daemon's queue pass would not take — one parked behind a gated prereq, or
held off by the cell cap. None of those exist: `POST /api/queue` starts a job
by hand, synchronously, and it is the only way any job starts.

### `POST /api/stop`

Request: `webReq` — `slug` (string, required), `text` unused.
Response: same as above.

Interrupts a working cell: sends one Escape keystroke to every in-flight
agent pane in the cell (zeus, any nested subagent sharing its pane, any live
tmux-backed team member) — stops short of a teardown. No job state is
written; the daemon's own roll-call reads the idled cell as `blocked` on its
next tick, same as an agent that stalled on its own.

### `POST /api/reorder`

Persists a manual drag-reorder within one board column. Distinct request
shape from `webReq` — this is a whole-column move, not a single-job action.

Request (`reorderReq`):

| key | type | optionality | notes |
|---|---|---|---|
| `column` | string | always | one of `draft`\|`working`\|`blocked`\|`done`. `queued` was removed in v6 and now 409s as an unrecognized column |
| `slugs` | array of string | always (`[]` ok) | the column's full intended slug order |

Response: `{"ok": true}` on success; non-2xx `{"error": string}` on failure
(409 for an unrecognized `column`). Posted slugs whose current
`DisplayState` no longer matches `column` — a concurrent transition
mid-drag, e.g. a draft submitted from another client — are silently
dropped from the saved order rather than failing the whole request.

### `GET /api/draft?slug=` / `POST /api/draft` → `webDraft` (draft.go)

Per-job prompt-composer draft text, kept in an in-memory store on the `k web`
process — additive (never bumps the contract), nothing here is written to disk, so
drafts are lost across a `k web` restart. Last-save-event-wins: no merge,
no history, a POST simply overwrites whatever was there. The stored draft is
cleared automatically on the success path of `/api/steer`, `/api/reactivate`,
`/api/done`, `/api/archive`, and `/api/delete` (NOT `/api/kill` or
`/api/question`), and lazily on any `GET /api/draft` for a slug that no
longer matches an active job.

GET request: `slug` query param (required; missing/empty → 400).
GET response:

| key | type | optionality |
|---|---|---|
| `slug` | string | always |
| `text` | string | always (`""` if no draft stored) |
| `updated_at` | number | always (unix millis; `0` if no draft stored) |

POST request: `webReq` — `slug` (string, required — empty → 400), `text`
(string: the draft text; empty clears the stored entry).
POST response: `{"ok": true}` on success; non-2xx `{"error": string}` on
failure (400 for bad JSON or missing slug; 405 for methods other than
GET/POST).

---

## Knowledge graph

Ten routes over the `kg_*` tables (internal/api/graph.go, plus
internal/api/recall.go), in the order register.go wires them: `graph`,
`graph/stats`, `graph/node`, `graph/fact`, `graph/episode`, `graph/pending`,
`graph/search`, `graph/recall`, `graph/add`, `graph/episode-done`.
**All additive — none of them bumps `apiContract`.** Every one is a NEW PATH, never a
key added to, retyped in, or removed from an existing shape, so a client that
predates them decodes nothing differently.

**`GET /api/graph` and `GET /api/graph/node` ARE modeled in Models.swift and
APIClient.swift** (the first and third routes above). The iOS graph screen
(`ios/Kraken/GraphView.swift`) is a native SwiftUI `Canvas` that consumes both
directly, via `GraphPayload`/`GraphNodeDetail` (`APIClient.fetchGraph` and
`fetchGraphNode`). It previously wrapped the web board's own `#/graph` page
in a `WKWebView`, on the argument that one server-computed layout should mean
one implementation; the layout half of that still holds — positions come from
`core.ComputeLayout` and both clients draw the same shape, there is no
client-side force simulation on either — but WebGL inside a sheet fights the
sheet's gestures, sigma's labels are sized for a desktop, and the
rail/canvas/rail layout does not fit a phone. The remaining five routes
(`stats`, `pending`, `search`, `add`, `episode-done`) stay web- and
`k graph`-only. The two dereference reads — `graph/fact` and `graph/episode` —
are narrower still: they are `k graph get`-only, with no web UI caller either,
since their reader is an agent following an injected
`[F<edge id> E<episode id>]` handle rather than a person clicking a canvas.
Neither is modeled in Models.swift or APIClient.swift.

The `stats` object's keys are snake_case (`live_edges`,
`missing_embeddings`) like the rest of this API. (They were briefly camelCase
during the parallel build; renamed before anything shipped, so no client ever
saw the old spelling.)

**Mirror mode**: the six GET reads plus `POST /api/graph/search` stay reachable
under `--mirror` (search is a POST only because a query is arbitrary user
text, not because it writes). `GET /api/graph/fact` and
`GET /api/graph/episode` are ungated for the same reason as the other reads,
and pointedly so: following a handle touches `kg_recall` not at all —
dereferencing a fact the agent was already handed is not a delivery, so there
is nothing for a mirror to refuse. `POST /api/graph/recall`,
`POST /api/graph/add` and `POST /api/graph/episode-done` are gated with the
usual 403 `disabled in mirror mode` — recall reads like a search but records
what it delivered.

### `GET /api/graph`

The viewer payload in one response: every node with its cached position,
every edge — **live and invalidated both**, so the viewer strikes retired
facts through rather than hiding history — and the stats block.

Response: `{"nodes": [webGraphNode], "edges": [webGraphEdge], "stats": webGraphStats}`.
`nodes` and `edges` are always arrays, never `null`.

`webGraphNode`:

| key | type | optionality | notes |
|---|---|---|---|
| `id` | number | always | |
| `name` | string | always | the canonical name |
| `type` | string | always | the extractor's entity type |
| `description` | string | always | the short gloss of what the concept IS, independent of any edge |
| `x` | number | always | server-computed layout position; **`0` when layout has not run for this node yet**, which the client places itself |
| `y` | number | always | as `x` |
| `aliases` | array of string | omitempty | populated here via one bulk join (the nightly graph ingest reads this route as its canonicalization inventory, and an alias-blind inventory re-derived the same merges night after night), and by `GET /api/graph/node`. A node with no aliases omits the key rather than sending an empty array |

`webGraphEdge`:

| key | type | optionality | notes |
|---|---|---|---|
| `id` | number | always | |
| `src` | number | always | source node id |
| `dst` | number | always | destination node id |
| `predicate` | string | always | |
| `fact` | string | always | the natural-language sentence — the edge's short description IS its name |
| `strength` | number | always | the extractor's 1-10 confidence |
| `occurrences` | number | always | distinct episodes independently stating this fact |
| `valid_until` | number | always | **world** time, unix millis; `0` = still true |
| `expired_at` | number | always | **system** time, unix millis; `0` = live |
| `episode` | number | always | the episode this fact was extracted from |

`webGraphStats`:

| key | type | optionality | notes |
|---|---|---|---|
| `nodes` | number | always | |
| `edges` | number | always | every edge, live or not |
| `live_edges` | number | always | `expired_at = 0` only |
| `episodes` | number | always | |
| `pending` | number | always | episodes still awaiting extraction |
| `missing_embeddings` | number | always | nodes + live edges with no vector yet; sits non-zero forever if no embedder is configured, which is a normal state, not an error |

Errors: 405 (non-GET), 500 (no store wired, or a read failed).

### `GET /api/graph/stats`

The same stats block on its own: `{"stats": webGraphStats}`, built by the
same function `GET /api/graph` uses so the two cannot drift. Its own route
because the one caller that wants stats alone is `k graph stats` inside a
cell, and making it pull every node and edge to print six integers would be
the most expensive read here for the cheapest answer.

Errors: 405 (non-GET), 500.

### `GET /api/graph/node?id=N`

The inspector's read: one node, its aliases, and **every** edge touching it,
live and invalidated both.

Response: `{"node": webGraphNode, "edges": [webGraphEdge]}`. The node's
`aliases` key is present here whenever the node has any. `edges` is always an
array, never `null`.

Errors: 400 (missing or non-positive-integer `id`), 404 (no node with that
id — a named node that does not exist is an answer, not an empty result),
405, 500.

### `GET /api/graph/fact?id=N` — `k graph get`-only (not called by the iOS app or web UI; not modeled in Models.swift/APIClient.swift)

The fact dereference: one edge in **full**, its two endpoint entities, the
episode it came from, and what else those entities are known for. Reached from
a cell as `k graph get F<edge id>` (or a bare edge id).

Its reason to exist is the injected memory block. A fact rendered into an
agent's context arrives clause-cut to 180 runes and carries a handle —
`[F<edge id> E<episode id>, seen Nx]` — precisely so the summary can be short;
this route is what turns that handle back into the whole thing.

Response:
`{"fact": webGraphFactFull, "src": webGraphNode, "dst": webGraphNode, "episode": webGraphEpisodeRef|null, "siblings": {"src": [webGraphSiblingFact], "dst": [webGraphSiblingFact]}}`.

An **invalidated** edge still dereferences — a handle cited in a brief written
last month may name a fact kraken has since unlearned, and a 404 would leave
the reader unable to tell "kraken never said this" from "kraken stopped
believing it". `expired_at` on the bundle says which.

`webGraphFactFull`:

| key | type | optionality | notes |
|---|---|---|---|
| `id` | number | always | the edge id — the `F<id>` half of the handle |
| `src` | string | always | endpoint **name**, falling back to `"#<id>"` when the node row is missing. The fact IS the answer here; a missing gloss never drops or 500s it |
| `dst` | string | always | as `src` |
| `predicate` | string | always | |
| `fact` | string | always | the stored sentence **byte-exact** — untruncated, never clause-cut. Getting past the injected block's 180-rune cut is the whole point of following a handle |
| `strength` | number | always | the extractor's 1-10 confidence |
| `occurrences` | number | always | distinct episodes independently stating this fact |
| `valid_from` | number | always | **world** time, unix millis |
| `valid_until` | number | always | **world** time; `0` = still true |
| `created_at` | number | always | **system** time — when kraken learned it |
| `expired_at` | number | always | **system** time; `0` = live |
| `episode` | number | always | the episode id this fact was extracted from — the `E<id>` half of the handle |

Both time axes ride in full here, unlike `webGraphFact` on `/api/graph/search`,
which trims them: a ranked list is scanned, a dereferenced fact is studied.
There is no `weak` key — that is a relevance verdict, and there is no query on
this route to be relevant to; publishing `false` would assert one that was
never computed.

`src` and `dst` are `webGraphNode`, the same type `GET /api/graph` serves, with
`aliases` populated via the node's alias rows and `x`/`y` always `0` — the same
"a fact bundle is a list to read, not a canvas to draw" reasoning
`/api/graph/search` states. A missing node row degrades to
`{"id": <id>, "name": "#<id>"}` (every other key at its zero value) rather than
a 404: the caller named a **fact**, and the fact exists.

`episode` is a provenance **pointer** — `{"id", "kind", "ref"}` and nothing
else. `content` is deliberately absent: an episode is a reduced transcript span
measured in kilobytes, and inlining it into every fact dereference would drown
the one sentence the caller asked for in the material it came from. Pull the
text by asking for it, on `/api/graph/episode`. An edge whose episode row is
absent serves an explicit `null` rather than an empty object.

`siblings` are the **live** edges touching each endpoint (via the same
`occurrences DESC, id` neighbour read the search expansion uses), **excluding
the dereferenced edge itself** — it is already the response's `fact`, and
repeating it would spend a slot saying nothing. Capped at **10 per endpoint**,
so a hub endpoint cannot turn a targeted dereference into a dump. Both lists
are always arrays, never `null`. A **self-loop** fact (`src == dst`) has one
endpoint and therefore one sibling list, served under both keys.

`webGraphSiblingFact` is `webGraphFact` minus `weak` (no query, so no verdict)
— and, like `webGraphFact`, without the system-time stamps, since siblings come
from `NeighborEdges` and are live-only: `id`, `src`, `dst`, `predicate`,
`fact`, `strength`, `occurrences`, `valid_until`. Endpoints are names with the
same `"#<id>"` fallback.

No `kg_recall` interaction: dereferencing a fact the agent was already handed
is not a delivery, so it must not spend that fact's once-per-session budget.

Errors: 400 (missing or non-positive-integer `id`), 404 (no fact with that id),
405, 500 (no store wired, or a read failed). Additive route — it never bumps
`apiContract`.

### `GET /api/graph/episode?id=N` — `k graph get`-only (not called by the iOS app or web UI; not modeled in Models.swift/APIClient.swift)

The episode dereference: one provenance unit **with its text**, and every fact
extracted from it. Reached from a cell as `k graph get E<episode id>` — the
other half of the injected handle.

Response: `{"episode": webGraphEpisode, "edges": [webGraphFactFull]}`.

`content` **does** inline here, unlike in the fact bundle's episode pointer:
pulling the provenance text is this route's entire purpose, and the caller
asked for it by id rather than receiving it as a side effect. The `episode`
object is the same `webGraphEpisode` `GET /api/graph/pending` serves (`id`,
`kind`, `ref`, `content`, `ts`; `hash` and `state` deliberately absent).

`edges` is every fact from that episode, **live and invalidated both**, in
ascending id order — the order the extractor stated them in, which is the
closest thing to the source text's own order. The question this route answers
is what the episode *produced*, not what survives of it, so `expired_at` is not
filtered; each element carries its own stamp. Endpoint names are resolved in
one bulk node read with the same `"#<id>"` fallback. Always an array, never
`null` — an episode that yielded no facts is a span that HAS been read and
produced nothing, which is an ordinary answer.

No `kg_recall` interaction, for the same reason as the fact route.

Errors: 400 (missing or non-positive-integer `id`), 404 (no episode with that
id), 405, 500. Additive route — it never bumps `apiContract`.

### `POST /api/graph/search`

Retrieval: embeds the query, fuses vector, BM25 and name-anchor candidates,
expands one hop, and returns both the structured facts and the triple text an
agent pastes into its context. The name-anchor leg is the query's own terms
matched against node **names and aliases** rather than fact sentences: a query
that names a low-degree entity ("who is lewis") retrieves that entity's facts
even when no sentence shares a word with it.

A missing or failing embedder degrades **silently** to BM25-only — a fresh
install with no `OPENAI_API_KEY` gets keyword search, never a 500.

Request: `{"query": string, "limit": number}`. `query` is required (blank →
400). `limit` is optional; absent or `0` means the default 20, and anything
over 200 is capped rather than refused. `DisallowUnknownFields`: any other
key is a 400.

Response: `{"nodes": [webGraphNode], "facts": [webGraphFact], "triples": string, "strong": number}`.
`nodes` and `facts` are always arrays, never `null`; `triples` is the same
facts serialized for an LLM context block — facts that clear the relevance
gate first, then (only when at least one fact fails it) a literal
`weakly related:` line and the rest. `strong` counts the facts that cleared
the gate — including the ones exempted from it for touching a node the query
named (see `weak` below); `0` with a non-empty `facts` means the graph had nothing on-topic
and the entries are nearest-neighbour context, not answers. Additive keys on
a route no iOS client decodes — they never bump `apiContract`.

`nodes` is the set of entities the matched facts touch, sorted by id (so the
response is deterministic for a fixed corpus). It is here because
`.claude/skills/graph/SKILL.md`'s canonicalization step resolves an incoming
name against what already exists via `k graph search "<name>"` — a judgement
about *entities*, which facts answer only indirectly. Elements are
`webGraphNode`, the same type `GET /api/graph` serves, except that `x`/`y`
are always `0` here: a search result is a list to read, not a canvas to draw,
so the layout table is not joined.

`webGraphFact`:

| key | type | optionality | notes |
|---|---|---|---|
| `id` | number | always | the edge id |
| `src` | string | always | endpoint **name**, not id — a search's reader is a human or an agent, neither of which can resolve an integer. Falls back to `"#<id>"` if the node is missing from the result set, so a fact is never dropped over a missing gloss |
| `dst` | string | always | as `src` |
| `predicate` | string | always | |
| `fact` | string | always | |
| `strength` | number | always | |
| `occurrences` | number | always | |
| `valid_until` | number | always | world time; `0` = still true |
| `weak` | bool | always | `true` when the fact failed the relevance gate. Three ways to pass: cosine ≥ 0.72 against the query; ≥2 shared content words when no usable vector exists (no query embedding, no stored embedding, or a dimension mismatch); or — **regardless of cosine or overlap** — either endpoint being a node the query NAMED, i.e. a low-degree node whose name or alias is fully covered by the query's content terms. A fact touching such a node is never `weak`, because the gate compares fact *sentences* and cannot see that the caller spelled the entity out. Marked, never dropped, so the caller decides what nearest-neighbour context is worth |

There is deliberately **no `score` key**: array order already expresses the
ranking, and publishing a fusion score would invite clients to threshold on a
number whose scale is an implementation detail — the same argument
`webJobLoad` makes for keeping the derived `roi` off the wire.

Errors: 400 (bad JSON, unknown field, blank query), 405, 413 (body over 1MB),
500.

### `POST /api/graph/add`

The extraction agent's write — one turn's whole output in one request, since
an edge names endpoints the same body creates and an invalidation is only
meaningful beside the edge that replaced it.

Request:

```json
{
  "episode_id": 12,
  "nodes":      [{"name": "...", "type": "...", "description": "...", "aliases": ["..."]}],
  "edges":      [{"src": "...", "dst": "...", "predicate": "...", "fact": "...", "strength": 7, "valid_from": 0, "valid_until": 0}],
  "invalidate": [{"edge_id": 44, "valid_until": 1769040000000}]
}
```

All three lists are optional individually; a body with none of them is a 400
(`nothing to add`). Edge endpoints are named by **name**, never by id: a name
that resolves to nothing creates a bare node, because a fact is worth keeping
and an entity mentioned only as an endpoint is still an entity.

Validation is total and runs **before any write** — there is no transaction
spanning the three lists, so anything refusable is refused first. Refusals
(all 400, each naming the offending index): blank node name, blank alias,
blank `src`/`dst`/`predicate`/`fact`, `strength` outside 1..10 **including an
absent one** (an omitted strength is not a default, it is an unstated
confidence), `edge_id` ≤ 0, and `valid_until` ≤ 0 on an **invalidation** (`0`
means "still true" on that column, which is the opposite of invalidating).
`DisallowUnknownFields` applies.

An **edge** may carry `valid_until` too, and it means something different from
an invalidation's: an episode can state a fact already known to have ended
("we used X until March"), which is one edge with both world-time bounds — not
a live edge plus an invalidation of something that was never live. Such a row
keeps `expired_at = 0`: kraken still believes the claim, the world simply
moved on. Only `invalidate` stamps system time, because only it records
kraken **unlearning** something it had asserted.

Response: `{"nodes": [{"name", "id", "added"}], "edges": [{"src", "dst", "id", "added"}], "invalidated": [number]}`.
`added` is `false` for a merge into an existing row — the only way a caller
can tell its "new" entity was already known under an alias, or that its fact
was a repeat sighting rather than a new edge.

A store failure partway through returns **500 with the same three result
lists filled in as far as they got**, plus `error`. That is deliberate: the
writes that landed cannot be rolled back, so the caller is told what to skip
on a retry. Every write on this path is idempotent under a retry anyway
(`UpsertNode` merges, `AddAlias` is `ON CONFLICT DO NOTHING`, `InsertEdge`
counts a repeat as a sighting).

Errors: 400, 405, 413, 500. Gated under `--mirror`.

### `GET /api/graph/pending?limit=`

The extraction queue read, oldest first so the backlog drains in ingest
order. `limit` is optional (default 20, capped at 200); a non-positive or
unparseable value is a 400.

Response: `{"episodes": [webGraphEpisode]}` — always an array, never `null`.

| key | type | optionality | notes |
|---|---|---|---|
| `id` | number | always | |
| `kind` | string | always | `transcript` \| `feedback` \| `watchlist` \| `artifact` \| `job` |
| `ref` | string | always | source pointer: `path#start-end` for files, row id otherwise |
| `content` | string | always | the reduced text handed to the extractor — inline, since it IS this route's payload; a URL per episode would be N round trips for nothing |
| `ts` | number | always | unix millis — when the SOURCE material was written, not when it was ingested |

The row's `hash` and `state` are deliberately absent: hash is the store's
internal dedupe key, and every row this route can serve is `pending`.

Errors: 400 (bad `limit`), 405, 500.

### `POST /api/graph/episode-done`

Flips one episode out of the pending queue. Request `{"id": number}`,
response `{"ok": true}`. Reached from a cell as `k graph done <episode id>`.

Its own route rather than a field of `/api/graph/add` because an episode can
legitimately yield **nothing** — a span with no facts in it has still been
read, and folding "done" into the write would leave it pending forever,
re-extracted on every pass.

An unknown id is a **404, not a silent 200**: the underlying `UPDATE` matches
nothing and reports no error, so without the lookup a typo'd id would be
acknowledged as done while the real episode sat pending forever.

Errors: 400 (bad JSON, unknown field, missing/non-positive id), 404, 405,
413, 500. Gated under `--mirror`.

### `POST /api/graph/recall` — localhost-only (not called by the iOS app or web UI; not modeled in Models.swift/APIClient.swift)

Mid-conversation memory (api/recall.go, jobs/recall.go). Its only caller is
`kraken recall-hook`, wired by every cell worktree as two Claude Code hooks:
`UserPromptSubmit` (query = the user's new message) and `PostToolUse`
(matcher-less — every tool call — query = the transcript's last assistant
text). The hook POSTs its payload here and emits the response as
`hookSpecificOutput.additionalContext`.

`Stop` is deliberately not wired: Claude Code's hooks reference is explicit
that `additionalContext` at Stop **continues the turn** rather than
annotating it, so injecting there would resume every agent past its own
stop-gates and chain paid calls until retrieval ran dry. `PostToolUse` has no
such problem — its context lands in a turn already continuing. The server
side is event-agnostic — nothing here reads `event` — so wiring changes live
in `jobs.recallHookEvents` alone.

Two server-side properties make the per-tool-call cadence affordable and
correct: firings for one `session_id` SERIALIZE on an in-process per-session
mutex (parallel tool calls fire parallel hooks; unserialized they could read
one skip-set and double-inject), and a firing whose query is byte-identical
to the session's last completed one — every post-tool-call firing after the
first within one assistant message — short-circuits to `""` before any
retrieval or embedding. The hook itself also skips any firing whose payload
carries an `agent_id`: settings hooks run inside in-process subagents too,
under the PARENT's `session_id`, and injecting there would mark facts
delivered into a context that is discarded when the subagent finishes.

Request:

| key | type | optionality | notes |
|---|---|---|---|
| `session_id` | string | required in practice | the dedup key; blank/whitespace always yields an empty context, since without one the same facts would re-inject on every firing |
| `event` | string | optional | `hook_event_name` verbatim — carried for debugging, never branched on |
| `prompt` | string | optional | the user's new message — `UserPromptSubmit`'s query |
| `transcript_path` | string | optional | fallback query source: the transcript's last assistant text, read when `prompt` is empty — `PostToolUse`'s query, since its payload carries no prompt |

Response: `{"context": string}` — the rendered block, or `""`.

**`""` is a normal 200, never an error status.** The caller is a hook running
inside somebody else's conversation, and "the graph has nothing to add" is the
common case. It is what comes back for: no query text, nothing retrieved,
nothing that clears `core.RelevantEdge`, everything relevant already delivered
to this session, or any store failure on the way — a hook that breaks a turn
is a bug by definition.

A non-empty context is recorded in `kg_recall` (one row per `(session_id,
edge_id)`) BEFORE it is returned, so **a given fact reaches a given session at
most once**; a failure to record withdraws the block rather than sending an
undeduplicatable one. What is recorded is exactly the facts the block CARRIES,
not every fact that cleared relevance — one skipped by the byte cap stays
eligible for a later firing. A request whose caller has already gone (the hook
gives up at 4s) records nothing at all, for the same reason: a fact nobody read
is not a fact delivered. The block is byte-identical in format to the one
`graphContext` appends to a fresh cell's brief — same header, same
`graphContextFacts`/`graphContextBytes` caps.

The query text is redacted (`core.RedactSecrets`) before it is sent to the
embedding API — this is the only path in the binary that ships a raw user
prompt off the machine. The BM25 leg searches the unredacted query, which
never leaves kraken.db.

Additive: it never bumps `apiContract`. Gated under `--mirror` (it writes).

Errors: 400 (bad JSON, unknown field), 405, 413, 500 (no store wired).

---

## Job editor

Note: `POST /api/job/save` is web-UI-only (used by `web/app.js`'s raw
editor), not called by the iOS app and not modeled in Models.swift/
APIClient.swift, but included here since it shares the raw-save shape with
`/api/job/raw`.

### `GET /api/job/raw?slug=` → map literal (web.go `webJobRaw`)

| key | type | optionality |
|---|---|---|
| `slug` | string | always |
| `content` | string | always |
| `mtime` | number | always |
| `editable` | bool | always |

Errors: 404 (no such job), 500 (ReadFile failure).

### `POST /api/job/create` → same shape as `/api/job/raw`

Request: `{"slug": string}`. Errors: 405 (non-POST), 400 (bad slug), 409 (name taken), 500 (WriteFile failure).

A 409 covers two takings, not one: an active job already holds the slug, or an
active job RESERVED it by being renamed away from it (`POST /api/job/rename`
keeps a job's former names off limits for as long as that job is active, so a
new job cannot start answering to a name a live cell is still calling by).
The second is invisible to a client — former names are not on the wire — so it
is reported as a refusal to be read, never as a 500.

### `POST /api/job/save` (web-UI-only)

Request: `{"slug": string, "content": string, "mtime": number|string}` (mtime via `mtimeToken`, 0/""/null skips the optimistic-lock check).
Response: `{"ok": true, "mtime": number, "warn": string}` (warn is `""` when the saved content parses cleanly — always present, it's a map literal).
Errors: 404 (no such job), 409 (not editable, mtime mismatch, or a changed `job:` line — see below), 500.

**The `job:` line is read-only here.** A save whose frontmatter names a slug
other than the job's own is refused with a 409 whose message points at
`POST /api/job/rename`; an unchanged or empty `job:` line saves exactly as
before. A slug is identity — the cell dir, the tmux session, the branch family,
the pluto artifact prefix and every peer job's prereq reference derive from it —
so moving it through a field map moved the column and nothing else. It is
refused rather than silently ignored: a save that keeps the old name while the
editor shows the new one is the same divergence in the other direction.

### `POST /api/job/rename` → map literal (api/jobrename.go `webJobRename`)

Renames one active job. Its own route for the two reasons `/api/job/color` and
`/api/job/load` have one — no `editableJob` gate, so a LIVE cell can be renamed,
and no full-replace body to wipe a field a client predates — plus one of its
own: a slug is identity rather than a field, so a rename must never ride in as a
side effect of an unrelated edit.

Request: `{"slug": string, "to": string}`. `slug` names the job by its
**current** name; `to` is the new one, stored slugified (`util.Slugify`), so the
response's `slug` is what to re-point to rather than the string sent. Both keys
are required and an empty either way is a 400. The body is decoded with
**unknown fields rejected** — `slug`, `to`, and nothing else.

Response: `{"ok": true, "slug": string, "skipped"?: [string], "notes"?: [string]}`.
Both lists are omitted when empty, and both describe a rename that SUCCEEDED —
neither is an error, and neither should be rendered as one. They are two keys
because they ask two different things of the reader.

`skipped` names preserved pluto artifact copies that kept the old prefix because
a file already occupied the new name. Every entry is the **source** path — the
file still sitting under the old name, the one to move or delete by hand — never
the blocker at the destination.

`notes` carries best-effort steps that FAILED, each with its cause. Three kinds
reach it, and only the first is about files: a preserved copy whose rename
errored outright (permissions, a name over the filesystem's limit, a full disk)
— not a conflict, nothing sits at its destination, so it is deliberately not in
`skipped`, and it is still stranded under the old prefix where the card cannot
show it; a `tmux` convergence step on a LIVE cell that did not land, leaving the
session under its old name until the daemon reconverges it; and the one-line
notice typed at zeus failing to deliver. Kraken retries the tmux ones on its own
— except a convergence refused because the session carries another job's marker,
which the retry will refuse the same way forever and needs a look — and the
notice costs only a missing pane message, so a note is worth showing but never
worth blocking on. Every note is also written to the event log, which is
the durable record — the response is simply the only surface carl is looking at
when he renames.

Errors: 400 (bad json; an unrecognized field; an empty `slug` or `to`), 404 (no
such job — resolved by CURRENT slug only, since renaming *by* a stale name means
the caller does not know the job's name), 409 (every refusal: a `to` that
slugifies to nothing, either name under the reserved `dream-`/`graph-` prefixes,
an archived job, a target taken by another active job or by an archived one, or
a target that is another active job's former name — the refusal text rides back
verbatim and is meant to be rendered inline), 413 (body over 1 MiB). Gated under
`--mirror` with the standard plain-text 403: this route renames a real tmux
session and moves real files under `~/Documents/pluto`. **Additive, no contract
bump** — no key on any existing shape changes, and `webJob` gains nothing (a
job's former names are deliberately not on the wire).

**Old names keep resolving.** The slug-taking verb routes — `/api/questions`,
`/api/feedback`, `/api/job/load`, the cell-side hooks `/api/pr-attach` and
`/api/limit-hit`, and `/api/job/saveform` — fall back to former-slug resolution
when a slug names no active job, so a cell that spawned before the rename keeps
working with the name it was given. A current slug always wins over a former
one: a new job that takes the old name is the job that answers for it.

The two hooks are the reason the fallback exists at all: their slug is baked
into the cell's `.claude/settings.json` when it spawns and is never rewritten
mid-run, so without the fallback a renamed job would lose PR auto-attach and
the usage-limit rescue's fast trigger for the rest of that cell's life.
`/api/job/saveform` is on the list for the editor's autosave: a save already in
flight when the rename lands still carries the old slug, and it must reach the
job it was typed into rather than 404 the edits away.

### `GET /api/job/form?slug=` → map literal (web.go `webJobForm`)

`skip_doc` is gone (v5, breaking): whether the doc phase runs is now purely
zeus' own read of the job spec text (does it invoke `/aim`/`/info`, or ask
for a doc/spec/explanation in words) — never a stored flag on the job.

`ready` and `prereqs` are gone (v6, breaking) — from this GET and from the
`saveform` POST alike. Submitting a job is what starts its cell, so there is
no flag to set; prereqs went with the feature (a job waits for nothing but
carl). Both keys are still ACCEPTED in a job's raw frontmatter text
(`/api/job/raw`, `/api/job/save`) and silently discarded, because every job
file written before the cutover carries them — but `POST /api/job/draft`
refuses either LOUDLY, since an agent writing one would otherwise be told
"200" for a line the server threw away.

| key | type | optionality | notes |
|---|---|---|---|
| `slug` | string | always | |
| `repo` | string | always | |
| `base` | string | always | |
| `solo` | bool | always | |
| `model` | string | always | claude model for zeus (`--model` value), `""` = kraken's default for zeus (`claude-fable-5`); applied once at cell start. Governs the cell's own claudes — zeus and its side threads; the teammates zeus spawns are opus regardless |
| `color` | string | always | the job's color — one of `red` \| `blue` \| `green` \| `yellow` \| `purple` \| `orange` \| `pink` \| `cyan`, or `""` for no color. Read-only here: it is written by `POST /api/job/color`, never by `saveform` |
| `spec` | string | always | |
| `mtime` | number | always | |
| `editable` | bool | always | |
| `err` | string | always | "" when clean |
| `slugs` | array of string \| null | always present key, value may be `null` | the other active jobs' slugs. It fed the prereq datalist originally and survives that feature's removal as a plain autocompletion source |
| `repos` | array of string \| null | always present key, value may be `null` | |

### `POST /api/job/saveform` → map literal (web.go `webJobSaveForm`)

Request: `jobFormFields` — `slug` (string), `repo` (string), `base` (string, optional to send — no `omitempty` tag, absence just decodes as ""), `solo` (bool), `model` (string, optional to send — claude model for zeus, "" = kraken's default for zeus, `claude-fable-5`), `spec` (string, optional to send), `mtime` (number|string via `mtimeToken`), `force` (bool, optional to send — skips the mtime conflict check when true). `skip_doc` is gone (v5, breaking) and `ready`/`prereqs` are gone (v6, breaking) — see the form-GET note above.

**`color` is NOT part of this request and must never be added to it.** This
endpoint is a FULL REPLACE: a field a client omits is written as its zero
value, so any build that predates a field silently wipes it. The job color
therefore has its own endpoint, `POST /api/job/color`, which is also
free of the not-editable 409 this one raises while a cell is in flight —
recoloring a running job is exactly when the picker is useful.

Response (success): `{"ok": true, "mtime": number, "warn": string}`.
Response (409 conflict, unforced mtime mismatch): `{"error": string, "conflict": true}` — the `conflict` key distinguishes a stale-mtime 409 from other 409s (e.g. "cell in flight") that don't carry it.
Errors: 404 (no job resolves under that slug — current OR former, see "Old names keep resolving" above), 409 (not editable, or mtime conflict), 500.

### `POST /api/job/color` → map literal (api/jobcolor.go `webJobColor`)

Sets one job's color. Its own one-field route rather than a `saveform` field
for two reasons, both downstream of color being purely cosmetic: it is
deliberately usable while a cell is LIVE (there is no `editableJob` gate, so
no "cell in flight" 409 — recoloring a running job is exactly when a picker
is useful), and it is not a full replace, so a client that never sends it
cannot clear it.

Request: `{"slug": string, "color": string}` — `color` is one of `red` |
`blue` | `green` | `yellow` | `purple` | `orange` | `pink` | `cyan`, or `""`
to clear. Anything else is a 400: the value becomes the argument of a
`/color` typed into a live claude, so the allowlist is enforced server-side.

Response: `{"ok": true}`. The new color rides back on the next `/api/board`
poll (`webJob.color`) — the board stays the single source of truth for job
state, so there is nothing else to echo here.
Errors: 400 (bad json, or a color not on the palette), 404 (no such job), 500.

Side effect: when the job has a live cell in `working`/`blocked`, kraken
types `/color <name>` (or `/color default` for the cleared case) into that
cell's zeus pane, so the claude's prompt bar matches the board. Delivery is
watermarked per cell and never reasserted, so a `/color` typed by hand in
the pane survives; it targets zeus only, never side threads or teammate
panes (Claude Code refuses `/color` inside a teammate session outright).

### `POST /api/job/load`

Writes one job's triage estimate — the pair the board's ROI badge renders.
Its own route rather than a `saveform` field for the same two reasons
`/api/job/color` has one, and one more: it is written on a LIVE cell (zeus
writes it at the end of every turn, and a blocked job is exactly when it
matters), `saveform` is a full replace that a client predating the field would
silently clear, and its writer is zeus via a cell verb rather than a human at
a form.

Request: `{"slug": string, "unblock": number, "scope": number, "note": string}`.

- `unblock` and `scope` are integers in **1..10 inclusive**, or **both 0** —
  the clear state, which returns the job to having no estimate at all (the
  `load` key then disappears from the job on `/api/board`). Zero for only one
  of the two is a 400: a half-estimate has no ROI. Both keys are **required**,
  and omitting them is not a clear — a body carrying neither number is 400,
  not a silent wipe of the estimate it failed to describe.
- `note` is optional, a one-line rationale, capped at 200 **runes** (not
  bytes). The server one-lines it — control characters become spaces and runs
  of whitespace collapse — and truncates rather than refusing an over-long one,
  landing on 199 runes plus `…`. The numbers are the payload; losing the write
  over a verbose sentence would be the worse answer.
- `at` is NOT part of this request — the server stamps it in unix millis at
  write time, so an estimate can never claim to be fresher than the write that
  made it. Sending it is a 400, not a silently-ignored key.
- The body is decoded with **unknown fields rejected**: `slug`, `unblock`,
  `scope`, `note` and nothing else. A misspelled or nested key is a 400 rather
  than a request that decodes to "clear everything". This route is stricter
  than its siblings because it has one writer (`k load`) and neither client
  posts to it at all.

Response: `{"ok": true}`. The new estimate rides back on the next `/api/board`
poll (`webJob.load`) — the board stays the single source of truth for job
state, so there is nothing to echo here. Same convention as `/api/job/color`.

Errors: 400 (bad json; an unrecognized field; either number out of `1..10`; a
half-estimate; or a body carrying neither number — `unblock`/`scope` absent is
refused rather than read as a clear, since a clear must be written as an
explicit `0`/`0`), 404 (no such job, including an empty `slug`), 413 (body over
1 MiB), 500 (write failed).
Gated under `--mirror` with the standard plain-text 403. **405 is served by the
route, not this verb** — `/api/job/load` answers GET and POST, so 405 means a
method that is neither.

The write deliberately does NOT bump the job's `rev` or `updated_at`: zeus
writes this at the end of every turn, `updated_at` is what both boards render
as a card's age, and `rev` is what stales out a concurrent editor save. Bumping
either for a number that is not part of the job's own contract would reset
every blocked card's age on every zeus turn. Same treatment as the PR
watermarks. **Additive, no contract bump.**

### `GET /api/job/load?slug=`

zeus-side read: this job's current estimate, re-read on resume through the
`kraken load get` cell verb. Not consumed by the iOS app, which reads the
estimate from `webJob.load` on `/api/board` instead — same arrangement as
`GET /api/feedback?slug=`.

Response: `{"load": {webJobLoad}}`, or `{"load": null}` when the job has no
estimate. The key is always present; `null` is the no-estimate answer here,
where on the board the same answer is the `load` key being absent from the job
object. The caller asked about one specific job, so "this one has no estimate"
is worth stating rather than omitting.

Errors: 400 `missing slug` (the query parameter absent or empty), 404 for an
unknown slug. A GET is a read, so it stays reachable under `--mirror`.
**Additive, no contract bump.**

### `GET /api/job/list`

The Slack intake bot's read of what jobs exist right now — iris polls it to answer "is there already a job about this?" before drafting a new one. Not consumed by the iOS app or the web UI, both of which read the whole job objects from `/api/board`; this route exists because an agent's context cannot afford that payload for a question one line per job answers.

Response: `{"jobs": [webJobListItem]}`. The array is always present and never null.

`webJobListItem` (jobdraft.go):

| key | type | optionality |
|---|---|---|
| `slug` | string | always |
| `state` | string | always |
| `summary` | string | always |

- `state` is `jobs.DisplayState` — the same string the board renders (`draft`, `working`, `blocked`, `done`), not the stored `state` column. The two differ on an ERRED job: its stored state is empty but it displays (and is editable) as `draft`.
- `summary` is the job body's **first non-empty line**, one-lined and capped at **120 runes** (a leading markdown `#` is dropped). Empty for a job with no body — a real answer, so the key still rides.
- ARCHIVED jobs are absent entirely: they are out of the active namespace, and iris has no business resurrecting one.

Errors: 405 (non-GET), 500 (no store wired). A GET is a read, so it stays reachable under `--mirror`. **Additive, no contract bump.**

### `POST /api/job/draft`

The Slack intake bot's only write on a job — it creates a draft, or amends an existing one that has not started. Not called by the iOS app or the web UI (carl uses `/api/job/saveform` and `/api/job/save`); its client is the `kraken job draft` cell verb.

This route is the **safety surface** of the whole Slack feature, so its refusals are the specification rather than validation niceties: iris is a claude turning arbitrary Slack text into kraken jobs, and the property that makes that safe is that it can only ever produce a DRAFT. **Nothing is written on any refusal.**

Request: `{"slug": string, "content": string, "mtime"?: number}`.

- `content` is the whole job text (frontmatter + spec), exactly what `/api/job/raw` prints. It is parsed with the strict job parser, so what is stored is exactly what those bytes mean.
- `mtime` is the optimistic-lock token from `/api/job/raw`, and is optional: absent or `0` skips the conflict check, the same contract the raw save's `mtime` has.
- Unknown envelope keys are **not** rejected (unlike `/api/slack/ack`): everything that can hurt this route is inside `content`, where the parser is the gate.

Response: `{"ok": true, "slug": string, "mtime": number}` — `mtime` is the row's NEW rev, so a follow-up edit in the same turn needs no re-read.

Refusal matrix (every row writes nothing):

| condition | status |
|---|---|
| `slug` empty after slugify | 400 |
| `content` empty | 400 |
| the job text does not parse | 400, carrying the parser's own error text (no salvage write — this route never does the raw editor's body+err save) |
| a `ready:` or `prereqs:` key in the FRONTMATTER (either path) | 400, naming the key as retired. Both are accept-and-ignore in the parser — the corpus of job files carries them, and erroring there would pin the whole board to draft — so this route refuses them itself. Silently swallowing one would answer 200 for a line the server discarded, and iris would report a start that never happened. Scoped to the frontmatter: a spec body may discuss prereqs in prose |
| **create** (no active job with the slug): any non-empty `state:` | 400 — `state` is kraken's |
| **create**: empty body | 400 |
| **create**, all guards passed | row created from the parsed fields |
| **edit** (the job exists): stored state is `working`, `blocked` or `done` | 409 — stricter than the editor's own `editableJob` gate, which lets a done job through for a respec |
| **edit**: any non-empty `state:` | 400 |
| **edit**: empty body | 400 |
| **edit**: `mtime` present, non-zero, and not the row's rev | 409 conflict |
| **edit**, all guards passed | writes ONLY `{repo, base, solo, model, body}`; `state` and every kraken-owned field (`session`, `run`, `prs`, `color`, `outcome`, `teardown`) keep their stored values, and the slug never changes |

There is no longer any way for this route to start a job, and that is now
STRUCTURAL rather than enforced: starting a job means submitting it on the
board, and nothing here reaches that. The route's remaining job is to keep
iris out of the fields kraken owns.

Other errors: 405 (non-POST), 409 (slug taken by a concurrent create), 413 (body over 1 MiB), 500 (no store wired; the write failed; or — on the create path only — the job WAS created and then its reload failed, in which case the error text says so explicitly). Gated under `--mirror` with the standard plain-text 403. **Additive, no contract bump.**

---

## Slack intake

The Slack intake bot's three routes. Their client is **iris** — a long-running claude outside any cell — through the `kraken slack inbox|status|ack` verbs; neither the iOS app nor the web UI calls any of them, and none is modeled in Models.swift/APIClient.swift. No Slack token, and nothing derived from one, ever appears in any of these responses.

### `GET /api/slack/inbox`

Every **unacked** inbox row, in `id` order (which is arrival order). This is iris' whole working set: a message stays here until iris explicitly acks it, so a crash, a compaction or a respawn loses nothing — the row is the durable state, iris' context is not.

Response: `{"messages": [webSlackMsg]}`. The array is always present and never null.

`webSlackMsg` (slack.go):

| key | type | optionality |
|---|---|---|
| `id` | number | always |
| `channel_id` | string | always |
| `channel_name` | string | always (empty for a DM — Slack only names channels in search results) |
| `ts` | string | always |
| `thread_ts` | string | always (empty for a top-level message) |
| `author_id` | string | always |
| `author_name` | string | always |
| `source` | string | always — `dm`, `mpim` or `mention` |
| `text` | string | always |
| `seen_at` | number | always (unix millis) |

`ts` and `thread_ts` are Slack's `"seconds.micros"` **strings** end to end and are never parsed through a float, here or anywhere else in the binary. The row's `delivered_at`, `acked_at` and `disposition` columns are deliberately absent: they are kraken's delivery bookkeeping, and this route serves only unacked rows anyway.

Errors: 405 (non-GET), 500 (no store wired, or the read failed). A GET is a read, so it stays reachable under `--mirror`. **Additive, no contract bump.**

### `POST /api/slack/ack`

Iris retiring a batch of messages, with one line saying what it did with them. This is the only thing that removes a message from the inbox read above — explicit rather than implied by the read, so a message iris looked at and never resolved comes back on the next call.

Request: `{"ids": [number], "disposition": string}`.

- `ids` are inbox row ids. The store call is **all-or-nothing**: an unknown id, or one already acked, fails the whole batch and applies nothing.
- `disposition` is a free string carrying what happened (`drafted:<slug>`, `edited:<slug>`, `ignored:<reason>`) — a plain string rather than an enum because it names the job it produced. It is one-lined and truncated to **200 runes** server-side rather than refused, the same treatment `/api/job/load`'s `note` gets. An EMPTY one (after trim) IS refused: "acked, reason unknown" is indistinguishable from a bug that acks everything.
- The body is decoded with **unknown fields rejected**: `ids`, `disposition`, and nothing else.

Response: `{"ok": true}`.

Errors: 405 (non-POST), 400 (bad json; an unrecognized field; empty or absent `ids`; empty `disposition`), 409 carrying the store's own sentence — which names the offending id — for an unknown or already-acked id, 413 (body over 1 MiB), 500 (no store wired). Gated under `--mirror` with the standard plain-text 403. **Additive, no contract bump.**

### `GET /api/slack/status`

Whether the feature is configured at all, when the poll last ran, and how deep the backlog is — the one place to look when "iris said nothing all day" needs to be told apart from "nothing arrived".

Response (`webSlackStatus`, slack.go):

| key | type | optionality |
|---|---|---|
| `configured` | bool | always |
| `config_error` | string | omitted unless the credential is half-configured |
| `last_poll_at` | number | always (unix millis; `0` when the poll has never run) |
| `total` | number | always |
| `undelivered` | number | always |
| `unacked` | number | always |

- `configured` is `core.LoadSlackConfig` succeeding. A failure is otherwise reported ONLY as this `false` — the config error is dropped rather than echoed, since the token value must never reach a response body.
- `config_error` is the ONE exception, and it is about a fixed sentence rather than about errors generally: a session (`xoxc-`) token configured without its `SLACK_D_COOKIE` cannot authenticate any call, so that state carries `core.ErrSlackHalfConfigured`'s constant text — which names the missing key and contains no credential. It distinguishes the two ways `configured` can be false: nothing set up (key absent, the normal fresh state) versus set up wrong (key present) — opposites in practice, identical without it.
- `undelivered` counts rows never bumped into iris' pane (`delivered_at == 0 AND acked_at == 0`); `unacked` counts every row iris has not resolved (`acked_at == 0`), so `unacked >= undelivered` always.
- An unconfigured machine answers `false` with zeroes rather than erroring — that is the answer, not a fault.

Errors: 405 (non-GET), 500 (no store wired, or the counts query failed). A GET is a read, so it stays reachable under `--mirror`. **Additive, no contract bump.**

---

## Peers

### `GET /api/peers` → `{"peers": [Peer]}` (webpeers.go, shape from peer.go)

`Peer` (peer.go):

| key | type | optionality | notes |
|---|---|---|---|
| `index` | number | always | |
| `pane` | string | omitempty | |
| `tmux_session` | string | omitempty | |
| `window` | number | always | |
| `pid` | number | omitempty | |
| `session_id` | string | omitempty | |
| `name` | string | omitempty | |
| `cwd` | string | omitempty | |
| `status` | string | always | includes `"polling"` — a claude mid poll-loop (`/loop`, `/poll-pr`) on a pending `ScheduleWakeup`, distinct from `"waiting"` |
| `status_at` | number | omitempty | |
| `wakeup` | object (`Wakeup`) | omitempty | present only when `status` is `"polling"`; additive, no contract bump |
| `session_id_guessed` | bool | omitempty | |
| `branch` | string | omitempty | |
| `model` | string | omitempty | |
| `kind` | string | omitempty | |
| `agent` | string | omitempty | `"claude"` \| `"codex"` \| absent (shell) |
| `role` | string | always | `"shell"` for agent-free panes |
| `cmd` | string | omitempty | |
| `cmd_seconds` | number | omitempty | |
| `self` | bool | omitempty | |
| `team` | string | omitempty | |
| `agent_name` | string | omitempty | |
| `parent_session_id` | string | omitempty | |
| `messageable` | bool | always | |

`peers` itself is always present and `[]` (never `null`) — `peersHandler` substitutes an empty slice when the source returns `nil`.

`Wakeup` (peer.go/wakeup.go): `reason` (string, always), `delay_seconds` (number, always), `fires_at` (number, always — unix millis). A pending `ScheduleWakeup` tool call pulled from the peer's own transcript; the client renders a countdown to `fires_at`.

### `GET /api/peers/read?pane=&n=` → map literal (webpeers.go)

| key | type | optionality |
|---|---|---|
| `blocks` | array of `{"kind": string, "text": string, "from": string?}` | always (`[]` not `null`) |
| `counted` | number | always |

Block kinds: `user`, `thinking`, `text`, `tool`, `teammate`. `from` rides only
on `teammate` blocks (the sending teammate's id); idle notifications arrive
pre-summarized as e.g. `idle (available)`.

Errors: 404 (no session in pane), 409 (readTranscriptPath refusal), 500 (I/O failure).

### `GET /api/peers/capture?pane=` → `{"pane": string, "content": string}`

Errors: 404 (no session in pane), 409 (capture failure).

### `POST /api/peers/send`

Request: `{"pane": string, "message": string}`, or `{"pane": string, "key": string}` to send a single named key instead of typed text — `message` and `key` are mutually exclusive (400 if both are non-empty).

`key` allowed values: `esc`/`escape`, `enter`, `tab`, `backspace`, `up`, `down`, `left`, `right`, `ctrl-a`…`ctrl-z`. Works for both agent and shell panes; sends exactly one key via `tmux send-keys` with no trailing Enter (e.g. `ctrl-c` sends only `C-c`, `enter` sends only `Enter`) — the deliberate way to interrupt a running turn or navigate a permission/question prompt from a keyboard that can't type these keys.

Response (message, agent target): `{"ok": true, "queued": bool}`.
Response (message, shell target): `{"ok": true, "queued": false, "shell": true}`.
Response (key): `{"ok": true, "key": true}`.
Errors: 400 (both `message` and `key` set), 404 (no session in pane), 409 (send refusal, or unsupported key name).

### `POST /api/peers/spawn`

Request: `{"kind": string, "dir": string (omitempty), "name": string (omitempty, claude-only), "prompt": string (omitempty, claude-only), "cmd": string (omitempty, shell-only)}`.
Response: `{"ok": true, "at": string}` (pane id of the new window).
Errors: 400 (bad kind/name/prompt/cmd/dir), 409 (session/spawn conflict). Requires `Content-Type: application/json` (415 otherwise, see `requireJSONPost`).

### `POST /api/peers/kill`

Request: `{"pane": string}`.
Response: `{"ok": true}`.
Errors: 404 (no session in pane, or pane gone on re-verify), 409 (belongs to a job cell — use the job's Kill action; or kill failure).

### `POST /api/peers/open` — web-UI-only (not called by the iOS app; not modeled in Models.swift/APIClient.swift)

Request: exactly one of `{"pane": string}` or `{"slug": string}`.
Response: `{"ok": true, "note": string}` (human-readable outcome).
`focus_error` (string) is additionally present when the session was raised but bringing its Ghostty window to the front failed — the window still needs a manual switch-to. Both keys are additive (no `apiContract` bump).
Errors: 400 (both or neither of pane/slug set), 404 (no session in pane / no job / no live cell agent), 409 (open failure).

### `POST /api/editor/open` — local-origin-only (not called by the iOS app; not modeled in Models.swift/APIClient.swift)

Request: `{"slug": string, "archived": bool?, "path": string, "line": number?, "col": number?}` — `path` is repo-relative (a bare basename resolves by unique match against the job's tracked files). `archived` picks the archive namespace for the slug (an archived job and a live one can share one); the web UI sets it from `parts.home === "archive"` for a ref clicked in an artifact served out of the archive home, and from `job?.archived` for a steer on an archived job. Body limited to 1MB.
Response: `{"ok": true}`, plus `note` (string) when the cell's worktree is gone and the file was opened from the primary `~/Developer/<repo>` checkout instead. `note` is additive and absent on the clean path.
The file opens inside a Zed window rooted at the resolved checkout — reusing the window that already has that root open, else creating one (core/zed.go passes the root as its own leading path argument).
The source IP must be loopback or one of this host's own interface addresses: a click from a phone or another machine on the tailnet answers 403, since the editor it would open runs here (api/editor.go). Mirror mode 403s it with the rest of the tmux/GUI-driving routes.
Errors: 403 (non-local origin, or mirror mode), 400 (bad json), 409 (empty/absolute/`..`/dot-prefixed path, negative line/col, unknown slug, unresolvable repo, missing file, basename matching zero or several tracked files, or a launch failure). POST only (405 otherwise) and requires `Content-Type: application/json` (415 otherwise, see `requireJSONPost`).

---

## Unified event log

The single source-of-truth record of everything kraken does (daemon
lifecycle + tick events, web server lifecycle, board actions, CLI moves,
installs, errors), stored as size-rotated JSONL files under `~/.kraken/logs`
(util/eventlog.go). The file is the pagination unit: clients load the newest
file, then walk backwards via `prev` as the user scrolls into history.

### `GET /api/logs/files` → map literal (api/logs.go `webLogFiles`)

`{"files": [logFileInfo]}` — always an array, `[]` when no logs exist.
`logFileInfo`: `name` (string, e.g. `20260722-153000.jsonl`), `size`
(number, bytes), `mtime` (number, unix millis).

In store mode (kraken serve, DB-backed) there are no JSONL files at all: the
listing is always exactly one virtual file named `"events"`, whose `size` is
the newest event's id (not a byte count) and whose `mtime` is the newest
event's ts — both move as events accrue rather than staying pinned at a
constant, so size/mtime-based change detection still has something to key
off. The wire shape itself is unchanged either way.

### `GET /api/logs[?file=]` → map literal (api/logs.go `webLogRead`)

No `?file=` returns the newest file. `file` must be a name from the listing
— anything else 404s. In store mode, `file` also accepts the opaque
scroll-back cursor form `events:<id>` (e.g. `events:1042`, as returned in a
previous response's `prev`) in addition to the bare `"events"` name; both
resolve, anything else 404s the same as an unrecognized file-mode name.

| key | type | optionality | notes |
|---|---|---|---|
| `file` | string | always | the file served; `""` when no logs exist yet |
| `prev` | string | always | next-older file name, `""` at the start of history — the scroll-back cursor |
| `entries` | array (`logEntry`) | always (`[]` not `null`) | in append order (oldest first) |

`logEntry`: `ts` (number, unix millis), `src` (string: `daemon`\|`web`\|`cli`\|`install`), `level` (string: `info`\|`error`), `event` (string, kebab id e.g. `cell-start`, `start-failed`, `queue`), `msg` (string).

Errors: 404 (unknown file name), 500.

### `POST /api/pr-attach` — localhost-only (not called by the iOS app or web UI; not modeled in Models.swift/APIClient.swift)

The endpoint jobs/prhook.go's in-cell pr-hook POSTs to instead of writing a
job's PR list directly, now that `kraken serve` is the sole DB writer. Also
reachable in principle from the daemon's own transcript-scanning/branch-list
backstops, though today those still call `jobs.AttachJobPR` in-process (same
package, no HTTP hop needed).

Request (`prAttachReq`): `slug` (string, required), `url` (string, required
— must exactly match a GitHub PR URL: `https://github.com/<owner>/<repo>/pull/<n>`).
Response: `{"ok": true, "attached": bool}` — `attached` is `false` when the
PR was already recorded (idempotent re-attach).
Errors: 400 (bad JSON, missing slug, or `url` doesn't match the GitHub PR
URL shape), 404 (no job resolves under that slug — current OR former, since
the hook posts the slug baked into the cell at spawn), 500 (write failure).

### `POST /api/limit-hit` — localhost-only (not called by the iOS app or web UI; not modeled in Models.swift/APIClient.swift)

The endpoint jobs/limithook.go's in-cell `kraken limit-hook` POSTs to when a
claude in a cell dies on a usage limit — Claude Code fires that verb as a
`StopFailure` hook (matcher `rate_limit`, installed into the cell's own
`.claude/settings.json` beside the `PostToolUse` pr-hook entry). The hook
process never opens kraken.db; it reports, and `kraken serve` records, the
same split `/api/pr-attach` above draws. The recorded hit is what the
daemon's rescue pass reads to decide whether to swap the main login to a
healthier pool account.

Deliberately **not** mirror-gated: a mirror must still record that a cell hit
a usage limit. It is the rescue, not the observation, that a mirror never
performs, and that gate lives in the daemon's own `!mirror` block.

Request (`limitHitReq`): `slug` (string, required), `error` (string, required
— Claude Code's error-enum value verbatim, e.g. `rate_limit`), `details`
(string, optional — the failure's message text; one-lined and capped at 200
**runes** through `core.TruncateOneLine` rather than refused, by the hook
before it is sent and again on receipt — the second pass is what makes the
stored value safe whoever posted it, the first is what stops an oversized
`error_details` hitting the 64 KiB body cap and losing the hit to a 400),
`session_id`
(string, optional), `agent_id` (string, optional — present only when the
failing claude was a subagent). Absent optional fields are the ordinary case,
not an error: a real captured payload carries neither `agent_id` nor
`error_details` on the main thread.

Response: `{"ok": true, "deliveries": N}` — `deliveries` is how many hook
calls have folded into this one recorded hit. A cell is a lead plus a crew of
teammates all reading one account, so a single exhausted window fires several
hooks within seconds; deliveries inside a 2-minute window carrying the same
error type fold into the first hit rather than replacing it.

Errors: 400 (bad JSON; missing `slug`; missing `error`; **no such job**), 405
(non-POST), 500 (store write failure).

The unknown-slug 400 is where this route deliberately differs from
`/api/pr-attach`, which answers 404 for the same condition. The only client
is a hook kraken itself installed with the job's own slug baked into the
command string, so an unknown slug here means the request is malformed — a
stale hook left by a torn-down job, or a hand-typed POST — rather than a
caller asking about a resource the server merely doesn't have yet. "Unknown"
includes former names before it is reached: a job renamed mid-run still
answers to the name baked into its cell, so the refusal only fires for a slug
that names no active job under any name it has held.

Every accepted delivery writes exactly one `limit-hit` event naming the error
type **verbatim**, including types the rescue never acts on, because that log
is how a future change to Claude Code's error enum gets noticed before the
rescue silently stops firing. `details` never appears in that line.

### `POST /api/logevent` — localhost-only (not called by the iOS app or web UI; not modeled in Models.swift/APIClient.swift)

The endpoint `k logevent` (cmd_jobs.go — the Makefile's install target, and
any other non-Go writer) POSTs to instead of appending the unified event log
JSONL file directly. The append still goes through `util.LogEvent`, whose
event sink (wired by `jobs.UseStore`) is what lands it in the events table
when `kraken serve` is running — additive to the JSONL file write, as
always.

Request (`logEventReq`): `src` (string, required), `level` (string,
omitempty — `info` or `error`, defaults to `info`), `event` (string,
required), `msg` (string, omitempty).
Response: `{"ok": true}` on success.
Errors: 400 (bad JSON; missing `src`/`event`; `level` not `info`/`error`),
405 (non-POST).

## Misc

### `GET /api/usage` → `webUsage` (usage.go)

The daemon's last usage-poller pass — one row per Claude account (main +
every `~/.claude-pool` slot). Additive, no contract bump. Read-only (GET
only, 405 otherwise). All `*_at` fields are unix millis. `accounts` is
always `[]`, never `null`.

| key | type | optionality | notes |
|---|---|---|---|
| `available` | bool | always | snapshot file exists and parsed |
| `fetched_at` | number | always | unix millis of the daemon's last completed pass; `0` when unavailable |
| `age_seconds` | number | always | server-derived `max(0, (now - fetched_at) / 1000)`; `0` when unavailable |
| `daemon_ok` | bool | always | the usage poller is the daemon: same liveness check as `/api/board`'s `daemon_ok` |
| `accounts` | array (`AccountUsage`) | always (`[]` not `null`) | |
| `rotation_mode` | string | always | `"auto"` or `"manual"` — see `jobs.RotationState`. Added additively (Slice C); absent on an older server, which older clients read the same as `undefined`/missing |
| `rotation_set_by` | string | omitempty | `"cli"`, `"web"`, or absent when the mode has never been explicitly set (auto-default) |
| `rotation_set_at` | number | omitempty | unix millis of the last explicit mode change; `0`/absent when auto-default |

`AccountUsage` (core/usagepool.go, marshaled as-is):

| key | type | optionality | notes |
|---|---|---|---|
| `slug` | string | always | `"main"` or the pool slot dir name |
| `email` | string | omitempty | |
| `display_name` | string | omitempty | |
| `tier` | string | omitempty | raw `rate_limit_tier` |
| `current` | bool | omitempty | |
| `session` | object (`UsageWindow`) | omitempty | absent when the account hasn't been fetched yet (no creds / reauth needed / error with no prior data) |
| `weekly` | object (`UsageWindow`) | omitempty | same absence rule as `session` |
| `models` | array (`ModelWindow`) | omitempty | weekly_scoped per-model limits; absent whenever `session`/`weekly` are absent, but ALSO absent on a successful fetch when the account simply has no per-model limits |
| `fetched_at` | number | omitempty | unix millis of last successful fetch for this account |
| `stale` | bool | omitempty | `session`/`weekly`/`models` are a cached fallback (e.g. main-account keychain unreachable), not this pass's live fetch |
| `error` | string | omitempty | last fetch error, if any |
| `reauth` | bool | omitempty | token refresh failed — run `ccp login` for this account |
| `no_creds` | bool | omitempty | no credentials found for this account (e.g. a disabled pool slot) |
| `cooldown_until` | number | omitempty | unix millis, advisory — this account is in a cooldown window |

`UsageWindow`: `percent` (number, always), `resets_at` (number, omitempty — unix millis, `0`/absent = unknown).

`ModelWindow`: `name` (string, always — model display name, e.g. `"Fable"`), `percent` (number, always), `resets_at` (number, omitempty).

### `POST /api/account/switch` → `accountSwitchResp` (account.go) — web-UI-only (not called by the iOS app; not modeled in Models.swift/APIClient.swift)

Manually switch the main Claude login to a `~/.claude-pool` slot — always
executes regardless of usage thresholds (unlike the auto rotation gate), and
pins rotation to `"manual"` on success (see `jobs.RotationState` / `k
account`). Additive, no contract bump.

Request:

| key | type | optionality | notes |
|---|---|---|---|
| `slug` | string | required | the pool slot dir name to switch main to |

Response (200):

| key | type | optionality | notes |
|---|---|---|---|
| `from` | string | always | outgoing main account's email |
| `to` | string | always | incoming (now main) account's email |
| `mode` | string | always | the read-back mode after the swap — normally `"manual"`, but can be `"auto"` on the rare case where the swap committed and the manual pin write itself failed to persist |

Errors: 405 (non-POST), 400 (missing `slug` / bad json / invalid slug —
`core.ErrInvalidSlug`), 409 (a previous rotation didn't complete —
`core.ErrRotationInFlight` — or the save-back slot would be the rotation
target — `core.ErrSaveBackIsTarget`), 500 (any other swap failure, e.g.
target slot needs `ccp login`). All error bodies are the generic
`{"error": string}` shape. A failed swap never changes the rotation mode.

### `POST /api/account/mode` → `accountModeResp` (account.go) — web-UI-only (not called by the iOS app; not modeled in Models.swift/APIClient.swift)

Sets rotation mode without swapping main — `"manual"` pins (parity with the
CLI's `k account auto`/manual-switch semantics and the web UI's banner
toggle), `"auto"` re-enables the rotation gate. Additive, no contract bump.

Request:

| key | type | optionality | notes |
|---|---|---|---|
| `mode` | string | required | `"auto"` or `"manual"` — any other value is a 400 |

Response (200):

| key | type | optionality | notes |
|---|---|---|---|
| `mode` | string | always | echoes the mode now in effect |

Errors: 405 (non-POST), 400 (missing/invalid `mode` / bad json), 500 (state
write failure).

`/api/account/switch` and `/api/account/mode` both persist to the same
`rotation.json` independently of one another — there's no merge, so
concurrent calls to either endpoint (or the CLI's `k account` commands) are
last-writer-wins on the mode field.

### `POST /api/update` → `updateResp` (api/update.go)

Starts a self-update: fetch → merge → `make install` → `launchctl kickstart
-k`. The handler itself does none of that — it preflights, stakes a claim,
and forks a detached `kraken update --detached` child, because the last step
of an update kills the very `kraken serve` process that served the request.
Mirror-gated (403 under `kraken serve --mirror`): a mirror board is
read-only, and this is the most destructive action on the surface.
Additive, no contract bump. Modeled in the iOS app (`UpdateStartResponse` in
Models.swift, `startUpdate` in APIClient.swift) — the daemon bar's ⟳
control.

Request (`updateReq`, an empty body is valid and means "just update"):

| key | type | optionality | notes |
|---|---|---|---|
| `force` | bool | optional to send | no `omitempty` tag — absence just decodes as `false`. Install even when already up to date |
| `test` | bool | optional to send | no `omitempty` tag — absence just decodes as `false`. Run `go test ./...` after the build, before the install |

Response (202 Accepted — the run has been *started*, not finished):

| key | type | optionality | notes |
|---|---|---|---|
| `ok` | bool | always | |
| `phase` | string | always | always `"preflight"` — the phase the claim was written at |
| `from_sha` | string | always | the commit the run starts from |

Errors: 405 (non-POST), 400 (malformed JSON — an empty body is not an
error), 409 (a run is already in flight: `update already in flight (started
HH:MM:SS)`; or a preflight refusal — not on `main`, uncommitted changes, no
`go`/`make` — whose message is `jobs.PreflightUpdate`'s own text, shared
verbatim with the `kraken update` CLI verb), 500 (the claim couldn't be
written, or the fork failed — in which case the status record is reset to
`failed` rather than left pinned at `preflight`).

Single-flight is the status write, not a lock: the handler writes
`phase: preflight` BEFORE spawning, and a second request reads that
non-terminal phase and 409s. The detached child takes the real flock
(`~/.kraken/update.lock`) and holds it for the whole run.

### `GET /api/update/status` → `webUpdate` (api/update.go)

The same `webUpdate` shape `/api/board` carries under `update` (documented
above), served on its own so the UI's in-flight poller doesn't re-run
discovery every tick. GET only (405 otherwise), never gated in mirror mode —
it's a read. Note that fetch failures against this endpoint are EXPECTED
while `phase` is `restarting`: the server is being replaced mid-poll.
Modeled in the iOS app (`WebUpdate` in Models.swift, `updateStatus` in
APIClient.swift): while a run is non-terminal the board screen polls this
every ~1.2s and swallows failures for up to 5 minutes — its own, more
generous version of the web board's 120s restart-gap ceiling, since the
phone reaches the Mac over the tailnet and may be re-establishing that link
while the server is down. Past the ceiling it synthesizes a `failed` record
rather than waiting forever — but only on two consecutive failed fetches,
and the gap clock restarts with each poll loop, because that clock is
wall-clock while the loop is frozen whenever the app is suspended or the
Board tab is off screen. Like the web board, it honors a terminal record
only within 60s of `updated_at` (both directions — a future timestamp from
clock skew would otherwise be fresh forever): `~/.kraken/update.json` keeps
its last `done` forever, so an unbounded read would show a stale ✓ on every
launch.
Unlike the web board, the app does NOT reload itself when a run finishes —
its UI ships in the app bundle, not out of the binary being replaced.

### `POST /api/ship-ios` → `shipIOSResp` (api/shipios.go)

Starts a TestFlight ship: `xcodegen generate` → `xcodebuild archive` →
`-exportArchive` → upload to App Store Connect. The handler itself does none
of that — it preflights, stakes a claim, and forks a detached
`kraken ship-ios --detached` child, so a run that takes ten minutes isn't
tied to an HTTP request. Mirror-gated (403 under `kraken serve --mirror`): a
mirror board is read-only, and this one pushes a build to a real Apple
account under a build number that can never be reused. Additive, no contract
bump. Modeled in the iOS app (`ShipIOSStartResponse` in Models.swift,
`startShipIOS` in APIClient.swift) — the daemon bar's ⇧ control.

Request: an empty body (`{}` or nothing at all). There are deliberately no
options — the credentials are environment (`ASC_KEY_ID`/`ASC_ISSUER_ID`,
never on the wire), and the script's `--allow-provisioning-updates`, which
MUTATES carl's Apple developer account, is reachable only from a terminal.
A malformed (non-empty, non-JSON) body is still a 400.

Response (202 Accepted — the run has been *started*, and it is a long way
from finished):

| key | type | optionality | notes |
|---|---|---|---|
| `ok` | bool | always | |
| `phase` | string | always | always `"preflight"` — the phase the claim was written at |

There is no `build` here: the build number isn't known until the shipper
announces it mid-run, so it arrives via the status record instead.

Errors: 405 (non-POST), 400 (malformed JSON — an empty body is not an
error), 409 (a run is already in flight: `ship already in flight (started
HH:MM:SS)`; or a preflight refusal — missing App Store Connect credentials,
no `xcodegen`, no script — whose message is `jobs.PreflightShipIOS`'s own
text, shared verbatim with the `kraken ship-ios` CLI verb), 500 (the claim
couldn't be written, or the fork failed — in which case the status record is
reset to `failed` rather than left pinned at `preflight`).

Single-flight is the status write, not a lock: the handler writes
`phase: preflight` BEFORE spawning, and a second request reads that
non-terminal phase and 409s. The detached child takes the real flock
(`~/.kraken/ship-ios.lock`) and holds it for the whole run.

### `GET /api/ship-ios/status` → `webShipIOS` (api/shipios.go)

The same `webShipIOS` shape `/api/board` carries under `ship_ios`
(documented above), served on its own so the UI's in-flight poller doesn't
re-run discovery every tick during a ten-minute archive. GET only (405
otherwise), never gated in mirror mode — it's a read. Modeled in the iOS app
(`WebShipIOS` in Models.swift, `shipIOSStatus` in APIClient.swift).

**Fetch failures here are NOT expected**, and that is the one real
difference from `/api/update/status`. An update ends by restarting the very
server being polled, so its poller has to treat a dead socket as the normal
middle of a successful run; a ship restarts nothing — `kraken serve`, the
daemon and the binary are all up from preflight through upload. Both clients
therefore report a persistent failure rather than swallowing it, after a
single retry to absorb one dropped request (web: 2 consecutive failures at
1s; app: 2 at 1.2s). Neither has any gap clock, and neither reloads when a
run finishes: nothing they are running was replaced. Like the update
control, a terminal record only speaks for the button while it is fresh
(both clients: 120s around `updated_at`, in both directions, since a future
timestamp from clock skew would otherwise be fresh forever) —
`~/.kraken/ship-ios.json` keeps its last `done` forever.

### `GET /api/health` → map literal (webpeers.go `healthHandler`)

| key | type | optionality | notes |
|---|---|---|---|
| `ok` | bool | always | |
| `host` | string | always | |
| `contract` | number | always | == `apiContract` |
| `stale` | bool | always | |
| `build` | string | always | the running binary's ldflags-stamped short commit, `"dev"` for an unstamped build. Additive, no contract bump — it's how a client tells whether a restart actually landed a new binary |

### `GET /api/artifact/<slug>/<cell\|pluto\|archive>/<name>`

Not JSON — serves the raw artifact file with a `Content-Type` picked by
extension (`artifactContentType`: html/svg/pdf/png/jpg/gif/webp get their
real type, everything else is `text/plain; charset=utf-8`). 404 on any
path-traversal attempt, hidden segment, unresolvable job, or missing file.

### `POST /api/upload` (upload.go)

Uploads an image attachment for a job — the web board's and iOS app's
composer attachment flow. Additive, no contract bump. Modeled in the iOS app
(`UploadResponse` in Models.swift, `uploadImage` in APIClient.swift).

Request: `multipart/form-data`, fields `slug` (string, required) and `file`
(the image, required). Body capped at 11MB total (1MB slack over the 10MB
file cap for multipart framing/other fields).

The saved extension always comes from sniffing the file's content
(`http.DetectContentType` on the first 512 bytes), never the client-supplied
filename — a PNG renamed `photo.jpg` still saves as `img-NNN.png`.
Supported types: PNG, JPEG, GIF, WebP.

Response (200):

| key | type | optionality | notes |
|---|---|---|---|
| `name` | string | always | saved filename, e.g. `img-001.png` |
| `path` | string | always | absolute path on the kraken host's disk |
| `url` | string | always | `/api/upload/<url-escaped slug>/<name>` — GET this to fetch the image |

Errors: 405 (non-POST), 404 (missing/unknown `slug`), 413 (file exceeds
10MB, or total body exceeds the 11MB ceiling), 415 (file doesn't sniff as
PNG/JPEG/GIF/WebP), 400 (malformed multipart form, or missing `file`), 500
(write failure).

### `GET /api/upload/<slug>/<name>` (web/serve.go)

Not JSON — serves the raw uploaded image file, `Content-Type` picked by
extension via the same `artifactContentType` the artifact route uses. GET
only (405 otherwise). `name` must exactly match the saved-name format
(`img-NNN.<png|jpg|gif|webp>`); 404 on any path-traversal attempt, malformed
name, unresolvable slug, or missing file — resolution goes through
`api.UploadPath`, which does not require an active job (an upload outlives
its job's own draft/working/etc. state changes, same lifecycle as artifacts).
