package core

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// tokens.go — the four token counters Claude Code stamps on every assistant
// record, summed over a whole project dir INCREMENTALLY.
//
// The word is deliberately "tokens" and never "usage": `usage` is already
// taken in this binary and means ACCOUNT QUOTA (usage.go, usagepool.go,
// jobs/usage.go's ~/.kraken/usage.json, api's /api/usage). Colliding on it
// would make two unrelated numbers indistinguishable in every future grep.
//
// Why incremental, rather than a sum on read: the numbers are wanted per JOB,
// on a board payload that carries ~74 jobs and rebuilds roughly every two
// seconds. A full re-decode of one 20-60 MB cell project dir costs
// 0.1-0.3s warm in Go, so even the live cells alone would put seconds inside
// the board build's mutex. Transcripts are append-only JSONL, which makes the
// cheap shape obvious: remember how many bytes of each file have already been
// summed and read only past that. A settled job then costs one stat per file
// forever after, and the daemon — not the board — pays even that.
//
// Everything here follows mine.go's contract for claude-owned data, because
// it reads exactly the same files: an unreadable file or a malformed line
// skips that file or that line and the sum keeps going. The one thing that is
// NOT degraded to silence is a partial scan — see SumTokensDelta's error
// contract. A failed read is not a zero.

// Tokens is the four counters Claude Code reports on an assistant record,
// summed, plus the number of transcript files they were summed from.
//
// All four are carried RAW and there is no weighted total: the four are
// billed at wildly different rates (cache reads are ~14x the volume of
// everything else on a real cell and the cheapest per token), and baking a
// price ratio in here would go stale the moment pricing moves. Consumers
// weight them themselves.
type Tokens struct {
	In         int64 // input_tokens — uncached input
	Out        int64 // output_tokens — generated
	CacheWrite int64 // cache_creation_input_tokens
	CacheRead  int64 // cache_read_input_tokens
	// Sessions is a FILE COUNT (root transcripts + subagent sidecars), which
	// is why it is the one field of a SumTokensDelta result that is a TOTAL
	// rather than a delta — see that function's doc comment.
	Sessions int

	// The ZEUS half of the same four counters: the part of In/Out/CacheWrite/
	// CacheRead above that a cell's LEAD agent spent, as opposed to everything
	// it delegated to (teammates and in-process subagents alike). Each is a
	// SUBSET of the counter it shadows, never a sibling of it, so the subagent
	// half is In-ZeusIn and so on.
	//
	// Only the zeus half is ever carried, and that is deliberate: two stored
	// halves can drift out of agreement with the total they are supposed to
	// add up to, a derived one cannot. Whoever renders the split subtracts.
	//
	// Flat named fields rather than a nested Tokens, because every struct in
	// this pipeline — core.Tokens, store.JobTokens, api.webTokens — spells its
	// counters out flat, and a nested one here would be the only place a
	// reader has to remember which of the two shapes they are holding.
	//
	// Which transcript is whose is decided per FILE, by isAgentTranscript, and
	// never persisted — see SumTokensDelta.
	//
	// json:"-" on all four for one reason: core.Tokens is marshaled in exactly
	// one place, `k mine digest`'s top-level roll-up (cmd/kraken/cmd_mine.go),
	// which sums a NIGHT across every session on the machine rather than one
	// job's cell. That roll-up has no lead agent to attribute to and mine.go
	// never populates these, so shipping them there would put four permanent
	// zeros in front of a reader who would reasonably read them as "zeus spent
	// nothing".
	ZeusIn         int64 `json:"-"`
	ZeusOut        int64 `json:"-"`
	ZeusCacheWrite int64 `json:"-"`
	ZeusCacheRead  int64 `json:"-"`
}

// tokenUsage is the `usage` object on an assistant record's message. It is
// its own type rather than four fields inline because both readers of it —
// mine.go's per-session digest and tokenRecord below — must agree on the
// wire names exactly, and a second transcription of
// `cache_creation_input_tokens` is precisely the sort of near-invisible typo
// that would report a plausible number that is silently wrong.
type tokenUsage struct {
	Input         int64 `json:"input_tokens"`
	Output        int64 `json:"output_tokens"`
	CacheCreation int64 `json:"cache_creation_input_tokens"`
	CacheRead     int64 `json:"cache_read_input_tokens"`
}

// tokenRecord is the whole of a transcript line the sum cares about, and it
// is deliberately NARROWER than mine.go's mineRecord: that one carries
// Message.Content as a json.RawMessage, which on an assistant record copies
// the entire content array (a Write call inlines a whole file body there).
// The sum touches every line of every transcript on the machine and needs
// none of it, so decoding into this instead skips that copy entirely.
type tokenRecord struct {
	Type string `json:"type"`
	// SessionID is `session_id` (SNAKE case) — the session that actually made
	// the API call this record reports. It is NOT `sessionId` (camel case),
	// which names the file the record currently lives in and is REWRITTEN on
	// every record when a session is forked. The snake-case one survives the
	// rewrite, which is what makes a record written by another session
	// identifiable. See tokenRecordOwned.
	SessionID string `json:"session_id"`
	// Timestamp is when the record was written. tokenRecordOwned compares it
	// with the file's creation time to tell a record COPIED in from another
	// session apart from one another session's process wrote here itself.
	Timestamp string `json:"timestamp"`
	Message   struct {
		// ID is the API message id. One assistant message is written as one
		// JSONL record PER CONTENT BLOCK (thinking / text / each tool_use),
		// all sharing this id — so this is what collapses a group back to one
		// contribution. Three of the four counters are stamped identically on
		// every record of a group; output_tokens is NOT, and tokenGroup is
		// what handles the difference.
		ID    string     `json:"id"`
		Usage tokenUsage `json:"usage"`
	} `json:"message"`
}

// tokenGroup is the state addTokenLine carries from one record to the next: a
// message's id, and how much of its output_tokens has already been committed
// to the running total.
//
// The id alone was the whole tracker once, on the premise that every record of
// a group carries a byte-identical `usage` and so a group could be collapsed
// by keeping the FIRST record and skipping the rest. That premise is true for
// exactly three of the four counters. THE CORPUS, stated once so every number
// below is unambiguous: every kraken cell transcript on this machine AS OF
// 2026-07-30 — 2,016 files, 1,752 root and 264 sidecar — grouped by message.id
// exactly as addTokenLine does, giving 59,047 groups, 39,818 of them
// multi-record. Every absolute COUNT here is from that one snapshot and will
// drift as live cells append; the RATIOS and the ZEROS are the load-bearing
// parts and are stable. Which of the four counters actually varies within a
// group:
//
//	                    multi-record   output_tokens   other three
//	                          groups          varies          vary
//	root transcripts          35,374               0             0
//	subagent sidecars          4,444           4,022             0
//
// So `input_tokens`, `cache_creation_input_tokens` and `cache_read_input_tokens`
// really are repeated verbatim, everywhere, and first-wins is right for them.
// `output_tokens` is repeated verbatim only in ROOT transcripts. In a subagent
// sidecar the leading records of a group carry a STREAMING PARTIAL and the last
// carries the message's total — real groups read `[2, 10728]`, `[3, 3241]`,
// `[5, 184]` — so first-wins took the 2 and threw away the message. Over those
// 4,444 sidecar groups that is 31,832 output tokens counted where 4,621,522
// were spent, a 145x under-count on the groups themselves and 6.57x on all
// sidecar output.
//
// The error is not a uniform scale factor and that is why it had to be fixed
// rather than tolerated: it is EXCLUSIVE to sidecars, with no exceptions
// anywhere in the corpus. Root output is 1.0000x — byte-identical before and
// after — and the sidecars' OTHER three counters are byte-identical too, so
// the whole of it lands on one counter of one file kind. 261 of 264 sidecars
// are affected; the worst single file counted 13 output tokens against 36,326
// actually spent (2,794x), and the largest absolute miss was 786 against
// 64,436.
//
// A sidecar is always the subagent half of the split (isAgentTranscript's arm
// 1 classifies it by path), so the direction is fixed: the DELEGATED half was
// deflated and zeus' SHARE correspondingly inflated. Sidecar spend read as
// 1.31% of all output where it is really 8.00%. That is the same failure
// direction CLAUDE.md already records for the `agent-setting` arm-2 bug,
// reached by an entirely separate route — worth knowing, because it means two
// independent defects were both flattering the lead's number rather than
// cancelling out.
//
// Note the vocabulary trap: "delegated" in kraken's split is sidecars PLUS
// teammate root transcripts, and it is ~72.6% of output. Teammate roots are
// root transcripts and were never affected. The 1.31%-to-8.00% figures are the
// SIDECAR share specifically, not the delegated half.
//
// THE RULE IS LAST-WINS, not max-wins, and the two are not the same claim even
// though they agree on every group ever measured here (0 of 59,047 groups have
// last != max; 0 are non-monotonic). Last-wins is the SEMANTIC statement —
// `usage` is cumulative for the message and the final record is the one that
// reports it finished — while max-wins is an approximation that happens to
// hold. The difference is load-bearing at the resume boundary: last-wins makes
// the seed EXACT (see precedingGroup), because "committed so far" is then just
// the preceding record's own value, whereas max-wins would need the running
// maximum of records already consumed and thrown away.
//
// out is therefore "how much of this group's output_tokens is already in the
// caller's total", and a repeat record contributes the DIFFERENCE.
//
// THAT DIFFERENCE MAY BE NEGATIVE, and that is correct rather than a bug: it
// would be a group whose tail reports LESS than its head, and under last-wins
// the right total is still the tail's, so the subtraction walks the running
// total back down to it. It is the one arithmetic path here with no live
// example — 0 of the snapshot's groups are non-monotonic — so it is stated and
// tested rather than left to be rediscovered as an apparent corruption. Do not
// "guard" it with a max() or a clamp at zero: that would silently convert the
// only shape this rule handles differently from max-wins into agreement with
// max-wins, which is the choice this comment argues against below. Under
// last-wins that difference may be negative, which is correct rather than a
// bug: it would be a group whose tail reports less than its head, and the right
// total is still the tail's.
type tokenGroup struct {
	id  string
	out int64
}

