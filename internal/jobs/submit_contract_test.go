package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
	"kraken/internal/util"
)

// submit_contract_test.go — the LOGIC dimension of "eliminate the queued
// status", authored blind from artifacts/50-contract.md before any
// implementation existed. Every assertion here is a transcription of a
// contract obligation, not of code.
//
// Every package-level identifier is prefixed `contract`; a sibling edge author
// shares this package's test binary.
//
// What this file pins, in contract order:
//   - SubmitJob's SUCCESS path: the seam is called exactly once, with the
//     RESOLVED repo path, and SubmitJob itself writes no job state.
//   - The two four-state truth tables (DisplayState, deriveState) row by row,
//     plus the lockstep the contract demands between them.
//   - ParseJob's tolerance of the retired keys the on-disk corpus still
//     carries (`ready:`, a `prereqs:` block) — Err == "" and nothing else moves.
//   - JobTemplate carries neither retired key.
//   - teardownFinalFields writes no "ready" key for any marker.
//   - The programmatic creators' generated job text carries neither retired line.

// --- SubmitJob: the success path ---

// contractStartRecord is one observed call of the submitStartCell seam.
type contractStartRecord struct {
	slug string
	repo string
}

// contractStartRecorder counts and captures every seam call. A count is as
// load-bearing as the arguments: "starts the cell" and "starts the cell twice"
// differ by one live tmux session and one git worktree.
type contractStartRecorder struct {
	calls []contractStartRecord
}

// contractStubStartCell swaps the package's start seam for the duration of the
// test and restores it afterwards. err is what the stub reports back.
func contractStubStartCell(t *testing.T, err error) *contractStartRecorder {
	t.Helper()
	rec := &contractStartRecorder{}
	prev := submitStartCell
	submitStartCell = func(j *Job, repo string) error {
		slug := ""
		if j != nil {
			slug = j.Slug
		}
		rec.calls = append(rec.calls, contractStartRecord{slug: slug, repo: repo})
		return err
	}
	t.Cleanup(func() { submitStartCell = prev })
	return rec
}

// contractFakeRepo makes ResolveRepo(name) resolve inside the sandboxed $HOME
// and returns the path it resolves to — a `.git` entry is the whole of the
// repo test, so no real checkout and no git binary are involved.
func contractFakeRepo(t *testing.T, name string) string {
	t.Helper()
	repo := filepath.Join(util.Home(), "Developer", name)
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return repo
}

