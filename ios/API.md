# kraken web API — iOS wire contract

Canonical reference for every JSON endpoint the iOS app (`ios/Kraken/`)
consumes. Source of truth is the Go code — the handlers and wire types in
`internal/api/` (`board.go`, `peers.go`, `update.go`, `shipios.go`, …),
`web/serve.go`, and the model layers they read
(`internal/jobs/{daemon,job,status}.go`, `internal/core/peer.go`); this doc
transcribes it, it does not define it independently.

**Current contract**: `apiContract = 7` (internal/api/board.go — the Go
constant + `contract_test.go` bump lands in the same commit as this doc, per
CLAUDE.md's API-contract rule), mirrored by `APIClient.expectedContract`
(APIClient.swift). `/api/health` and
`/api/board` both serve `contract` (int) + `stale` (bool). v3 bumped when
the auto-approve feature (and `auto_approve` on the job form GET/POST) was
removed. v4 bumped when the job-level `type` (`build`|`info`) concept was
deleted (Q14) — `type` removed from `/api/board`'s job objects and from the
job form GET/POST shapes; the pipeline distinction now lives in `/aim` vs
`/info` skill choice plus the operator's `/do` steering, never a wire field. v5
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
draft · working · blocked · done (+ `archived`). v7 bumped for the **request
gate** (`internal/api/gate.go`, see "Nodes and pairing" below): no key changed
shape, but who may call did — a request from another tailnet device that
carries no pairing token is the *node* tier and is answered `401
{"error":"unauthorized"}` on every board route, so an unpaired shipped app
stops working against a v7 server and must pair first. The keys v7 adds
(`webJob.visibility`, `webJob.owner`, `webBoard.nodes`) are additive.

**Bump policy**: bump `apiContract` ONLY on a breaking change — a JSON key
retyped, renamed, or removed, or a change in who may call a route a shipped
client already calls (v7, the request gate). Additive keys never bump it: old clients
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
`WebBoard.shipIOS`, `WebBoard.goals`: all always present on a current
server, missing from builds that predate them, where the absence is itself
what the app checks for. **Blast-radius containment** — every field `WebUpdate` and
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
| `goals` | object (`{"week": [goal], "day": [goal]}`) | always (both arrays `[]`, never `null`) | the CURRENT local week's and today's goals — the same two arrays `GET /api/goals` serves, from the same store call, so the board key and that endpoint can never disagree. Periods are `store.GoalPeriods(time.Now())`: `day` is today's local `2006-01-02` date, `week` is the local Monday of this week. Added additively, no contract bump. Modeled in the iOS app as `WebGoals` (Models.swift), **Optional** on `WebBoard` for the same reason `update` is, and hand-decoded for the same blast-radius reason (see that struct's comment) |
| `background` | object (`{"updated_at": number}`) | omitted when no background is set | the board's optional background image (api/background.go): the upload stamp alone (unix millis), never the bytes — `GET /api/background` serves those, with `?v=<updated_at>` as the cache-buster. ABSENT is the wire's only "no background". Added additively, no contract bump. Modeled in the iOS app as `WebBackground` (Models.swift), **Optional** on `WebBoard`; the app decodes it but does not render a background yet (a noted follow-up) |
| `operator` | string | always | the name kraken's agents use for the person running it — `jobs.Operator()`: `KRAKEN_OPERATOR` (from the environment or `~/.kraken/env`), else `$USER`, else `"the operator"`. The web board labels the operator's turns in a side thread with it (falling back to `you` when the key is absent, i.e. an older server). Added additively, no contract bump. Modeled in the iOS app as `operatorName: String?` (JSON key `operator` — `operator` is a Swift keyword), **Optional** on `WebBoard`; the app decodes it but renders nothing from it yet |
| `node_id` | string | omitempty | this node's tailscale stable id (the `node_id` of its `NodeEnvelope`); absent when tailscale is unreadable or under `KRAKEN_NODES=off`. A client compares a headless thread's `origin.node_id` with it to tell this node's own threads from another node's. Added additively, no contract bump. Modeled in the iOS app as `nodeId: String?` on `WebBoard` |
| `node_label` | string | omitempty | this node's device label (`jobs.DeviceLabel`, the `label` of its `NodeEnvelope`), read off the same cached envelope as `node_id`; absent under `KRAKEN_NODES=off`. Names the "this Mac" row of the board bar's machines dropdown (web) and of Settings' machine filter (iOS) without a second poll of `/api/node`; a client that finds it absent (an older server) shows the bare words "this Mac". Added additively, no contract bump. Modeled in the iOS app as `nodeLabel: String?` on `WebBoard` |

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
queue pass is gone, and the one start it still makes (a dream job it
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
| `detail` | string | omitempty | zeus' last assistant text, full multi-line text capped at 4000 runes (not bytes). Set wherever zeus' transcript resolves: live started cells, `cell_gone` jobs, and DONE jobs (by stamped session — the cell is torn down but the transcript outlives it), so a done card carries its final report until archived. Additive, no contract bump |
| `question` | object (`webQuestion`) | omitempty (null-able) | |
| `zeus_status` | string | omitempty | zeus' own refined status (running/polling/waiting/question); live started, non-done cells only, omitted when zeus isn't resolvable |
| `stage` | string | omitempty | the pipeline skill zeus most recently used — one of the four phase-governing skill names (`info`/`aim`/`do`/`dream`; `graph` was retired with the knowledge graph and `pr` folded into `do` when the PR became do's own last step, and a client renders an unknown stage with its generic fallback), read off zeus' transcript (`jobs.FindStage`: last Skill invocation, `/name` command message, or Read of a `.claude/skills/<name>/SKILL.md` wins; the reference skill `load` never counts). Started jobs with a resolvable transcript only (live, `cell_gone`, or done by stamped session). Absence covers three states the wire does not separate — no resolvable transcript, any failed or truncated scan, and no pipeline skill used yet — all rendered the same way (no badge). Rendered as one card chip on both clients. Additive, no contract bump |
| `wakeup` | object (`webWakeup`) | omitempty (null-able) | pending `ScheduleWakeup` from zeus' transcript — cell is on a poll timer, still counts as `working`; render a countdown chip. Additive, no contract bump |
| `roster` | array (`webAgent`) | omitempty | live crew (teammates) PLUS zeus' live background tasks (in-process subagents, monitors, background shells — each entry's `kind` says which; job 612), live started non-done cells only |
| `health` | string | omitempty | "" (absent) = healthy |
| `mtime` | number | always | unix millis |
| `archived` | bool | omitempty | |
| `display_slug` | string | omitempty | original slug, archived jobs only |
| `cell_gone` | bool | omitempty | started + non-done + tmux session vanished; worktree (incl. uncommitted WIP) still on disk — reactivate preserves it, done/kill discard it |
| `reactivatable` | bool | omitempty | has a recorded zeus session, and either done or `cell_gone` |
| `last_sent` | string | omitempty | last real user message from zeus' session transcript, any send channel (board steer, `k s`, direct tmux typing); omitted when no transcript is resolvable or it has no user turn yet; one-line, capped 4000 runes (not bytes). Question-answer digit keypresses never appear (they're tool_results, not user text); started jobs with a resolvable transcript — live, `cell_gone`, or done (by stamped session) |
| `prs` | array (`jobPR`) | omitempty | every PR shipped from the cell branch (daemon-tracked history, never cleared); render as linked state chips |
| `stack` | array (`jobStackLink`) | omitempty | the cell's manual-test URLs, registered by the cell through `kraken stack add` (`job_stacks` rows, schema v35); render as linked chips beside the PRs, dimmed when `status` is `down`. Absent means the cell registered none; always absent on an archived job (the rows are deleted at cell teardown, so a reactivated job starts with none). The web board renders a sibling node's links as plain labels, since `localhost` on another machine is not this stack; iOS decodes and does not render yet. Additive, no contract bump |
| `doc_questions` | array (`webDocQuestion`) | omitempty | zeus' open doc questions (answered or not), served straight from `questions` rows in `kraken.db` — no file, no parse step, so there is no more cell-gone-read-only or malformed-file case; never set for archived jobs. Additive, no contract bump |
| `feedback` | array (`webFeedback`) | omitempty | per-job feedback entries (open + addressed), served straight from `feedback` rows in `kraken.db`; unlike `doc_questions`, IS populated for archived jobs (rows outlive the cell). Additive, no contract bump |
| `color` | string | omitempty | the job's color, one of `red` \| `blue` \| `green` \| `yellow` \| `purple` \| `orange` \| `pink` \| `cyan` (the names Claude Code's `/color` accepts, in that order); absent or `""` means the job has NO color and must render exactly as an uncolored job always has — no swatch, no reserved space. Clients degrade an unrecognized name to "no color" rather than failing. Additive, no contract bump |
| `agent` | string | omitempty | which coding agent the cell's zeus runs — `codex` for a codex cell, absent for the claude default so a card without the key renders exactly as it always has. Set from the job's `agent:` frontmatter (`claude` \| `codex`). Additive, no contract bump |
| `threads` | array (`webThread`) | omitempty | this job's side threads — every fork of zeus' session, live or closed, served straight from `threads` rows in `kraken.db`; a closed thread outlives its cell (its transcript is still readable), so the array survives teardown. Additive, no contract bump |
| `tokens` | object (`webTokens`) | omitempty | this job's summed token spend, straight from its `job_tokens` row. **Absent means NEVER MEASURED, which is not the same as zero** — a job that was measured and cost nothing serves the object with all-zero counters. Present for done and archived jobs too (the row outlives the cell). Additive, no contract bump |
| `load` | object (`webJobLoad`) | omitempty | zeus' two 1–10 triage estimates for this job — how expensive the operator's unblock is, and how big the claude work it buys. **Absent means "no estimate to act on", which is not the same as an estimate of zero**, and unlike `tokens` it covers THREE cases: never estimated, cleared (a clear zeroes `at`, and a zero `at` serves no object), and archived. **Withheld on archived jobs regardless of whether one exists** — the four columns survive archival and the key returns on unarchive; the archived loop drops it because a card that cannot be blocked cannot pose the question the badge answers. Served on every active job whatever its display state, so the key never flickers as a card moves column — the blocked-only rule is a RENDERING rule the clients apply, not a payload rule. The ROI the badge renders is DERIVED by the client (`scope / unblock`) and deliberately never on the wire. Additive, no contract bump |
| `goal` | object (`{"id": number, "text": string, "done": bool}`) | omitempty | the job's PRIMARY goal — durably `goals[0]`, the head of the array below, present under exactly the same condition and never on its own. Retained beside `goals` so a client that predates multi-goal links keeps rendering one goal rather than none. Additive, no contract bump. Modeled in the iOS app as `WebJobGoal` on `WebJob` |
| `goals` | array (`[{"id": number, "text": string, "done": bool}]`) | omitempty | EVERY goal this job links (`job_goals`), in link-position order, present exactly when it links at least one — each from ANY period and done or not, so a job carried over from last week still names the goals it belongs to. Written only by `POST /api/job/goal` (below), never by `saveform`: the link is deliberately absent from the job form's field set, so a full form save cannot touch it. Additive, no contract bump. Modeled in the iOS app as `WebJobGoalList` on `WebJob` (a wrapper struct, because its decode is hand-written) and rendered as ONE card chip per goal (`GoalChip`, GoalsDrawer.swift) — `◎` plus the goal text truncated by the same 24-character rule web's `goalChipText` uses, muted when `done`, full text in the accessibility label since a phone has no tooltip; no `+N` badge for the rest, the chips wrap. That decode is tolerant in two places, since chips are cosmetic and the board is not: a `goals` that is not an array degrades to an empty list, and a malformed ELEMENT is dropped while the rest survive |
| `account` | object (`{"slug": string, "email": string, "at": number}`) | omitempty | the pool slot this cell is BOUND to (schema v28's three `jobs` columns) — the login every claude in the cell bills, delivered to its panes as `CLAUDE_SECURESTORAGE_CONFIG_DIR=~/.claude-pool/<slug>` and stamped at spawn (rebound in place by the limit rescue). **Which POOL the slug names is decided by the job's `agent`, not by this object**: with `agent: "codex"` the slug names a slot under `~/.codex-pool/accounts/<slug>`, delivered to the cell's pane as `CODEX_HOME=<that dir>` and matched against `/api/usage`'s `codex_accounts`; otherwise it is a `~/.claude-pool` slot matched against `accounts`. The two pools are disjoint and a slugified email can name a slot in both, so a client that joined on the slug alone would draw one pool's percentages under the other pool's cell. `slug` is the slot directory name and is the same key `/api/usage`'s account rows carry, so a client joins the two on `agent` plus it and never on the email; `email` may be `""` for a slot ccp has no identity for yet; `at` is unix MILLIS of the bind stamp. **Absence is the wire's only spelling of UNBOUND** — the cell runs on the machine's main login exactly as every cell did before per-cell binding — and it covers two live states no client separates (the pool was dry or fully excluded at spawn; rotation is pinned `manual`, where kraken takes no account decision at all) plus a third, ARCHIVED: **withheld on archived jobs regardless of whether a binding exists**, which is `load`'s call rather than `tokens`' and for `load`'s reason — a card with no cell is not spending on anything, so the badge would answer a question it cannot pose. The columns survive archival and the key returns on unarchive. Served on every active bound job whatever its display state, so the key never flickers as a card moves column; the live-cards-only rule is a RENDERING rule the clients apply, not a payload rule. Additive, no contract bump. Rendered on web as one card chip (`AccountChip` — `⬢` plus the email's LOCAL PART, falling back to the slug, full address + slot + bind age in `title`/`aria-label`) and as the steer modal's read-only `.steer-account` section (bound-to, bind age, and that slot's bars joined from the 60s `/api/usage` poll — session/Fable for a claude cell, weekly plus a session bar only when the codex slot reports a genuinely shorter window for a codex one; no bars at all when the poll carries no row for it, never a fabricated percentage). Modeled in the iOS app as `WebJobAccount` on `WebJob`, **decode-only** this pass — nothing on the phone renders it yet. That decode is TOLERANT, the same way `goals` above is and for the same reason: `WebBoard.jobs` is a strictly decoded array, so a throw inside one job's `account` would take the entire board payload with it, and a cosmetic badge must never cost the board it is drawn on. A present-but-MALFORMED object (a retyped `slug`, a future server, a hand-edited response) degrades to nil — the card silently loses its badge — rather than failing the decode; absent and null are a different case absorbed one level up. `slug` stays required inside the object precisely so a malformed one is detectable: an object with no slug names no account at all |
| `context` | object (`webJobContext`: `{"pct": number, "window": number, "used": number, "at": number, "handoff_pct": number, "handoff"?: {"fires": number, "last_at": number, "exhausted": bool}}`) | omitempty | the cell LEAD's context-window fill as its statusline last reported it — the kv row `cell_ctx:<job id>:<session>` that `kraken ctx-hook --slug` writes through `POST /api/cell/context` on every statusline render and the daemon's auto-handoff pass reads. Nothing is recomputed: `pct` is Claude Code's own `used_percentage` (input-only: input + cache_creation + cache_read, over the model's REAL window; may carry decimals), `window` is `context_window_size` (0 when the hook sent none), `used` is `total_input_tokens` (0 when the reporting hook predates the field — a client then derives `pct × window / 100` and marks it approximate; a billed session never reports a real zero), `at` is unix MILLIS of the report (age here is time since the last statusline RENDER, not staleness — an idle zeus renders nothing new), `handoff_pct` is the fill at which the daemon types `/handoff` into the pane (served so no client hardcodes 75), and `handoff`, the one omitempty key inside, is the pass' state for THIS session: `fires` in the CURRENT streak (reset once the last fire is two hours old — never a lifetime total), `last_at` millis (0 when never fired), `exhausted` when the streak hit its cap. **Absence is the wire's only spelling of UNREPORTED** — no stamped zeus session yet, a cell whose `statusLine` kraken left alone because it already ran something else, a codex cell (no statusline hook) — plus ARCHIVED: **withheld on archived jobs regardless of whether a row exists** (`load`'s call: a card with no cell is not filling; the row is never deleted, so the key returns on unarchive). A DONE but unarchived job KEEPS it: the row survives teardown and a reactivation resumes the same session near that fill. Served whatever the display state, so the key never flickers as a card moves column. "context", never "tokens" (summed spend) or "usage" (account quota). Rides the fleet payload as built (`nodeCard` clears only `owner` and `account`). Rendered on web as the steer modal's `.steer-ctxwin` panel beside `.steer-tokens` in one `.steer-meters` row (headline `NN%`, a bar with a tick at `handoff_pct`, `used / window`, report age, the handoff streak once it has fired; `u-ok` below 50, `u-warn` to just under `handoff_pct`, `u-danger` from it). Modeled in the iOS app as `WebJobContext` on `WebJob`, **decode-only** this pass, with `account`'s tolerant decode: a present-but-malformed object degrades to nil rather than failing the board. Additive, no contract bump |

`goal` and `goals` (`webJobGoal`) are a THREE-key projection of the goal row,
not the whole `goal` object `/api/goals` serves: a card renders the goal's
text and whether it is ticked off, and never its scope, period, position or
parent.

`start_error` and `prereqs` were REMOVED from this shape in v6. `start_error`
was a side channel for a failure the board could not otherwise see: the daemon
started queued jobs in the background, so the reason one would not start
reached nobody. A start is a foreground HTTP request now — the reason comes
back as `POST /api/queue`'s own 409 body — and a key that can only ever be
empty is one a client keeps rendering affordances for. `prereqs` went with the
feature.

`jobPR`: `url` (string, always), `state` (string, always: `open` \| `merged` \| `closed`). The PR number for display derives from the url's trailing `/pull/<n>`.

`jobStackLink` (jobstack.go): `url` (string, always — `http`/`https` only, no credentials; the server refuses anything else), `label` (string, always, may be `""` — the cell's short name, capped at 24 runes server-side), `status` (string, always: `up` \| `down` \| `unknown` — the daemon's loopback probe's last verdict; `unknown` from registration and forever for a non-loopback host, which is never dialled; decode tolerantly, a future word is possible), `at` (number, always, unix millis — registration time). The chip label is `⇱ <label> :<port>` with the port taken from the url (scheme default when absent); a `down` link renders muted and struck through but stays clickable, since the probe can be one tick stale after a restart.

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
is true), `kind` (string, omitempty, additive — job 612: `teammate` for the
crew proper, else the background task riding the roster: `subagent` (an
in-process Agent-tool spawn; `name` is its description, `role` its agent
type, `model` its alias), `monitor` or `shell` (`name` is the command's
description, `role` repeats the kind, `model` is empty); a task is always
`working`, since the server has already pruned the dead ones; absent on a
pre-612 server, in which case every entry is a teammate).

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
| `answer` | string | omitempty | the operator's answer once posted; absent means still open |
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
thread is a fork of zeus' session, of one of two kinds: a **pane** thread runs
as a second claude in its own tmux window of the same cell; a **headless**
thread (an "ask thread") has no window and runs one `claude -p` per question,
read-only, and may be opened from another node — see `POST /api/node/action`.
`kind` says how a thread runs and `role` says what it is for; they are
independent, except that a headless thread is always `role: "ask"`):

| key | type | optionality | notes |
|---|---|---|---|
| `tid` | string | always | `t<digits>` counting from `t1`, unique per job and never recycled; the target every op below takes |
| `title` | string | omitempty | the operator's name for the thread; absent for one branched without a title — clients fall back to the tid |
| `state` | string | always | `live` \| `closed`. `closed` = the tmux window is gone; the row (and the transcript it points at) survives, and `revive` resumes the same claude session |
| `pane` | string | omitempty | the live thread's tmux pane, e.g. `%42` — usable with `GET /api/peers/capture`. Cleared on close (tmux recycles pane ids), so it is absent for a closed thread rather than stale |
| `model` | string | omitempty | the tier this thread was branched on — `haiku` \| `sonnet` \| `opus` \| `fable`, the alias rather than the resolved id. Absent for a thread branched with no pick (and for every thread branched before the field existed), which falls back to the job's `model` and then kraken's default; a revive re-reads this row, so a thread never changes tier mid-conversation |
| `role` | string | always | `ask` (an ordinary side thread — every row branched before schema v33 reads back as `ask`) \| `mind` (the job's mind thread, branched by `POST /api/mm/capture`: the one thread allowed to write the operator's mental model). Clients show a `mind` chip. Additive |
| `created_at` | number | always | unix millis the thread was branched |
| `closed_at` | number | omitempty | unix millis the thread was closed; absent (0) while live |
| `kind` | string | always | `pane` \| `headless` (schema v36). Every thread from before headless threads existed is `pane`, in either role. A headless thread never has a `pane`; `closed` means it was closed, not that a window died. Not to be confused with `role`'s `ask`, which a pane thread carries too. Additive |
| `origin` | object | omitempty | who opened a headless thread: `{"node_id": string, "label": string, "login": string}` — the node's tailscale stable id and label and the person's login, all three always present once the object rides. Absent when the row names nobody (every pane thread). Compare `node_id` with the board's own `node_id` to tell this node's threads from another node's. Additive |
| `answering` | bool | omitempty | `true` while a question is in flight on a headless thread; absent otherwise. Additive |
| `note` | string | omitempty | the last ask run's failure line (`ask failed: …`, `ask timed out after 5m0s`, `ask cancelled`, `answer interrupted — kraken serve restarted`); absent after a clean answer. Additive |

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
BLOCKED job: how much effort the operator must spend to unblock it, and how big the
claude work that reply buys):

| key | type | optionality | notes |
|---|---|---|---|
| `unblock` | number | always | 1..10 — the operator's cost to unblock. 1 = a mechanical action; 5 = moderate thought; 10 = deep thought, many decisions, lots of context |
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
exactly the card the feature exists for: zeus estimates, the operator answers, the cell
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
TUI's `s` key and the daemon's own dream starts.

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

One endpoint, three operator-facing forms distinguished by which fields are
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
  as currently served, note, the operator's session, submit timestamp). Works for
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
| `note` | string | always | the operator's note text |
| `at` | number | always | unix millis, submit time (immutable — an edit does not restamp it) |
| `addressed` | string | omitempty | the nightly dream pass' account of the durable fix it shipped for the note — that pass is its only writer, so absent means "no durable fix has shipped", not "nobody handled it" |
| `addressed_at` | string | omitempty | RFC3339 |
| `snapshot_url` | string | always | `/api/feedback/snapshot?id=<id>` — always present; a row whose frozen blob is empty 404s when fetched (the sweep excludes blobs, so it cannot know which) |

Errors: 400 (missing/non-numeric `since`), 500 `no store wired`. A GET is a
read, so it stays reachable under `--mirror`.

### `GET /api/feedback/snapshot?id=<row id>`

Not JSON — serves the raw bytes a feedback row froze at submit (or last edit)
time: the artifact exactly as the operator was looking at it when they wrote the note.
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

**A watch item is a row in its own `watch_items` table.** The observation is
the row's own sentence; the same row carries the dream-loop workflow and the operator's
judgement. It spent one release as a knowledge-graph EDGE with a thin `kg_watch`
overlay before moving back into its own table; the knowledge graph itself has
since been removed. Two consequences for a client:

- **`id` is a WATCH-ROW id**, not an edge id. It is opaque to the app, and the id
  both writes below take. Ids from before the move do not survive it; a client
  holding one refetches, exactly as it would after any restart.
- **A CLOSED item leaves the list.** `remove` stamps `expired_at` rather than
  deleting the row, and every read filters to live rows — so to a client an
  item still simply disappears, which is what a removal has always looked like.

Nothing else moved: the keys, their json names, their types and their
optionality are byte-for-byte what they were before EITHER move, so the DREAM
view and the iOS app decode this unchanged and **`Models.swift` needs no
change**. `src`/`dst`/`predicate` are deliberately NOT on this wire — adding a
key to an existing shape is a contract bump, and nothing on either client
renders them.

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
| `id` | number | always | the watch row's id |
| `observation` | string | always | what the pass noticed, whitespace-collapsed to one line |
| `class` | string | always | `feedback`\|`correction`\|`repetition`\|`waste`\|`prompting` |
| `state` | string | always | `watching`\|`shipped`\|`declined` |
| `sightings` | number | always | a repeat folds into the existing item and increments this, rather than adding a second one |
| `first_seen` | number | always | unix millis |
| `last_seen` | number | always | unix millis |
| `evidence` | string | omitempty | JSON sample of the most recent occurrence, replaced wholesale on each sighting. Local-only — it never enters git |
| `suggested_fix` | string | omitempty | the agent's proposed correction |
| `pr_url` | string | omitempty | |
| `updated_at` | number | always | unix millis — the AGENT's last write; a verdict deliberately does not bump it |
| `verdict` | string | omitempty | operator-owned: `fine`\|`fix` |
| `comment` | string | omitempty | operator-owned |
| `commented_at` | string | omitempty | operator-owned, RFC3339, stamped server-side |

The row's columns have two owners, and the two writes below are split to
match — the store enforces it column by column (`UpdateWatchItem` never
writes the operator's trio; `SetWatchVerdict` never writes anything else).

### `POST /api/watchlist`

**The operator's write** — the board's DREAM view judging an item. Web-UI/iOS-facing.
Nothing else: it cannot express an observation edit, and a request that even
MENTIONS an agent-owned key (`observation`, `class`, `state`, `sightings`,
`first_seen`, `last_seen`, `evidence`, `suggested_fix`, `pr_url`,
`updated_at`, `src`, `dst`, `predicate`) is refused with a 400 rather than
having it silently dropped. `strength` is no longer on that list: it was the
edge's confidence, and the standalone row has no such column.

| key | type | optionality | notes |
|---|---|---|---|
| `id` | number | required | the watch row's id |
| `verdict` | string | required | `fine`\|`fix`, or `""` to CLEAR the judgement |
| `comment` | string | omitempty | trimmed |

`commented_at` is stamped server-side in RFC3339 and is never accepted from
the client — the store verb takes no timestamp, so there is nothing a caller
could send. An empty `verdict` **and** an empty `comment` reopens the item —
the documented undo.

Response: `{"ok": true}`. Errors: 400 (missing/zero `id`, a verdict outside
the set, an agent-owned key), 404 (unknown item — an id naming no row, **or**
a row that has been CLOSED, which every read already hides), 413 (body over
1MB), 500. Gated under `--mirror` with the standard plain-text 403.

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
| `id` | number | required for remove | the watch row's id |

`watchItemWire` — every key optional on the wire; the handler validates per op:

| key | type | notes |
|---|---|---|
| `id` | number | required on `update`, ignored on `add` |
| `observation` | string | `add` only, required, non-blank after whitespace collapse. **Refused on `update`** |
| `class` | string | required on `add`, one of the five classes above; optional on `update` |
| `src` / `dst` | string | `add` only, OPTIONAL — entity names describing what the observation runs between. They were required while a watch item was an edge (a new fact needs two endpoints); a row does not. **Refused on `update`**, ignored on a fold |
| `predicate` | string | `add` only, optional, defaults to `concerns`. **Refused on `update`** |
| `state` | string | one of the three states; blank defaults to `watching` on add, and leaves the current state alone on update |
| `sightings` | number | blank/zero on update leaves the current count alone |
| `first_seen` / `last_seen` | number | unix millis; blank/zero leaves the current values alone — the store's update is partial per column, so a restatement of one field never zeroes the rest |
| `evidence` | string | JSON sample, replaced wholesale |
| `suggested_fix` | string | |
| `pr_url` | string | |

**`add` is fold-or-create.** If a LIVE row already states this observation —
matched on the whitespace-collapsed sentence — the add FOLDS: `sightings`
increments, `last_seen` moves, `evidence` is replaced, and
`class`/`state`/`suggested_fix`/`pr_url` are applied when sent. `src`/`dst`/
`predicate` are ignored: the observation already has them, and letting a re-add
move them would rewrite history on the strength of one night's phrasing.
Otherwise a new row is inserted, with `state` defaulting to `watching` and
`sightings` 1.

The PROMOTE branch is gone with the edge model — there is no longer a separate
population of facts an observation could be adopted from.

**`update` writes the workflow columns only.** The observation and its
endpoints are refused with a 400 rather than ignored: the fold matches on the
sentence, so an accepted reword would silently split one tracked pattern in
two. Remove and re-add to reword.

**`remove` CLOSES the row.** `expired_at` is stamped; the row leaves every read
but stays readable by id, the operator's verdict on it included — no watch row is ever
deleted. Closing a closed row is a 200, not a 404: the caller asked for it to
be gone and it is gone. A later add of the same wording therefore creates a
FRESH row rather than reviving the closed one, since the fold matches live rows
only.

Responses:

| op | body |
|---|---|
| `add` | `{"ok": true, "id": number, "folded": bool}` — `id` is the row id, whether created or folded into; `folded` tells the caller its observation was a repeat |
| `update` | `{"ok": true, "id": number}` |
| `remove` | `{"ok": true, "unwatched": true}` — spelled out because "remove" is narrower than it reads: the row is closed, not destroyed |

The id on `add`/`update` is why those aren't the standard `{"ok": true}`: a
caller that just watched something has no other way to learn its id. These
three shapes are CLI-facing only (the iOS app never calls this route), so the
added keys are not a contract bump.

Errors: 400 (unknown op, missing item, blank observation, bad class or state,
an immutable key on update, missing id on update/remove, an operator-owned key),
404 (an id naming no row, on update/remove/verdict — and, on update and
verdict only, a row that has been CLOSED, which every read already hides),
413 (body over 1MB), 500. Gated under `--mirror`.

### `GET /api/dream`

The board's DREAM view: the night reports, newest first. **Additive, no
contract bump.** A report is the `00-dream-report.md` artifact of a job whose
slug starts `dream-`, resolved through the same cell ∪ store artifact
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
| `text` | string | required for `send` and for a headless `branch`; optional for a pane `branch` | |
| `model` | string | optional for `branch`, ignored by every other op | `haiku` \| `sonnet` \| `opus` \| `fable` — the tier the new thread's claude runs on, matched EXACTLY (not trimmed, not case-folded). Omit or send `""` for the pre-existing behaviour: the job's own `model`, then kraken's default. Anything else is a 400. The web board's branch overlay sends it; the iOS client has no picker, which is why `ThreadActionRequest` in Models.swift carries no `model` field |
| `role` | string | optional for `branch`, ignored by every other op | `ask` (default when omitted or `""`) \| `mind`, matched exactly; anything else is a 400 `bad role — want ask or mind`, nothing spawned. Stored on the row and kept across a `revive`. Clients normally reach a mind thread through `POST /api/mm/capture` rather than this field |
| `headless` | bool | optional for `branch`, ignored by every other op | how the thread runs, independent of `role`. With `role` `ask` (or omitted): absent or `true` opens a **headless** thread, `false` a **pane** thread exactly as before headless threads existed. With `role` `mind`: absent or `false` (a mind thread is always a pane); `true` is a 400 `a mind thread cannot be headless`. Additive |

- **`branch`** (headless, the default for `role` `ask`) — `{slug, op:"branch", title, text, model?, headless?: true}`:
  opens a headless thread whose origin is this node and asks `text` as its
  first question: one `claude -p` process forks zeus' session under a
  brand-new session id, with read-only tools (Read, Grep, Glob, WebFetch,
  WebSearch) and the cell's worktree as its cwd, and the question on stdin.
  The reply comes as soon as the process has started, with the row's
  `answering` true; the answer lands in the thread's transcript
  (`GET /api/thread/read`). `text` is REQUIRED here (400 `missing text`). A
  title over 120 characters (after trimming) is 400 `<slug>: ask title is too
  long (max 120 characters)`, and a question over 16384 bytes is 400 `<slug>:
  ask question is too long (max 16 KB)` — the same two bounds on every route
  that asks or sends. Further refusals: the pane branch's own (no live cell,
  no forkable zeus, bad model). The 5-thread cap (409 `ask thread cap reached
  — close one first`) counts only live headless threads opened by OTHER
  nodes and binds only them, so another node can never block the owner's own
  branches.
- **`branch`** (pane) — `{slug, op:"branch", title, text?, model?, role?, headless:false}` (or `role:"mind"` with `headless` absent): forks zeus' session
  into a new claude in a fresh window of the same cell, in the same worktree, under
  a brand-new session id. `text` is the seed prompt and is OPTIONAL — branching
  with nothing to say yet is legitimate, and the thread comes up idle. `model` is
  the thread's own tier and is likewise optional; it is stored on the row, so a
  later `revive` comes back on the same one. `branch` is
  the only op whose success body carries the new `tid`, headless or not.
- **`kill`** — `{slug, op:"kill", tid}`: kills the thread's window and marks
  the row closed. The ROW SURVIVES: its session id still locates the
  transcript, so the conversation stays readable and revivable. On a headless
  thread: kills any question in flight (its note becomes `ask cancelled`) and
  closes the row; no window is involved.
- **`revive`** — `{slug, op:"revive", tid}`: resumes the SAME claude session
  in a fresh window (no re-fork). Refused if the thread is already live, or
  if the job's cell is gone — reactivate the job first. On a headless thread:
  reopens the row only (nothing is spawned until the next `send`); refused
  with `thread <tid> of <slug> is already live` on a live one, and with the
  5-thread cap's 409 when the thread was opened by another node and that
  node's share of the cap is full.
- **`rename`** — `{slug, op:"rename", tid, title}`: retitles the row; never
  touches the pane, so it works on closed threads too.
- **`send`** — `{slug, op:"send", tid, text}`: types a message into the live
  thread's pane, through the same verify-before-acting guard `k s` uses. On a
  headless thread: asks `text` as this node's operator by resuming the
  thread's own session headlessly, replying `{"ok": true}` as soon as the run
  has started (before the answer exists); 409 `store: thread is already
  answering` while a question is in flight, 409 `store: thread is closed` on a
  closed one.

Response: `{"ok": true}`, plus `"tid"` on a `branch`. Errors: 400 for a
missing required field, an unrecognized `op` (the message names every valid
form) or a `branch` `model` outside the four aliases (`bad model — want haiku,
sonnet, opus or fable`; nothing is spawned), 404 for an unknown slug or an
unknown `tid`, 409 for a refusal about
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
| `live` | bool | always | the row is `live` AND either still has a pane or is a headless thread — i.e. there is somewhere to send to; gate the composer on this |
| `pane` | string | always | the live thread's pane (`""` when closed) — feed it to `/api/peers/capture` to mirror the window |
| `fork_uuid` | string | always | the transcript uuid the fork hangs off (`""` when none was recorded) |
| `source` | string | always | `transcript` when the fork's transcript was readable, `none` when there is nothing to read yet |
| `session` | string | always | the thread's OWN claude session id (`""` before one is recorded) — what a mind-button capture of one of this thread's blocks sends as the transcript source's `session`. Additive (schema v33) |
| `kind` | string | always | `pane` \| `headless`, as on `webThread`. Additive |
| `answering` | bool | always | `true` while a headless thread has a question in flight; poll until it clears. Additive |
| `note` | string | always | the last ask run's failure line, `""` when none (see `webThread.note`). Additive |
| `origin` | object | omitempty | as on `webThread`: `{"node_id", "label", "login"}`, absent when the row names nobody. Additive |

Each block is `/api/peers/read`'s shape — `kind` (string, always:
`user`\|`text`\|`thinking`\|`tool`\|`teammate`), `text` (string, always),
`from` (string, omitempty — teammate blocks only) — plus:

