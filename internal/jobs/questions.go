package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"kraken/internal/core"
	"kraken/internal/util"
)

// questions.go — the per-job doc-questions file: <cellDir>/questions.yaml,
// beside (not inside) artifacts/. zeus writes the whole file (its own
// questions, freely reformatted) while the cell is live; kraken only ever
// patches in `answer`/`answered-at` lines when carl answers one from the web
// board (AnswerDocQuestion), never touching anything else zeus wrote. Same
// "strict means loud, not fragile" spirit as job.go's ParseJob: a hand-rolled
// tiny YAML subset, no library, anything not fully understood errors loudly
// rather than guessing.

// QuestionsPath is the per-job doc-questions file, beside (not inside)
// artifacts/.
func QuestionsPath(j *Job) string { return filepath.Join(cellDir(j), "questions.yaml") }

// A DocQuestion is one open (or answered) doc-question zeus raised for carl
// to resolve from the web board. Its JSON shape becomes wire-contract-pinned
// the moment the API layer serves it (internal/api) — treat field
// renames/removals the same as any other apiContract-covered type.
type DocQuestion struct {
	ID             string `json:"id"`
	Text           string `json:"text"`
	Context        string `json:"context,omitempty"`
	Importance     string `json:"importance"`
	Recommendation string `json:"recommendation,omitempty"`
	Answer         string `json:"answer,omitempty"`
	AnsweredAt     string `json:"answered_at,omitempty"`
}

// docQuestionIDRe bounds a question id to Q<digits> — Q1, Q01, Q42, never a
// bare number or a different prefix.
var docQuestionIDRe = regexp.MustCompile(`^Q[0-9]+$`)

// validImportance mirrors validStates in job.go: a closed set, hand-mangled
// values error rather than reading as some silent default.
var validImportance = map[string]bool{"P0": true, "P1": true, "P2": true}

// docQuestionAllowedKeys are the only continuation keys an item may carry;
// "id" is deliberately absent — it only ever arrives via the "- id: Qn" line.
var docQuestionAllowedKeys = map[string]bool{
	"text": true, "context": true, "importance": true,
	"recommendation": true, "answer": true, "answered-at": true,
}

// docQuestionCleanValue prepares a questions.yaml value for storage:
// delegates entirely to OneLine (cell.go) for control-character handling —
// a questions.yaml value has already been split off its own line before it
// ever reaches here (each continuation is one "    key: value" line), so
// there's no embedded "\n" for OneLine to fold, and folding a stray literal
// tab to a space (rather than deleting it outright) matches how every other
// frontmatter-ish scalar in this codebase gets flattened. Unlike job.go's
// stripComment, a "#" inside the value is left alone — OneLine has no "#"
// handling either, so that's preserved unchanged. Values here round-trip
// through Parse -> Answer -> Parse (carl's answers, zeus' own text/context),
// so a whitespace-flanked "#" must survive as literal content;
// inline-comment stripping was the format's original bug: it read `"# yes"`
// back as empty and defeated the already-answered guard. Full-line comments
// (a line whose first non-whitespace character is "#") are still recognized
// and skipped — that happens per-line, in ParseDocQuestions, before any
// value ever reaches this function.
func docQuestionCleanValue(s string) string {
	return OneLine(s)
}

// isDocQuestionItemStart reports whether line is a "  - id: Qn" item-start
// line, indented by EXACTLY two spaces (a third leading space disqualifies
// it — that's a misindent, not a shallower/deeper variant of the same line).
func isDocQuestionItemStart(line string) bool {
	if len(line) <= 2 || line[0] != ' ' || line[1] != ' ' || line[2] == ' ' {
		return false
	}
	return strings.HasPrefix(line[2:], "- id:")
}

// isDocQuestionContinuation reports whether line is a "    key: value"
// continuation line, indented by EXACTLY four spaces (a fifth leading space
// disqualifies it, same reasoning as isDocQuestionItemStart).
func isDocQuestionContinuation(line string) bool {
	if len(line) <= 4 || line[4] == ' ' || line[4] == '\t' {
		return false
	}
	return line[:4] == "    "
}

