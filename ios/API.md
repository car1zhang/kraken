# kraken web API — iOS wire contract

Canonical reference for every JSON endpoint the iOS app (`ios/Kraken/`)
consumes. Source of truth is the Go code — the handlers and wire types in
`internal/api/` (`board.go`, `peers.go`, `update.go`, `shipios.go`, …),
`web/serve.go`, and the model layers they read
(`internal/jobs/{daemon,job,status}.go`, `internal/core/peer.go`); this doc
transcribes it, it does not define it independently.

**Current contract**: `apiContract = 5` (internal/api/board.go — the Go
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
parse).

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
for. **Blast-radius containment** — every field of `WebUpdate` and
`WebShipIOS`, all of them always present on the wire, made Optional (with a
hand-written `init(from:)`) purely so a drifted sub-key costs the ⟳ / ⇧
control rather than the entire `/api/board` decode; see those structs'
header comments in Models.swift.

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
| `cell_cap` | number | always | |
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
| `phase` | string | always | `idle`\|`preflight`\|`fetching`\|`merging`\|`building`\|`installing`\|`restarting`\|`done`\|`failed`\|`uptodate`. `idle` when no run has ever happened (missing status file) |
| `from_sha` | string | always | the commit the in-flight/last run started from |
| `to_sha` | string | always | the commit it moved to; `""` until the merge lands |
| `behind` | number | always | commits `HEAD..origin/main`, from the daemon's 10-minute availability check |
| `error` | string | always | the RUN's error only — a failed availability check is never surfaced here |
| `started_at` | number | always | unix millis; `0` = never run |
| `updated_at` | number | always | unix millis of the last phase transition |
| `build_sha` | string | always | the running binary's ldflags-stamped short sha, `"dev"` unstamped |
| `checked_at` | number | always | unix millis of the last availability check; `0` = never checked |

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

`webJob` (web.go):

| key | type | optionality | notes |
|---|---|---|---|
| `slug` | string | always | archived jobs: numbered archive file base |
| `repo` | string | always | |
| `state` | string | always | draft/queued/working/blocked/done/archived |
| `err` | string | omitempty | |
| `outcome` | string | omitempty | |
| `cell` | string | omitempty | tmux session name |
| `live` | bool | always | |
| `prereqs` | array (`webPrereq`) | omitempty | |
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

`webPrereq`: `job` (string, always), `gated` (bool, always), `done` (bool, always).

`jobPR`: `url` (string, always), `state` (string, always: `open` \| `merged` \| `closed`). The PR number for display derives from the url's trailing `/pull/<n>`.

`webArtifact`: `name` (string, always — in store mode this may contain `/`
for a nested path, e.g. `icon-variations/v2.png`), `url` (string, always).

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
| `addressed` | string | omitempty | zeus' own free-text account of how it addressed the note; absent means still open (this is prose, not a boolean) |
| `addressed_at` | string | omitempty | RFC3339 timestamp, server-stamped alongside `addressed` |

### `POST /api/queue` `/api/dequeue` `/api/done` `/api/retry` `/api/kill` `/api/archive` `/api/unarchive` `/api/delete`

Request: `webReq` — `slug` (string, required), `text` (string, omitempty, unused by these).
Response: `{"ok": true}` on success; non-2xx `{"error": string}` on failure (409 for validation conflicts).

`/api/queue` refuses (409) a job whose spec body is empty — "queued" is
supposed to mean there's real content for a cell to pick up, so an empty
spec never reaches the daemon.

`/api/dequeue` is the queued→draft reverse move: legal only for a job that
is queued (or ready-but-blocked-on-prereqs, which `DisplayState` also shows
as queued) and has never started a cell — there's nothing to tear down yet,
so it's just a `ready` flip back to `false`. The server re-derives state
itself rather than trusting the board's snapshot, so a stale board can't
cancel a job that has since actually started: that case 409s with a message
naming `/api/kill` as the correct recovery instead.

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

Not modeled: the zeus-facing fourth form, `{slug, id, addressed}` (UPDATEs
only the zeus-owned `addressed`/`addressed_at` columns), is reached by zeus
through the `kraken feedback addressed` cell verb, never by the iOS app —
same convention as `/api/pr-attach`/`/api/logevent` above, so it is not in
Models.swift/APIClient.swift. A non-empty `addressed` is capped at the same
8000 **runes** after trim as `feedback` (refused 400 over that — `addressed`
also rides every `/api/board` poll, and its writer is zeus, not a human). An
explicitly empty `addressed` (`{slug, id, addressed: ""}`, as opposed to the
key being absent) clears a prior addressed note back to open; the cell verb
reaches that form as `kraken feedback addressed --clear <slug> <id>`.
Sending `addressed` and `feedback` together is refused (400): they are two
different forms, and the addressed branch wins the dispatch, so honoring the
request would silently drop the note edit.

### `GET /api/feedback?slug=`