| key | type | optionality | notes |
|---|---|---|---|
| `inherited` | bool | omitempty | `true` on the pre-fork turns this thread inherited from zeus' session |
| `uuid` | string | always | the transcript RECORD the block came from (several blocks can share one); `""` when the record carried none. What a mind-button capture of the block sends as its transcript source's `uuid`. Additive |

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

`text` is delivered to zeus' pane verbatim — no `"from <operator>: "` prefix — so
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

## Goals

Two routes over the `goals` table (internal/api/goals.go), plus the additive
keys they put on `/api/board` (`goals` on the board; `goals` and its head
`goal` on a job — all documented above). A goal has a **scope** (`week` or `day`) and a
**period**: for a day goal, a local `2006-01-02` date; for a week goal, the
local **Monday** of that week in the same layout. `store.GoalPeriods(now)`
derives both from the clock, and "current" everywhere below means that pair.
A day goal may name a week goal as its `parent_id` — that is the only legal
parent link (a week goal never has one, and a day goal can only point at a
week goal).

Both routes are additive: nothing about them bumps `apiContract`.

### `GET /api/goals[?slug=]` → `{"goals": {"week": […], "day": […]}}` (api/goals.go)

The current week's and today's goals. Both arrays are always present and
never `null`; each item is the full goal object:

