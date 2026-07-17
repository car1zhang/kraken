package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// bus.go — the escalation/artifact bus. hermes (a cell's human-interface
// agent) drops markdown escalation files into the cell's inbox/ whenever it
// needs a decision only carl can make; kraken surfaces them, carries carl's
// answer back into the file and into hermes' pane, and derives job state
// (blocked/plan-review) from what's still open. Reads of hermes' files are
// lenient — a format slip must never lose an escalation — while the writes
// kraken alone owns (answers, the mid-run log) are atomic and touch nothing
// they don't have to.

// An Escalation is one <inbox>/NNN-*.md file: hermes' write, kraken's read.
// Body and ModTime are bookkeeping, not board data, so (like Job.Body/ModTime)
// they carry no JSON tag.
type Escalation struct {
	File     string    `json:"file"`
	JobSlug  string    `json:"job_slug"`
	Status   string    `json:"status"`
	Category string    `json:"category,omitempty"`
	Title    string    `json:"title,omitempty"`
	Body     string    `json:"-"`
	ModTime  time.Time `json:"-"`
}

// parseEscalation reads one escalation file LENIENTLY: hermes is another
// agent, not a format kraken owns, so unlike parseJob's strict frontmatter
// parser, nothing here ever errors the job — a schema slip degrades to a
// visibly-open escalation with the raw content as its body, never a silently
// dropped one. path is used only to derive a fallback title.
func parseEscalation(jobSlug, path string, data []byte, mtime time.Time) *Escalation {
	e := &Escalation{File: path, JobSlug: jobSlug, Status: "open", ModTime: mtime}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	body := string(data)
	if len(lines) > 0 && isFence(lines[0]) {
		end := -1
		for i := 1; i < len(lines); i++ {
			if isFence(lines[i]) {
				end = i
				break
			}
		}
		if end >= 0 {
			for _, line := range lines[1:end] {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" || strings.HasPrefix(trimmed, "#") {
					continue
				}
				key, rest, ok := strings.Cut(trimmed, ":")
				if !ok {
					continue // unparsable line: ignore it, don't lose the file over it
				}
				val := stripComment(rest)
				switch strings.TrimSpace(key) {
				case "status":
					if val == "answered" || val == "acked" {
						e.Status = val
					} else {
						// absent, "open", or any unrecognized value (typo, a
						// status hermes invents) all read as open — hiding an
						// escalation is the one failure mode worse than a
						// noisy one
						e.Status = "open"
					}
				case "category":
					e.Category = val
				case "title":
					e.Title = val
				default:
					// unknown keys are ignored, not an error — the opposite of
					// parseJob's strict handling, because this format is
					// hermes' to extend, not kraken's to gatekeep
				}
			}
			body = strings.Join(lines[end+1:], "\n")
		}
		// unterminated frontmatter (end < 0): treat the whole file as body
		// with status/category/title left at their defaults, same as if the
		// fence were never opened
	}
	e.Body = strings.TrimSpace(body)
	if e.Title == "" {
		e.Title = titleFromFilename(path)
	}
	return e
}

// titleFromFilename derives a fallback title from an escalation's basename
// when frontmatter didn't supply one: strip hermes' "NNN-" numbering and the
// extension, then un-kebab.
func titleFromFilename(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), ".md")
	i := 0
	for i < len(name) && name[i] >= '0' && name[i] <= '9' {
		i++
	}
	if i > 0 && i < len(name) && name[i] == '-' {
		name = name[i+1:]
	}
	return strings.ReplaceAll(name, "-", " ")
}

// loadEscalations scans every job's inbox for *.md files. A missing inbox
// (job never cell'd, or no escalations yet) skips silently — it is the normal
// case, not an error. Sorted open-first, then newest first within each group.
func loadEscalations(jobs []*Job) []*Escalation {
	var out []*Escalation
	for _, j := range jobs {
		entries, err := os.ReadDir(cellInbox(j))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			path := filepath.Join(cellInbox(j), entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue // hermes may be mid-write/rename; pick it up next poll
			}
			var mtime time.Time
			if info, err := entry.Info(); err == nil {
				mtime = info.ModTime()
			}
			out = append(out, parseEscalation(j.Slug, path, data, mtime))
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		ea, eb := out[a], out[b]
		aOpen, bOpen := ea.Status == "open", eb.Status == "open"
		if aOpen != bOpen {
			return aOpen
		}
		return ea.ModTime.After(eb.ModTime)
	})
	return out
}

