package core

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kraken/internal/util"
)

// --- the codex account pool ---
//
// A "pool slot" is one ChatGPT login kept in its own directory, exactly the
// way ~/.claude-pool holds one Claude login per slot. The codex pool is laid
// out by the `cxp` helper as
//
//	~/.codex-pool/accounts/<slug>/     one whole CODEX_HOME per account
//	~/.codex-pool/removed/             slots cxp has retired
//	~/.codex-pool/snapshots.json       cxp's own bookkeeping
//
// and a slot directory is a drop-in replacement for ~/.codex: codex reads
// everything it needs — auth.json, sessions/, config.toml — under whatever
// directory CODEX_HOME names. That is the whole mechanism by which a cell
// bills one account and a second cell bills another: kraken prefixes the
// pane's command with CODEX_HOME='<slot dir>', and the codex it starts there
// never sees the machine's own login.
//
// Only `accounts/` is ever read. `removed/` and `snapshots.json` belong to
// cxp, and a slot kraken binds must be one cxp still considers live.
//
// Nothing here writes into a slot except PlaceCodexRollout, and nothing here
// returns, logs or embeds a token: auth.json carries three live credentials
// beside the identity claims, and the only two facts kraken needs out of it
// are the account's email and its plan.

// codexPoolAccountsDir is the one subdirectory of the pool root that holds
// slots. Named once so the slug lister and the slot-dir builder cannot drift.
const codexPoolAccountsDir = "accounts"

// CodexPoolDir returns the codex account pool root: $CODEX_POOL_DIR or
// ~/.codex-pool. The env override is what lets a test point the whole pool at
// a directory it owns (and what lets carl move the pool off the home dir);
// it mirrors CLAUDE_POOL_DIR on the claude side.
func CodexPoolDir() string {
	if d := os.Getenv("CODEX_POOL_DIR"); d != "" {
		return d
	}
	return filepath.Join(util.Home(), ".codex-pool")
}

// CodexSlotDir is the ONE derivation of a slot's directory from its slug —
// the same single-derivation rule jobs.slotDir follows on the claude side, and
// for the same reason: the directory a slot is PROBED at (CodexSlotUsable)
// must never differ from the one a cell is pointed at (the CODEX_HOME prefix),
// or a cell can pass its usability check and still come up logged out.
func CodexSlotDir(slug string) string {
	return filepath.Join(CodexPoolDir(), codexPoolAccountsDir, slug)
}

// CodexPoolSlugs lists the pool's slot names, sorted ascending.
//
// Plain files are skipped (a slot is a whole CODEX_HOME, so it is always a
// directory), as are dot-names and the name "main": "main" is reserved for the
// machine's own ~/.codex login everywhere in the binding code, and a slot
// literally called main would be silently dropped by SelectBindTarget anyway —
// dropping it here means it never reaches a usage table pretending to be
// bindable.
//
// A missing or unreadable accounts dir is an empty result, never an error:
// a machine with no codex pool is the ordinary case, not a fault.
func CodexPoolSlugs() []string {
	entries, err := os.ReadDir(filepath.Join(CodexPoolDir(), codexPoolAccountsDir))
	if err != nil {
		return nil
	}
	var slugs []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || e.Name() == "main" {
			continue
		}
		slugs = append(slugs, e.Name())
	}
	sort.Strings(slugs)
	return slugs
}

// codexAuthFile is the shape of <CODEX_HOME>/auth.json that kraken reads.
// Only the id_token is taken, and only so its identity claims can be decoded;
// the access and refresh tokens beside it are deliberately not modelled, so
// there is no field on this struct that could ever be printed by accident.
type codexAuthFile struct {
	Tokens struct {
		IDToken string `json:"id_token"`
	} `json:"tokens"`
}

// The two claims kraken reads out of a codex id_token. The plan sits inside a
// namespaced claim, which is how OIDC carries a vendor-specific field — the
// claim NAME is the URL, it is not a URL kraken fetches.
const (
	codexEmailClaim = "email"
	codexAuthClaim  = "https://api.openai.com/auth"
	codexPlanClaim  = "chatgpt_plan_type"
)