// commit folds one assistant record into the group and returns the four
// counters the caller should ADD. It is the single definition of what "one
// message contributes once" means, and every reader of a transcript in this
// package goes through it: addTokenLine (the incremental per-job sum),
// digestTranscript (mine.go's per-session digest), and assistantGroup (the
// resume-boundary seed, which derives the state by replaying this very
// function rather than by restating its rules).
//
// THAT SINGLE DEFINITION IS THE POINT, and it is the fix for how the bug
// survived as long as it did. The rule used to be written out twice — once
// here and once inline in digestTranscript's loop — so the two readers agreed
// with each other and both disagreed with the transcripts, which is exactly
// what makes a false premise read as ground truth. Do not re-inline it.
//
// THE RULE. `eligible` is the caller's own gate (ownership for the sum;
// ownership AND the time window for the digest); an ineligible record commits
// nothing and returns zeros. Otherwise:
//
//   - A NEW group contributes all four counters.
//   - A REPEAT — same non-empty message.id as the previous assistant record —
//     contributes ONLY the output DELTA against what the group has already
//     committed. Its other three are a verbatim repeat of the first record's
//     and counting them is the ~2x over-count this dedupe exists to prevent.
//
// The id advances on EVERY assistant record, eligible or not, because the
// tracker follows the file's record sequence rather than the counted subset.
//
// THE CONTRACT A CALLER MUST SATISFY. These are preconditions, not remarks
// about the three call sites that exist today: the helper is advertised as the
// single definition of the rule, so the next caller will arrive with its own
// loop, and every one of these is an unstated premise of the kind this whole
// rule already got wrong once.
//
//	P1. ONE tokenGroup PER FILE. The tracker's lifetime is one transcript's
//	    record sequence and nothing here bounds it, so a caller that summed a
//	    whole DIRECTORY with one shared group would be wrong rather than
//	    merely untidy: a forked transcript is SEEDED with its parent's records
//	    (tokenRecordOwned), so one message.id genuinely appears in two files —
//	    1,135 such ids across 6 cell dirs on this machine, 498 in the worst
//	    one. A shared tracker reads file B's first record as a continuation of
//	    file A's group and contributes a DELTA, frequently negative, where all
//	    four counters were owed.
//
//	P2. ONLY `assistant` RECORDS MAY REACH commit, and this is the likelier
//	    mistake because it looks like a simplification. `eligible == false`
//	    means "contributes nothing"; it does NOT mean "changes nothing" — the
//	    record still advances `g.id`, and being !repeat it also clears `g.out`
//	    and thereby ENDS the group. So folding the type test into the
//	    eligibility expression —
//	        g.commit(rec.Message.ID, rec.Message.Usage, rec.Type == "assistant" && owned)
//	    which reads exactly like the tidy-up this refactor invites — makes
//	    every tool_result between two blocks of one message a group boundary,
//	    and the second block then re-counts all four. That is 9,385 groups on
//	    this machine, in the OVER-count direction. All three current call sites
//	    return early on the type BEFORE calling: addTokenLine, assistantGroup,
//	    and digestTranscript's `case "assistant"`.
//
//	P3. ONE GROUP, ONE DESTINATION. Every eligible record of a group must be
//	    accumulated into the SAME total, and `eligible == false` must mean
//	    "contributes nothing", never "goes somewhere else". `out` is a running
//	    total of what THAT destination has already taken, so a gate ROUTING
//	    part of a group elsewhere leaves every later delta measured against a
//	    total its destination never received — silently, and in whichever
//	    direction the split fell. A future caller wanting to split one group's
//	    spend needs a tokenGroup per destination, not a cleverer gate.
//
// Both current gates satisfy P3 trivially: `tokenRecordOwned` and the digest's
// window both mean "drop".
//
// A GATE MAY FLIP WITHIN A GROUP, and all three of those shapes are handled
// WITHIN ONE PASS. The qualifier is not decoration: `repeat` is cross-record
// state, and the resume-boundary seed cannot reconstruct it from the single
// record it replays (see assistantGroup), so an INELIGIBLE record sitting
// exactly on a boundary is the one place a flip is handled differently either
// side of it. Two further limits on how to read this list: it is about the GATE
// flipping, and the fourth shape below is not a gate flip at all.
//
//   - INELIGIBLE then eligible (a group straddling the digest's `since`, and
//     the first record after a resume): `out` is 0 at the group's start, so
//     the first eligible record contributes the message's cumulative figure in
//     full. Right: nothing has been taken yet.
//   - eligible then INELIGIBLE (the same straddle the other way): the total
//     keeps the partial the eligible records established and the trailing
//     records add nothing.
//   - eligible then INELIGIBLE then eligible: the middle record must LEAVE
//     `out` alone. Zeroing it there — which this code did until it was traced —
//     makes the third record contribute its whole cumulative output on top of
//     what the first already committed: `[out=2][unowned][out=10728]` summed to
//     10,730 against a true 10,728. That is an OVER-count, the one direction
//     these accumulate-only counters can never recover from, and the exact
//     direction the truncation argument below exists to avoid. It is currently
//     UNREACHABLE, and that was verified rather than assumed: NO multi-record
//     group anywhere carries more than one distinct `session_id`, NO assistant
//     record is undated, and NO group's timestamps run backwards — so neither
//     gate can flip twice within one group. Latent, not live. Those are zeros
//     rather than counts, which is why they are quoted without a date: a
//     single counter-example would make this reachable, so the claim is
//     exactly as strong as the next re-measurement.
//
// So `out` is cleared only when the ineligible record STARTS a new group, and
// that clear is load-bearing in its own right: without it a new group inherits
// the previous group's figure and its first eligible record subtracts against
// it. Traced: `[A out=500][B ineligible][B out=30]` sums to 30 against a true
// 530. The condition is exactly "not a repeat", which is why it is written as
// the same `repeat` the eligible path uses rather than as a second test.
//
// THE FOURTH SHAPE, so the three above are not read as the complete account of
// mid-group behaviour: the ID changing and then coming BACK —
// `[m1 out=2][m2 or a blank id][m1 out=10728]`. The third record is !repeat, so
// it opens a fresh group and re-counts all four. Nothing here defends against
// that and nothing needs to: the records of one message are CONTIGUOUS within a
// file (measured, zero non-contiguous reappearances across a whole cell dir),
// which is what makes "count it when the id CHANGES" sufficient in the first
// place. That fact lives in sumTokensFrom's GROUP DEDUPE paragraph, two
// functions away, and is repeated here because this is where a reader now comes
// to learn what a group tolerates. If it ever stops holding, `commit` is the
// wrong place to fix it — the tracker would need memory of every id in the
// file, not just the previous one.
//
// WHY LAST-WINS IS SAFE ON A GROUP TRUNCATED MID-WRITE, which is the question
// to ask of any "take the terminal record" rule. A transcript is appended to
// live, so a pass routinely sees a group's first records and not its last.
// Last-wins always has an answer for that: the group's committed figure is
// whatever the last COMPLETE record said — sumTokensFrom never counts a line
// that isn't newline-terminated — so a half-written group reads as a FLOOR
// that rises as the file grows, and the very next pass raises it via the delta
// (see precedingGroup). Under-count that self-heals, never an over-count that
// cannot. Two alternatives were measured and rejected on exactly this point:
//
//   - MAX-WINS agrees with last-wins on all 59,047 groups here (0 have
//     last != max, 0 are non-monotonic) and has the same floor property, but
//     it makes the resume seed inexact — "committed so far" becomes a running
//     maximum over records the earlier pass discarded, rather than a value
//     still on disk. Right for a whole-file scan, subtly wrong for this one.
//   - "THE RECORD WHOSE stop_reason IS NON-NULL" sounds equivalent and is not:
//     over the same 2026-07-30 snapshot of 59,047 groups, 35,400 carry MORE
//     THAN ONE non-null stop_reason (so it identifies no single record) and 518
//     (0.88%) carry none at all (so they would contribute nothing, forever).
//     The weaker true statement — the LAST non-null one is the last record,
//     58,529 of 58,529 — is just last-wins with an extra field to read and a
//     518-group hole.
func (g *tokenGroup) commit(id string, u tokenUsage, eligible bool) tokenUsage {
	repeat := id != "" && id == g.id
	g.id = id
	if !eligible {
		if !repeat {
			// Only a record that STARTS a new group clears the figure — see
			// the contract above. Clearing it mid-group over-counts; not
			// clearing it here lets a new group inherit the previous one's.
			g.out = 0
		}
		return tokenUsage{}
	}
	if repeat {
		delta := tokenUsage{Output: u.Output - g.out}
		g.out = u.Output
		return delta
	}
	g.out = u.Output
	return u
}