| key | type | optionality | notes |
|---|---|---|---|
| `id` | number | always | the row id — what `update`/`remove` and `POST /api/job/goal` address |
| `scope` | string | always | `week` \| `day`. Render an unrecognized value from a newer server as "neither of my two groups" rather than failing |
| `period` | string | always | `2006-01-02`; a week goal's is a Monday |
| `text` | string | always | |
| `done` | bool | always | |
| `position` | number | always | insertion order within (scope, period); the arrays arrive already ordered by it, so a client re-sorting is redundant, not wrong |
| `parent_id` | number | omitempty | the week goal a DAY goal hangs under. Absent (never `0`) when it hangs under nothing — `0` is the store's no-parent value and is omitted on the wire |

With `?slug=<slug>` the response gains two more keys, and only when that job
links at least one goal:

| key | type | optionality | notes |
|---|---|---|---|
| `job_goals` | array (`[{"id","text","done"}]`) | present only when the job links ≥1 goal | every goal that job links, in link-position order, each from ANY period and done or not — deliberately NOT restricted to the current periods the two arrays are, because the question `?slug=` asks is "what is this job's goals", which last week's goal answers just as well |
| `job_goal` | object (`{"id","text","done"}`) | present only when the job links ≥1 goal | the PRIMARY link — durably `job_goals[0]`, present under exactly the same condition. Retained for clients that predate multi-goal links |

An unknown slug is a 404. GET is reachable in mirror mode (read-only kraken
mirrors serve it); the POST below is not.

The iOS app does not call this endpoint — the board payload's `goals` key
carries the same two arrays on the poll it already makes, so the drawer
needs no second request. `kraken goals get [slug]` is this route's CLI face
and prints its body verbatim.

### `POST /api/goals` → `{"ok": true}` (api/goals.go)

Every goal write, dispatched on an `op` field. **Mirror-gated**: in mirror
mode this returns 403 with the body `disabled in mirror mode` (the GET above
stays reachable). Each successful write logs a `web` event and invalidates
the board cache, so the next `/api/board` poll carries the new state — the
board stays the single source of truth and nothing is echoed here.

| `op` | body | notes |
|---|---|---|
| `add` | `{"op":"add","goal":{"scope","period"?,"text","parent_id"?}}` | `period` absent defaults to the CURRENT period for that scope. `text` is trimmed and must be non-empty. `parent_id` is legal only on a `day` goal whose parent exists and is `week`-scope |
| `update` | `{"op":"update","id":N,"text"?:string,"done"?:bool,"parent_id"?:number}` | absent field = unchanged (this is a partial update, not a replace); an explicit `"parent_id":0` CLEARS the parent. Unknown id → 400 |
| `remove` | `{"op":"remove","id":N}` | deleting an absent id is a no-op and still 200. Deleting a week goal leaves its day children in place with their parent cleared, and a job linked to it keeps reading cleanly with no goal |