// CodexSlotIdentity answers "whose account is this, and on what plan" for a
// codex home — a pool slot or ~/.codex alike. It is the only reader of
// auth.json in the binary, and it returns exactly two strings so that no
// caller can ever hold a token by accident.
//
// The id_token is a JWT: three base64url segments separated by dots, of which
// only the middle one (the payload) carries claims. It is decoded, never
// verified: kraken is not an authorization server, the file is already local,
// and an EXPIRED token still names its owner correctly — which is the only
// question being asked. Refusing a stale token here would make a slot whose
// credentials simply need a refresh look like a slot belonging to nobody, and
// an identity-less row is one the binder excludes outright.
//
// Anything missing or malformed — no file, no tokens block, an id_token that
// is not a string, a payload that is not base64url or not a JSON object —
// yields two empty strings and never an error. Short of that the two claims
// are INDEPENDENT: a payload that carries one of them and not the other
// returns the one it carries, and "the other" includes a claim of an
// unexpected JSON type. decodeCodexIDToken says why that matters.
func CodexSlotIdentity(homeDir string) (email, plan string) {
	if homeDir == "" {
		return "", ""
	}
	data, err := os.ReadFile(filepath.Join(homeDir, "auth.json"))
	if err != nil {
		return "", ""
	}
	// The struct here models exactly the token and nothing else, so the only
	// shapes it can fail on ARE the token: a `tokens` that is not an object,
	// or an `id_token` that is not a string. Both are a malformed token, which
	// is the blanket case. Every other key in the file — the API key slot, the
	// refresh timestamp, anything codex adds later — is not modelled and so
	// cannot fail the read whatever type it arrives as.
	var auth codexAuthFile
	if json.Unmarshal(data, &auth) != nil {
		return "", ""
	}
	return decodeCodexIDToken(auth.Tokens.IDToken)
}

// decodeCodexIDToken base64url-decodes a JWT's payload segment and reads the
// two claims kraken cares about. Padding is optional in the JWT spec and
// codex's own token is written unpadded, so the trailing '=' run is stripped
// and the raw (unpadded) alphabet does both shapes with one decoder.
//
// Each claim is decoded on its own, out of a map of raw values rather than
// into one struct, and that shape is the point rather than a preference:
// json.Unmarshal into a struct fails the WHOLE document on the first field
// whose JSON type does not match, so decoding into one struct would cost the
// email as well whenever the namespaced auth claim arrived as a string — a
// shape change at OpenAI, not a corrupt file.
//
// An emptied email is not a safe degradation, which is what makes this worth
// the extra decode. The binder excludes the machine's own login from a cell's
// candidates by MATCHING a slot's email against it, so an identity that comes
// back empty stops excluding anything: the failure is fail-OPEN, towards
// binding a cell onto the very account carl is using interactively, and it
// would have looked like a slot nobody owned rather than like a read that
// went wrong.
//
// A payload that is not a JSON object at all is the one case where both come
// back empty — there are no claims in it to be independent about.
func decodeCodexIDToken(token string) (email, plan string) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 || parts[1] == "" {
		return "", ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", ""
	}
	var claims map[string]json.RawMessage
	if json.Unmarshal(payload, &claims) != nil {
		return "", ""
	}
	email = jsonString(claims[codexEmailClaim])
	var auth map[string]json.RawMessage
	if json.Unmarshal(claims[codexAuthClaim], &auth) == nil {
		plan = jsonString(auth[codexPlanClaim])
	}
	return email, plan
}

// CodexMainEmail is who the machine's own codex login (~/.codex) belongs to.
// The binder needs it for one purpose: excluding a pool slot that mirrors the
// main account, so a cell does not "spread" onto the very login carl is using
// interactively. Empty when ~/.codex has no readable identity, and an empty
// needle is no exclusion at all rather than a weaker one — the same
// convention resolvedMainEmail follows on the claude side.
func CodexMainEmail() string {
	email, _ := CodexSlotIdentity(filepath.Join(util.Home(), ".codex"))
	return email
}