// tokenRecordOwned reports whether an assistant record represents an API call
// this FILE actually paid for, as opposed to one replayed into it from
// another session.
//
// A kraken side thread is `claude --fork-session` in the cell's worktree (and
// `k b` is the same), so a fork's transcript is SEEDED with the parent
// conversation — every replayed assistant record carried over verbatim,
// `usage` and all. Both files live in the one project dir the sum walks, so
// without this the pre-fork history is counted once per fork. Measured on a
// real cell dir (`btw-on-jobs`, 53 files): 260 message ids appearing in more
// than one file, and a 1.43x over-count on top of the group inflation above.
// The bias is the wrong way round in the worst possible sense — it falls
// hardest on jobs that used side threads, which is exactly how a block gets
// investigated, and so exactly the jobs the nightly pass should look hardest
// at.
//
// The rule: a record is owned when it names no session at all, names THIS
// file's own session, or names another session but was written AFTER this
// file was created.
//
//   - Absent is owned. Subagent sidecars (`subagents/agent-*.jsonl`) carry no
//     `session_id` whatsoever — verified, 159 of 159 records on a real cell —
//     and they hold ~24% of a cell's work, so treating absent as foreign
//     would throw away more than the double-count it fixed.
//   - Present and equal to `own.ID` is owned.
//   - Present, different, and dated at or after `own.Born` is owned. The
//     snake `session_id` names the PROCESS that wrote the record, not the
//     file: a `/clear` starts a new file whose every record still carries the
//     process' original session, and an interactive resume from a fresh
//     process appends records under that process' session. Neither is a
//     copy — the API call exists in no other file — and both are written
//     after the file they land in was created.
//   - Present, different, and dated BEFORE `own.Born` is a replay: skip it. A
//     fork copies its parent's history, so every copied record predates the
//     fork's own file.
//   - Present and different with no usable date, or with `own.Born` unknown,
//     is treated as a replay. That is this rule before it learned the
//     creation time, so a file whose creation time cannot be read — or was
//     reset by a copy — counts exactly what it counted then.
//
// The third arm can only ADD records to what the first two own, never drop
// one, so the failure directions are asymmetric by construction: a lost
// creation time under-counts back to the old rule, and an over-count needs a
// copied record dated after the file it was copied into.
//
// `own.ID` is the file's own session id, which for a root transcript is its
// filename stem. A sidecar's stem is `agent-<id>` and never matches anything,
// but its records have no `session_id` to compare, so it is carried by the
// absent rule rather than by the stem.
//
// Verified against an independent whole-corpus dedupe by globally-unique
// message id: the two-arm rule reproduced that number EXACTLY on 118 of 119
// real cell project dirs, and to within 0.001% across the corpus as a whole,
// the single divergence being a synthetic fixture whose records predate
// `session_id` entirely — which is the honest limit of the rule and is stated
// on SumTokensDelta. The three-arm rule was re-run against the same dedupe
// over every project dir present on the machine at the re-run, cell or not:
// it matched in every one, every cell dir's total was unchanged (each
// cell-dir file naming another session is a fork), and the only totals that
// moved belonged to directories holding no cell.
func tokenRecordOwned(rec tokenRecord, own fileOwner) bool {
	if rec.SessionID == "" || rec.SessionID == own.ID {
		return true
	}
	if own.Born <= 0 {
		return false
	}
	ts := isoMillis(rec.Timestamp)
	return ts > 0 && ts >= own.Born
}

// fileOwner is what tokenRecordOwned judges a record against: the transcript
// file's own session, and when that file came into existence.
type fileOwner struct {
	// ID is the file's own session id — see fileSessionID.
	ID string
	// Born is the file's creation time in unix millis, 0 when unknown.
	Born int64
}

// fileBorn is the creation time of an open transcript, in unix millis, or 0
// when it cannot be read.
//
// It is the file's BIRTH time rather than its modification time: a transcript
// is appended to for as long as its session lives, and only the moment it
// first existed separates history copied into it from work done in it. The
// rule assumes the file exists before its first record is dated, which every
// transcript measured here satisfies. kraken is macOS-only and reads the field
// directly. A file copied or restored gets a new birth time later than its
// records, which tokenRecordOwned degrades on in the under-count direction.
func fileBorn(f *os.File) int64 {
	if f == nil {
		return 0
	}
	fi, err := f.Stat()
	if err != nil {
		return 0
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	return time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec).UnixMilli()
}

// fileSessionID is the session a transcript file belongs to — the value
// tokenRecordOwned compares a record's `session_id` against.
//
// A root transcript is NAMED for its session, so the stem is the answer. A
// subagent sidecar is not: it lives at
// <project>/<session-id>/subagents/agent-<id>.jsonl and its stem is
// `agent-<id>`, a value no `session_id` can ever equal. Deriving the parent
// session from the path instead of falling back on the stem matters because
// the stem makes the ownership test unsatisfiable for sidecars, leaving them
// carried ENTIRELY by the "absent is owned" arm — true today (159 of 159
// records carry no `session_id`) but a format kraken does not control. If
// Claude Code ever starts stamping sidecars with their parent's session id,
// the stem would reject every one of them and silently drop ~24% of a cell's
// work; the parent id accepts them, which is the correct answer either way.
func fileSessionID(path, name string) string {
	if filepath.Base(filepath.Dir(path)) == "subagents" {
		// <project>/<session-id>/subagents/<name>
		return filepath.Base(filepath.Dir(filepath.Dir(path)))
	}
	return strings.TrimSuffix(name, ".jsonl")
}