// TestSubmitContractSubmitJobStartsOnceWithTheResolvedRepo is the whole of the
// success path: every refusal passed, the repo resolved, and the ONE start
// route taken exactly once with the resolved absolute path (never the job's
// `repo:` substring — StartCell needs a checkout, not a name). A nil return
// means the cell came up.
func TestSubmitContractSubmitJobStartsOnceWithTheResolvedRepo(t *testing.T) {
	st := useTestStore(t) // sandboxes HOME
	repo := contractFakeRepo(t, "submit-logic-repo")
	rec := contractStubStartCell(t, nil)

	sj := mustCreateJob(t, st, store.Job{
		Slug: "submit-happy", Repo: "submit-logic-repo", Body: "do the thing\n",
	})
	j := JobBySlug(LoadJobs(), sj.Slug)
	if j == nil {
		t.Fatal("job not found after create")
	}

	if err := SubmitJob(j); err != nil {
		t.Fatalf("SubmitJob on a clean draft = %v, want nil", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("submitStartCell called %d times, want exactly 1: %+v", len(rec.calls), rec.calls)
	}
	if rec.calls[0].slug != "submit-happy" {
		t.Errorf("seam got job %q, want submit-happy", rec.calls[0].slug)
	}
	if rec.calls[0].repo != repo {
		t.Errorf("seam got repo %q, want the resolved checkout %q", rec.calls[0].repo, repo)
	}
}

// TestSubmitContractSubmitJobWritesNoJobState: StartCell owns the `working`
// write. SubmitJob must leave the row exactly as it found it — a SubmitJob that
// stamped `working` itself would leave a job reading as live after a start that
// never happened, which is the dead-cell shape the board cannot distinguish
// from a real one.
func TestSubmitContractSubmitJobWritesNoJobState(t *testing.T) {
	st := useTestStore(t)
	contractFakeRepo(t, "submit-nostate-repo")
	contractStubStartCell(t, nil)

	sj := mustCreateJob(t, st, store.Job{
		Slug: "submit-nostate", Repo: "submit-nostate-repo", Body: "do the thing\n",
	})
	j := JobBySlug(LoadJobs(), sj.Slug)
	if j == nil {
		t.Fatal("job not found after create")
	}
	beforeRev := j.Rev

	if err := SubmitJob(j); err != nil {
		t.Fatalf("SubmitJob = %v, want nil", err)
	}

	// the in-memory value the caller still holds
	if j.State != "" {
		t.Errorf("SubmitJob set j.State = %q in memory, want it untouched", j.State)
	}
	// and the row behind it
	row, err := st.GetJobBySlug("submit-nostate")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if row.State != "" {
		t.Errorf("row state = %q after SubmitJob, want unchanged empty — StartCell owns that write", row.State)
	}
	if row.Rev != beforeRev {
		t.Errorf("row rev moved %d -> %d: SubmitJob wrote to the job row", beforeRev, row.Rev)
	}
}

// TestSubmitContractSubmitStartCellIsAWiredSeam pins that the seam exists as a
// package var with a real default: a nil var would make every real submit a
// panic that no stubbed test can see. Its TYPE — func(*Job, string) error — is
// pinned at compile time by contractStubStartCell's assignment above.
func TestSubmitContractSubmitStartCellIsAWiredSeam(t *testing.T) {
	if submitStartCell == nil {
		t.Fatal("submitStartCell is nil — SubmitJob has no start route at all")
	}
}

// --- the two four-state truth tables ---

// contractDisplayRow is one row of DisplayState's table: the four kraken states
// crossed with parseable/unparseable. Nothing else is an input any more — the
// bySlug parameter went with prereqs.
type contractDisplayRow struct {
	state string
	err   string
	want  string
}

// contractDisplayTable is DisplayState's complete truth table: done wins, then
// an error reads draft, then a kraken-written state wins, else draft.
var contractDisplayTable = []contractDisplayRow{
	{state: "", err: "", want: "draft"},
	{state: "", err: "boom", want: "draft"},
	{state: "working", err: "", want: "working"},
	{state: "working", err: "boom", want: "draft"},
	{state: "blocked", err: "", want: "blocked"},
	{state: "blocked", err: "boom", want: "draft"},
	{state: "done", err: "", want: "done"},
	{state: "done", err: "boom", want: "done"},
}

func TestSubmitContractDisplayStateTruthTable(t *testing.T) {
	for _, c := range contractDisplayTable {
		j := &Job{Slug: "x", State: c.state, Err: c.err}
		if got := DisplayState(j); got != c.want {
			t.Errorf("DisplayState(state=%q err=%q) = %q, want %q", c.state, c.err, got, c.want)
		}
	}
}

// TestSubmitContractDisplayStateNeverSaysQueued is the absence assertion the
// table above cannot make: no input at all may produce the retired word. A
// fifth arm reintroduced by a later edit would satisfy every row above and
// still put a queued card on the board.
func TestSubmitContractDisplayStateNeverSaysQueued(t *testing.T) {
	four := map[string]bool{"draft": true, "working": true, "blocked": true, "done": true}
	for _, state := range []string{"", "working", "blocked", "done"} {
		for _, errText := range []string{"", "boom"} {
			got := DisplayState(&Job{Slug: "x", State: state, Err: errText})
			if !four[got] {
				t.Errorf("DisplayState(state=%q err=%q) = %q, outside the four-state model", state, errText, got)
			}
		}
	}
}

// contractDeriveRow is one row of deriveState's table.
type contractDeriveRow struct {
	state  string
	err    string
	live   bool
	active bool
	want   string
}

// contractDeriveTable is deriveState's COMPLETE truth table: all four states ×
// parseable/unparseable × live × active. Enumerated rather than computed from a
// reference implementation on purpose — a second copy of the rule would agree
// with a wrong first copy.
var contractDeriveTable = []contractDeriveRow{
	// State == "": draft, regardless of anything else. This is the row that
	// replaces "ready → queued": an unstarted job is a draft, full stop.
	{state: "", err: "", live: false, active: false, want: "draft"},
	{state: "", err: "", live: false, active: true, want: "draft"},
	{state: "", err: "", live: true, active: false, want: "draft"},
	{state: "", err: "", live: true, active: true, want: "draft"},
	{state: "", err: "boom", live: false, active: false, want: "draft"},
	{state: "", err: "boom", live: false, active: true, want: "draft"},
	{state: "", err: "boom", live: true, active: false, want: "draft"},
	{state: "", err: "boom", live: true, active: true, want: "draft"},

	// started + parseable: liveness then the roll call decide.
	{state: "working", err: "", live: false, active: false, want: "blocked"},
	{state: "working", err: "", live: false, active: true, want: "blocked"},
	{state: "working", err: "", live: true, active: false, want: "blocked"},
	{state: "working", err: "", live: true, active: true, want: "working"},
	{state: "blocked", err: "", live: false, active: false, want: "blocked"},
	{state: "blocked", err: "", live: false, active: true, want: "blocked"},
	{state: "blocked", err: "", live: true, active: false, want: "blocked"},
	{state: "blocked", err: "", live: true, active: true, want: "working"},

	// an unparseable job reads draft even when its cell is alive and busy.
	{state: "working", err: "boom", live: false, active: false, want: "draft"},
	{state: "working", err: "boom", live: false, active: true, want: "draft"},
	{state: "working", err: "boom", live: true, active: false, want: "draft"},
	{state: "working", err: "boom", live: true, active: true, want: "draft"},
	{state: "blocked", err: "boom", live: false, active: false, want: "draft"},
	{state: "blocked", err: "boom", live: false, active: true, want: "draft"},
	{state: "blocked", err: "boom", live: true, active: false, want: "draft"},
	{state: "blocked", err: "boom", live: true, active: true, want: "draft"},

	// done wins over everything, including an error and a dead cell.
	{state: "done", err: "", live: false, active: false, want: "done"},
	{state: "done", err: "", live: false, active: true, want: "done"},
	{state: "done", err: "", live: true, active: false, want: "done"},
	{state: "done", err: "", live: true, active: true, want: "done"},
	{state: "done", err: "boom", live: false, active: false, want: "done"},
	{state: "done", err: "boom", live: false, active: true, want: "done"},
	{state: "done", err: "boom", live: true, active: false, want: "done"},
	{state: "done", err: "boom", live: true, active: true, want: "done"},
}

func TestSubmitContractDeriveStateTruthTable(t *testing.T) {
	if len(contractDeriveTable) != 32 {
		t.Fatalf("the table has %d rows, want all 32 (4 states x 2 err x live x active)", len(contractDeriveTable))
	}
	for _, c := range contractDeriveTable {
		j := &Job{Slug: "x", State: c.state, Err: c.err}
		if got := deriveState(j, c.live, c.active); got != c.want {
			t.Errorf("deriveState(state=%q err=%q live=%v active=%v) = %q, want %q",
				c.state, c.err, c.live, c.active, got, c.want)
		}
	}
}

// TestSubmitContractDeriveStateNeverSaysQueued is deriveState's absence half of
// the same guarantee DisplayState gets above.
func TestSubmitContractDeriveStateNeverSaysQueued(t *testing.T) {
	four := map[string]bool{"draft": true, "working": true, "blocked": true, "done": true}
	for _, c := range contractDeriveTable {
		got := deriveState(&Job{Slug: "x", State: c.state, Err: c.err}, c.live, c.active)
		if !four[got] {
			t.Errorf("deriveState(state=%q err=%q live=%v active=%v) = %q, outside the four-state model",
				c.state, c.err, c.live, c.active, got)
		}
	}
}

// TestSubmitContractDisplayAndDeriveStateAgreeInLockstep is the invariant the
// contract states in prose: the two tables move in one commit. Wherever
// deriveState's live/active inputs are irrelevant — done, unparseable, and
// never-started — the two functions must return the SAME answer, so a change to
// one that skipped the other fails here rather than shipping a board whose
// column disagrees with its own detail line.
func TestSubmitContractDisplayAndDeriveStateAgreeInLockstep(t *testing.T) {
	for _, c := range contractDisplayTable {
		// the three shapes where liveness cannot matter
		if !(c.state == "done" || c.err != "" || c.state == "") {
			continue
		}
		j := &Job{Slug: "x", State: c.state, Err: c.err}
		want := DisplayState(j)
		if want != c.want {
			t.Fatalf("DisplayState(state=%q err=%q) = %q, want %q", c.state, c.err, want, c.want)
		}
		for _, live := range []bool{false, true} {
			for _, active := range []bool{false, true} {
				if got := deriveState(j, live, active); got != want {
					t.Errorf("lockstep broken for state=%q err=%q live=%v active=%v: deriveState = %q, DisplayState = %q",
						c.state, c.err, live, active, got, want)
				}
			}
		}
	}
}

// --- ParseJob tolerance for the retired keys ---

// TestSubmitContractParseJobToleratesRetiredKeys: the corpus on disk carries
// `ready:` and `prereqs:` (every hand-written job file, and every frontmatter
// composed before the cutover). Deleting the parser's cases would error the
// whole corpus into draft with an error badge, so both keys stay
// accept-and-ignore: Err == "" and nothing else moves.
func TestSubmitContractParseJobToleratesRetiredKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
	}{
		{"ready true", "---\njob: x\nrepo: monolith\nready: true\n---\n\nspec text\n"},
		{"ready false", "---\njob: x\nrepo: monolith\nready: false\n---\n\nspec text\n"},
		{"ready empty", "---\njob: x\nrepo: monolith\nready:\n---\n\nspec text\n"},
		{"prereqs empty list", "---\njob: x\nrepo: monolith\nprereqs: []\n---\n\nspec text\n"},
		{
			"prereqs block with gated continuation lines",
			"---\njob: x\nrepo: monolith\nprereqs:\n  - job: dep-a\n    gated: true\n  - job: dep-b\n---\n\nspec text\n",
		},
		{
			"both keys, with the template's inline comments",
			"---\njob: x\nrepo: monolith            # substring-matched\nready: true               # flip to true to queue\nprereqs:\n  - job: dep-a\n    gated: true\n---\n\nspec text\n",
		},
		{
			"ready before the identity keys",
			"---\nready: true\njob: x\nrepo: monolith\n---\n\nspec text\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := ParseJob("x"+JobSuffix, []byte(tc.src))
			if j == nil {
				t.Fatal("nil job (must never crash)")
			}
			if j.Err != "" {
				t.Fatalf("retired key must parse clean, got Err %q", j.Err)
			}
			// "no behavioral effect": the keys are consumed, and everything
			// around them still lands.
			if j.Slug != "x" || j.Repo != "monolith" {
				t.Errorf("identity dropped: slug=%q repo=%q", j.Slug, j.Repo)
			}
			if !strings.Contains(j.Body, "spec text") {
				t.Errorf("body truncated by the retired key: %q", j.Body)
			}
			if j.State != "" {
				t.Errorf("state = %q, want empty — a retired key must not imply a state", j.State)
			}
			if got := DisplayState(j); got != "draft" {
				t.Errorf("DisplayState = %q, want draft — a `ready:` line no longer queues anything", got)
			}
		})
	}
}