// CodexSlotUsable is the per-bind guard: may a cell be pointed at this slot?
//
// It answers the one question that matters at spawn time — will the codex
// that starts under CODEX_HOME='<slotDir>' find a login there — and it is
// deliberately a structural check rather than a content one. A slot dir that
// is absent, or is a plain file, or holds no auth.json, gives a pane that
// comes up at codex's login prompt while the board reads "working", which is
// the worst outcome this feature can produce.
//
// An auth.json of size zero is the shape a half-finished write leaves behind,
// so it counts as no login at all; a DIRECTORY named auth.json is likewise
// not a credential file. The file's CONTENT is never read here: whether the
// token inside is expired or refreshable is codex's business, and opening it
// in the guard would put credential bytes on a path that has no need of them.
func CodexSlotUsable(slotDir string) bool {
	if slotDir == "" {
		return false
	}
	fi, err := os.Stat(slotDir)
	if err != nil || !fi.IsDir() {
		return false
	}
	af, err := os.Stat(filepath.Join(slotDir, "auth.json"))
	if err != nil || !af.Mode().IsRegular() {
		return false
	}
	return af.Size() > 0
}

// --- usage, read out of the rollouts a slot has already written ---
//
// Codex has no usage endpoint kraken can poll the way the claude pool polls
// Anthropic's. What it does have is the `token_count` record codex appends to
// the active rollout on every turn, which carries the account's live rate
// limit windows. So a slot's usage is simply the newest such record it has
// ever written — free to read, exactly as accurate as the last time that
// account was used, and 0%/never-fetched for a slot that has never run.
//
// That zero is a decision, not an accident: a slot with no evidence is
// ELIGIBLE at 0 percent. Treating "no data" as "unknown, exclude" would make
// a freshly added account permanently unbindable, since only a bind can
// produce the evidence that would let it be bound.

// codexWeeklyWindowMinutes is the boundary between the two windows codex
// reports. Routing is by DURATION and never by which JSON key the window
// arrived under: codex has shipped both orders — a 5h `primary` beside a 7d
// `secondary`, and a 7d `primary` beside a shorter `secondary` — so a reader
// keyed on the key name mislabels half the fleet.
const codexWeeklyWindowMinutes = 10080

// codexRateTailWindow is how far back from a rollout's end the limits scan
// reads. Codex appends a token_count record per turn, so the last one sits a
// few KB from the end; reading a multi-MB rollout in full to find it would
// cost the daemon one whole-file read per slot per pass.
const codexRateTailWindow = 256 * 1024

// codexPoolMaxRollouts bounds how many of a slot's rollouts the usage scan
// will open before giving up. The newest file that carries a limits record
// almost always IS the newest file; the walk back exists for the case where
// the last session ended before its first turn, and an unbounded one would
// walk the slot's entire history on a pool with no usage at all.
const codexPoolMaxRollouts = 20

// codexPoolWindow is one rate-limit window as codex writes it. Separate from
// usage.go's codexRateWindow because these fields must be NULLABLE where they
// appear: codex writes `"secondary":null` when an account has only one window,
// and a value type turns that into a present window reading 0% — a healthy
// row invented out of a missing one.
type codexPoolWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}

type codexPoolLimits struct {
	Primary   *codexPoolWindow `json:"primary"`
	Secondary *codexPoolWindow `json:"secondary"`
	// ReachedType is raw rather than *string so that a value of an
	// unexpected JSON type fails this one field instead of the whole record:
	// the windows beside it are still worth having.
	ReachedType json.RawMessage `json:"rate_limit_reached_type"`
}

type codexPoolRecord struct {
	Timestamp string `json:"timestamp"`
	Payload   struct {
		Type       string           `json:"type"`
		RateLimits *codexPoolLimits `json:"rate_limits"`
	} `json:"payload"`
}

// windows returns the record's non-null windows routed by duration.
func (r codexPoolRecord) windows() (session, weekly *UsageWindow) {
	if r.Payload.RateLimits == nil {
		return nil, nil
	}
	for _, w := range []*codexPoolWindow{r.Payload.RateLimits.Primary, r.Payload.RateLimits.Secondary} {
		if w == nil {
			continue
		}
		uw := &UsageWindow{Percent: w.UsedPercent}
		if w.ResetsAt > 0 {
			uw.ResetsAt = w.ResetsAt * 1000 // codex writes unix seconds; every kraken window is millis
		}
		if w.WindowMinutes >= codexWeeklyWindowMinutes {
			weekly = uw
		} else {
			session = uw
		}
	}
	return session, weekly
}