// ZeusAgentNamePrefix and ThreadAgentNamePrefix are the two `agentName`
// prefixes that mean a transcript is the cell LEAD's rather than a delegated
// agent's — see isAgentTranscript, which is their only consumer.
//
// They are literals HERE, in core, rather than read from the code that mints
// them, because the minting lives in internal/jobs (ZeusName, crew.go;
// ThreadNamePrefix, thread.go) and the dependency runs util ← store ← core ←
// jobs. core cannot import jobs, and threading a job's name down through
// SumTokensDelta would put a job-shaped parameter on a function whose whole
// contract is "sum this directory".
//
// So the coupling is made explicit instead of hidden: these are exported
// precisely so a test on the JOBS side — TestZeusAndThreadNamesMatchCoreLeadPrefixes
// in internal/jobs, which may import core — can assert that what ZeusName and
// ThreadAgentName actually produce still begins with them. Without that test
// the two halves of this rule drift apart in silence, and the symptom is not a
// crash but a lead's spend quietly appearing in the subagent column.
const (
	ZeusAgentNamePrefix   = "zeus-"
	ThreadAgentNamePrefix = "thread-"
)

// IsLeadAgentName reports whether an `agentName` from a transcript head names
// a cell LEAD — zeus itself or one of its side threads — as opposed to a
// delegated agent.
//
// An empty name is NOT a lead by this function; the caller decides what an
// absent name means, because "no agentName record at all" (a manual claude, a
// session predating the record) is a different state from "a name that is not
// zeus'".
func IsLeadAgentName(name string) bool {
	return strings.HasPrefix(name, ZeusAgentNamePrefix) ||
		strings.HasPrefix(name, ThreadAgentNamePrefix)
}

// agentMarkerLines and agentMarkerBytes bound the head probe that tells a
// DELEGATED agent's root transcript from the lead's (isAgentTranscript). Two
// bounds rather than one because either alone is unsafe: a record count says
// nothing about how much is read when a single transcript line can be 8.3 MB
// (mineScanBufMax's own reason), and a byte ceiling alone would keep decoding
// json out of a file that had already answered the question.
//
// Both are set above what the data needs, re-measured rather than inherited.
// THE CORPUS, stated once so every number below is unambiguous: every file
// under every ~/.claude/projects/-Users-<user>--kraken-cells-* dir on this
// machine — 1,750 in all, of which 1,588 are ROOT transcripts and 162 are
// subagent sidecars. The sidecars never reach this probe (the path arm
// classifies them first), so both bounds are measured over the 1,588 roots:
//
//   - `agentName` — the load-bearing signal — appears on a head record of
//     1,581 of them, at worst on LINE 5.
//   - `agent-setting` appears on 1,239, at worst on line 2.
//   - 7 roots carry neither and are read as the lead. All seven are manual
//     claudes carl started in a cell worktree by hand ("hello bro", "hi", "are
//     you able to open zip files?") or sessions with no user turn at all, so
//     the lead is the correct answer for every one of them.
//
// THE BYTE BOUND IS NOT ABOUT WHERE THE SIGNAL STARTS, and reading it that way
// is a bug this probe has already had once. `agentName` sits on a DEDICATED
// `agent-name` record on only 120 of those 1,588 files; on the other 1,467 it
// rides the session's first USER record — the teammate-message brief the lead
// sent — which is a single line with a whole prompt in it. The record starts at
// byte 431 at the latest, but the line it is on ENDS as far in as 57,148 bytes
// (median 4,114; p99 23,362). A window that admits only the start decodes a
// truncated line, fails, finds no signal, and books a teammate's whole spend to
// the lead — which is exactly what an 8 KB window did to 6 hermes transcripts
// (8.5M tokens) in one cell dir. So the bound must cover the whole RECORD:
// 256 KB is 4.6x the largest ever seen.
//
// That ceiling is not what is normally read, and agentMarkerInit is what makes
// that true rather than merely hoped for. bufio.Scanner.Buffer ALLOCATES its
// initial buffer immediately — `s.buf = buf[0:cap(buf)]`, before the first
// Scan — and only ever doubles from there, so the initial capacity is a real
// per-probe cost while the maximum is only a ceiling. Seeding it with
// mineScanBufInit (64 KB, sized for the main transcript scan) spent 64 KB on
// every probe of every grown file to read a median 4 KB of head; 4 KB seeds it
// at the median instead and doubles the handful of times a fat brief needs.
const (
	agentMarkerLines = 16
	agentMarkerBytes = 256 << 10
	agentMarkerInit  = 4 << 10
)

// isAgentTranscript reports whether a transcript is a DELEGATED agent's — the
// subagent half of the split — as opposed to the cell lead's own.
//
// Three shapes of file live in one cell's project dir and the rule sorts all
// three:
//
//   - The lead's own root transcript — zeus. Not an agent.
//   - A SIDE THREAD's root transcript — also not an agent, deliberately. A
//     thread is `claude --fork-session` of zeus in the same worktree (carl
//     branching off to ask about the work), so it speaks in zeus' place rather
//     than being work zeus handed out. Only its own turns land here anyway:
//     the parent records replayed into it are already dropped by
//     tokenRecordOwned.
//   - A delegated agent's transcript — an agent, in both of its forms. A
//     TEAMMATE (hephaestus, athena-*, hermes-*: an Agent-tool call carrying a
//     name) runs as its own claude under teammateMode: tmux and so gets its
//     own ROOT transcript in the very same project dir; an in-process subagent
//     gets a sidecar at <project>/<session-id>/subagents/agent-*.jsonl.
//
// THREE ARMS, in the order they are decided:
//
//  1. PATH. A file under a `subagents/` dir is an in-process subagent's
//     sidecar — the same shape fileSessionID already uses to recognise one,
//     because a sidecar's location identifies it and no file content is
//     needed to see it.
//  2. `agent-setting` in the head. Claude Code writes that record only for an
//     Agent-tool spawn, so it never lies. It is kept even though arm 3
//     currently subsumes it entirely (measured: of 1,239 roots carrying
//     `agent-setting`, ZERO lack an `agentName`) because it costs one field of
//     one decode and it is the arm that still holds if `agentName` ever stops
//     being written.
//  3. `agentName` in the head, judged by IsLeadAgentName. This is the arm that
//     actually does the work, and it exists because arm 2 ALONE IS WRONG: the
//     `agent-setting` record's payload is the spawn's `subagent_type`
//     (`agentSetting: "general-purpose"`), so it records a spawn OPTION rather
//     than the fact of being a spawn. A teammate given a `name` and a prompt
//     but no `subagent_type` — which is exactly what zeus' crew is — gets no
//     such record at all. Measured on this machine: 222 of 1,461 teammate
//     transcripts (15.2%) carry no `agent-setting`, spread across 20 cell dirs
//     and dated as recently as the day before this was written, and on one
//     real job that alone booked 10.6M tokens of teammate spend to the lead —
//     rendering its zeus share 38% too high.
//
// Arm 3 is a prefix test rather than a name lookup because kraken MINTS both
// lead names: `zeus-<slug>` (jobs.ZeusName) and `thread-<slug>-<tid>`
// (jobs.ThreadAgentName). Everything else with an `agentName` was spawned by
// the Agent tool. Getting the thread prefix in here is not cosmetic — 2 side
// thread transcripts on this machine carry `agentName: "thread-…"`, and
// without it they would land in the subagent half in flat contradiction of the
// rule stated above.
//
// Both content arms decode JSON per line rather than substring-matching, on
// purpose: `"type":"agent-setting"` or an `agentName` appearing inside some
// record's prose would otherwise mis-bin a whole transcript. That is also what
// makes the byte bound load-bearing rather than cosmetic — a decode needs the
// WHOLE line, and the line carrying `agentName` is usually a whole brief.
//
// The scan stops at the first record that answers. Reading on would cost the
// rest of a head window whose fattest records sit right there, and it can
// change nothing: measured across the 1,245 transcripts carrying both signals,
// `agent-setting` precedes the first `agentName` in every single one, so no
// early return can hide it.
//
// A probe that cannot READ, and a head window with no signal in it, both
// degrade to NOT-agent, i.e. to the lead. The lead is the conservative bucket
// in the only sense that matters here: the job's total is identical either way
// (the split is a partition of a number already summed), so the failure mode
// is a visibly-too-large lead half rather than a total that is silently wrong.
func isAgentTranscript(f *os.File, path string) bool {
	if filepath.Base(filepath.Dir(path)) == "subagents" {
		return true
	}
	// A SectionReader rather than a Seek + Read, so this stays a pread like
	// every other probe here and cannot disturb the caller's file offset.
	// The scanner's buffer GROWS from agentMarkerInit only as a line demands
	// it, so the agentMarkerBytes ceiling is a bound and not an allocation.
	// The seed is this probe's OWN constant rather than mineScanBufInit — see
	// agentMarkerInit: Buffer allocates its initial capacity up front, so
	// borrowing the main scan's 64 KB seed spent 64 KB to read a median 4 KB.
	sc := bufio.NewScanner(io.NewSectionReader(f, 0, agentMarkerBytes))
	sc.Buffer(make([]byte, 0, agentMarkerInit), agentMarkerBytes)
	for i := 0; i < agentMarkerLines && sc.Scan(); i++ {
		rec := headMarkers(sc.Bytes())
		if rec.Type == "agent-setting" {
			return true // arm 2: never lies, so it answers immediately
		}
		if rec.AgentName != "" {
			return !IsLeadAgentName(rec.AgentName) // arm 3
		}
	}
	// No signal in the window. That is the lead ANSWER without being a lead
	// NAME: a manual claude in a cell worktree carries no agentName, and so
	// does a transcript whose head could not be read — a scan error, or a line
	// past the ceiling, both of which end the loop here rather than guessing.
	return false
}