// --- JobTemplate ---

// contractFrontmatterKeys returns every frontmatter key in a job file, with a
// leading `#` comment marker stripped so a merely COMMENTED-OUT retired key is
// still reported. The template is what `k n`/nvim expands, so a commented
// `# ready:` line would keep teaching the retired concept to every new job.
func contractFrontmatterKeys(src string) []string {
	lines := strings.Split(src, "\n")
	var keys []string
	inFM := false
	for i, line := range lines {
		if strings.TrimSpace(line) == "---" {
			if i == 0 || !inFM {
				inFM = true
				continue
			}
			break
		}
		if !inFM {
			continue
		}
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
		idx := strings.Index(trimmed, ":")
		if idx <= 0 {
			continue
		}
		keys = append(keys, strings.TrimSpace(trimmed[:idx]))
	}
	return keys
}

func TestSubmitContractJobTemplateCarriesNeitherRetiredKey(t *testing.T) {
	src := JobTemplate("template-logic")
	for _, retired := range []string{"ready", "prereqs"} {
		for _, key := range contractFrontmatterKeys(src) {
			if key == retired {
				t.Errorf("JobTemplate still carries a %q key:\n%s", retired, src)
			}
		}
		if strings.Contains(src, retired+":") {
			t.Errorf("JobTemplate still mentions %q::\n%s", retired, src)
		}
	}
	// and it must still be a template that parses — the removal is a deletion,
	// not a rewrite of the skeleton's shape.
	j := ParseJob("template-logic"+JobSuffix, []byte(src))
	if j.Err != "" {
		t.Fatalf("JobTemplate no longer parses: %s\n%s", j.Err, src)
	}
	if got := DisplayState(j); got != "draft" {
		t.Errorf("a fresh template job reads as %q, want draft", got)
	}
}