// fetchedAt is the record's own timestamp in unix millis — when the account
// last reported these numbers, which is what makes a stale row readable as
// stale. 0 when the field is missing or unparseable.
func (r codexPoolRecord) fetchedAt() int64 {
	if r.Timestamp == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, r.Timestamp)
	if err != nil {
		if t, err = time.Parse(time.RFC3339, r.Timestamp); err != nil {
			return 0
		}
	}
	return t.UnixMilli()
}

// jsonString decodes a raw JSON value that is expected to be a string.
// Anything else — null, a number, absent — is the empty string.
func jsonString(raw json.RawMessage) string {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// lastCodexRateRecord returns the LAST token_count record carrying
// rate_limits in a rollout file. Last rather than first because these records
// are cumulative: each one supersedes the ones before it, so the tail of the
// file is the only part that describes the account now.
//
// Every failure — missing file, unreadable, no such record — is (zero, false),
// never an error.
func lastCodexRateRecord(path string) (codexPoolRecord, bool) {
	var out codexPoolRecord
	if path == "" {
		return out, false
	}
	f, err := os.Open(path)
	if err != nil {
		return out, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return out, false
	}
	start := info.Size() - codexRateTailWindow
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, 0); err != nil {
		return out, false
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return out, false
	}
	lines := strings.Split(string(data), "\n")
	if start > 0 && len(lines) > 0 {
		lines = lines[1:] // the window landed mid-line: that line is not parseable anyway
	}
	found := false
	for _, line := range lines {
		if !strings.Contains(line, `"rate_limits"`) {
			continue
		}
		var rec codexPoolRecord
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if rec.Payload.Type != "token_count" || rec.Payload.RateLimits == nil {
			continue
		}
		out, found = rec, true
	}
	return out, found
}

// codexSlotRollouts lists a codex home's newest rollout files, at most
// codexPoolMaxRollouts of them.
//
// It descends the date partition codex writes — sessions/YYYY/MM/DD — taking
// each directory's entries in NAME-DESCENDING order and stopping the moment
// it has enough. Every component of that partition is zero-padded, so name
// order is date order, and the newest day is the first directory entered.
//
// Walking the whole tree and then sorting by mtime is the obvious
// implementation and it is the wrong one, which is what makes it worth
// writing down: the machine's own ~/.codex held 13,220 rollout files
// (measured 2026-09-17), and a bare `find` over them took 0.52s — per slot,
// on every usage pass, to answer a question that only ever needs the newest
// handful. Descending stops after a few directory reads instead.
//
// The DATE decides which files are collected, but modification time decides
// the order they come back in: a rollout is filed under the day its session
// STARTED and appended to for as long as the conversation runs, so a thread
// resumed today can sit under an older date directory with a newer mtime
// than anything under today's. The bounded set is therefore re-sorted by
// mtime, newest first, before it is returned — the cap bounds the I/O, the
// sort keeps the reader's "newest evidence first" meaning over what the cap
// let through.
//
// An unreadable directory is skipped rather than failing the walk — this
// reads a tree another program owns — and a layout that is not the date
// partition still works, since the descent makes no assumption beyond
// "directories hold more of them".
func codexSlotRollouts(homeDir string) []string {
	type found struct {
		path string
		mod  time.Time
	}
	var out []found
	var descend func(dir string)
	descend = func(dir string) {
		if len(out) >= codexPoolMaxRollouts {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() > entries[j].Name() })
		var files []found
		var dirs []string
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, filepath.Join(dir, e.Name()))
				continue
			}
			name := e.Name()
			if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
				continue
			}
			info, ierr := e.Info()
			if ierr != nil {
				continue
			}
			files = append(files, found{filepath.Join(dir, name), info.ModTime()})
		}
		sort.SliceStable(files, func(i, j int) bool {
			if files[i].mod.Equal(files[j].mod) {
				return files[i].path > files[j].path // deterministic when mtimes tie
			}
			return files[i].mod.After(files[j].mod)
		})
		// Newest-first HERE decides which of a directory's files get in when
		// the cap fills mid-directory; the sort before the return decides the
		// order of what got through.
		for _, f := range files {
			if len(out) >= codexPoolMaxRollouts {
				return
			}
			out = append(out, f)
		}
		for _, d := range dirs {
			if len(out) >= codexPoolMaxRollouts {
				return
			}
			descend(d)
		}
	}
	descend(filepath.Join(homeDir, "sessions"))
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].mod.Equal(out[j].mod) {
			return out[i].path > out[j].path
		}
		return out[i].mod.After(out[j].mod)
	})
	paths := make([]string, 0, len(out))
	for _, f := range out {
		paths = append(paths, f.path)
	}
	return paths
}