Errors: 400 with the store's own validation sentence as the `error` body (a
scope that is not `week`/`day`, a period that is not `2006-01-02`, a `week`
period that is not a Monday, empty text, an illegal parent), 403 in mirror
mode, 405 for other methods.

The iOS app sends exactly ONE of these shapes — `{"op":"update","id":N,
"done":bool}`, modeled as `GoalUpdateRequest` (Models.swift) and posted by
`APIClient.setGoalDone` from the board's goals drawer. Adding, editing and
deleting goals are web-only in v1 by design, not by omission: goals are
composed at the Mac and the phone is for ticking them off.

### `POST /api/job/goal` → `{"ok": true}` (api/goals.go)

Links a job to a goal, unlinks one, or clears every link. Its own one-field
route rather than a `saveform` field for the same reasons `POST
/api/job/color` has one: it works on a LIVE cell, and it is not a full
replace, so a client that never sends it cannot clear the links.

Request: `{"slug": string, "goal_id": number, "op": string}` — one job may
link several goals, so the write says which way it goes:

| body | effect |
|---|---|
| `{"slug","goal_id":N>0,"op":"add"}` | appends that link (already linked is a no-op that keeps its position) |
| `{"slug","goal_id":N>0,"op":"remove"}` | drops that link (not linked is a no-op); the surviving links keep their positions |
| `{"slug","goal_id":0}` | clears ALL links, whatever `op` says — the legacy shape, unchanged |
| `{"slug","goal_id":N>0}` with no/unknown `op` | 400 naming the valid ops: a non-zero id with no direction is ambiguous, and guessing one would silently rewrite a link set |

Response: `{"ok": true}`; the new links ride back on the next `/api/board`
poll as `webJob.goals` (and its head as `webJob.goal`). Errors: 400 (bad
json, a non-zero `goal_id` naming no goal, or the missing/unknown `op`
above), 403 in mirror mode, 404 (no such job), 405 for other methods.

The write touches NEITHER the job's `rev` NOR its `updated_at` — the same
property `POST /api/job/color` has, and for the same reason: a goal link is
metadata about the job, not an edit of it, and bumping `rev` would make an
open editor's next save conflict over a field the editor never touched.

Web-UI-only: the iOS app has no goal picker (this route is not modeled in
APIClient.swift), though `webJob.goal` and `webJob.goals` are both modeled
in Models.swift and drawn on the card.

---

## Artifact library

Four routes over the `saved_artifacts` table (internal/api/library.go,
internal/store/saved.go): the artifacts the operator asked to KEEP. A library entry
is a **frozen copy** of an artifact's bytes as they stood at save time, not a
bookmark — agents rewrite artifacts in place and cell dirs are deleted at
teardown, so an entry that followed the live file would change under the operator and
eventually 404. The copy is addressed by its own row id and survives the job
it came from (the row carries no foreign key onto `jobs`, the `job_tokens`
survival rule), archiving and hard delete included.

All four routes are additive: nothing about them bumps `apiContract`, and
nothing about them touches `/api/board` — no key on a job object, no board
cache interaction. The library is its own view with its own poll.

### `GET /api/library` → `{"items": [LibraryItem]}` (api/library.go)

The whole library, newest first — `saved_at` descending, `id` descending as
the tiebreak. `items` is always present and never `null`; a client renders
"nothing saved yet" from an empty array. Clients render the served order and
do not re-sort.

| key | type | optionality | notes |
|---|---|---|---|
| `id` | number | always | the row id — what `POST /api/library/remove` addresses and what the artifact URL below carries |
| `slug` | string | always | the job's DISPLAY slug as it stood at save time (an archived job's numbered archive name, an active job's own slug). Display only — identity is the row id, and the job may since have been renamed, archived or deleted |
| `name` | string | always | the artifact's path within its job, slashes intact |
| `saved_at` | number | always | unix millis, stamped server-side at save |
| `url` | string | always | `/api/library/artifact/<id>/<name>` — the FROZEN bytes, never the live artifact |

Reachable in mirror mode (a read is a read); the two POSTs below are not.

### `POST /api/library` → `{"ok": true, "id": number}` (api/library.go)

Freezes an artifact's CURRENT bytes into the library. Request:
`{"slug": string, "home": string, "name": string}` — the same triple
`POST /api/feedback`'s create form takes, and `home` plays the same role:
`archive` resolves the slug in the archived-jobs namespace, anything else in
active jobs, so an archived `foo-2` and an active job slugged `foo-2` are
different files. The bytes come from the same resolution the artifact route
serves from, so a saved copy and a feedback snapshot taken at the same
moment are byte-identical.

Saving an artifact that is already in the library **replaces** it under the
same `id`: one entry whose bytes and `saved_at` move forward, never a second
row.

Response carries the row `id` so a client that has just saved can address the
entry without waiting for its next poll. Errors: 400 (bad json, an
unrecognized field, or a missing/empty `slug`, `home` or `name` — the
refusal names the one that is missing), 403 in mirror mode with the body
`disabled in mirror mode`, 404 (no such job, or no such artifact), 405 for
other methods, 413 for a body over 1 MB.

### `POST /api/library/remove` → `{"ok": true}` (api/library.go)

Drops one entry. Request: `{"id": number}` — the row id, never
`(slug, name)`: a saved artifact outlives its job, so a removal keyed on the
job would stop working exactly when the library is most useful.

Errors: 400 (bad json, an unrecognized field, or an `id` that is absent or
≤ 0), 403 in mirror mode, 404 for an id naming no row — deliberately not a
silent 200, because the remove affordance sits on a row the caller is
looking at, so "already gone" means that view is stale — 405 for other
methods, 413 for a body over 1 MB.

### `GET /api/library/artifact/<id>/<name>`

Not JSON — serves the saved entry's **frozen** bytes with a `Content-Type`
picked by extension (the same mapping `GET /api/artifact/...` uses) and
`Cache-Control: no-store`. Lookup is by `<id>` alone; the `<name>` segment is
display and extension only, so a download lands with a real filename — the
content type comes from the STORED name, not the requested one. An unknown or
malformed id is a 404. Reachable in mirror mode.

---

## Mental model

Nine routes over the `mm_items` + `mm_code_anchors` tables
(internal/api/mm.go), in the order register.go wires them: `mm`,
`mm/snapshot`, then the seven writes `mm/decision`, `mm/artifact`,
`mm/element`, `mm/code`, `mm/note`, `mm/detail`, `mm/remove`.
**All additive — none of them bumps `apiContract`, which stays 6.** Every one
is a NEW PATH, never a key added to, retyped in, or removed from an existing
shape, so a client that predates them decodes nothing differently.

Schema v33 (the mind layer) adds ten more — `mm/show`, `mm/source`,
`mm/flags` (reads) and `mm/capture`, `mm/commit`, `mm/revise`, `mm/flag`,
`mm/flag/raise`, `mm/flag/dismiss`, `mm/flag/resolve` (writes) — plus
`GET /api/job/transcript`, and ADDS keys to the `GET /api/mm` item (`title`,
`scope`, `repo`, `dir`, `quote`, `thread_session`, `revised_at`,
`capture_source`). Also additive, so still `apiContract` 6. They are
documented under "The mind layer" below. A client reaches only `capture`,
`flags`, `flag/resolve`, `source` and `job/transcript`; `commit`, `revise`,
`flag`, `flag/raise` and `flag/dismiss` are agent-side (`kraken mm …`).

