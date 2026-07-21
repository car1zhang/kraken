# kraken web API — iOS wire contract

Canonical reference for every JSON endpoint the iOS app (`ios/Kraken/`)
consumes. Source of truth is the Go code (`web.go`, `webpeers.go`,
`daemon.go`, `job.go`, `status.go`); this doc transcribes it, it does not
define it independently.

**Current contract**: `apiContract = 3` (web.go), mirrored by
`APIClient.expectedContract` (APIClient.swift). `/api/health` and
`/api/board` both serve `contract` (int) + `stale` (bool). v3 bumped when
the auto-approve feature (and `auto_approve` on the job form GET/POST) was
removed.

**Bump policy**: bump `apiContract` ONLY on a breaking change — a JSON key
retyped, renamed, or removed. Additive keys never bump it: old clients
already ignore fields they don't recognize, and `Decodable` optionals
absorb a key's absence.

**Change rule**: any change to a JSON shape served to or accepted from the
app/web UI lands in the same commit as: the Go change (web.go/webpeers.go/
daemon.go/job.go), this doc, `ios/Kraken/Models.swift`, and
`contract_test.go`. See also the "API contract" note in the root
`CLAUDE.md`.

**omitempty → Optional rule** (from Models.swift's header comment): a Go
struct field tagged `,omitempty` becomes a Swift `Optional` (the key may be
entirely absent; for slices, Go's nil-vs-empty distinction can even surface
as JSON `null`, which `Decodable` treats the same as an absent key for an
Optional property). A field with no `omitempty` tag is always present on
the wire and modeled as non-optional in Swift. Map-literal responses (the
job-editor endpoints, built as `map[string]any` rather than a tagged
struct) always emit every key — even ones a Swift model marks Optional for
other reasons (nil-vs-empty on the Go side, not the wire's absence).

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
| `jobs` | array (`webJob`) | always (`[]` not `null`) | |
| `contract` | number | always | == `apiContract` |
| `stale` | bool | always | this `k web` process' self-check |

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
| `type` | string | always | `build` \| `info` |
| `repo` | string | always | |
| `state` | string | always | draft/queued/working/blocked/done/archived |
| `err` | string | omitempty | |
| `outcome` | string | omitempty | |
| `cell` | string | omitempty | tmux session name |
| `live` | bool | always | |
| `prereqs` | array (`webPrereq`) | omitempty | |
| `artifacts` | array (`webArtifact`) | omitempty | |
| `detail` | string | omitempty | zeus' last assistant text, full multi-line text capped at 4000 chars |
| `question` | object (`webQuestion`) | omitempty (null-able) | |
| `roster` | array (`webAgent`) | omitempty | live nested subagents |
| `health` | string | omitempty | "" (absent) = healthy |
| `mtime` | number | always | unix millis |
| `archived` | bool | omitempty | |
| `display_slug` | string | omitempty | original slug, archived jobs only |
| `reactivatable` | bool | omitempty | done + has a recorded zeus session |
| `last_sent` | string | omitempty | carl's last free-text message; question-answer entries excluded, started + non-done jobs only |
| `prs` | array (`jobPR`) | omitempty | every PR shipped from the cell branch (daemon-tracked history, never cleared); render as linked state chips |

`webPrereq`: `job` (string, always), `gated` (bool, always), `done` (bool, always).

`jobPR`: `url` (string, always), `state` (string, always: `open` \| `merged` \| `closed`). The PR number for display derives from the url's trailing `/pull/<n>`.

`webArtifact`: `name` (string, always), `url` (string, always).

`webAgent` (roster entry): `name` (string, always), `model` (string, always).

`webQuestion`: `tool_use_id` (string, always), `questions` (array of `webQuestionItem`, always).

`webQuestionItem`: `question` (string, always), `header` (string, always),
`multi_select` (bool, always — note: no `omitempty` tag, so `false` still
rides on the wire), `options` (array of `webQuestionOption`, always).

`webQuestionOption`: `label` (string, always), `description` (string, always).

### `POST /api/queue` `/api/done` `/api/retry` `/api/kill` `/api/archive` `/api/unarchive` `/api/delete`

Request: `webReq` — `slug` (string, required), `text` (string, omitempty, unused by these).
Response: `{"ok": true}` on success; non-2xx `{"error": string}` on failure (409 for validation conflicts).

### `POST /api/question`

Request: `webReq` — `slug` (string), `text` (string: a single digit `"1"`–`"9"`, the 1-based option index).
Response: same as above.

### `POST /api/steer`

Request: `webReq` — `slug` (string), `text` (string: free-text message, required non-empty).
Response: same as above.

### `POST /api/reactivate`

Request: `webReq` — `slug` (string), `text` (string, omitempty: optional steering message folded into the resume prompt).
Response: same as above.

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

| key | type | optionality | notes |
|---|---|---|---|
| `slug` | string | always | |
| `type` | string | always | |
| `repo` | string | always | |
| `base` | string | always | |
| `ready` | bool | always | |
| `solo` | bool | always | |
| `prereqs` | array (`Prereq`) | always (`[]` not `null`) | |
| `spec` | string | always | |
| `mtime` | number | always | |
| `editable` | bool | always | |
| `err` | string | always | "" when clean |
| `slugs` | array of string \| null | always present key, value may be `null` | other job slugs, for prereq datalist |
| `repos` | array of string \| null | always present key, value may be `null` | |

`Prereq`: `job` (string, always), `gated` (bool, `omitempty` on the wire — absent means false).

### `POST /api/job/saveform` → map literal (web.go `webJobSaveForm`)

Request: `jobFormFields` — `slug` (string), `type` (string, must be `build`|`info`), `repo` (string), `base` (string, optional to send — no `omitempty` tag, absence just decodes as ""), `ready` (bool), `solo` (bool), `prereqs` (array of `Prereq`), `spec` (string, optional to send), `mtime` (number|string via `mtimeToken`), `force` (bool, optional to send — skips the mtime conflict check when true).

Response (success): `{"ok": true, "mtime": number, "warn": string}`.
Response (409 conflict, unforced mtime mismatch): `{"error": string, "conflict": true}` — the `conflict` key distinguishes a stale-mtime 409 from other 409s (e.g. "cell in flight") that don't carry it.
Errors: 400 (bad type), 404 (no such job), 409 (not editable, or mtime conflict), 500.

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
| `status` | string | always | |
| `status_at` | number | omitempty | |
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
Response: `{"ok": true}`.
Errors: 400 (both or neither of pane/slug set), 404 (no session in pane / no job / no live cell agent), 409 (open failure).

---

## Misc

### `GET /api/health` → map literal (webpeers.go `healthHandler`)

| key | type | optionality | notes |
|---|---|---|---|
| `ok` | bool | always | |
| `host` | string | always | |
| `contract` | number | always | == `apiContract` |
| `stale` | bool | always | |

### `GET /api/artifact/<slug>/<cell\|pluto\|archive>/<name>`

Not JSON — serves the raw artifact file with a `Content-Type` picked by
extension (`artifactContentType`: html/svg/pdf/png/jpg/gif/webp get their
real type, everything else is `text/plain; charset=utf-8`). 404 on any
path-traversal attempt, hidden segment, unresolvable job, or missing file.
