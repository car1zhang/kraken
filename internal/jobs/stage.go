package jobs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// stage.go — which pipeline phase a cell is in, derived by kraken rather than
// declared by any agent, exactly like the rest of status derivation: the
// board's stage badge is read off zeus' own transcript, from the last
// pipeline skill the session actually used. There is no stage field anywhere
// an agent writes — a self-reported stage would outlive the phase the same
// way a session file's "busy" outlives the turn.

// pipelineStageSet names the four phase-governing skills — the only skill
// names that count as a stage. `pr` was a fifth until its PR step folded
// into `do` (job 624: every /do now ends with the PR, opened BEFORE the
// manual-test gate, and revisions are pushed to it), so a Read of a
// `pr/SKILL.md` in an older transcript no longer matches and the badge
// falls back to the signal before it — `do`, the phase that job was
// actually in. `load`, `handoff` and `direct` are
// deliberately absent: `load` is a reference skill (no phase, no cell
// section — see skills.go's SkillsFS comment) consulted mid-phase, so a
// `load` hit would overwrite the real phase with a lookup; `handoff` is a
// compaction-with-a-record that returns the agent to the phase it was in,
// and the daemon types it into a pane on its own, so a `handoff` hit would
// overwrite the badge with a word that names no phase; `direct` is a
// modifier over aim/info/do (one agent, no crew) that a job reads FIRST and
// then follows in tandem with the phase skill it modifies, so the phase
// skill's read is the real signal and a `direct` stage would badge every
// quick job with a word that is not a phase. Every other skill a cell might touch
// (personal skills, a target repo's own project skills) is ignored by virtue
// of not being in this set. Kept as a literal rather than derived from SkillsFS,
// which is nil whenever root's init hasn't run (unit tests of this package);
// the root skills-embed test pins the two lists against each other in both
// directions, so a new pipeline skill can't be added to the bundle without
// landing here too, and a stage cannot outlive its skill.
var pipelineStageSet = map[string]bool{
	"info":  true,
	"aim":   true,
	"do":    true,
	"dream": true,
}

// IsPipelineStage reports whether a skill name is one of the four
// phase-governing pipeline skills. Exported for the root package's
// skills-embed test, which holds this set in lockstep with the embedded
// bundle (every embedded skill except `load`, `handoff` and `direct` must
// be a stage, and a stage cannot outlive its skill).
func IsPipelineStage(name string) bool { return pipelineStageSet[name] }