// atomicWrite writes content to path via temp file + rename in the same dir —
// the same crash-safe pattern writeJobFields uses — so a reader never
// observes a half-written file.
func atomicWrite(path, content string) error {
	tmp := path + ".kraken.tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// answerFileContent applies carl's answer to an escalation file's bytes: an
// appended "## answer" section plus a status: answered patch. Split out of
// answerEscalation so the mutation is unit-testable without touching disk.
func answerFileContent(content, answer string) (string, error) {
	appended := content
	if !strings.HasSuffix(appended, "\n") {
		appended += "\n"
	}
	appended += "\n## answer (" + time.Now().Format("2006-01-02 15:04") + ")\n\n" + strings.TrimRight(answer, "\n") + "\n"
	return patchField(appended, "status", "answered")
}

// answerEscalation is the TUI's answer path: patch the escalation file, log
// it in the job's mid-run log, then hand it to hermes. The file/log writes
// happen first and are never rolled back — if delivery to hermes fails, carl
// still has a durable, correctly-stated record and just needs to be told to
// poke hermes by hand.
func answerEscalation(j *Job, e *Escalation, answer string) error {
	data, err := os.ReadFile(e.File)
	if err != nil {
		return fmt.Errorf("read %s: %v", e.File, err)
	}
	content, err := answerFileContent(string(data), answer)
	if err != nil {
		return fmt.Errorf("%s: %v", filepath.Base(e.File), err)
	}
	if err := atomicWrite(e.File, content); err != nil {
		return fmt.Errorf("write %s: %v", filepath.Base(e.File), err)
	}
	if err := appendMidRunLog(j, "me→hermes", "answer to "+filepath.Base(e.File)+": "+oneLine(answer)); err != nil {
		return fmt.Errorf("answer saved to %s, but the mid-run log update failed: %v", filepath.Base(e.File), err)
	}
	all, _ := orderAll(discoverAll())
	var hermes *Peer
	for _, p := range all {
		if p.Name == hermesName(j) && p.Messageable && p.Depth == 0 {
			hermes = p
			break
		}
	}
	if hermes == nil {
		return fmt.Errorf("answer saved and logged, but hermes (%s) isn't live — poke it manually with the answer", hermesName(j))
	}
	if err := sendMessage(hermes, "answer to "+e.File+": "+answer); err != nil {
		return fmt.Errorf("answer saved and logged, but delivery to hermes failed (%v) — poke it manually", err)
	}
	// an answered cell is a working cell again; if hermes escalates once more,
	// syncStatesFromInbox re-flags it
	return writeJobState(j, "executing")
}

// appendMidRunLog appends one line to a job file's "## Mid-run log" section
// (adding the heading if the file doesn't have one yet), touching nothing
// above it — the frontmatter and human-authored spec body are never rewritten
// by this path. Atomic temp+rename, same as writeJobFields.
func appendMidRunLog(j *Job, actor, text string) error {
	data, err := os.ReadFile(j.File)
	if err != nil {
		return err
	}
	content := string(data)
	entry := "- [" + time.Now().Format("2006-01-02 15:04") + " " + actor + "] " + text
	// look for the heading in the body only: frontmatter text (e.g. a
	// success block scalar that mentions the heading) must not count, or the
	// real heading never gets written and log lines glue onto the spec
	lines := strings.Split(content, "\n")
	fencesToSkip := 0
	if len(lines) > 0 && isFence(lines[0]) {
		fencesToSkip = 2 // frontmatter present: body starts after its fences
	}
	hasHeading := false
	fences := 0
	for _, line := range lines {
		if fences < fencesToSkip {
			if isFence(line) {
				fences++
			}
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "## Mid-run log") {
			hasHeading = true
			break
		}
	}
	trimmed := strings.TrimRight(content, "\n")
	var newContent string
	if hasHeading {
		newContent = trimmed + "\n" + entry + "\n"
	} else {
		newContent = trimmed + "\n\n## Mid-run log\n" + entry + "\n"
	}
	return atomicWrite(j.File, newContent)
}

// syncStatesFromInbox derives each started, not-yet-done job's state from its
// open escalations: "plan-review" if any is categorized plan-review,
// otherwise "blocked". Jobs with no open escalation are left alone — the
// answer path (answerEscalation) is what restores "executing". Returns a
// human-readable line per transition actually written, for the caller to log.
func syncStatesFromInbox(jobs []*Job, escs []*Escalation) []string {
	openByJob := map[string][]*Escalation{}
	for _, e := range escs {
		if e.Status == "open" {
			openByJob[e.JobSlug] = append(openByJob[e.JobSlug], e)
		}
	}
	var transitions []string
	for _, j := range jobs {
		if !j.started() || j.State == "done" {
			continue
		}
		open := openByJob[j.Slug]
		if len(open) == 0 {
			// self-heal the daemon/TUI write race: a job left in a waiting
			// state with nothing open was answered — a stale daemon write
			// clobbered the answer's transition, so restore it here
			if j.State == "blocked" || j.State == "plan-review" {
				prev := j.State
				if err := writeJobState(j, "executing"); err == nil {
					transitions = append(transitions, fmt.Sprintf("%s: %s → executing (no open escalations)", j.Slug, prev))
				}
			}
			continue
		}
		desired := "blocked"
		for _, e := range open {
			if e.Category == "plan-review" {
				desired = "plan-review"
				break
			}
		}
		if j.State == desired {
			continue
		}
		prev := j.State
		if err := writeJobState(j, desired); err != nil {
			continue // leave state alone; the next poll retries
		}
		transitions = append(transitions, fmt.Sprintf("%s: %s → %s (%d open escalation(s))", j.Slug, prev, desired, len(open)))
	}
	return transitions
}

// listArtifacts lists a cell's artifact files, sorted by name. Missing dir
// (no cell, or nothing produced yet) reads as no artifacts, not an error.
// listArtifacts returns absolute paths of every file under the cell's
// artifacts dir, recursing into subdirectories (an artifact can be organized
// into folders, e.g. icon-variations/) so the board and the app can list and
// navigate them. Hidden files and dirs are skipped.
func listArtifacts(j *Job) []string {
	root := cellArtifacts(j)
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip it, keep walking the rest
		}
		if d.IsDir() {
			if p != root && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		out = append(out, p)
		return nil
	})
	sort.Strings(out)
	return out
}