// headAgentName returns the transcript head's `agentName`, or "" when the
// head window carries none (a sidecar, a manual claude, an unreadable head).
// Same window, bounds and decode rule as isAgentTranscript — it exists so the
// digest can NAME the agent where the splitter only classifies it, and it is
// a separate scan rather than a second return value because isAgentTranscript
// early-returns on `agent-setting` before any name appears.
func headAgentName(f *os.File) string {
	sc := bufio.NewScanner(io.NewSectionReader(f, 0, agentMarkerBytes))
	sc.Buffer(make([]byte, 0, agentMarkerInit), agentMarkerBytes)
	for i := 0; i < agentMarkerLines && sc.Scan(); i++ {
		if rec := headMarkers(sc.Bytes()); rec.AgentName != "" {
			return rec.AgentName
		}
	}
	return ""
}

// headMarker is the two fields isAgentTranscript reads out of a head record.
// Narrow on purpose, like tokenRecord: these lines are decoded for every
// transcript that grew since the last pass, and the line they are decoded from
// is routinely a whole teammate brief, so a shape with a Content field would
// copy that brief for nothing.
type headMarker struct {
	Type      string `json:"type"`
	AgentName string `json:"agentName"`
}

// headMarkers decodes one head line into the two markers. Malformed lines cost
// only themselves, matching this package's contract for data another program
// owns; a line that IS valid JSON with one wrongly-typed field keeps whatever
// decoded, exactly as addTokenLine treats one.
func headMarkers(line []byte) headMarker {
	var rec headMarker
	if err := json.Unmarshal(line, &rec); err != nil {
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) {
			return headMarker{}
		}
	}
	return rec
}

// withZeusHalf fills in a per-file result's zeus counters: everything a LEAD
// transcript contributed is zeus spend, and everything a delegated agent's
// contributed is not.
//
// A copy of the four rather than a second pass over the records, because the
// classification is a property of the FILE and not of any record in it — which
// is also why the caller applies this once per file rather than per line.
func withZeusHalf(tk Tokens, lead bool) Tokens {
	if !lead {
		return tk
	}
	tk.ZeusIn, tk.ZeusOut = tk.In, tk.Out
	tk.ZeusCacheWrite, tk.ZeusCacheRead = tk.CacheWrite, tk.CacheRead
	return tk
}

// ProjectDirName encodes a cwd the way Claude Code names its transcript dirs
// under ~/.claude/projects. It is the exported face of projectDirName
// (discover.go) — the same implementation, never a second copy of the
// munging rule — for callers outside this package that need to go from a
// directory to the transcripts written in it. internal/jobs is the one such
// caller: a job's cell worktree path munges to the dir holding every
// transcript zeus, its side threads and its whole teammate crew wrote.
func ProjectDirName(cwd string) string { return projectDirName(cwd) }