// --- teardownFinalFields ---

// TestSubmitContractTeardownFinalFieldsNeverWritesReady sweeps every marker the
// binary understands plus the two it refuses: none of them may write a "ready"
// key, because there is no such field any more. Kill returns a job to draft by
// clearing `state` alone.
func TestSubmitContractTeardownFinalFieldsNeverWritesReady(t *testing.T) {
	markers := []string{
		"kill",
		"done",
		"done:",
		"done:accepted",
		"done:shipped the fix",
		"",
		"nonsense",
	}
	for _, marker := range markers {
		got, ok := teardownFinalFields(marker)
		if !ok {
			if got != nil {
				t.Errorf("teardownFinalFields(%q) refused but returned %v, want nil", marker, got)
			}
			continue
		}
		if len(got)%2 != 0 {
			t.Fatalf("teardownFinalFields(%q) = %v, want an even key/value list", marker, got)
		}
		for i := 0; i < len(got); i += 2 {
			if got[i] == "ready" {
				t.Errorf("teardownFinalFields(%q) still writes a %q key: %v", marker, "ready", got)
			}
			if got[i] == "prereqs" {
				t.Errorf("teardownFinalFields(%q) still writes a %q key: %v", marker, "prereqs", got)
			}
		}
	}

	// kill's exact remaining shape: state cleared, teardown marker cleared.
	got, ok := teardownFinalFields("kill")
	if !ok {
		t.Fatal(`teardownFinalFields("kill") refused; kill must return a job to draft`)
	}
	if want := "state||teardown|"; strings.Join(got, "|") != want {
		t.Errorf(`teardownFinalFields("kill") = %v, want state="" and teardown="" alone`, got)
	}
}