zeus-side read: open + addressed feedback entries for a job, re-read on
every resume via the `kraken feedback get` cell verb. Not modeled in
Models.swift/APIClient.swift — the iOS app reads feedback from the per-job
`feedback` array on `/api/board` instead, never this endpoint directly.

Response: `{"feedback": [webFeedback]}` (same `webFeedback` shape as the
board's per-job array above). Always an array, never `null` — a job with no
feedback yet serves `[]`.

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

### `POST /api/steer`

Request: `webReq` — `slug` (string), `text` (string: free-text message, required non-empty).
Response: same as above.

`text` is delivered to zeus' pane verbatim — no `"from carl: "` prefix — so
a message starting with `/` invokes a real slash command in zeus' Claude
session (e.g. `/do`) rather than being typed as plain conversation text.

### `POST /api/reactivate`

Request: `webReq` — `slug` (string), `text` (string, omitempty: optional steering message folded into the resume prompt).
Response: same as above.

### `POST /api/start`

Request: `webReq` — `slug` (string, required), `text` unused.
Response: same as `/api/queue` etc above.

Boots a queued job's cell immediately, by hand — same validation (job must
currently display as `queued`, every prereq's dependency done — gated or
not), same `StartCell` mechanics the daemon's own auto-start uses. Since
this is carl acting by hand, it doubles as the manual release for a job
parked behind a *gated* prereq (an already-done dependency flagged for
human sign-off) and bypasses the daemon's `CellCap` entirely.

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
| `column` | string | always | one of `draft`\|`queued`\|`working`\|`blocked`\|`done` |
| `slugs` | array of string | always (`[]` ok) | the column's full intended slug order |

Response: `{"ok": true}` on success; non-2xx `{"error": string}` on failure
(409 for an unrecognized `column`). Posted slugs whose current
`DisplayState` no longer matches `column` — a concurrent transition
mid-drag, e.g. a queued job that started — are silently dropped from the
saved order rather than failing the whole request.

### `GET /api/draft?slug=` / `POST /api/draft` → `webDraft` (draft.go)

Per-job prompt-composer draft text, kept in an in-memory store on the `k web`
process — additive (contract stays 2), nothing here is written to disk, so
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

Request: `{"slug": string}`. Errors: 405 (non-POST), 400 (bad slug), 409 (slug already exists), 500 (WriteFile failure).

### `POST /api/job/save` (web-UI-only)

Request: `{"slug": string, "content": string, "mtime": number|string}` (mtime via `mtimeToken`, 0/""/null skips the optimistic-lock check).
Response: `{"ok": true, "mtime": number, "warn": string}` (warn is `""` when the saved content parses cleanly — always present, it's a map literal).
Errors: 404 (no such job), 409 (not editable, or mtime mismatch), 500.

### `GET /api/job/form?slug=` → map literal (web.go `webJobForm`)

`skip_doc` is gone (v5, breaking): whether the doc phase runs is now purely
zeus' own read of the job spec text (does it invoke `/aim`/`/info`, or ask
for a doc/spec/explanation in words) — never a stored flag on the job.

| key | type | optionality | notes |
|---|---|---|---|
| `slug` | string | always | |
| `repo` | string | always | |
| `base` | string | always | |
| `ready` | bool | always | |
| `solo` | bool | always | |
| `model` | string | always | claude model for zeus (`--model` value), `""` = kraken's default (`claude-opus-5`); applied once at cell start |
| `prereqs` | array (`Prereq`) | always (`[]` not `null`) | |
| `spec` | string | always | |
| `mtime` | number | always | |
| `editable` | bool | always | |
| `err` | string | always | "" when clean |
| `slugs` | array of string \| null | always present key, value may be `null` | other job slugs, for prereq datalist |
| `repos` | array of string \| null | always present key, value may be `null` | |

`Prereq`: `job` (string, always), `gated` (bool, `omitempty` on the wire — absent means false).

### `POST /api/job/saveform` → map literal (web.go `webJobSaveForm`)

Request: `jobFormFields` — `slug` (string), `repo` (string), `base` (string, optional to send — no `omitempty` tag, absence just decodes as ""), `ready` (bool), `solo` (bool), `model` (string, optional to send — claude model for zeus, "" = kraken's default, `claude-opus-5`), `prereqs` (array of `Prereq`), `spec` (string, optional to send), `mtime` (number|string via `mtimeToken`), `force` (bool, optional to send — skips the mtime conflict check when true). `skip_doc` is gone (v5, breaking) — see the form-GET note above.

Response (success): `{"ok": true, "mtime": number, "warn": string}`.
Response (409 conflict, unforced mtime mismatch): `{"error": string, "conflict": true}` — the `conflict` key distinguishes a stale-mtime 409 from other 409s (e.g. "cell in flight") that don't carry it.
Errors: 404 (no such job), 409 (not editable, or mtime conflict), 500.

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
URL shape), 404 (no such job), 500 (write failure).

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
its job's own draft/queued/etc. state changes, same lifecycle as artifacts).