// CodexSlotUsage builds one account row for a pool slot: who it belongs to,
// what plan it is on, whether it can be bound at all, and how much of its
// limits the last session burned.
//
// The row is the same core.AccountUsage the claude pool produces, deliberately
// — the binder's selector (SelectBindTarget) and the board's account table
// both already speak it, and a second row type would mean a second copy of
// every gate. The fields codex cannot answer are left at their zero values:
// Models is nil (codex publishes no per-model windows), Reauth is false
// (nothing here can distinguish "needs a re-login" from "has never run"), and
// Current is false (a codex slot is bound per cell; there is no machine-wide
// "current" codex account the way there is for claude).
//
// A slot with no auth.json comes back NoCreds with both windows nil, which is
// what makes the selector exclude it. A usable slot that has simply never run
// comes back at 0% with FetchedAt 0 — eligible, and visibly never-fetched.
func CodexSlotUsage(slug string) AccountUsage {
	dir := CodexSlotDir(slug)
	email, plan := CodexSlotIdentity(dir)
	row := AccountUsage{
		Slug:    slug,
		Email:   email,
		Tier:    plan,
		NoCreds: !CodexSlotUsable(dir),
	}
	if row.NoCreds {
		return row
	}
	for _, rollout := range codexSlotRollouts(dir) {
		rec, ok := lastCodexRateRecord(rollout)
		if !ok {
			continue // a session that ended before its first turn carries no limits
		}
		session, weekly := rec.windows()
		if session == nil && weekly == nil {
			// A record whose windows are BOTH null carries no usage. No such
			// record has been seen from real codex on this machine, so this is
			// defensive rather than a shape kraken has measured: taking it
			// would report the slot at 0% on the authority of a record that
			// never said so, and stop the search at the very file that has
			// nothing to say; the older file behind it usually does.
			continue
		}
		row.Session, row.Weekly = session, weekly
		row.FetchedAt = rec.fetchedAt()
		if row.Weekly != nil && row.Session == nil {
			// Some accounts report only the long window. The selector gates on
			// Session, so leaving it nil would exclude a perfectly healthy slot
			// over a shape difference rather than over its usage.
			w := *row.Weekly
			row.Session = &w
		}
		return row
	}
	// Usable creds, no evidence: eligible at zero, and FetchedAt 0 says so.
	row.Session = &UsageWindow{}
	return row
}

// FetchCodexUsage is one pass over the whole codex pool. It shells out to
// nothing and makes no network call — every number comes off files the pool
// already holds — so unlike its claude counterpart it cannot partially fail.
func FetchCodexUsage() []AccountUsage {
	slugs := CodexPoolSlugs()
	rows := make([]AccountUsage, 0, len(slugs))
	for _, slug := range slugs {
		rows = append(rows, CodexSlotUsage(slug))
	}
	return rows
}