// PipelineStages returns the stage names, sorted. Exported for the same
// skills-embed test as IsPipelineStage: the predicate alone can only answer
// per-name membership, so the test could never catch a stage lingering in
// this set after its skill left the bundle — enumerating is what lets it
// check the lockstep in both directions.
func PipelineStages() []string {
	names := make([]string, 0, len(pipelineStageSet))
	for name := range pipelineStageSet {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// The three shapes a skill use takes in a zeus transcript, each measured from
// real cell transcripts (2026-08-14):
//
//  1. an assistant tool_use named "Skill" whose input carries the skill name
//     (`"input":{"skill":"aim", ...}`) — the harness's own invocation route;
//  2. a user command message — carl typing `/do` into the pane arrives as a
//     STRING content carrying `<command-name>/do</command-name>`;
//  3. an assistant Read of `<anywhere>/.claude/skills/<name>/SKILL.md` — the
//     route the cell brief actually mandates (crew.go: skills are read by
//     path, never invoked by name, since a personal skill shadows the
//     worktree copy), so for a /do that skipped Phase 1 — nobody typed `/do`;
//     zeus read do's SKILL.md and started — it is the ONLY signal that
//     appears.
//
// stageCommandRe matches shape 2's tag (with or without the leading slash —
// both spellings appear in real transcripts; the class includes `:` `.` `/`
// so a scoped spelling like `/kraken:do` is captured whole and normalized by
// the same stageName trim shape 1 uses). stageSkillPathRe matches shape 3's
// path; the `.claude/skills/` segment is required so an incidental read of
// some other skills-ish path can't count, while still matching both the
// worktree copy and a shadowing personal one (`~/.claude/skills/...`).
var (
	stageCommandRe   = regexp.MustCompile(`<command-name>/?([A-Za-z0-9_:./-]+)</command-name>`)
	stageSkillPathRe = regexp.MustCompile(`\.claude/skills/([A-Za-z0-9_-]+)/SKILL\.md$`)
)

// stageName normalizes a skill name from shapes 1 and 2 down to a stage, or
// "": directory/plugin-scoped skills spell as `<scope>:<name>` — `apps/web:do`
// from the Skill arm, or `kraken:do` once stageCommandRe has taken a typed
// `/kraken:do`'s slash — and the stage is the part after the last colon. One
// normalizer for both shapes, so a typed scoped command and the equivalent
// Skill invocation cannot disagree.
func stageName(name string) string {
	if i := strings.LastIndex(name, ":"); i >= 0 {
		name = name[i+1:]
	}
	if pipelineStageSet[name] {
		return name
	}
	return ""
}

// stageCache memoizes FindStage results per transcript path, keyed on
// (size, mtime) via os.Stat — the same shape (and the same reason) as
// status.go's pendingQuestionCache: the board polls this against multi-MB
// transcripts on every payload rebuild.
var (
	stageCacheMu sync.Mutex
	stageCache   = map[string]stageCacheEntry{}
)

type stageCacheEntry struct {
	size  int64
	mtime time.Time
	stage string
}

// FindStage scans a zeus session transcript for the last pipeline skill the
// session used and returns its name ("aim", "do", ...), or "" when no
// pipeline skill has been used yet. Lenient throughout, like every reader of
// Claude-owned files: an unreadable file, malformed lines, or no skill use
// all read as "", never an error. Results are memoized on (size, mtime); an
// unchanged file returns the cached verdict without rescanning.
//
// Last-wins across all three signal shapes is the whole algorithm: a cell
// moves through its phases in transcript order (spec → /aim → /do, the PR
// being do's own last step), so the most recent signal is the current
// stage. The known false positive is a cell that READS a pipeline skill it
// is not in — and for a dream cell, whose whole job is opening and editing
// the skill files, that is the NORMAL case, not an exotic one: its badge
// lands on whichever skill it touched last and stays wrong for the rest of
// the run, since no later real signal ever arrives (measured on
// dream-2026-08-14, when `pr` was still a skill of its own: the last three
// signals were reads of do, aim, pr — badge `pr`). The simple rule is kept
// anyway because the badge is advisory, a dream job's slug already names
// what it is, and ranking Read below the other shapes would break the
// common case to fix the rare one: a cell that skips Phase 1 ENTERS do by
// reading do's SKILL.md (the brief forbids invoking it by name, and nobody
// typed `/do`), so for the phase most worth showing the Read is often the
// only signal there is.
func FindStage(transcript string) string {
	info, statErr := os.Stat(transcript)
	if statErr == nil {
		stageCacheMu.Lock()
		if e, ok := stageCache[transcript]; ok && e.size == info.Size() && e.mtime.Equal(info.ModTime()) {
			stageCacheMu.Unlock()
			return e.stage
		}
		stageCacheMu.Unlock()
	}

	stage := scanStage(transcript)

	if statErr == nil {
		stageCacheMu.Lock()
		stageCache[transcript] = stageCacheEntry{size: info.Size(), mtime: info.ModTime(), stage: stage}
		stageCacheMu.Unlock()
	}
	return stage
}

// stageEvent is the slice of a transcript line scanStage cares about.
// Content stays raw because its TYPE is the discriminator: a command message
// (shape 2) is a user event whose content is a bare JSON string, while tool
// calls live in an assistant event whose content is a block array. Everything
// else in a real event is ignored by virtue of not being in this struct.
type stageEvent struct {
	Type    string `json:"type"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type stageBlock struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// scanStage does the actual line-by-line transcript scan behind FindStage's
// cache.
func scanStage(transcript string) string {
	f, err := os.Open(transcript)
	if err != nil {
		return ""
	}
	defer f.Close()

	var stage string

	sc := bufio.NewScanner(f)
	// Same headroom as scanPendingQuestion: transcript lines run to several
	// MB, and bufio's 64KB default would silently truncate the scan.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev stageEvent
		if json.Unmarshal(line, &ev) != nil {
			continue // malformed line: skip it, don't lose the rest of the transcript
		}
		switch ev.Type {
		case "assistant":
			var blocks []stageBlock
			if json.Unmarshal(ev.Message.Content, &blocks) != nil {
				continue
			}
			for _, b := range blocks {
				if b.Type != "tool_use" {
					continue
				}
				if s := stageFromToolUse(b); s != "" {
					stage = s
				}
			}
		case "user":
			// Only a STRING content is a candidate: command messages arrive
			// that way, while array content is tool_results and attachment
			// blocks — a quoted `<command-name>` in a tool_result (a tool
			// reading some other transcript, this very feature's own tests)
			// is someone else's words, and neither shape is this session
			// using a skill.
			var text string
			if json.Unmarshal(ev.Message.Content, &text) != nil {
				continue
			}
			for _, m := range stageCommandRe.FindAllStringSubmatch(text, -1) {
				if s := stageName(m[1]); s != "" {
					stage = s
				}
			}
		}
	}
	if sc.Err() != nil {
		// scan failed partway (e.g. a line over the buffer max): fail closed
		// rather than report a stage off a truncated scan — the tail is
		// exactly where the CURRENT stage's signal lives.
		return ""
	}
	return stage
}

// stageFromToolUse extracts a stage name from one assistant tool_use block,
// or "" — shapes 1 (Skill) and 3 (Read of a skill's SKILL.md).
func stageFromToolUse(b stageBlock) string {
	switch b.Name {
	case "Skill":
		var input struct {
			Skill string `json:"skill"`
		}
		if json.Unmarshal(b.Input, &input) != nil {
			return ""
		}
		return stageName(input.Skill)
	case "Read":
		var input struct {
			FilePath string `json:"file_path"`
		}
		if json.Unmarshal(b.Input, &input) != nil {
			return ""
		}
		if m := stageSkillPathRe.FindStringSubmatch(input.FilePath); m != nil && pipelineStageSet[m[1]] {
			return m[1]
		}
	}
	return ""
}