**Seven of the nine ARE modeled in `Models.swift` and `APIClient.swift`** — the
phone has full parity with the board, writes included (the operator, gate R2: "I need
ios parity even in v1 with full writes"). `GET /api/mm` decodes as
`MMPage`/`WebMMItem`/`WebMMAnchor`; the six client-reachable writes are
`APIClient.markArtifactRead`, `markElementRead`, `markDecision`, `markDetail`,
`addMMNote` and `dropMM`, each answering the shared `MMCreated` (`{id}` or
`{id, already}`) except the last, which answers `{"ok": true}`.

The other two are **deliberately absent from the app**. `POST /api/mm/code`
takes an absolute WORKTREE path, a repo-relative file and a start row, which is
the Zed capture task's world — a phone has no checkout to name. And
`GET /api/mm/snapshot` has no Swift model and no `APIClient` verb. It serves
bytes rather than JSON, and it means something for exactly one source —
`artifact`, whose item carries the artifact's NAME as its content and the bytes
only behind that route — so rendering it needs an artifact viewer over a
non-artifact URL. The web pane links it out to a browser tab; the phone's pane
does not offer the frozen copy at all in v1, which is a real gap rather than a
decision: a whole-artifact row there shows what was marked and when, not what
it said.

What the app renders, and where:

| surface | file | what it does |
|---|---|---|
| MODEL pane — the Memory tab's second segment beside Library (`MemoryRootView.swift` is the tab root) | `MentalModelView.swift` | every mark newest-first, kind chips with counts (all / decisions / read / notes / dead), a note quick-add, rows expanding to the frozen content — an `artifact-element` item through the app's NATIVE markdown pipeline (`MarkdownText`) so a marked table renders as a table, its headline being that block's first non-empty line truncated at 80 — the board's `elementHeadline` rule, held in step — and a ✕ per row. An invalidated row shows its reason and its ✕ is DISABLED rather than absent |
| ✓ read / mark selection | `ArtifactView.swift` (the pager's toolbar, beside ☆ save) | with nothing selected it is the whole-artifact toggle, pressed from the live `artifact` item and dropping through `/api/mm/remove`; with a non-empty selection in the web view it becomes the mark-selection form and posts `/api/mm/element` with the RENDERED text, exactly as the board does — the server locates the source span — then clears the selection on a mark that landed. The selection form is offered only on a markdown or plain-text artifact, never on an `.html`/`.svg`/`.pdf`/image page, where a selection would be text out of the server's own markup. The board's equivalent gate is its `md`-class check, which is stricter still — it excludes plain-text bodies too, where the phone allows them |
| ◆ decision | `JobDetailView.swift` (`DocQuestionCard`) | on ANSWERED question rows only, since an unanswered one is a 400. Idempotent, so it stays pressable once decided |
| detail mark | `JobDetailView.swift` (the Detail section) | marks the cell's rendered detail line, whose text exists nowhere else |

Two notes on the parity, both deliberate. **Table corner-✓ affordances are not
on iOS**: the web board injects a control into every `<table>` of its own
rendered body, which on the phone would mean injecting controls into a
WKWebView — the selection path covers the same ground (select the table, mark
it) and per-table controls are a follow-up. And every toggle in the app paints
from SERVER state with a refetch after each write, never from a local latch,
which is what lets the daemon's verify pass retire a mark behind the app's
back and have the control drop back to unmarked on the next read.

A mental-model item is one thing the operator has **read, decided or noted**,
frozen at the moment they marked it. The freeze is the whole feature: an artifact mark
keeps a SNAPSHOT of the bytes, an element mark keeps the element's own text,
and a code mark keeps the blob's text plus the commit and line range it was
read at. A daemon pass then asks whether what they read is still there, and when
it is not the item is **invalidated one-way** — never deleted, never
resurrected — so the frozen copy is what the board renders as "what you knew"
against what is there now.

The mental model is **the operator's**. Agents never write it: no pipeline skill
documents these routes, and the write verbs (`k mm mark|note`) are for the
operator's own shell and their Zed capture task. It is off the board's hot payload
entirely — no key on a job object, no board-cache interaction — with its own
view and its own poll, exactly the artifact library's posture.

### `GET /api/mm[?live=1][&file=<path>[&worktree=<abs>]]` → `{"items": [MMItem]}` (api/mm.go)

The whole model, newest first — `created_at` descending, `id` descending as
the tiebreak. `?live=1` narrows to items that have not been invalidated;
anything else means EVERYTHING, invalidated included, because those are half
the point. `items` is always present and never `null`.

`?file=` (additive, contract unchanged) narrows to CODE marks whose anchor
names that repo-relative path — the read an agent makes before explaining a
file, citing it, or briefing a diagram over it. `?worktree=` (an absolute
local path) resolves the repo half of the identity through the same rule the
mark route uses, so a filtered read from any checkout of a repo matches marks
made in any other; without it the path matches alone, with the combined
`repo/path` spelling accepted. `file` also returns dir-scoped captured items:
a LIVE item whose `repo` is the one `?worktree=` resolves to and whose `dir`
is the file's directory or an ancestor of it (`""` covers every file;
`internal/jobs` covers `internal/jobs/cell.go` and `internal/jobs/x/y.go`;
`internal/job` does not — the comparison is per path segment). Without
`?worktree=`, or for an item with no `repo`, only the code-anchor match
applies. `kraken mm get --file <path>` is the CLI front and does the worktree
resolution itself. iOS does not send these params.

`?q=`, `?repo=`, `?dir=`, `?scope=` (schema v33, additive, contract unchanged)
pass straight into the store's filter, and an empty one is no filter: `q` is a
case-insensitive substring over `title`, `content` and `quote` (any one
matching qualifies); `repo` is exact; `dir` matches an item whose `dir` is that
directory OR any ancestor of it (`dir=a/b` matches items at `a/b`, `a` and the
repo root `""`); `scope` is exact (`universal`\|`repo`). They compose with
`live` and `file`. `kraken mm get --q/--repo/--dir/--scope` sends them
verbatim; the web Model pane binds its filter box to `q`.

| key | type | optionality | notes |
|---|---|---|---|
| `id` | number | always | the row id — what `POST /api/mm/remove` and `GET /api/mm/snapshot` address |
| `kind` | string | always | `decision`\|`reading`\|`note` |
| `source` | string | always | `question`\|`artifact`\|`artifact-element`\|`code`\|`agent-output`\|`manual`\|`capture` (`capture` = committed by a mind thread, schema v33). Stays a STRING: the capture's source pointer rides `capture_source` below, because retyping this key would break every shipped build that decodes it as `String` |
| `slug` | string | always | the job's DISPLAY slug at mark time, or `""` for the two sources that belong to no job (`code`, `manual`). Display only — the job may since have been renamed, archived or deleted |
| `source_ref` | string | always | what was marked, per source: a `Q<n>` for a question, the artifact name for an artifact or element, `<repo>/<path>:<start>-<end>` for code, the first 16 hex of the text's sha256 for an agent detail, `""` for a note |
| `content` | string | always | served WHOLE, never cut — it is what the operator read, and the invalidated view exists to show it back in full. For an artifact mark it is the source ref: an artifact's content is its snapshot, which rides its own route |
| `context` | string | always | the question's context, the note the operator attached to a code mark, or `""` |
| `created_at` | number | always | unix millis |
| `episode_id` | number | always | legacy: the knowledge-graph episode once minted with the item. Still always emitted, and `0` for every row created since the graph was removed (older rows keep their old value). Shipped iOS builds decode it non-optional, which is why the key stays; current builds decode it as `Int64?` and read nothing from it |
| `invalidated_at` | number | omitempty | unix millis. **The absence of these three keys is the wire's only spelling of "live"** |
| `invalid_reason` | string | omitempty | `removed` (the operator unmarked it), `content-changed`, `artifact-deleted`, `file-deleted` |
| `invalid_detail` | string | omitempty | free text |
| `anchor` | object | omitempty | present on `source: "code"` only — the table below |
| `title` | string | always | schema v33: the one-line claim the rules files carry; `""` on every legacy row (clients fall back to the first line of `content`) |
| `scope` | string | always | `universal` \| `repo` (legacy rows read `repo`) |
| `repo` | string | always | the repo a `repo`-scoped item belongs to; `""` for universal and legacy rows |
| `dir` | string | always | repo-relative directory, `""` = the repo root |
| `quote` | string | always | the verbatim text the operator highlighted when capturing; `""` on legacy rows |
| `thread_session` | string | always | the mind thread's claude session that committed the item; `""` on legacy rows |
| `revised_at` | number | always | unix millis of the last `POST /api/mm/revise`; `0` = never revised |
| `capture_source` | object \| null | always | the parsed source pointer (`CaptureSource`, table below), or `null` when the row has none — every legacy row. Decode as optional |

`CaptureSource` (the same object `POST /api/mm/capture` takes as `source`):

| key | type | optionality | notes |
|---|---|---|---|
| `kind` | string | always | `artifact` \| `transcript` \| `dir` \| `item` \| `manual` |
| `slug` / `home` / `name` / `sha` | string | omitempty | `artifact`: the job, artifact home (`cell`\|`pluto`\|`archive`), artifact name (required for this kind) and the sha256 hex of the bytes the viewer had loaded |
| `session` / `uuid` / `tid` | string | omitempty | `transcript`: the claude session (required), the record uuid, and the side thread's tid when the text came from a thread |
| `repo` / `dir` | string | omitempty | `dir`: the repo (required) and repo-relative directory |
| `item_id` | number | omitempty | `item`: the mental-model item the text was highlighted in |

`anchor`:

| key | type | optionality | notes |
|---|---|---|---|
| `repo` | string | always | the repo's identity, from origin's basename — so a mark made in a cell worktree and one made in the primary checkout are the same identity |
| `path` | string | always | relative to the repo root |
| `orig_path` | string | omitempty | set once, the first time a verify follows a rename |
| `start_line` / `end_line` | number | always | 1-based, inclusive |
| `anchor_sha` | string | omitempty | **absent exactly when `anchor_kind` is `dirty`** — text that exists in no commit has no commit to name |
| `anchor_kind` | string | always | `origin-main`\|`merge-base`\|`head`\|`dirty`, in descending order of durability |
| `text_sha` | string | always | sha256 of the anchored text (which is the item's `content`) |
| `checked_at` | number | omitempty | unix millis; absent until the first verify pass has looked at it |
| `checked_sha` | string | omitempty | the head the last successful verify ran against |
| `check_error` | string | omitempty | a failed READ (offline, repo gone, timeout). Disjoint from an invalidation — a read that failed never says the code changed |
| `ambiguous` | bool | omitempty | the text occurs more than once; the nearest occurrence to the expected position was taken. Never an invalidation |
| `invalid_sha` / `invalid_pr` | string | omitempty | where the item died, best effort |

There is deliberately no `text` key: the item's `content` IS the anchored text,
and carrying it twice would let a client render two spellings of one thing.
Reachable in mirror mode (a read is a read); the seven writes below are not.

### `GET /api/mm/snapshot?id=N`

Not JSON — serves an artifact mark's **frozen** bytes with a `Content-Type`
picked by the stored extension (the same mapping `GET /api/artifact/...` uses)
and `Cache-Control: no-store`. Never the live artifact: the agent that owns it
rewrites it in place and the cell dir goes at teardown. An unknown id, and an
item with no snapshot, are both 404 — neither names frozen bytes. Reachable in
mirror mode.

### The seven writes

All POST-only, all gated under `--mirror` with the standard plain-text 403
body `disabled in mirror mode`, and all sharing one decode discipline: a 1 MB
cap (413), `DisallowUnknownFields` (400 naming the route's accepted field set,
never echoing the offending key), and an empty required field refused by name.

| route | request | what it records |
|---|---|---|
| `POST /api/mm/decision` | `{slug, qid}` | kind `decision`, source `question`. The server copies the row's own fields — `<text>` + `"\n→ "` + `<answer>`, plus `"\nrecommendation: …"` when there is one — rather than taking text from the client, so the record is what was actually asked and answered. An UNANSWERED question is a 400: there is no decision yet |
| `POST /api/mm/artifact` | `{slug, home, name}` | kind `reading`, source `artifact`. Freezes the artifact's current bytes as the snapshot. Same bytes as a live mark → `already`; DIFFERENT bytes → the earlier item is superseded and a new one inserted, so the list never shows two live "I read this" rows for one file |
| `POST /api/mm/element` | `{slug, home, name, text}` | kind `reading`, source `artifact-element`. `text` ≤ 32K runes, `\r\n`-normalized. The element's own text is the anchor, so a rewrite AROUND it leaves the mark alone |
| `POST /api/mm/code` | `{worktree, file, start_row, text, note}` | kind `reading`, source `code`, plus an `anchor`. `worktree` absolute, `file` relative to it, `start_row` ≥ 1. An EMPTY `text` means the single line at `start_row`, read from disk. The end row is derived from the selection's newline count. The anchor ladder then finds the text in the most durable commit that holds it — origin/main, else merge-base(HEAD, origin/main), else HEAD, else `dirty` — and on a commit rung the STORED text and coordinates are the BLOB's, never the editor buffer's |
| `POST /api/mm/note` | `{text, context}` | kind `note`, source `manual`. `text` ≤ 8K runes. Belongs to no job and anchors to nothing, and has no `already` probe: two identical notes are two things the operator thought |
| `POST /api/mm/detail` | `{slug, text}` | kind `reading`, source `agent-output`. `text` ≤ 8K runes. The steer modal's rendered detail line, whose text exists nowhere else on disk — which is why the item's content IS the record |
| `POST /api/mm/remove` | `{id}` | the operator unmarking: an invalidation with reason `removed`, never a delete |

Every successful create answers `{"id": number}`. It no longer carries an
`episode` key: nothing is minted since the knowledge graph was removed, and
`MMCreated.episode` was always optional on the client. An idempotent re-mark of
something already live answers `{"id": number, "already": true}` instead —
same id, no second row — which is what lets a client fire a mark button without
first checking whether it is already on. `POST /api/mm/remove` answers the
standard `{"ok": true}`.

Errors: 400 (bad json, an unrecognized field, a missing or empty required
field, an over-long text, a non-absolute worktree, a file path that escapes it,
`start_row` < 1, an unanswered question, an `id` ≤ 0), 403 in mirror mode, 404
(no such job, question, artifact or item), 405 for non-POST, 413 for a body
over 1 MB, 500 (no store wired, or a store write failed). Re-removing an already-invalidated item is a **200**, and the earlier
reason is left in place: the caller asked for it to be gone and it is gone.

### The mind layer (schema v33) — all additive, `apiContract` stays 6

Capture routes highlighted text to the job's **mind thread** — a side thread
(`role: "mind"`) forked from zeus — which drafts an item with the operator and
finalizes it through `kraken mm commit`. The writes share the seven writes'
decode discipline (1 MB cap → 413, `DisallowUnknownFields` → 400, nested
objects included), are POST-only (405 otherwise) and are gated under
`--mirror` (plain-text 403 `disabled in mirror mode`). The three GETs stay
reachable under `--mirror`. Every error body is `{"error": string}`.

#### `POST /api/mm/capture` → `{"tid": string, "created": bool}`

Request `{slug, text, source}` — `source` is a `CaptureSource` (above). The
server finds the job's LIVE mind thread forked at zeus' current last message
and sends it the capture (`created: false`), or branches a new one
(`created: true`); either way the client opens the thread overlay on `tid`.

Errors: 400 `missing slug`, a text over 32,000 runes, an empty text, or an
invalid source (unknown `kind`; an `artifact` with no `name`, a `transcript`
with no `session`, a `dir` with no `repo`); 404 unknown slug; 409 no live cell
or forkable zeus (codex jobs too) — show the message as a toast; 500 a store
or spawn failure.

#### `POST /api/mm/commit` → `{"id": number}`

Request `{"thread_session": string, "item": {title, scope, repo, dir, kind, body, quote, source}}`.
Agent-side only (`kraken mm commit`); no client UI sends it. 403
`{"error":"not a mind thread"}` unless `thread_session` names a live mind
thread; 400 with the reason for an invalid item (title 1–120 runes, body
1–8,000 runes, kind `decision`\|`reading`\|`note`, scope `universal`\|`repo`
with `repo` naming a resolvable repo and `dir` repo-relative). The item is
stored with `source: "capture"`.

#### `POST /api/mm/revise` → `{"id": number}`

Request `{"thread_session": string, "id": number, "item": {…same as commit}}`.
Same 403/400 as commit; 400 `missing id` for `id` ≤ 0; 404 unknown item; 409
an invalidated item; 409 an item whose `source` is not `capture` (a legacy
one-shot mark is never revised — the message names `kraken mm commit` as the
move: commit a new item instead).

#### `POST /api/mm/flag` → `{"id": number, "state": "pending"|"raised"}`

Request `{session, slug, item_id, against_id, note}` — an agent recording that
item `item_id` contradicts item `against_id` or the transcript (either id may
be 0). Born `raised` when `session` is the job's lead (zeus) session, else
`pending` until zeus raises or dismisses it. `slug` may be the job's current
slug, its cell-dir spelling (`Slugify(slug)`, with or without `-<id>`, when
exactly one active job matches), or a former slug after a rename; the server
resolves it to the current one. 400 empty note; 404 a slug that resolves to no
active job or a named item that does not exist.

#### `POST /api/mm/flag/raise`, `POST /api/mm/flag/dismiss` → `{"ok": true}`

Request `{session, id, note}` — the lead's verdict on a pending flag. 400
`missing id`; 403 when `session` is not the flag's job's lead session; 404
unknown flag; 409 the flag is not `pending`. A raise fires one macOS
notification and puts the flag in the board's Flags section.

#### `POST /api/mm/flag/resolve` → `{"ok": true}`

Request `{id, resolution}` with `resolution` `drop` \| `keep` (400 otherwise)
— the operator's answer to a raised flag: `drop` also invalidates the flagged item
(reason `contradicted`), `keep` only closes the flag. Either way the flag's
`state` becomes `resolved`. 404 unknown flag; 409 a flag that is not `raised`.
This is the Flags section's `drop item` / `keep` buttons.

#### `GET /api/mm/flags[?state=pending|raised|dismissed|resolved]` → `{"flags": [MMFlag]}`

Newest first; always an array. 400 for a `state` outside the four. The Flags
section reads `?state=raised` — a resolved flag has left that state, so every
row it gets is still awaiting the operator.

| key | type | optionality | notes |
|---|---|---|---|
| `id` | number | always | |
| `item_id` / `against_id` | number | always | `0` = none |
| `job_id` | number | always | `0` for a flag tied to no job |
| `slug` | string | always | the job's slug at flag time (`open job` opens its steer modal) |
| `session` | string | always | the session that recorded the flag |
| `note` | string | always | what contradicts what |
| `state` | string | always | `pending` (recorded, awaiting the lead) \| `raised` (awaiting the operator) \| `dismissed` (the lead rejected it) \| `resolved` (the operator answered — see `resolution`) |
| `raised_by` | string | always | the session that raised/dismissed it, `verify` for one raised by the artifact verify pass, `""` while pending |
| `raise_note` | string | always | the lead's note on raising/dismissing |
| `raised_at` / `resolved_at` / `created_at` | number | always | unix millis, `0` = not yet |
| `resolution` | string | always | `""` \| `drop` \| `keep` — the operator's |

#### `GET /api/mm/show?id=N` → `MMItem` + `source_hint`

One item in full (the `MMItem` shape above, `anchor` included, live or not)
plus `"source_hint": "kraken mm source <id>"`. 400 bad id; 404 unknown item.

#### `GET /api/mm/source?id=N` → `{kind, available, changed, text, ext}`

The material behind an item. `available: false` is an answer, never an error —
a missing source is always a 200.

| `kind` | `text` |
|---|---|
| `artifact` | the frozen snapshot when the item has one, else the artifact's current bytes. `changed` is true when the current bytes' sha differs from the capture's `sha` (or, with none recorded, the snapshot's), or the artifact is gone. Legacy `artifact`/`artifact-element` rows resolve through their `slug` + `source_ref` |
| `transcript` | ±10 blocks around the record `uuid` in the session's transcript (a forward scan of the whole file), one per paragraph as `[kind] text` (`[kind from] text` for a block with a sender), the captured record's blocks prefixed `▶ `. Unavailable when the session is not uuid-shaped, or the transcript or the uuid is not found |
| `dir` | a one-level listing of the directory in the repo's primary checkout, one name per line, directories suffixed `/` |
| `code` | a legacy code mark's anchored text; `ext` is the file's language extension |
| anything else (`item`, `manual`, `question`, `agent-output`) | none — `available: false` |

`ext` is the artifact's extension (`md`, `html`, …) or `""`. 400 bad id; 404
unknown item.

#### `GET /api/job/transcript?slug=&n=&turns=` → `{"session": string, "blocks": [TranscriptBlock]}` (api/transcript.go)

The tail of zeus' own conversation for the steer modal's transcript panel.
`turns`, when a positive integer, selects the last that many TURNS — each of
the operator's prompts and everything zeus did after it, clamped to 1…50, at most 400
blocks with the oldest whole turns dropped to fit (never a turn's head) — and
is what the board's panel sends (`turns=5`). Otherwise `n` selects the last
that many blocks, defaulting to 50 and clamped to 1…200. Each block is `{kind,
text, from?, uuid}` — `/api/thread/read`'s block shape with `uuid` always
present and no `inherited`. A job with no stamped session or no transcript yet
answers `{"session": "", "blocks": []}`. 400 missing slug; 404 unknown slug. A
read, so reachable under `--mirror`. Additive (`turns` is a new query
parameter), so `apiContract` stays 6.

---

## Recall

### `POST /api/recall` — localhost-only (not called by the iOS app or web UI; not modeled in Models.swift/APIClient.swift)

Mid-conversation context for a cell (api/recall.go, jobs/recall.go). It replaced `POST /api/graph/recall` when the knowledge graph was removed; the request body is the same. Its only caller is `kraken recall-hook`, wired by every cell worktree as a `UserPromptSubmit` hook, which POSTs its payload here and emits the response as `hookSpecificOutput.additionalContext`.

The response is up to two blocks concatenated, each `""` when it has nothing to say: the operator's **unread notes** for the job (every feedback row and answered question not yet delivered, once per job and note-as-written, addressed to the job's stamped lead session only), then the operator's **goals** (re-delivered whenever the rendered state differs from what the session last saw). A non-lead session gets the goals block or `""`. Any event other than `UserPromptSubmit` gets `""`. Nothing leaves the machine: no embedder is called.

Request:

| key | type | optionality | notes |
|---|---|---|---|
| `session_id` | string | required in practice | blank yields `""` |
| `event` | string | optional | `hook_event_name` verbatim |
| `prompt` | string | optional | accepted and ignored (the hook forwards it verbatim) |
| `transcript_path` | string | optional | accepted and ignored, as `prompt` |
| `slug` | string | optional | the cell's job slug, baked into the installed hook command; what the notes and goals blocks resolve the job by |

Response: `{"context": string}`. **`""` is a normal 200**, never an error status: the caller is a hook running inside someone else's conversation.

Additive: it never bumps `apiContract`. Gated under `--mirror` (it records what it delivered).

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
and every peer job's prereq reference derive from it —
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

`skipped` is always absent now: it named preserved `~/Documents/pluto` artifact
copies that kept the old prefix, and that integration is retired (artifacts live
only in the store, keyed by job id, so a rename moves no file). The key is kept
for shipped clients that decode it.

`notes` carries best-effort steps that FAILED, each with its cause, and never a
filesystem path. Two kinds reach it: a `tmux` convergence step on a LIVE cell that did not land, leaving the
session under its old name until the daemon reconverges it; and the one-line
notice typed at zeus failing to deliver. Kraken retries the tmux ones on its own
— except a convergence refused because the session carries another job's marker,
which the retry will refuse the same way forever and needs a look — and the
notice costs only a missing pane message, so a note is worth showing but never
worth blocking on. Every note is also written to the event log, which is
the durable record — the response is simply the only surface the operator is
looking at when they rename.

Errors: 400 (bad json; an unrecognized field; an empty `slug` or `to`), 404 (no
such job — resolved by CURRENT slug only, since renaming *by* a stale name means
the caller does not know the job's name), 409 (every refusal: a `to` that
slugifies to nothing, either name under the reserved `dream-`/`graph-` prefixes,
an archived job, a target taken by another active job or by an archived one, or
a target that is another active job's former name — the refusal text rides back
verbatim and is meant to be rendered inline), 413 (body over 1 MiB). Gated under
`--mirror` with the standard plain-text 403: this route renames a real tmux
session. **Additive, no contract
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
the operator). Both keys are still ACCEPTED in a job's raw frontmatter text
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
| `model` | string | always | model for zeus (claude `--model` / codex `-m` value), `""` = kraken's default for zeus (`claude-fable-5-1`; codex cells: `gpt-6-astra`); applied once at cell start. Governs the cell's own agents — zeus, and any side thread branched without its own `model` (see `POST /api/threads`); the teammates zeus spawns are opus regardless |
| `color` | string | always | the job's color — one of `red` \| `blue` \| `green` \| `yellow` \| `purple` \| `orange` \| `pink` \| `cyan`, or `""` for no color. Read-only here: it is written by `POST /api/job/color`, never by `saveform` |
| `agent` | string | always | which coding agent the cell's zeus runs — `codex` or `""` (the claude default). Unlike `color` it IS form-writable: `saveform` persists it, so a client can offer a claude/codex control at create/edit time |
| `spec` | string | always | |
| `mtime` | number | always | |
| `editable` | bool | always | |
| `err` | string | always | "" when clean |
| `slugs` | array of string \| null | always present key, value may be `null` | the other active jobs' slugs. It fed the prereq datalist originally and survives that feature's removal as a plain autocompletion source |
| `repos` | array of string \| null | always present key, value may be `null` | |