// SumTokensDelta sums one project dir's token counters incrementally.
//
// projectDir is an ABSOLUTE path to a dir under ~/.claude/projects.
// priorOffsets maps an absolute transcript path to the number of bytes of it
// already summed by an earlier call; a path missing from the map is summed
// from zero.
//
// The returned Tokens is a DELTA — the tokens found PAST each prior offset —
// with one deliberate exception: Sessions is the TOTAL number of transcript
// files present in the dir right now, not a count of newly-appeared ones.
// A file count does not accumulate (a caller that added it every pass would
// report a job with 12 transcripts as having 4,000 of them), so a caller
// folds the four counters in with += and Sessions in with =. Both halves of
// that rule are stated on store.BumpJobTokens, which is what actually applies
// it.
//
// The returned map is the NEW offsets, one entry per transcript file found —
// which is also how a caller learns that a file vanished (it is absent) and
// how it learns a file was truncated or replaced (its offset went DOWN).
// Append-only is the assumption everywhere else; a file SHORTER than its
// recorded offset can only be a file that is no longer the one that was
// summed, so it is re-read whole from zero.
//
// Offsets always land on a COMPLETE line. A transcript being appended to
// right now can end mid-record, and recording the file's size as the offset
// would skip the rest of that record forever; the final line is therefore
// only counted once it is known to be newline-terminated.
//
// WHAT COUNTS AS ONE CONTRIBUTION. Two rules, both of which exist because the
// naive reading of a transcript over-counts by about 2x on real data:
//
//   - One API message is written as N records, so a message contributes
//     exactly ONCE — see sumTokensFrom's group handling. Measured inflation
//     without this: 1.98x across every kraken cell dir on this machine.
//     "Once" is not "the first record", though: three of the four counters
//     are stamped identically on every record of the group and are taken from
//     the first, while `output_tokens` GROWS across a sidecar's group and is
//     taken from the last. See tokenGroup for the measurement.
//   - A record replayed into a forked session's file is not that file's
//     spend — see tokenRecordOwned. Measured inflation without this: a
//     further 1.43x on a cell that used side threads. A record another
//     session's process WROTE here — after a `/clear`, or on an interactive
//     resume — is this file's spend, and the file's creation time is what
//     tells the two apart.
//
// THE ZEUS SPLIT. Alongside the four counters, the result carries the ZEUS
// half of each: the part spent by the cell's lead agent rather than by
// anything it delegated to. It is a partition of the SAME sum — nothing is
// counted, skipped or dropped differently because of it — decided per FILE by
// isAgentTranscript and copied in by withZeusHalf, which is why the subagent
// half is In-ZeusIn and is never carried in its own right.
//
// The classification is recomputed on every pass that opens a file and is
// never persisted. A transcript's KIND is fixed at birth and the probe is a
// bounded read of bytes that can always be re-read, so a stored flag would be
// state with nothing to justify it — and one more thing to migrate. The
// steady state is untouched: a file whose offset already equals its size is
// never opened at all, contributes a zero delta, and so needs no
// classification.
//
// The probe's ANSWER is a slightly weaker claim than the file's kind, and the
// difference is worth naming because the columns accumulate: a pass whose
// answer differed from an earlier pass' would split ONE file's spend across
// both halves permanently. Two states could do it — a transient read failure,
// and a head window holding no complete line yet. Neither is reachable WITH A
// NON-ZERO DELTA, because both marker records precede a session's first
// ASSISTANT record by construction (`agentName` rides the first user turn, and
// an assistant record can only follow one), so a pass that summed any usage at
// all had already read a complete marker line.
//
// Its one honest limit, measured rather than assumed: 7 of the 1,588 root
// transcripts on this machine carry neither marker, and every one of those is
// genuinely the lead — a claude carl started in a cell worktree by hand. There
// is currently NO transcript this rule books to the wrong half. The rule that
// could go stale is the prefix one: kraken mints its lead names
// (ZeusAgentNamePrefix, ThreadAgentNamePrefix) and a rename on the jobs side
// would send zeus' own spend to the subagent column, which is why a test over
// there pins the two together.
//
// Together they reproduce a whole-corpus dedupe by globally-unique message id
// exactly on 118 of 119 real cell project dirs. Three cases they cannot see,
// all documented rather than claimed away:
//
//   - A transcript whose creation time was reset — copied, or restored from a
//     backup — reads every record naming another session as replayed, the
//     genuine ones included. An under-count, and only of the records the
//     creation time was needed for.
//   - A fork whose replayed records carry no `session_id` at all (a transcript
//     format predating that field) still counts twice. Corpus-wide that is
//     0.001% of the total, all of it in one synthetic fixture.
//   - A replayed record whose PARENT transcript is not in this project dir
//     (a fork started from a different cwd) is skipped here and counted
//     nowhere — an under-count. It does not arise for kraken side threads or
//     `k b` inside a cell, since both inherit the worktree cwd.
//
// Errors mean the sum for this pass is NOT EXACT, and the message says how.
// A missing project dir, an unreadable file and a malformed line are all
// normal and cost only themselves — they yield a smaller sum, never an error.
// Everything else is reported, in one of two flavours:
//
//   - SHORT (a floor): a file scanned only part way (a line over the 16 MB
//     ceiling, a mid-file read error), a SUBTREE that could not be listed, a
//     file that could not be STAT'd. The counters must not be presented as a
//     total, and — the part a caller must not skip — the returned offset map
//     is INCOMPLETE, so an absent path no longer means "this file is gone".
//     A caller that replaces its stored watermarks with this map will drop
//     the watermark of a file it merely failed to see, and re-sum that file
//     from zero into counters that only ever accumulate. refreshJobTokens
//     (internal/jobs/tokens.go) is what carries the unseen ones forward.
//   - LONG (possibly): the group seed at a resume boundary could not be
//     resolved within the lookback bounds, so one message may be counted
//     twice. See precedingGroup.
//
// The two are distinguished only by the message, deliberately: a caller's
// correct response to both is the same — fold the result in, say out loud
// that it is not exact — and a typed error would invite branching on a
// distinction nothing downstream can act on differently.
//
// The returned Tokens and offsets are still valid and still worth folding in:
// the offset of a truncated scan stops at the last complete line it did
// count, so the next pass retries from there rather than skipping the gap.
func SumTokensDelta(projectDir string, priorOffsets map[string]int64) (Tokens, map[string]int64, error) {
	var total Tokens
	offsets := map[string]int64{}
	var partial []string

	// WalkDir rather than ReadDir, for MineTranscripts' reason: a project dir
	// holds its root transcripts at the top level and its subagent sidecars
	// two levels down under <session-id>/subagents/. The sidecars hold ~24%
	// of all tool calls (mine.go), and their assistant records carry their
	// own usage — a sum that skipped them would be wrong by roughly that
	// much, with nothing anywhere reporting a problem.
	_ = filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// The ROOT failing is the normal "this job's cell never came up"
			// state — no dir, no transcripts, nothing wrong — and must stay
			// silent, or the daemon would log once a tick forever for every
			// job that never started.
			//
			// A failure DEEPER than the root is a different animal: the dir
			// exists and we could not list part of it (EMFILE in a long-lived
			// daemon, a permission blip, a subtree removed mid-walk). Files
			// under it are then missing from `offsets` and indistinguishable
			// from deleted ones, which is precisely the case that makes a
			// caller drop their watermarks and re-sum them from zero. So it
			// is reported, and the reporting is the whole fix.
			if path != projectDir {
				partial = append(partial, fmt.Sprintf("%s: %v", filepath.Base(path), err))
			}
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			// The sibling of the walk error above, and it has to be reported
			// for the identical reason: the file is real, we simply could not
			// stat it (an un-traversable parent, an lstat that lost a race
			// with a rename), so it is missing from `offsets` while still
			// existing on disk. Left silent, a caller doing a replace-all
			// write deletes its watermark and re-sums the whole file into
			// accumulate-only counters the moment it becomes readable again.
			partial = append(partial, fmt.Sprintf("%s: stat: %v", d.Name(), err))
			return nil
		}
		size := info.Size()
		total.Sessions++

		prior := priorOffsets[path]
		if prior > size {
			prior = 0 // truncated or replaced: this is not the file we summed
		}
		if prior == size {
			// The settled case, and the one the whole design exists for:
			// the file has not grown, so it is never opened. One stat.
			offsets[path] = prior
			return nil
		}

		own := fileSessionID(path, d.Name())
		tk, end, scanErr := sumTokensFrom(path, prior, size, own)
		total.In += tk.In
		total.Out += tk.Out
		total.CacheWrite += tk.CacheWrite
		total.CacheRead += tk.CacheRead
		// The zeus half rides in alongside, already zeroed by sumTokensFrom for
		// a delegated agent's transcript, so this is the same accumulation and
		// not a second decision.
		total.ZeusIn += tk.ZeusIn
		total.ZeusOut += tk.ZeusOut
		total.ZeusCacheWrite += tk.ZeusCacheWrite
		total.ZeusCacheRead += tk.ZeusCacheRead
		offsets[path] = end
		if scanErr != nil {
			partial = append(partial, scanErr.Error())
		}
		return nil
	})

	if len(partial) > 0 {
		return total, offsets, fmt.Errorf("partial token scan in %s: %s", projectDir, strings.Join(partial, "; "))
	}
	return total, offsets, nil
}