// ParseDocQuestions parses questions.yaml's tiny hand-rolled YAML subset:
//
//	questions:
//	  - id: Q1
//	    text: ...
//	    context: ...
//	    importance: P0
//	    recommendation: ...
//	    answer: ...
//	    answered-at: ...
//
// Strict like ParseJob: an unknown/duplicate key, a duplicate or malformed
// id, a stray/misindented line, a missing text, or a bad importance all
// error loudly with a line number rather than being silently dropped or
// guessed at. An empty file, or one that's only blank lines and full-line
// comments, is a valid "no questions yet" state: nil, nil.
//
// Per-line, not per-byte: a CRLF file is normalized to LF before parsing (so
// a CRLF questions.yaml parses identically to its LF equivalent), and a
// leading UTF-8 BOM is stripped before the header is located.
//
// Exported (not just LoadDocQuestions' concern) because the API layer also
// calls it directly, on store-mirrored bytes, to serve a dead job's
// questions — there's no live file to Stat in that path, so the memoized
// loader doesn't apply.
func ParseDocQuestions(data []byte) ([]DocQuestion, error) {
	s := strings.TrimPrefix(string(data), "\ufeff")
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")

	headerLine := -1
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		headerLine = i
		break
	}
	if headerLine < 0 {
		return nil, nil
	}
	// Trailing spaces/tabs on the header line are tolerated (a stray editor
	// artifact, not a format violation); the mismatch error quotes the RAW
	// line via %q so invisible whitespace (that IS the mismatch) is visible
	// rather than silently trimmed away.
	if strings.TrimRight(lines[headerLine], " \t") != "questions:" {
		return nil, fmt.Errorf("line %d: expected \"questions:\" header, got %q",
			headerLine+1, lines[headerLine])
	}

	var out []DocQuestion
	seenID := map[string]bool{}

	var cur *DocQuestion
	var curSeenKeys map[string]bool
	var curLine int

	finishItem := func() error {
		if cur == nil {
			return nil
		}
		if cur.Text == "" {
			return fmt.Errorf("line %d: item %s missing text", curLine+1, cur.ID)
		}
		if !validImportance[cur.Importance] {
			return fmt.Errorf("line %d: item %s bad importance %q", curLine+1, cur.ID, cur.Importance)
		}
		out = append(out, *cur)
		cur = nil
		curSeenKeys = nil
		return nil
	}

	for i := headerLine + 1; i < len(lines); i++ {
		line := lines[i]
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}

		switch {
		case isDocQuestionItemStart(line):
			// Finish (and validate) the PREVIOUS item before validating
			// anything about this one, so errors surface in file order —
			// a previous item's missing-text/bad-importance error is about
			// an earlier line than anything wrong with this item-start line.
			if err := finishItem(); err != nil {
				return nil, err
			}
			rest := strings.TrimPrefix(line[2:], "- ")
			key, val, ok := strings.Cut(rest, ":")
			if !ok || strings.TrimSpace(key) != "id" {
				return nil, fmt.Errorf("line %d: item must start with \"- id: Qn\", got %q", i+1, core.TruncateOneLine(t, 40))
			}
			idVal := docQuestionCleanValue(val)
			if !docQuestionIDRe.MatchString(idVal) {
				return nil, fmt.Errorf("line %d: bad id %q, want Q<digits>", i+1, idVal)
			}
			if seenID[idVal] {
				return nil, fmt.Errorf("line %d: duplicate id %q", i+1, idVal)
			}
			seenID[idVal] = true
			cur = &DocQuestion{ID: idVal}
			curSeenKeys = map[string]bool{}
			curLine = i

		case isDocQuestionContinuation(line):
			if cur == nil {
				return nil, fmt.Errorf("line %d: value line outside any item %q", i+1, core.TruncateOneLine(t, 40))
			}
			key, val, ok := strings.Cut(line[4:], ":")
			if !ok {
				return nil, fmt.Errorf("line %d: bad line %q", i+1, core.TruncateOneLine(t, 40))
			}
			k := strings.TrimSpace(key)
			if !docQuestionAllowedKeys[k] {
				return nil, fmt.Errorf("line %d: unknown key %q", i+1, core.TruncateOneLine(k, 40))
			}
			if curSeenKeys[k] {
				return nil, fmt.Errorf("line %d: duplicate key %q", i+1, core.TruncateOneLine(k, 40))
			}
			curSeenKeys[k] = true
			v := docQuestionCleanValue(val)
			switch k {
			case "text":
				cur.Text = v
			case "context":
				cur.Context = v
			case "importance":
				cur.Importance = v
			case "recommendation":
				cur.Recommendation = v
			case "answer":
				cur.Answer = v
			case "answered-at":
				cur.AnsweredAt = v
			}

		default:
			return nil, fmt.Errorf("line %d: stray/misindented line %q", i+1, core.TruncateOneLine(t, 40))
		}
	}
	if err := finishItem(); err != nil {
		return nil, err
	}
	return out, nil
}

// docQuestionsCache memoizes ParseDocQuestions results per file path, keyed
// on (size, mtime) via os.Stat — same shape as pendingQuestionCache in
// status.go (the board polls this every tick too), but kept as its own
// cache/mutex since the two guard unrelated files.
var (
	docQuestionsCacheMu sync.Mutex
	docQuestionsCache   = map[string]docQuestionsCacheEntry{}
)

type docQuestionsCacheEntry struct {
	size  int64
	mtime time.Time
	qs    []DocQuestion
	err   error
}