### `POST /api/job/saveform` → map literal (web.go `webJobSaveForm`)

Request: `jobFormFields` — `slug` (string), `repo` (string), `base` (string, optional to send — no `omitempty` tag, absence just decodes as ""), `solo` (bool), `model` (string, optional to send — model for zeus, "" = kraken's default for zeus, `claude-fable-5-1`, or `gpt-6-astra` for a codex cell), `agent` (string, optional to send — `codex` or "" for the claude default; form-writable, unlike `color`), `spec` (string, optional to send), `mtime` (number|string via `mtimeToken`), `force` (bool, optional to send — skips the mtime conflict check when true). `skip_doc` is gone (v5, breaking) and `ready`/`prereqs` are gone (v6, breaking) — see the form-GET note above.

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

### `POST /api/job/stack`

The cell's write of its STACK LINKS — the URLs of whatever it spun up for manual testing — reached only through the `kraken stack add|rm|clear` cell verb (cmd/kraken/cmd_stack.go). Its own one-field route for `/api/job/load`'s reasons (written on a LIVE cell, never through the full-replace form save), and PER-OP for `/api/job/goal`'s: a client sends what it wants changed, never the list it believes is current.

Request: `{"slug": string, "op": "add" | "remove" | "clear", "url": string, "label": string}`.

- `op: add` needs `url`, which must parse with scheme `http` or `https`, a non-empty host and no userinfo (400 `bad url: …` naming the rule otherwise). Re-adding a url the job already has UPDATES its label and keeps its `status` and `at`. The ninth distinct url is a 400 naming the cap (8 per job). `label` is optional, one-lined and truncated at 24 runes rather than refused. Refused 409 when the job has never started or is done — there is no cell to link a stack to.
- `op: remove` needs a token in `url`: matched against each link's url first, then its label; nothing matched is a 404.
- `op: clear` takes neither and is a 200 even on an empty list.
- The body is decoded with **unknown fields rejected**: `slug`, `op`, `url`, `label` and nothing else.

Response: `{"ok": true}`. The list rides back on the next `/api/board` poll (`webJob.stack`).

Errors: 400 (bad json; an unrecognized field; a missing or unknown `op`; `add` without `url` or with a refused url; `remove` without a token; the per-job cap), 404 (no such job, or `remove` of a token no link matches), 409 (`add` on a draft or done job), 413 (body over 1 MiB), 500 (write failed). Gated under `--mirror` with the standard plain-text 403; 405 is served by the route for any method but GET and POST.

Like `/api/job/load`, the write bumps NEITHER the job's `rev` NOR `updated_at`. Rows are deleted at cell teardown (`teardownCell`), so a done or reactivated job carries none. **Additive, no contract bump.**

### `GET /api/job/stack?slug=`

zeus-side read through the `kraken stack get` cell verb; the clients read `webJob.stack` on `/api/board` instead.

Response: `{"stack": [jobStackLink]}` — always an array, **empty** (never null) for a job with no links, in registration order.

Errors: 400 `missing slug`, 404 for an unknown slug. A read, so it stays reachable under `--mirror`. **Additive, no contract bump.**

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

The Slack intake bot's only write on a job — it creates a draft, or amends an existing one that has not started. Not called by the iOS app or the web UI (the operator uses `/api/job/saveform` and `/api/job/save`); its client is the `kraken job draft` cell verb.

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

### `POST /api/job/visibility` → `{"ok": true}` (api/jobvisibility.go `webJobVisibility`)

Body `{"slug": string, "visibility": "private"|"readonly"|"public"}`. Sets who on the tailnet may see or drive the job: **private** never leaves this machine, **readonly** (the default, and what every job created before v7 reads) is visible to every node and drivable by none, **public** may be driven by any node. A one-field route for `/api/job/color`'s reasons: it works on a live (working/blocked) job, and the form save never writes the field. Errors: 405 non-POST, 413 body over 1 MiB, 400 `bad json`, 400 `{"error":"unknown visibility <v>"}` (anything but the three lowercase words), 404 `no such job`, 500 write failed. Not mirror-gated (it writes a row and types nothing). The same level is hand-authorable as a `visibility:` line in the job text and rides every card as `webJob.visibility`.

---

## Nodes and pairing

Every `kraken serve` on the tailnet is a **node**. A node finds its siblings through `tailscale status`, polls their card lists into its `nodes` table, and serves them on its own board as cards carrying an `owner`. A phone binds to exactly one node by a pairing password that is proven, never sent. The **request gate** (`api.Gate`, wrapping the mux both listeners serve) classifies every request into a tier and refuses a route whose floor is above it with `401 {"error":"unauthorized"}`:

| Tier | Recognised by | Reaches |
|---|---|---|
| owner | loopback, or one of this host's own interface addresses | everything |
| paired | `Authorization: Bearer <token>`, or the cookie `kraken_pair=<token>` (for `<img>`/`<iframe>`/WKWebView loads), whose SHA-256 matches a paired device | everything except `/api/pair/password`, `/api/pair/devices`, `/api/editor/open` |
| node | any other tailnet device (tailscale status, else `tailscale whois`) | `/api/health`, `/api/node*`, `GET /api/nodes`, `/api/pair/challenge`, `POST /api/pair`, the SPA shell and assets |
| anon | anything else | `/api/health` |

A route with no policy entry under `/api/` is owner-only (fail closed). Past the tier check, the gate answers every `POST` under `/api/` that does not carry `Content-Type: application/json` with `415 {"error":"content-type must be application/json"}` — on every tier, owner included — except the two multipart uploads, `POST /api/upload` and `POST /api/background`. A JSON POST is never a CORS simple request, so a page on another origin cannot send one without a preflight this server never grants; without the rule, a script in any page could fire a text/plain POST at loopback and have it run as owner. The JSON rule alone does not cover the two multipart uploads, so the gate also answers any `POST` whose `Origin` header is present and is not this server's own (`http://<Host>`; the literal `null` an opaque-origin frame sends counts as foreign) with `403 {"error":"cross-origin post refused"}`, checked before the 415. A request with no `Origin` passes — the app, the CLI and the cell hooks send none. Every gate refusal (401, 403, 415) carries `X-Content-Type-Options: nosniff`. The app sends the bearer on every JSON call and installs the same token as the `kraken_pair` cookie for artifact and image loads. A 401 means "not paired (any more)": clear the stored record and show the pair screen.

Wire additions on existing shapes (all additive):
- `webJob.visibility` — string, always present: `private`|`readonly`|`public` (readonly when the job never set one).
- `webJob.owner` — object, **absent on every local card**: `{"node_id": string, "label": string, "operator": string, "online": bool}` naming the node a remote card runs on. A card's identity is owner plus slug — two nodes can each have a `dream-2026-10-20`.
- A remote card's `artifacts[].url` is always THIS node's proxy URL, `/api/nodes/artifact?node=<node_id>&slug=<slug>&name=<name>` (`name` last, so a `.html` name still ends the URL); the URL the owner served is never passed on, and an artifact with a blank name is dropped.
- `webBoard.nodes` — array of `webNode` (below, without `jobs`), **absent** when the registry is empty (every single-machine kraken).
- Under `KRAKEN_NODES=off` both `nodes` and every owner-chipped card are absent, whatever the registry holds.

`webNode`: `{"node_id": string, "label": string, "operator": string, "dns_name": string, "ip": string, "port": int, "build": string, "contract": int, "online": bool, "last_ok": int (unix millis, 0 = never), "last_err"?: string, "jobs"?: [webJob]}` — `jobs` only on `GET /api/nodes`, and only for a node that has been polled.

`NodeEnvelope` (a node's self-description): `{"node_id": string (tailscale stable id; "" when tailscale is unreadable), "label": string, "operator": string, "dns_name": string, "ips": [string] (never null), "port": int, "build": string, "contract": int}`.

### `GET /api/node` (node tier) → `NodeEnvelope`

The probe target a sibling's discover pass hits. `404 {"error":"fleet off"}` when this machine set `KRAKEN_NODES=off`.

### `GET /api/node/jobs` (node tier) → `{"node": NodeEnvelope, "jobs": [webJob]}`

This node's non-archived **readonly** and **public** cards — a private card is absent, not blanked. Each is the board's own card object minus exactly two keys: `owner` (the receiver stamps it from its registry) and `account` (the binding names this machine's pool login, which means nothing on another machine). Everything else rides as built — `feedback`, `tokens`, `threads`, `doc_questions`, `artifacts`, `prs`, `load`, `goals`/`goal`, `question`, `roster`, `detail`, `last_sent`, `color`, `visibility`. The `artifacts[].url` values are the OWNER's (`/api/artifact/<slug>/<home>/<name>`); a receiving board rewrites each to its own `/api/nodes/artifact?node=&slug=&name=` and never serves the owner's URL onward. `jobs` is `[]` under `KRAKEN_NODES=off`.

Readonly and public are READ alike on every node route below; only `POST /api/node/action` tells them apart, and its three ask-thread actions (`thread-ask`, `thread-send`, `thread-kill`) are admitted on both. The owner-only writes (feedback, doc-question answers, pane threads and every other thread op, goals, rename, visibility, uploads, the mental model) have no node route on any visibility. Visibility is read from the store on every request, so a flip to private refuses everything that arrives after it.

### `GET /api/node/job?slug=` (node tier) → `{"job": webJob, "artifacts": [string], "question"?: webQuestion}`

One readonly or public card (as in `/api/node/jobs`), its artifact names, and its pending AskUserQuestion when there is one. A private, archived or unknown slug all answer the identical `404 {"error":"no such job"}`.

### `GET /api/node/job/raw?slug=` (node tier)

`GET /api/job/raw`'s exact body (`{"slug","content","mtime","editable"}`) for a **readonly or public** job. `editable` is the owner's local rule; whether a client offers a save is its own remote mode's business (a save is an action, public only). `405` for anything but GET; 404 `no such job` for private, unknown, archived, an empty slug, or under `KRAKEN_NODES=off`.

### `GET /api/node/read?slug=&n=` (node tier)

A readonly or public cell's last `n` zeus transcript blocks (default 5, clamped 1–50), `/api/peers/read`'s shape: `{"blocks": [{"kind": string, "text": string, "from"?: string}], "counted": int}`. 404 as above; `409 {"error":"<slug> has no live zeus"}` when the cell has no live zeus.

### `GET /api/node/artifact?slug=&name=` (node tier) → bytes

One artifact of a readonly or public job. Refusals, in order: `405 {"error":"GET only"}` for anything but GET; 404 `no such job` (as above); `404 {"error":"no such artifact"}` when `name` is blank, is not EXACTLY the `name` of one entry of the owner's own artifact list for that job, or cannot be read — and, for a live cell, when any segment of the path is a symlink or the last is not a regular file. An artifact larger than 16 MB is `413 {"error":"artifact over 16 MB"}` and is never read whole; exactly 16 MB is served. That is the same bound the receiving node's relay applies. The bytes come from the same source the list did — the live cell's artifacts dir, else the artifacts table. EVERY reply, refusals included, carries `X-Content-Type-Options: nosniff` and `Cache-Control: no-store`; when the requested `name` is html or svg (`text/html`, `image/svg+xml`) every reply also carries `Content-Security-Policy: sandbox` (no `allow-scripts`, no `allow-same-origin`: another node's page renders with no script at all, in an opaque origin). Any other name — a PDF, an image — gets no CSP, since it cannot script and a browser's built-in PDF viewer can refuse to load inside a sandboxed document. A 200 adds `Content-Type` from the name's extension (the board's artifact mapping) and the bytes verbatim. Not mirror-gated.

### `GET /api/node/transcript?slug=&n=&turns=` (node tier)

`GET /api/job/transcript`'s exact reply (`{"session": string, "blocks": [...]}`, its own 500 included) for a readonly or public job, `n` and `turns` passed through when given. `405` for anything but GET; 404 `no such job` as above. Not mirror-gated.

### `GET /api/node/thread/read?slug=&tid=&n=` (node tier)

`GET /api/thread/read`'s exact reply for one side thread of a readonly or public job, `tid` and `n` passed through when given. `405` for anything but GET; 404 `no such job` as above; then that route's own `400 {"error":"missing tid"}` and `404 {"error":"unknown thread"}` (a `tid` of another job's thread is `unknown thread`). Not mirror-gated.

### `POST /api/node/announce` (node tier) → `NodeEnvelope`

Body: the sender's `NodeEnvelope`. Records (upserts) the sender under the address the request came from — the body's `ips` are not used — and replies with this node's envelope. Refusals, in order: 400 when `node_id` is empty; 200 with nothing recorded when `node_id` is this node's own; `403 {"error":"node id does not match caller"}` unless the caller's tailnet identity is exactly `node_id` (a device may announce only itself); `400 {"error":"port must be 1..65535"}`; `404 fleet off` under `KRAKEN_NODES=off`. Mirror-gated (plain-text 403).

### `POST /api/node/action` (node tier)

Body `{"slug": string, "action": string, "payload": object, "from": {"cell": string}}`. Drives a **public** cell from another node (or asks in a readonly one — the ask-thread actions below). Order of refusals: 404 `no such job` (unknown or private), `403 {"error":"not public"}` (visibility read from the store at request time; skipped for the three ask-thread actions), `400 unknown action` (anything but `steer`, `question`, `kill`, `done`, `submit`, `archive`, `color`, `save`, `stop` and the three ask-thread actions), `400 bad payload` for a payload that does not decode. Each action runs the handler the local route runs and replies exactly what it replies:

| action | payload | local route |
|---|---|---|
| `steer` | `{"text"}` | `/api/steer`, the text prefixed `[from <label> · <login>]` — or `[from <label> · cell <slug>]` when `from.cell` is set — with the label taken from the registry row bound to the caller's tailnet identity (else its tailnet device name, else its address), never from the body or a row matched by address |
| `question` | `{"text": "<digit>"}` | `/api/question` |
| `kill` `done` `submit` `archive` `stop` | none | `/api/kill` `/api/done` `/api/queue` `/api/archive` `/api/stop` |
| `color` | `{"color"}` | `/api/job/color` |
| `save` | `{"content", "mtime"}` (a `text` key is ignored) | `/api/job/save` |

**Ask-thread actions** — `thread-ask`, `thread-send`, `thread-kill` — are admitted on a **readonly or public** cell alike: after the 404 above they skip the `not public` 403 entirely. They open and drive **headless** threads only (`kind: "headless"`, always `role: "ask"`). The thread's origin is the caller's verified tailnet identity — `{"node_id": <caller's tailscale stable id>, "label": <the steer prefix's label>, "login": <caller's login>}` — never anything in the body. A headless thread opened this way has NO tools at all and runs with the cell dir as its cwd. A question another node sends runs with NO tools even in a thread the owner opened (whose cwd stays the worktree): the read-only tools apply only when both the thread and the question are the owner's own. Every question is stamped `[from <label> · <login>]` (`[from <label>]` when the caller's login is unknown) on stdin, and any line of the question itself that begins with `[from` is prefixed `· `, so it cannot pass for a stamp. A question not both opened and sent by the owner also has every `@` rewritten to `＠` (U+FF20), so it cannot `@`-mention a file, and the `[from …]` stamp's label and login are rewritten the same way on every run. The origin's label and login are truncated to 120 runes. Each success invalidates the board cache and logs a `node-action` event naming the action, slug and sender label (never the text).

| action | payload | behaviour and refusals |
|---|---|---|
| `thread-ask` | `{"title", "text", "model"?}` | 400 `missing title` / `missing text` on blanks; 400 `bad model — want haiku, sonnet, opus or fable` for a `model` outside the four aliases (omitted: the job's `model`, then kraken's default); 400 `<slug>: ask title is too long (max 120 characters)` / `<slug>: ask question is too long (max 16 KB)`; then the local headless `branch`'s refusals (409 with jobs' own message, e.g. the 5-thread cap — at most 5 live headless threads opened by other nodes per job — or no live cell). Success `{"ok": true, "tid": "<tid>"}`, returned as soon as the first run has started |
| `thread-send` | `{"tid", "text"}` | 400 `missing tid` / `missing text`; 400 `<slug>: ask question is too long (max 16 KB)`; 404 `unknown thread`; 409 for a pane thread, a closed thread (`store: thread is closed`), a question already in flight (`store: thread is already answering`) or no live cell. Any caller may send into any headless thread. Success `{"ok": true}` before the answer exists |
| `thread-kill` | `{"tid"}` | 400 `missing tid`; 404 `unknown thread`; `409 {"error":"thread <tid> of <slug> is a pane thread — only its owner can close it"}`; `403 {"error":"not your thread"}` unless the thread's `origin.node_id` is the caller's own; then the local `kill` (stops any run in flight, closes the row). Success `{"ok": true}` |

Mirror-gated (plain-text 403).

### `GET /api/nodes` (node tier) → `{"nodes": [webNode]}`

The registry with each node's stored card list in `jobs`. Node tier so an unpaired phone can fan its pairing out from the registry its seed serves. `nodes` is `[]` when empty, and `{"nodes":[]}` under `KRAKEN_NODES=off`. Each node's `jobs` is re-encoded card by card as the board shows them: no `owner` or `account`, and every `artifacts[].url` rewritten to this node's `/api/nodes/artifact?node=&slug=&name=`. Online nodes first, then by label.

### `POST /api/nodes/action` (paired)

Body `{"node": string (node_id), "slug": string, "action": string, "payload": object}`. Forwards to that node's `POST /api/node/action`, setting `from.cell` from the request's `X-Kraken-From-Cell` header (the CLI sets it inside a cell from `KRAKEN_JOB_SLUG`), and relays the sibling's status and JSON body, always as `application/json` with `X-Content-Type-Options: nosniff`. A sibling 401 is relayed as `502 {"error":"<label> did not recognise this node"}` (never as a 401, which a client would read as its own gate) and a non-JSON body as `502 {"error":"<label> answered non-JSON"}`, `<label>` being the node's label or else its id; a transport error is `502 {"error": "<transport error>"}`; 404 `no such node`. Timeout 5 s (60 s for `submit`, 30 s for `thread-ask`, whose far side runs a discovery pass and starts a process before answering; `thread-send` and `thread-kill` take the 5 s default). Mirror-gated (plain-text 403).

### `GET /api/nodes/job/raw?node=&slug=`, `GET /api/nodes/read?node=&slug=&n=`, `GET /api/nodes/transcript?node=&slug=&n=&turns=`, `GET /api/nodes/thread/read?node=&slug=&tid=&n=` (paired)

The same relay (JSON-forced, 401/non-JSON → 502; `405` for anything but GET; 404 `no such node`) to the sibling's `/api/node/job/raw`, `/api/node/read`, `/api/node/transcript` and `/api/node/thread/read`, forwarding only the listed parameters that are non-empty. Not mirror-gated.

### `GET /api/nodes/artifact?node=&slug=&name=` (paired) → bytes

The URL every remote card's `artifacts[].url` carries. Refusals, in order: `405 {"error":"GET only"}`; `404 {"error":"no such node"}`. Forwards to the sibling's `GET /api/node/artifact?slug=&name=` (timeout 15 s). Every reply carries `X-Content-Type-Options: nosniff` and `Cache-Control: no-store`, the 405/404/502s included, plus `Content-Security-Policy: sandbox` exactly when `name` is html or svg (the `/api/node/artifact` rule above). A sibling 200 is served with status 200, `Content-Type` computed HERE from `name`'s extension (the sibling's header is never read), and the body verbatim; a body over 16 MB is `502 {"error":"<label> answered more than 16 MB"}` (exactly 16 MB passes), and the owner's `/api/node/artifact` refuses such an artifact itself with `413 {"error":"artifact over 16 MB"}`, which is relayed as is. So an artifact larger than that (a big rendered page, a video) is viewable on its owner's own board but not from another node; a sibling 401 is `502 {"error":"<label> did not recognise this node"}`; any other sibling status is relayed as JSON when its body is JSON, else `502 {"error":"<label> answered non-JSON"}`; a transport error is `502 {"error": "<transport error>"}`. Not mirror-gated.

### `POST /api/pair/password` (owner) → `{"ok": true, "revoked": int}`

Body `{"password": string}`. A non-empty password under 12 characters is `400 {"error":"password must be at least 12 characters"}`. A valid one replaces the stored verifier (a fresh 16-byte salt, `PBKDF2-HMAC-SHA256(password, salt, 600000, 32)`); an empty one clears it. **Either way every paired device is revoked**; `revoked` is how many. Mirror-gated (plain-text 403).

### `GET /api/pair/challenge` (node tier) → `{"node_id", "label", "salt", "iters", "nonce"}`

`salt` and `nonce` are base64url without padding; `nonce` is 16 random bytes, single use, valid 60 s. `409 {"error":"no pairing password set"}` when none is set; 429 when the caller already holds 20 live nonces.

### `POST /api/pair` (node tier) → `{"token", "node_id", "label"}`

Body `{"nonce": string, "proof": string, "device_label": string}` where `proof = base64url(HMAC-SHA256(K, nonce_bytes ‖ node_id ‖ device_label))` and `K = PBKDF2-HMAC-SHA256(password, salt, iters, 32)` from the challenge. On a match the node mints a 32-byte token (base64url), stores only its SHA-256 with the device label (≤ 120 characters) and the caller's tailnet device name, and returns it **once**. Refusals: `429 {"error":"too many attempts"}` (5 per minute per address; 10 failures lock the address out for 10 minutes), 409 no password set, `401 {"error":"no match"}` for a spent/expired/unknown nonce or a wrong proof.

### `POST /api/pair/revoke` (paired) → `{"ok": true}`

Body `{"id"?: int}`. A paired caller with no `id` (or its own) revokes itself — the app's Log out. The owner revokes any device by `id` (404 when none; 400 when `id` is missing). A paired caller naming another device's `id` is 401.

### `GET /api/pair/devices` (owner) → `{"devices": [{"id": int, "label": string, "tailnet_name": string, "created_at": int, "last_seen_at": int, "last_ip": string}]}`

Oldest first; `devices` is `[]` when none. Times are unix millis.

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

## Agent-side board and cell verbs

`kraken board get [slug]` and `kraken cell steer|submit|answer <slug>` are agent-side CLI verbs with no routes of their own: `board get` reads `GET /api/board`, and the cell verbs post to the board's existing `/api/steer`, `/api/queue`, `/api/question` and `/api/doc-question`, so every guard the operator's own clicks go through applies to an agent unchanged and there is no second, weaker door onto a live cell. Each cell verb logs an audit line through `/api/logevent` with `src: "cell"` on success. (They were introduced as the kronos orchestrator's control surface; kronos and its five routes under `/api/orch/*`, `/api/graph/recall-rearm` and `/api/kronos` were removed in job 549, and the verbs were kept as general agent-side verbs.)

### `POST /api/cell/context` — localhost-only, agent-side (not called by the iOS app or web UI; not modeled in Models.swift/APIClient.swift)

A cell claude's statusline hook (`kraken ctx-hook --slug <slug>`) reporting its context fill: body `{"slug", "session_id", "used_percentage", "context_window_size"}` (unknown fields rejected), stored per job and session for the daemon's auto-handoff pass. Once the job has a stamped zeus session, a report from any other session is answered `{"ok": true}` without being stored. Response `{"ok": true}`; errors 405, 400 (bad json, unknown field, blank `slug`/`session_id`, absent or out-of-range `used_percentage`), 404 (no such job), 413, 500. Gated under `--mirror`. **Additive, no contract bump.**

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

The endpoint jobs/limithook.go's `kraken limit-hook` POSTs to when a claude
dies on a usage limit — a cell's (the `StopFailure` hook, matcher
`rate_limit`, installed into the cell's own `.claude/settings.json` beside
the `PostToolUse` pr-hook entry, naming its job by `slug`) or a STANDING
agent's (`kraken limit-hook --agent iris`, naming its fixed agent id by
`agent`; iris is the only id on the accepted list since the kronos
orchestrator was removed, and it has no hook today — its hits arrive
through the daemon's transcript arm, so the arm is the door a future hook
would use). The hook process never opens kraken.db; it reports, and
`kraken serve` records, the same split `/api/pr-attach` above draws. The
recorded hit is what the daemon's rescue passes read to decide whether to
swap the main login to a healthier pool account.