// CodexLimitReached reports whether the conversation in a rollout ended
// against a usage limit — the codex analogue of the rate_limit banner a
// stalled claude leaves in its transcript, and the trigger the limit rescue
// keys on.
//
// The signal is `rate_limit_reached_type` on the LAST token_count record.
// Only the last one counts, and that is the whole subtlety: these records are
// cumulative, so an earlier turn that hit a limit and a later turn that ran
// fine both live in the same file, and reading any record but the last would
// rescue a cell that recovered on its own hours ago.
//
// resetsAt is reported in unix SECONDS, as codex writes it, and is taken from
// the window with the greatest used_percent — the window that is actually
// holding the account back, which need not be the one codex named.
func CodexLimitReached(rolloutPath string) (reachedType string, resetsAt int64, ok bool) {
	rec, found := lastCodexRateRecord(rolloutPath)
	if !found {
		return "", 0, false
	}
	reached := jsonString(rec.Payload.RateLimits.ReachedType)
	if reached == "" {
		return "", 0, false
	}
	var worst *codexPoolWindow
	for _, w := range []*codexPoolWindow{rec.Payload.RateLimits.Primary, rec.Payload.RateLimits.Secondary} {
		if w == nil {
			continue
		}
		if worst == nil || w.UsedPercent > worst.UsedPercent {
			worst = w
		}
	}
	if worst == nil {
		return reached, 0, true
	}
	return reached, worst.ResetsAt, true
}

// codexHomeEnvPrefix is the exact prefix jobs.accountEnvPrefix emits for a
// bound codex job. Written here as well because this file holds the inverse,
// and the two must agree byte for byte or a bound pane reads back as unbound.
const codexHomeEnvPrefix = "CODEX_HOME='"

// CodexHomeFromCommand recovers the slot directory out of a pane's start
// command — the exact inverse of the prefix jobs.accountEnvPrefix puts on it.
//
// This is how discovery learns which account a running cell is billing
// WITHOUT kraken keeping a side table: the pane itself carries the answer, so
// a cell rebound by the limit rescue is read correctly from the moment its
// pane is respawned, and a pane carl started by hand is read correctly too.
//
// tmux hands a start command back as it was given, which for a command
// containing spaces means wrapped in double quotes with inner quotes
// backslash-escaped, so at most one enclosing pair is peeled off first. What
// remains must BEGIN with the prefix: a CODEX_HOME= appearing anywhere else
// in the command line belongs to something the cell is running, not to the
// cell, and acting on it would attribute the pane to the wrong account.
//
// The value is read as one single-quoted shell word, ending at a quote
// followed by exactly the one space the prefix puts there. The sh escape for
// an embedded single quote — quote, backslash, quote, quote, the only way one
// can appear inside single quotes at all — decodes back to one quote. Every
// other input, an unquoted assignment included, is "".
func CodexHomeFromCommand(cmd string) string {
	s := cmd
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = strings.ReplaceAll(s[1:len(s)-1], `\"`, `"`)
	}
	rest, ok := strings.CutPrefix(s, codexHomeEnvPrefix)
	if !ok {
		return ""
	}
	var b strings.Builder
	for i := 0; i < len(rest); {
		if rest[i] != '\'' {
			b.WriteByte(rest[i])
			i++
			continue
		}
		if strings.HasPrefix(rest[i:], `'\''`) {
			b.WriteByte('\'')
			i += 4
			continue
		}
		// A closing quote: the assignment is only this one if the word ends
		// here, which the prefix guarantees with a single trailing space.
		if i+1 < len(rest) && rest[i+1] == ' ' {
			return b.String()
		}
		return ""
	}
	return "" // never closed: not an assignment kraken wrote
}

// IsCodexRollout reports whether a transcript path is a codex rollout rather
// than a claude transcript. It is the ONE dispatch every reader of a
// transcript path goes through, and it is written as a shape test rather than
// a substring test for a reason the pool created: a rollout used to be
// recognised by containing "/.codex/sessions/", which is true of the machine
// login's rollouts and false of every rollout a POOL SLOT writes — those live
// under <pool>/accounts/<slug>/sessions/. A bound cell's transcript would
// have been parsed as claude JSON and read as empty.
//
// The shape is stable across both: a "sessions" path component, and a base
// name codex itself formats as rollout-<timestamp>-<uuid>.jsonl. A claude
// transcript is <session-uuid>.jsonl under ~/.claude/projects/<munged-cwd>/,
// so it fails the base-name test whatever its directory is called.
func IsCodexRollout(path string) bool {
	if path == "" {
		return false
	}
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "rollout-") || !strings.HasSuffix(base, ".jsonl") {
		return false
	}
	parts := strings.Split(filepath.ToSlash(path), "/")
	for _, p := range parts[:len(parts)-1] {
		if p == "sessions" {
			return true
		}
	}
	return false
}