// LoadDocQuestions reads and parses path, memoized on (size, mtime). Every
// return — cache hit or fresh parse — hands back a clone of the cached
// slice, never the slice backing the cache entry itself: a caller that
// mutated it in place would silently poison every subsequent cache hit for
// that path. A missing file is a valid "no questions yet" state: nil, nil,
// not an error — and it evicts any stale cache entry for path, so a file
// that existed (and got cached) and was then deleted can't keep serving its
// last-seen questions forever.
func LoadDocQuestions(path string) ([]DocQuestion, error) {
	info, statErr := os.Stat(path)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			docQuestionsCacheMu.Lock()
			delete(docQuestionsCache, path)
			docQuestionsCacheMu.Unlock()
			return nil, nil
		}
		return nil, statErr
	}

	docQuestionsCacheMu.Lock()
	if e, ok := docQuestionsCache[path]; ok && e.size == info.Size() && e.mtime.Equal(info.ModTime()) {
		docQuestionsCacheMu.Unlock()
		return slices.Clone(e.qs), e.err
	}
	docQuestionsCacheMu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			docQuestionsCacheMu.Lock()
			delete(docQuestionsCache, path)
			docQuestionsCacheMu.Unlock()
			return nil, nil
		}
		return nil, err
	}
	qs, perr := ParseDocQuestions(data)

	docQuestionsCacheMu.Lock()
	docQuestionsCache[path] = docQuestionsCacheEntry{size: info.Size(), mtime: info.ModTime(), qs: qs, err: perr}
	docQuestionsCacheMu.Unlock()
	return slices.Clone(qs), perr
}

// docQuestionsAnswerMu serializes AnswerDocQuestion's read -> parse -> patch
// -> write end to end (precedent: uploadMu in uploads.go). Without it, two
// concurrent answers (say Q1 and Q2, answered back to back) each read the
// file, parse it, patch in their own answer, and write the whole thing back
// — with no exclusion, the second writer's read can reflect the file as it
// stood before the first writer's AtomicWrite landed, silently losing the
// first answer. This only serializes writers within THIS process; a
// concurrent zeus rewrite of the file while the cell is live is a separate,
// accepted race — zeus is told the answer/answered-at lines are
// kraken-owned and never touches them itself.
var docQuestionsAnswerMu sync.Mutex

// AnswerDocQuestion patches carl's answer into questions.yaml as a surgical
// edit — never a re-serialize — so every LINE zeus wrote outside the
// answer/answered-at lines survives untouched (CRLF files are normalized to
// LF in the process, same as ParseDocQuestions, so a CRLF file round-trips
// as LF). It reads the file fresh on every call rather than going through
// the memoized LoadDocQuestions cache: a concurrent zeus save landing
// between an answer's read and write must never be clobbered by writing
// back stale cached bytes. It errors on a missing file, a malformed file, an
// unknown id, an empty answer, or a question that's already answered;
// otherwise it locates that question's item block by line and replaces (if
// a present-but-empty line already exists) or appends the answer/
// answered-at lines at the end of that block, then writes the whole file
// back atomically.
func AnswerDocQuestion(path, id, answer, answeredAt string) error {
	docQuestionsAnswerMu.Lock()
	defer docQuestionsAnswerMu.Unlock()

	flat := OneLine(answer)
	if flat == "" {
		return fmt.Errorf("answer must not be empty")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no questions file at %s", path)
		}
		return err
	}
	qs, perr := ParseDocQuestions(data)
	if perr != nil {
		return perr
	}
	var found *DocQuestion
	for i := range qs {
		if qs[i].ID == id {
			found = &qs[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("no such question %q", id)
	}
	if found.Answer != "" {
		return fmt.Errorf("question %s already answered", id)
	}

	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")

	startIdx := -1
	for i, line := range lines {
		if !isDocQuestionItemStart(line) {
			continue
		}
		rest := strings.TrimPrefix(line[2:], "- ")
		_, val, ok := strings.Cut(rest, ":")
		if ok && docQuestionCleanValue(val) == id {
			startIdx = i
			break
		}
	}
	if startIdx < 0 {
		// ParseDocQuestions found it, so this would only happen if the file
		// changed between the two reads — treat it the same as not found.
		return fmt.Errorf("no such question %q", id)
	}

	lastContentLine := startIdx
	for i := startIdx + 1; i < len(lines); i++ {
		line := lines[i]
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if isDocQuestionItemStart(line) {
			break
		}
		lastContentLine = i
	}

	replaceOrAppend := func(key, value string) {
		for i := startIdx + 1; i <= lastContentLine; i++ {
			line := lines[i]
			if !isDocQuestionContinuation(line) {
				continue
			}
			k, _, ok := strings.Cut(line[4:], ":")
			if ok && strings.TrimSpace(k) == key {
				lines[i] = "    " + key + ": " + value
				return
			}
		}
		newLine := "    " + key + ": " + value
		// tail is built by appending onto a len-1/cap-1 slice literal, which
		// always reallocates a fresh backing array the moment it grows — so
		// it never aliases lines' own backing array, and the append below
		// needs no three-index slice-cap guard against clobbering it.
		tail := append([]string{newLine}, lines[lastContentLine+1:]...)
		lines = append(lines[:lastContentLine+1], tail...)
		lastContentLine++
	}

	replaceOrAppend("answer", flat)
	replaceOrAppend("answered-at", OneLine(answeredAt))

	return util.AtomicWrite(path, strings.Join(lines, "\n"))
}