Deliberately **not** mirror-gated: a mirror must still record that a cell hit
a usage limit. It is the rescue, not the observation, that a mirror never
performs, and that gate lives in the daemon's own `!mirror` block.

Request (`limitHitReq`): exactly one of `slug` (string — a job's slug) or
`agent` (string — a standing agent id from the closed list, today only `iris`);
both set or neither set is refused. Plus `error` (string, required
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
hooks within seconds; a same-error delivery folds into the still-UNRESOLVED
hit with no time window (resolution, not time, is the event boundary — the
old 2-minute window is gone), and a resolved hit starts a fresh record.

A delivery whose `session_id` is one of the job's SIDE THREADS (a thread row
of that job, live or closed) is recorded under the thread's own key
(`limit_hit:<job id>:<tid>`), resolved on record with outcome `thread-hit`,
and never under the job's row the daemon's rescue reads — a thread shares its
cell's account and hooks, so its first turn on an exhausted window fires the
cell's hook with the cell's slug, and recorded as the cell's hit it drove a
rebind and a kill-and-resume of an idle zeus for a pane the rescue cannot
reach. The response is the same shape (`deliveries` is 1, since a resolved
record never folds), the `limit-hit` event line says `side thread <tid> — the
cell is untouched`, and one macOS notification names the thread — once per
quiet gap of ten minutes measured from the previous delivery, and never from
a `--mirror` serve, which records the hit identically and does not notify.

Errors: 400 (bad JSON; neither or both of `slug`/`agent`; an `agent` outside
the closed list — the body names the refused id and the accepted ids; missing `error`; **no such
job** on the slug form), 405 (non-POST), 500 (store write failure).

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

The daemon's last usage passes — TWO pools, one array each. `accounts` is the claude pool (main + every `~/.claude-pool` slot), fetched from Anthropic's OAuth usage endpoint. `codex_accounts` is the codex pool (every `~/.codex-pool/accounts/<slug>` slot), read from each slot's own newest codex rollout file rather than from any API, so a slot codex has never opened a session in reports `fetched_at: 0` and zeroed windows — "never measured", not "measured at zero". The two arrays share the `AccountUsage` row shape but are disjoint pools: a pool slug is a slugified email, so the SAME slug can name a slot in both, and a client joins a job to a row on the job's `agent` plus the slug, never on the slug alone. Additive, no contract bump. Read-only (GET only, 405 otherwise). All `*_at` fields are unix millis. `accounts` and `codex_accounts` are always `[]`, never `null`.

| key | type | optionality | notes |
|---|---|---|---|
| `available` | bool | always | snapshot file exists and parsed |
| `fetched_at` | number | always | unix millis of the daemon's last completed pass; `0` when unavailable |
| `age_seconds` | number | always | server-derived `max(0, (now - fetched_at) / 1000)`; `0` when unavailable |
| `daemon_ok` | bool | always | the usage poller is the daemon: same liveness check as `/api/board`'s `daemon_ok` |
| `accounts` | array (`AccountUsage`) | always (`[]` not `null`) | the claude pool, sorted current-first then by slug |
| `codex_accounts` | array (`AccountUsage`) | always (`[]` not `null`) | the codex pool (`~/.codex-pool/accounts/<slug>`), sorted by slug. Never carries `models`, `current`, `cooldown_until`, `reauth` or `stale` — codex has no machine-login swap, no per-model windows and no refresh to fail; `no_creds` is a slot with no usable `auth.json`. `fetched_at: 0` with a present `session` of `0%` means codex has never run in that slot, which is a slot kraken will still bind (a no-evidence slot is eligible at 0 percent). Added additively; absent on an older server. Decode-only on iOS — nothing on the phone renders it |
| `codex_fetched_at` | number | omitempty | unix millis of the daemon's last completed CODEX usage pass — its own clock, independent of `fetched_at` above, since the two passes are separate and either can fail without the other |
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

Manually switch the main Claude login to a `~/.claude-pool` slot. Since
per-cell binding retired the spawn-time rotation gate, this route and its CLI
twin `k account <slug>` are the ONLY things that change the machine-wide login
at all — no automation swaps main any more, not at spawn and not on a usage
limit (the limit rescue rebinds the one affected cell instead). So it always
executes regardless of usage thresholds, there being no automatic threshold
left to be unlike, and it pins rotation to `"manual"` on success (see
`jobs.RotationState` / `k account`). Additive, no contract bump.

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

Sets rotation mode without swapping main. The mode no longer governs anything
that swaps the machine-wide login — nothing does that automatically any more —
it governs PER-CELL ACCOUNT BINDING: `"auto"` arms the binder, so every cell
spawn picks the emptiest eligible `~/.claude-pool` slot and stamps it on the
job (`webJob.account`), and the daemon's limit rescue rebinds one exhausted
cell to a fresh slot. `"manual"` pins: kraken takes no account decision at all,
new cells spawn UNBOUND in the main lane, and the rescue parks rather than
rebinding — parity with the CLI's `k account auto`/manual-switch semantics and
the web UI's banner toggle. Additive, no contract bump.

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
MUTATES the operator's Apple developer account, is reachable only from a terminal.
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
`cell` reads the live cell's `artifacts/` dir; `pluto` (an active job's
torn-down artifacts — the home keeps its historical name on the wire) and
`archive` (an archived job's) are served from the store's artifact rows
alone, and a name with no row there is a 404.
Every path segment of a served artifact URL (the `url` on a `webArtifact`) is
percent-encoded on its own, `/` separators kept, so a name with `#`, `%` or a
space round-trips: use the URL exactly as given, never re-encode or decode it.
A torn-down job's uncommitted root-level notes (`*.md` files at the worktree
root, captured at teardown) list among its artifacts under `root/<name>`.