// PlaceCodexRollout copies a rollout into another codex home at the same
// relative path, and returns where it put it.
//
// It exists because `codex resume <id>` (and `codex exec resume <id>`, the
// form the 2026-09-17 measurement used) resolves the thread against the
// rollout files under ITS OWN CODEX_HOME: measured on codex-cli 0.154.0,
// resuming under a slot the conversation was not recorded in fails
// with `no rollout found for thread id`, and resuming with the file copied to
// the matching relative path under that slot's sessions/ works and appends to
// the copy. So when the limit rescue moves a cell from one account to
// another, the conversation has to be placed before it can be resumed.
//
// This is the ONLY function in kraken that writes anything into a pool slot.
// The write is atomic — a temp file in the destination directory, then a
// rename — because the destination is a directory codex itself reads: a
// half-written rollout there would be a thread that resolves and then fails
// to parse.
//
// Refusals happen before anything is created, so a rejected placement leaves
// the target home exactly as it was. Five beyond the argument checks:
//
//   - the source must live under <fromHome>/sessions/ — anything else means
//     the caller has confused a home with a path, and would scatter files
//     into arbitrary places under toHome;
//   - the two homes must differ, since same-home "placement" would copy a
//     live file over itself;
//   - toHome must already exist as a directory. Creating one would be
//     kraken inventing a codex home: a slot that is not there is a binding
//     that went wrong, and a placement into a freshly-made empty directory
//     would resolve the thread and then fail on the missing auth.json,
//     turning a recoverable refusal into a dead pane;
//   - the source must be readable;
//   - an existing destination may be replaced, but only when it is not
//     LARGER than the source. A rollout is append-only, so a longer
//     destination cannot be an older copy of this conversation — it is
//     either a different conversation that collided on the path, or the
//     copy that has since been resumed and appended to under toHome, and
//     overwriting either one destroys turns that exist nowhere else.
//
// The source is never modified.
func PlaceCodexRollout(rolloutPath, fromHome, toHome string) (string, error) {
	if rolloutPath == "" || fromHome == "" || toHome == "" {
		return "", errCodexPlace("need a rollout path and both codex homes")
	}
	fromAbs, err1 := filepath.Abs(fromHome)
	toAbs, err2 := filepath.Abs(toHome)
	if err1 != nil || err2 != nil {
		return "", errCodexPlace("cannot resolve codex home paths")
	}
	if fromAbs == toAbs {
		return "", errCodexPlace("source and destination codex home are the same directory: " + fromAbs)
	}
	if fi, err := os.Stat(toHome); err != nil || !fi.IsDir() {
		return "", errCodexPlace(toHome + " is not an existing codex home directory")
	}
	sessions := filepath.Join(fromHome, "sessions")
	rel, err := filepath.Rel(sessions, rolloutPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errCodexPlace(rolloutPath + " is not under " + sessions)
	}
	data, err := os.ReadFile(rolloutPath)
	if err != nil {
		return "", errCodexPlace("read rollout: " + err.Error())
	}
	dest := filepath.Join(toHome, "sessions", rel)
	if fi, err := os.Stat(dest); err == nil && fi.Size() > int64(len(data)) {
		return "", errCodexPlace(dest + " is larger than the rollout being placed — it is not an older copy of it")
	}
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", errCodexPlace("create " + dir + ": " + err.Error())
	}
	tmp, err := os.CreateTemp(dir, ".kraken-rollout-*.tmp")
	if err != nil {
		return "", errCodexPlace("stage rollout: " + err.Error())
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", errCodexPlace("write rollout: " + err.Error())
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", errCodexPlace("write rollout: " + err.Error())
	}
	if err := os.Rename(tmpName, dest); err != nil {
		os.Remove(tmpName)
		return "", errCodexPlace("place rollout: " + err.Error())
	}
	return dest, nil
}

// errCodexPlace keeps every PlaceCodexRollout refusal one sentence with the
// same lead-in, so a rescue log line says which step declined without the
// caller having to classify it.
func errCodexPlace(msg string) error {
	return &codexPlaceError{msg: msg}
}

type codexPlaceError struct{ msg string }

func (e *codexPlaceError) Error() string { return "place codex rollout: " + e.msg }