// --- programmatic creators: dream ---

// contractRetiredLine reports whether src carries a frontmatter line for one of
// the retired keys. The generated text is what ParseJob sees, so a key at the
// start of a line is exactly the shape that matters.
func contractRetiredLine(src, key string) bool {
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), key+":") {
			return true
		}
	}
	return false
}

// TestSubmitContractDreamJobContentCarriesNeitherRetiredLine: the nightly job's
// text used to carry `ready: true` and lean on the queue pass to start it.
// The queue pass is gone, so the line is gone with it — and the job it renders
// must still parse clean, or the night's card comes up with an error badge.
func TestSubmitContractDreamJobContentCarriesNeitherRetiredLine(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.Local)
	for _, includeFeedback := range []bool{false, true} {
		slug := "dream-2026-08-12"
		content := dreamJobContent(slug, now.Add(-24*time.Hour).UnixMilli(), now.UnixMilli(), includeFeedback)
		for _, retired := range []string{"ready", "prereqs"} {
			if contractRetiredLine(content, retired) {
				t.Errorf("dreamJobContent (feedback=%v) still carries a %q line:\n%s", includeFeedback, retired, content)
			}
		}
		j := ParseJob(slug+JobSuffix, []byte(content))
		if j.Err != "" {
			t.Fatalf("dream job content does not parse: %s\n%s", j.Err, content)
		}
		if got := DisplayState(j); got != "draft" {
			t.Errorf("a freshly created dream job reads as %q, want draft — the tick starts it, the board does not queue it", got)
		}
	}
}