// sumTokensFrom sums one transcript's assistant records from byte offset
// `from` up to `limit` (the size stat'd by the caller), returning the new
// offset — always the end of the last COMPLETE line it counted.
//
// limit rather than "to EOF" so the window is exactly the one the caller
// measured: the file may be appended to while this runs, and a scan that ran
// past the stat'd size would count bytes the returned offset does not
// promise were counted. io.LimitReader is what pins that, and it is also why
// this can never be io.ReadAll — the largest transcript on this machine is
// 42 MB with single LINES seen at 8.3 MB, which is what mineScanBufMax
// (16 MB, mine.go) is sized for.
//
// The deferred-line dance is the append-only correctness bit: a line is only
// counted once the NEXT Scan succeeds, which is the only proof that it was
// newline-terminated rather than the half-written tail of a file being
// appended to right now. The final line is counted only if the window's last
// byte is in fact a newline.
//
// GROUP DEDUPE. One API assistant message is written as one record per
// content block (thinking / text / each tool_use), and claude stamps the
// cumulative `usage` on every one — a real ROOT group seen here repeats eight
// times with byte-identical `out=7479 cache_read=430139`. Cache-read is per
// API call and cannot legitimately be counted eight times, so a message
// contributes exactly once. The records of a group are contiguous within a
// file (measured: zero non-contiguous reappearances across a whole 53-file
// cell dir), so "count it when the id CHANGES" is sufficient.
//
// It needs a little more memory than the previous id, and only for one
// counter: in a SUBAGENT SIDECAR the leading records of a group carry a
// streaming PARTIAL `output_tokens` and the last carries the message's total
// (`[2, 10728]` and the like), so the group's output is taken from the last
// record while the other three stay taken from the first. tokenGroup carries
// both halves of that and states the measurement.
//
// The one hazard in that is this function's own incrementality: if `from`
// lands in the middle of a group, the first record read here would look like
// a fresh group and the message would be counted a second time. Rather than
// persisting the last-counted group (a schema column) or rewinding the offset
// to the group's start (which would leave a settled file's final group
// permanently uncounted AND re-read on every pass, defeating the one-stat
// steady state), the group state is RECOVERED from the file: precedingGroup
// reads the single record immediately before `from`. That keeps the boundary
// exact with no stored state, and costs one small read on files that grew.
// It recovers the group's committed OUTPUT along with its id, which is what
// lets the streaming-partial rule below survive a resume — see tokenGroup and
// precedingGroup for why last-wins is what makes that seed exact.
//
// THE ZEUS HALF is filled in here too, once per file rather than per record,
// because it is a property of the FILE (isAgentTranscript) and not of anything
// inside it. Every return that can carry counters goes through withZeusHalf,
// including the partial-scan one — a floor is still a floor of somebody's
// spend, and leaving the split off it would make the zeus and subagent halves
// stop adding up to the total for exactly the rows that are already inexact.
//
// An unopenable/unseekable file degrades to "nothing summed, offset
// unchanged" with no error, matching this package's contract for data
// another program owns. Only a PARTIAL scan errors — sc.Err() being non-nil
// means the scan STOPPED early (a line over the ceiling, a read error), so
// everything after it is invisible, and reporting a clean zero there is the
// exact bug class jobs/status.go's scanPendingQuestion was fixed for. The
// offset is left at the last counted line rather than skipped past the
// offending one: re-reading it next pass is a bounded cost, whereas jumping
// the gap would under-count permanently and silently.
func sumTokensFrom(path string, from, limit int64, ownID string) (Tokens, int64, error) {
	var tk Tokens
	f, err := os.Open(path)
	if err != nil {
		return tk, from, nil
	}
	defer f.Close()

	// The ownership test needs the file's creation time beside its session
	// id. It is read from the handle this function already holds, on every
	// pass rather than persisted: a birth time never changes, so the seed at
	// a resume boundary and the forward scan after it judge every record by
	// the same value whichever pass reads it.
	own := fileOwner{ID: ownID, Born: fileBorn(f)}

	// Which half of the split this file's spend belongs to. Decided HERE
	// rather than by the caller because the probe costs no extra open — this
	// function already holds the handle, and ReadAt is a pread that disturbs
	// nothing below — and because a caller doing it would have to open every
	// file including the settled ones the whole design exists not to open.
	lead := !isAgentTranscript(f, path)

	// Seed the group tracker with whatever record precedes the resume point,
	// so a message split across two passes is still counted once. A seed the
	// walk could not determine is reported, never assumed: the group at the
	// boundary is then counted as new, and if it was a continuation that one
	// message is counted twice — an over-count, which nothing downstream can
	// subtract back out.
	group, seeded := precedingGroup(f, from, own)
	var seedErr error
	if !seeded {
		seedErr = fmt.Errorf("%s: group seed at byte %d unresolved (lookback exhausted); one message may be double-counted",
			filepath.Base(path), from)
	}

	// Is the last byte of the window a newline? ReadAt is a pread and does
	// not disturb the seek offset below, so the order of these two is free.
	complete := false
	if limit > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], limit-1); err == nil {
			complete = last[0] == '\n'
		}
	}
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return withZeusHalf(tk, lead), from, nil
	}

	sc := bufio.NewScanner(io.LimitReader(f, limit-from))
	sc.Buffer(make([]byte, 0, mineScanBufInit), mineScanBufMax)
	off := from
	var pending []byte
	havePending := false
	for sc.Scan() {
		if havePending {
			addTokenLine(&tk, pending, own, &group)
			off += int64(len(pending)) + 1 // +1 for the newline that proved it complete
		}
		pending = append(pending[:0], sc.Bytes()...)
		havePending = true
	}
	if err := sc.Err(); err != nil {
		// pending IS complete here, and that is not a guess: the scan
		// stopped because something FOLLOWED pending in the stream (the
		// oversized line, or the read that failed), which is the same proof
		// a successful next Scan would have given. So it is counted, and off
		// advances to just past it — meaning the next pass restarts at the
		// offending line rather than at the top of the file. It will fail
		// there again, which is deliberate: re-reading one line is a bounded
		// cost, whereas skipping past it would under-count permanently and
		// silently. The error is what says the total is only a floor.
		if havePending {
			addTokenLine(&tk, pending, own, &group)
			off += int64(len(pending)) + 1
		}
		// Both failures can hold at once, and the scan error must not swallow
		// the seed warning: they describe DIFFERENT inexactness (a floor vs a
		// possible double-count) about the same file, and this package's rule
		// is that every path seeing less than the whole truth says so. The
		// counters are unaffected either way — callers treat every error
		// identically — but a log that mentioned only the scan error would drop
		// the double-count warning for the one file that has one.
		scanErr := fmt.Errorf("%s: %v", filepath.Base(path), err)
		if seedErr != nil {
			scanErr = fmt.Errorf("%v; %v", scanErr, seedErr)
		}
		return withZeusHalf(tk, lead), off, scanErr
	}
	if havePending && complete {
		addTokenLine(&tk, pending, own, &group)
		off += int64(len(pending)) + 1
	}
	return withZeusHalf(tk, lead), off, seedErr
}

// addTokenLine folds one transcript line into tk, advancing the caller's
// group tracker.
//
// It owns exactly two decisions and delegates the third:
//
//  1. Not an `assistant` record — only those carry a usage object, and gating
//     on the type keeps a future record shape that happens to echo one from
//     being counted. The tracker is left completely alone, which is what makes
//     a group survive the tool_result records interleaved through it.
//  2. Owned by this file or not (tokenRecordOwned) — a record replayed in from
//     a forked parent session is ineligible. That is this function's `eligible`
//     argument and nothing more.
//  3. What the record then CONTRIBUTES is tokenGroup.commit's, not this
//     function's: mine.go's digest applies the identical rule through the
//     identical helper, and the two used to have their own copies of it, which
//     is how a premise false since the first sidecar went unnoticed.
//
// A record with no `message.id` at all cannot be grouped, so it is counted on
// its own; that is the conservative direction (it can only over-count, and
// only for a shape that does not occur on current transcripts).
//
// A blank or malformed line costs only itself. A line that IS valid JSON but
// has one wrongly-typed field is deliberately NOT discarded: encoding/json
// fills in every field it could and reports the type error at the end, so
// throwing the record away would lose three good counters to one bad one.
// Same treatment, and the same reasoning, as mineInputSummary's in mine.go.
func addTokenLine(tk *Tokens, line []byte, own fileOwner, g *tokenGroup) {
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	var rec tokenRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) {
			return // not JSON at all, or truncated: skip it
		}
	}
	if rec.Type != "assistant" {
		return
	}
	d := g.commit(rec.Message.ID, rec.Message.Usage, tokenRecordOwned(rec, own))
	tk.In += d.Input
	tk.Out += d.Output
	tk.CacheWrite += d.CacheCreation
	tk.CacheRead += d.CacheRead
}

// groupLookbackRecords bounds how many records precedingGroup will walk
// back over while looking for the previous ASSISTANT one. The effective
// tolerance is groupLookbackRecords-1 INTERVENING records, since one
// iteration is spent reading the assistant record itself.
//
// It has to walk at all — rather than read the single record before the
// offset — because the forward tracker skips non-assistant records without
// resetting, so a message whose blocks are interleaved with tool_result
// records is still one group. The bound used to be 64, sized against a
// measured maximum of FIVE intervening tool_result records. That premise is
// dead: Claude Code now writes a run of per-turn bookkeeping records between
// one assistant message and the next (`last-prompt`, `custom-title`,
// `agent-name`, `mode`, `permission-mode`, `atis-latch`, `bridge-session`,
// `file-history-snapshot`), a dozen or more `attachment` records per prompt,
// and — the one that scales — an `api-request-blob` record PER MESSAGE of the
// outgoing request, so a fork's or a resumed session's first turn dumps its
// whole inherited history as blobs. Measured runs of 60-70 intervening records
// exhausted the old bound on two files in one day and on six separate days
// before that, each costing a permanent over-count (see precedingGroup). The
// two bounds cross at 64 MB / 8192 = 8 KB per record: a storm of small records
// (a request blob is about 1 KB) stops on the record count after about 8 MB
// of lines, and a gap of fat tool_results stops on the byte bound. Both are
// an order of magnitude past the longest run measured (135 records, 0.15 MB),
// and exhaustion stays REPORTED rather than silent.
const groupLookbackRecords = 8192

// groupLookbackBytes bounds the same walk by volume. Which bound binds (the
// crossover above) matters because an exhausted lookback costs up to a whole
// message's output (see precedingGroup): single transcript lines do hit 8.3
// MB, so a handful of fat tool_results in one gap must not exhaust it, and 64
// MB covers that with room. Neither constant bounds the walk's I/O on its own:
// every lineBefore step reads at least one lineBeforeWindow whatever the
// line's length, so the read cost is records × window (32 MB at the record
// bound) plus up to a few times the line bytes (the doubling overshoots) —
// which is why that window starts small, and why the walk is paid only on a
// resume of a transcript that grew since the last pass.
const groupLookbackBytes = 64 << 20