### `GET /iso.html?src=<artifact path>` (web/_ui/iso.html) — not an API route (embedded SPA page; no JSON, no Models.swift type)

A second vite entry in the embedded SPA bundle (served by the same
FileServer as `index.html`, on the no-cache branch). The isoscene viewer page: fetches the diagram card
named by `src` (which must be a same-origin `/api/artifact/...` path — the
page refuses anything else), compiles it client-side into the animated 3D
isometric scene, and `location.replace`s to the flat card when it is bespoke
or not a diagram. The iOS app routes every `.html`/`.htm` artifact through
it (`isoViewerURL` in `ArtifactView.swift`); no JSON, no Models.swift type,
no contract bump.

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
(write failure), and 403 from the gate for a browser POST with a foreign
Origin (see Nodes and pairing).

### `GET /api/upload/<slug>/<name>` (web/serve.go)

Not JSON — serves the raw uploaded image file, `Content-Type` picked by
extension via the same `artifactContentType` the artifact route uses. GET
only (405 otherwise). `name` must exactly match the saved-name format
(`img-NNN.<png|jpg|gif|webp>`); 404 on any path-traversal attempt, malformed
name, unresolvable slug, or missing file — resolution goes through
`api.UploadPath`, which does not require an active job (an upload outlives
its job's own draft/working/etc. state changes, same lifecycle as artifacts).

### `GET`/`POST`/`DELETE /api/background` (api/background.go) — web-UI-only today (decoded but not rendered by the iOS app)

The board's one optional background image — a singleton blob in kraken.db
(`board_bg`, schema v26), belonging to the BOARD rather than any job.
Additive, no contract bump. The board payload's `background` key (above) is
how a client learns one is set without ever pulling the bytes.

- **GET** — the success path is not JSON: it serves the raw image bytes,
  `Content-Type` from the type sniffed at upload, `Cache-Control: private,
  max-age=31536000` (safe because clients request it as
  `/api/background?v=<updated_at>` — a replaced image is a new URL, and the
  stamp is strictly increasing). Errors ARE JSON `{"error": …}`: 404 when no
  background is set, plus the 500 every method here shares when no store is
  wired.
- **POST** — `multipart/form-data` with a single `file` field; same 10MB
  cap, 11MB body ceiling, and content-sniffed PNG/JPEG/GIF/WebP allow-list
  as `POST /api/upload`, and the same error statuses (413/415/400/500, and
  403 from the gate for a browser POST with a foreign Origin).
  Replaces any previous background. Response (200): `{"ok": true,
  "updated_at": <unix millis>}`.
- **DELETE** — removes the background (the plain board is the fallback).
  Idempotent: deleting with none set still answers `{"ok": true}`.

Any other method: 405.