// precedingGroup returns the group state left behind by the last assistant
// record before byte offset `from` — its `message.id` and the output_tokens
// already committed for that group — skipping over any non-assistant records
// in between, or the zero group when there is none within the lookback bound.
//
// This is what makes the group dedupe survive the incremental resume
// boundary, and it must mirror the forward tracker EXACTLY: addTokenLine
// leaves the tracker alone for blank, malformed and non-assistant lines, so
// this walks past exactly those and stops at the first assistant record, and
// it zeroes `out` for an unowned one for the same reason addTokenLine does.
// Reading the state back out of the file is cheaper and less stateful than
// persisting it in the store, and unlike rewinding the offset to the group's
// start it leaves a settled file's offset equal to its size — which is what
// keeps the steady-state cost at one stat per file.
//
// WHY THE SEED CAN CARRY `out` AT ALL, and why that is exact rather than a
// guess: under last-wins (see tokenGroup) the amount a pass has committed for
// a group is always the `output_tokens` of the LAST record of it that pass
// read — each record sets the group's committed figure to its own value, and
// the caller's total agrees by induction. So the single record this walk
// already reads to recover the id carries the committed figure too, with no
// extra read, no extra state, and no dependence on records that were consumed
// and discarded. Max-wins would not have this property, which is the reason
// the forward rule is last-wins.
//
// A group SPLIT ACROSS THE BOUNDARY therefore neither double-counts nor loses
// its tail: the first record after the resume is recognised as a repeat (id
// matches), and it contributes its output MINUS what the earlier pass already
// booked. The safe direction is built into that subtraction — if the seed were
// dropped to zero the tail would be counted in full on top of the head, and
// these counters only ever accumulate, so an over-count here is permanent and
// unrecoverable. Nothing downstream can subtract it back out.
//
// The bool distinguishes a CONCLUSIVE answer from a give-up, and that
// distinction is the whole reason this returns two values. Reaching the start
// of the file, or a record that is genuinely the previous assistant one, is
// conclusive — ok=true, even when the group is zero. Running out of lookback
// (either bound) or failing to read is NOT: the seed is then unknown, the
// group at the boundary gets counted as if it were new, and if it was in fact
// a continuation that message is counted TWICE. That is an over-count, the
// one direction this number can never recover from, so it must reach the
// caller rather than hide behind a zero value that means five other things.
//
// PRICE AN EXHAUSTION AT A WHOLE MESSAGE, not at a handful of tokens. An
// unresolved seed is a zero group, so the boundary record reads as NEW and what
// gets counted a second time is whatever the earlier pass had ALREADY COMMITTED
// for that group — under last-wins that is the running cumulative figure, which
// late in a group approaches the message's entire output. Same direction as
// ever (this fails LONG, never short) and still reported, so nothing downstream
// changes; but anyone re-tuning groupLookbackRecords or groupLookbackBytes
// should budget for a message rather than for a few tokens.
//
// The penalty is nonetheless strictly SMALLER than it was under first-wins, and
// this is worth stating precisely because the intuitive reading gets it exactly
// backwards. With G the group's per-record cumulative outputs and the boundary
// after index split-1:
//
//	post-fix  over-count = G[split-1]   (pass 1 committed it; pass 2 re-counts to G[-1])
//	pre-fix   over-count = G[split]     (pass 1 committed G[0]; pass 2 counts G[split])
//
// G is non-decreasing, so G[split-1] <= G[split] — never larger, and strictly
// smaller wherever the group streams. Simulated on the real sidecar group
// [2, 1204, 10728]: splitting after record 1 costs 2 post-fix against 1,204
// pre-fix; after record 2, 1,204 against 10,728. Equality holds only where
// consecutive values are equal, i.e. root transcripts, where both rules
// over-count identically (7,479 either way).
//
// THE TRAP, named because it cost a review round: it is true that the boundary
// record's CONTRIBUTION grew — it now carries the cumulative total rather than
// a streaming partial — but the CORRECT TOTAL it is measured against grew with
// it, from 2 to 10,728. What is double-counted is the earlier pass's committed
// figure, and last-wins made that figure more accurate, not larger relative to
// the truth. Any future claim about this penalty should be simulated against
// each rule's OWN correct total, never compared across rules in absolute
// tokens.
func precedingGroup(f *os.File, from int64, own fileOwner) (tokenGroup, bool) {
	end := from
	var scanned int64
	for i := 0; i < groupLookbackRecords; i++ {
		line, start, ok := lineBefore(f, end)
		if !ok {
			// Either the file starts here — no preceding record exists, which
			// is a real and correct answer — or a read failed. lineBefore
			// reports the former by returning the first line; reaching this
			// with end <= 1 is the start-of-file case.
			return tokenGroup{}, end <= 1
		}
		if scanned += end - start; scanned > groupLookbackBytes {
			return tokenGroup{}, false
		}
		if g, isAssistant := assistantGroup(line, own); isAssistant {
			return g, true
		}
		end = start
	}
	return tokenGroup{}, false
}

// lineBeforeWindow is the first window lineBefore reads when walking back to
// a line's start. It is small because precedingGroup can take thousands of
// steps over tiny records (groupLookbackRecords), each step reading at least
// one window whatever the line's length: at the 64 KB scanner seed that walk
// read and allocated 512 MB in the worst case, far past the byte bound meant
// to cap it. The window doubles until the line's start appears, so a fat
// line still resolves in a handful of reads.
const lineBeforeWindow = 4 << 10

// lineBefore returns the complete line whose terminating newline sits at
// end-1, together with that line's start offset.
//
// The search doubles its window until it finds the line's start, and is
// guaranteed to reach one rather than give up: the forward scanner refuses
// any line over mineScanBufMax, so a line that produced an offset was
// necessarily within that ceiling.
func lineBefore(f *os.File, end int64) ([]byte, int64, bool) {
	if end <= 1 {
		return nil, 0, false
	}
	term := end - 1 // the '\n' that terminated the line we want
	for window := int64(lineBeforeWindow); ; window *= 2 {
		start := term - window
		if start < 0 {
			start = 0
		}
		buf := make([]byte, term-start)
		if len(buf) == 0 {
			return nil, 0, false
		}
		if _, err := f.ReadAt(buf, start); err != nil {
			return nil, 0, false
		}
		if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
			return buf[i+1:], start + int64(i) + 1, true
		}
		if start == 0 {
			return buf, 0, true // the line we want is the file's first
		}
		if window >= mineScanBufMax {
			return nil, 0, false
		}
	}
}

// assistantGroup reports the group state one line leaves behind — the state
// addTokenLine would have left had it read this record — and whether the line
// was an assistant record at all.
//
// The bool is what lets precedingGroup tell "this is a user record, keep
// walking" apart from "this is an assistant record that happens to carry no
// id, stop here" — collapsing those two into a bare zero group would walk
// straight past a real group boundary.
//
// Rather than restating the forward tracker's rules it REPLAYS them: commit is
// run on a fresh group and the resulting state is the seed. That keeps the
// mirror structural — an unowned record leaving the id but no output, an empty
// id grouping with nothing — instead of a second hand-written copy that drifts.
// The discarded return value is the point: this wants commit's effect on the
// group, not its contribution, which belonged to the pass that already read
// this record.
//
// ONE INPUT IT CANNOT MIRROR, stated as an exception because a named one is
// worth more than a guarantee that is almost true. `repeat` is cross-record
// state and this replay has exactly one record, so it always evaluates
// repeat=false. For every input but one that is immaterial. The exception is an
// INELIGIBLE record that is a repeat OF ITS OWN GROUP: the forward tracker
// takes commit's `if !repeat` branch and PRESERVES the group's committed
// output, while this replay takes the other branch and clears it. Executed on
// `[X elig 100][X inelig 500][X elig 800]` split after the middle record: the
// forward state carries out=100, the replayed seed says out=0, so the third
// record deltas against 0 and the two passes total 900 where one whole-file
// pass gives 800.
//
// It is not fixable here — one record cannot know whether it repeated the one
// before it — and it is not worth fixing: it requires a gate that flips
// mid-group, which is the same unreachable premise the third flip shape in
// commit's contract rests on (no multi-record group carries two `session_id`s,
// no assistant record is undated). Reaching it would need a NEW caller whose
// gate flips within a group, and that caller's author is the reader this
// paragraph is addressed to: seeding across a boundary would then need the
// previous record's identity too, not just this one's.
func assistantGroup(line []byte, own fileOwner) (tokenGroup, bool) {
	var rec tokenRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) {
			return tokenGroup{}, false
		}
	}
	if rec.Type != "assistant" {
		return tokenGroup{}, false
	}
	var g tokenGroup
	g.commit(rec.Message.ID, rec.Message.Usage, tokenRecordOwned(rec, own))
	return g, true
}
