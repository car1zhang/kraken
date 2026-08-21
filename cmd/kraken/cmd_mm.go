package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"kraken/internal/core"
	"kraken/internal/util"
)

// cmd_mm.go — `k mm mark|drop|note|get|lsp`: carl's own verbs for the mental
// model, proxying to kraken serve's /api/mm routes (internal/api/mm.go).
// `mark` records a range of code he has just read, `drop` unmarks one, `note`
// records something he thought, `get` prints the model back, and `lsp` is the
// long-running odd one out — a language server (mmlsp.go) that draws the marks
// back into Zed and offers a code action to drop one from there.
//
// These are CARL's verbs, not a cell's, and that is the whole posture of the
// feature (Q5): agents never write the mental model, so no pipeline skill
// documents these and nothing in a cell brief points at them. The callers are
// carl's own shell and the Zed capture task.
//
// `mark` is shaped by what an editor can actually hand over. Zed's task
// runner exports the worktree root, the file relative to it and the 1-based
// row a selection STARTS on, but `ZED_END_ROW` was closed as not-planned —
// so the end row is derived from the selection's own newline count, by
// core.EndRowFromSelection, which the server uses too. And the selection
// travels in the ENVIRONMENT rather than in argv: a marked block can be
// thousands of lines, and macOS' argv limit would turn a large selection into
// a failed exec rather than a mark.
//
// Behavior contract, identical to cmd_load.go's and cmd_graph.go's:
//   - success: a read prints the server's JSON to stdout verbatim, exit 0; a
//     write prints one short confirmation line to STDERR, exit 0.
//   - a 4xx prints the server's own error message to stderr, exit non-zero —
//     never a raw HTTP dump.
//   - kraken serve unreachable: a stderr message naming the address this
//     process tried, exit non-zero — never silently succeeds. The Zed task
//     runs with `hide: on_success`, so a failure is the only thing carl sees;
//     a silent one would be a mark he believes he made.
//   - bad/missing args: a usage message to stderr, exit non-zero.
//
// It NEVER opens kraken.db: only kraken serve writes it (single-writer rule),
// so every op here is exactly one HTTP round trip through qfGet/qfPost.
//
// parse/run are split from cmdMM so the exit-code paths are testable without
// going through util.Die's os.Exit — the same shape cmd_load.go uses.
func cmdMM(args []string) {
	if len(args) == 0 {
		util.Die("usage: k mm mark|drop|note|get|lsp ...")
	}
	op, rest := args[0], args[1:]
	switch op {
	case "mark":
		f, err := parseMMMark(rest)
		if err != nil {
			util.Die("%v", err)
		}
		line, err := runMMMark(f, mmSelection())
		if err != nil {
			util.Die("%v", err)
		}
		fmt.Fprintln(os.Stderr, line)
	case "note":
		f, err := parseMMNote(rest)
		if err != nil {
			util.Die("%v", err)
		}
		line, err := runMMNote(f)
		if err != nil {
			util.Die("%v", err)
		}
		fmt.Fprintln(os.Stderr, line)
	case "drop":
		id, err := parseMMDrop(rest)
		if err != nil {
			util.Die("%v", err)
		}
		line, err := runMMDrop(id)
		if err != nil {
			util.Die("%v", err)
		}
		fmt.Fprintln(os.Stderr, line)
	case "get":
		out, err := runMMGet(rest)
		if err != nil {
			util.Die("%v", err)
		}
		writeStdout(out)
	case "lsp":
		// The one verb here that is a SERVER rather than a round trip: it
		// holds stdio for as long as Zed keeps it alive, so it takes no
		// arguments and prints nothing to stdout that is not an LSP frame —
		// anything else on that stream desynchronises the client.
		if len(rest) > 0 {
			util.Die("usage: k mm lsp  (no arguments; stdio JSON-RPC, run by the Zed extension)")
		}
		if err := runMMLSP(os.Stdin, os.Stdout); err != nil {
			if errors.Is(err, errMMLSPUncleanExit) {
				// `exit` without `shutdown`: the protocol asks for a non-zero
				// status, and for silence — the client is already gone.
				os.Exit(1)
			}
			util.Die("%v", err)
		}
	default:
		util.Die("usage: k mm mark|drop|note|get|lsp ... (unknown op %q)", op)
	}
}

// mmSelectionEnv is where `mark` reads the selected text from — an
// environment variable rather than an argument because a whole marked block
// can be far past what an exec argument list will carry, and because a
// selection full of quotes, backticks and newlines is exactly what
// shell-quoting mangles.
//
// mmSelection falls back to ZED_SELECTED_TEXT, which Zed itself EXPORTS into
// every task's environment (task_template.rs extends the spawned env with
// all task variables), so the Zed task never mentions the selection at all.
// It used to: the task command carried
// `KRAKEN_MM_SELECTION="${ZED_SELECTED_TEXT:}"`, and Zed substitutes
// `${VAR:}` TEXTUALLY into the command string before any shell parses it —
// so the first selection containing a double quote broke out of the
// assignment and ran the rest of carl's selected test code as shell (exit
// 127, `command not found: cell-demo-2:`), and newlines were mangled
// besides. An env value has neither problem. KRAKEN_MM_SELECTION stays
// first so a hand invocation or a non-Zed editor can still pass a selection
// explicitly.
//
// Neither variable set is not an error: an empty selection means "the single
// line at start_row", which the server reads off disk. That is the commonest
// way this verb is reached — a keystroke with the cursor parked on a line.
const mmSelectionEnv = "KRAKEN_MM_SELECTION"

func mmSelection() string {
	if s := os.Getenv(mmSelectionEnv); s != "" {
		return s
	}
	return os.Getenv("ZED_SELECTED_TEXT")
}

const mmMarkUsage = "usage: k mm mark --worktree <dir> --file <path relative to it> --start-row <n> [--note \"...\"]  (selection read from $" + mmSelectionEnv + ")"

// mmMarkFields is one parsed `mark` request. A named struct rather than four
// loose returns: they only ever travel together, and a positional
// (string, string, int, string) is exactly the shape a caller silently swaps
// two arguments in.
type mmMarkFields struct {
	worktree string
	file     string
	startRow int
	note     string
}

// mmFlagValue reads `--flag value` and `--flag=value` alike, returning the
// value and the index to continue from. Both spellings are accepted because
// the two callers write them differently — a Zed task template interpolates
// `--file $ZED_RELATIVE_FILE`, while carl by hand tends to write `--file=x`.
func mmFlagValue(args []string, i int, name, raw string, inline bool, usage string) (string, int, error) {
	if inline {
		return raw, i, nil
	}
	if i+1 >= len(args) {
		return "", i, fmt.Errorf("%s (%s needs a value)", usage, name)
	}
	return args[i+1], i + 1, nil
}

// parseMMMark reads `--worktree W --file F --start-row N [--note S]`.
// Anything else is a usage error rather than a silently ignored argument: a
// mistyped flag must not quietly mark the wrong thing, since the Zed task
// hides its output on success and carl would never see it.
func parseMMMark(args []string) (mmMarkFields, error) {
	var f mmMarkFields
	var haveRow bool
	for i := 0; i < len(args); i++ {
		name, raw, inline := strings.Cut(args[i], "=")
		if !inline {
			name = args[i]
		}
		switch name {
		case "--worktree", "--file", "--start-row", "--note":
		default:
			return f, fmt.Errorf("%s (unexpected argument %q)", mmMarkUsage, args[i])
		}
		v, next, err := mmFlagValue(args, i, name, raw, inline, mmMarkUsage)
		if err != nil {
			return f, err
		}
		i = next
		switch name {
		case "--worktree":
			f.worktree = strings.TrimSpace(v)
		case "--file":
			f.file = strings.TrimSpace(v)
		case "--start-row":
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return f, fmt.Errorf("bad --start-row %q: want a whole number 1 or greater (%s)", v, mmMarkUsage)
			}
			if n < 1 {
				// Refused locally as well as server-side, for cmd_load.go's
				// reason: a local refusal costs no round trip and can say
				// `--start-row` rather than `start_row`, naming the thing the
				// caller actually typed. Rows are 1-based everywhere in this
				// feature; a 0 is an editor exporting a 0-based cursor.
				return f, fmt.Errorf("bad --start-row %d: rows are 1-based (%s)", n, mmMarkUsage)
			}
			f.startRow, haveRow = n, true
		case "--note":
			f.note = v
		}
	}
	if f.worktree == "" || f.file == "" || !haveRow {
		return f, fmt.Errorf("%s (--worktree, --file and --start-row are all required)", mmMarkUsage)
	}
	return f, nil
}

// runMMMark posts the mark and returns the confirmation line.
//
// The selection is `\r\n`-normalized before it goes, so a file with Windows
// endings marks identically to one without — the server normalizes too, and
// both do it because the value has to be the same on either side of a retry.
//
// The range in the confirmation is the SELECTION's, not necessarily the
// stored anchor's: the server runs a ladder that may locate the same text at
// different coordinates in origin/main, and reporting its own answer back
// would need a second round trip for a line carl reads in passing.
func runMMMark(f mmMarkFields, selection string) (string, error) {
	sel := strings.ReplaceAll(selection, "\r\n", "\n")
	worktree, file := mmReroot(f.worktree, f.file)
	raw, err := qfPost("/api/mm/code", map[string]any{
		"worktree":  worktree,
		"file":      file,
		"start_row": f.startRow,
		"text":      sel,
		"note":      f.note,
	})
	if err != nil {
		return "", err
	}
	end := core.EndRowFromSelection(f.startRow, sel)
	// The confirmation names the RE-ROOTED path — the one the row holds —
	// because the docs send carl to compare anchor.path against the file he
	// has open when a mark does not draw, and a confirmation line naming the
	// pre-reroot path is a string that comparison would never match.
	where := fmt.Sprintf("%s:%d-%d", file, f.startRow, end)
	return mmWriteLine(raw, "marked "+where, "already marked "+where), nil
}

// mmReroot moves a mark's coordinates from the editor's PROJECT root onto the
// repo's GIT root, so that capture and display agree about what a mark's path
// is.
//
// Zed hands the task `$ZED_WORKTREE_ROOT` — the folder carl opened — and
// `$ZED_RELATIVE_FILE` relative to it. That is the git root only when he
// opened the repo root. Open `~/Developer/monolith/agent-service`, an ordinary
// way to open a service, and the mark is stored as `path: "foo.py"`, while the
// LSP that draws marks resolves the same file as `agent-service/foo.py`
// (mmlsp.go's resolveDoc, which has no project root to work from and so must
// use the git root). The mark then never draws in the file it names, and can
// draw on a same-named file at the repo root instead. The same skew degrades
// capture itself: the anchor ladder looks for `foo.py` in a tree whose blob is
// at `agent-service/foo.py`, finds it nowhere, and records a `dirty` anchor
// for text that is committed on origin/main.
//
// So the walk happens HERE, once, before the POST, rather than being pushed
// into the route: the route is also reached by clients that never had a
// project root, and a rewrite there would be guessing at what their paths mean.
//
// NO `.git` ANYWHERE ABOVE is a pass-through, not a failure. A mark taken in a
// directory that is not a checkout at all is a legitimate `dirty` mark, and
// the api's own tests point `--worktree` at bare temp dirs.
//
// The walk starts at the FILE's own directory, not the project root — the
// same starting point resolveDoc uses — so the two halves share one rule AND
// one starting point. Walking from the project root instead agreed with the
// display side only while the repo boundary sat at or above the project
// folder: a project opened ABOVE the repo (`~/Developer` holding several
// checkouts) or a nested checkout inside the project both put a `.git`
// between the two starting points, and the halves diverged again.
//
// An ABSOLUTE --file passes through untouched. filepath.Join swallows an
// absolute second element, so joining first would rewrite it into a bogus
// repo-relative path that the route's own IsAbs refusal — the correct answer
// to that input — would then never see.
func mmReroot(worktree, file string) (string, string) {
	if filepath.IsAbs(file) {
		return worktree, file
	}
	root, ok := mmGitRoot(filepath.Dir(filepath.Join(worktree, file)))
	if !ok || root == filepath.Clean(worktree) {
		return worktree, file
	}
	rel, err := filepath.Rel(root, filepath.Join(worktree, file))
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		// The file is not under the root the walk found. With the walk
		// starting at the file's own directory this should be unreachable,
		// but posting a path that climbs out of the repo is worse than
		// posting the unrooted pair the caller gave, so the guard stays.
		return worktree, file
	}
	return root, filepath.ToSlash(rel)
}

// mmGitRoot walks up from dir for a `.git` ENTRY and returns the directory
// holding it.
//
// Existence, never directory-ness: a git worktree's `.git` is a FILE, and
// every kraken cell checkout is one — a directory test would fail on exactly
// the trees carl reads most code in.
//
// It is shared by the two halves of the feature on purpose. `k mm mark` uses
// it to store a git-root-relative path and the LSP uses it to look one up; one
// walk means the two cannot answer differently, which they did before this
// existed.
func mmGitRoot(dir string) (string, bool) {
	dir = filepath.Clean(dir)
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

const mmDropUsage = "usage: k mm drop <id>"

// parseMMDrop reads the single positional item id.
//
// It is refused locally when it is not a positive whole number, for the
// reason parseMMMark refuses a --start-row of 0: a local refusal costs no
// round trip and can name what the caller typed. The server refuses `id <= 0`
// too (internal/api/mm.go's mmRemoveHandler), and both checks exist because
// an absent JSON key and an explicit 0 decode to the same value — the one
// shape that must never be read as "drop something".
func parseMMDrop(args []string) (int64, error) {
	if len(args) != 1 {
		return 0, fmt.Errorf("%s", mmDropUsage)
	}
	id, err := strconv.ParseInt(strings.TrimSpace(args[0]), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad id %q: want the mm item id, a whole number (%s)", args[0], mmDropUsage)
	}
	if id < 1 {
		return 0, fmt.Errorf("bad id %d: item ids start at 1 (%s)", id, mmDropUsage)
	}
	return id, nil
}

// runMMDrop unmarks one item. The server's answer is `{"ok":true}` whether it
// invalidated the row now or found it already invalidated, so the
// confirmation line says what carl asked for rather than which of those
// happened — the two are the same outcome from where he is standing, and the
// route is idempotent precisely so a second drop is safe.
func runMMDrop(id int64) (string, error) {
	if _, err := qfPost("/api/mm/remove", map[string]any{"id": id}); err != nil {
		return "", err
	}
	return "dropped mm " + strconv.FormatInt(id, 10), nil
}

const mmNoteUsage = "usage: k mm note \"<text>\" [--context \"...\"]"

type mmNoteFields struct {
	text    string
	context string
}

// parseMMNote reads `"<text>" [--context S]`. The note is one positional
// rather than the remaining words joined, so a note that happens to start
// with a hyphen is unambiguous — and `--` ends flag parsing for exactly that
// case, the escape cmd_graph.go's `search` carries for the same reason.
func parseMMNote(args []string) (mmNoteFields, error) {
	var f mmNoteFields
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	} else if len(args) > 0 && strings.HasPrefix(args[0], "-") {
		return f, fmt.Errorf("%s (a note starting with '-' needs `k mm note -- \"<text>\"`)", mmNoteUsage)
	}
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return f, fmt.Errorf("%s", mmNoteUsage)
	}
	f.text = args[0]
	for i := 1; i < len(args); i++ {
		name, raw, inline := strings.Cut(args[i], "=")
		if !inline {
			name = args[i]
		}
		if name != "--context" {
			return f, fmt.Errorf("%s (unexpected argument %q)", mmNoteUsage, args[i])
		}
		v, next, err := mmFlagValue(args, i, name, raw, inline, mmNoteUsage)
		if err != nil {
			return f, err
		}
		i = next
		f.context = v
	}
	return f, nil
}

func runMMNote(f mmNoteFields) (string, error) {
	raw, err := qfPost("/api/mm/note", map[string]any{"text": f.text, "context": f.context})
	if err != nil {
		return "", err
	}
	return mmWriteLine(raw, "noted", "noted"), nil
}

const mmGetUsage = "usage: k mm get [--all] [--file <path>]"

// runMMGet is `k mm get [--all] [--file <path>]`: GET /api/mm — prints
// {"items": [...]} straight through, newest first.
//
// The DEFAULT is live items only, and `--all` is what widens it to include
// the invalidated ones. That way round because the common read is "what do I
// currently know", while the invalidated rows are a retrospective view — what
// carl knew that has since stopped being true — and putting them in the
// default answer would make every read carry claims the model has already
// withdrawn.
//
// --file narrows to code marks anchored in one file — the read an agent makes
// before explaining that file, citing it, or briefing a diagram over it. The
// path is resolved exactly the way `k mm mark` resolves its capture pair
// (mmGetFileQuery), so the query asks about the same identity the marks were
// stored under.
func runMMGet(args []string) ([]byte, error) {
	all := false
	file := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--all":
			all = true
		case "--file":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("%s (--file needs a value)", mmGetUsage)
			}
			i++
			file = args[i]
		default:
			return nil, fmt.Errorf("%s (unexpected argument %q)", mmGetUsage, args[i])
		}
	}
	q := url.Values{}
	if !all {
		q.Set("live", "1")
	}
	if file != "" {
		f, wt := mmGetFileQuery(file)
		q.Set("file", f)
		if wt != "" {
			q.Set("worktree", wt)
		}
	}
	return qfGet("/api/mm?" + q.Encode())
}

// mmGetFileQuery turns --file's path into the (file, worktree) query pair:
// absolute against the cwd, then re-rooted onto the git root found by walking
// up from the FILE's own directory — mmGitRoot, the same walk capture and the
// LSP use, so all three surfaces answer "which file is this" identically. The
// returned worktree is that root (the server derives the repo half from it
// with the same rule the mark route applies); no root anywhere, or a path
// that will not resolve, degrades to the path as typed with no worktree — a
// path-only match server-side, never an error, because a filtered read that
// refuses is worse than one that matches a little loosely.
func mmGetFileQuery(file string) (string, string) {
	abs := file
	if !filepath.IsAbs(abs) {
		cwd, err := os.Getwd()
		if err != nil {
			return filepath.ToSlash(strings.TrimPrefix(file, "./")), ""
		}
		abs = filepath.Join(cwd, file)
	}
	if root, ok := mmGitRoot(filepath.Dir(abs)); ok {
		if rel, err := filepath.Rel(root, abs); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel), root
		}
	}
	return filepath.ToSlash(strings.TrimPrefix(file, "./")), ""
}

// mmWriteLine renders a write's confirmation from the server's own response:
// `created` when it minted a row, `already` when the same thing was already
// marked. Decoding is best-effort by design — the LINE is a courtesy and the
// exit code is the contract, so a response this verb cannot read still reports
// success rather than turning a completed write into a failure.
func mmWriteLine(raw []byte, created, already string) string {
	var resp struct {
		ID      int64 `json:"id"`
		Already bool  `json:"already"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return created
	}
	line := created
	if resp.Already {
		line = already
	}
	if resp.ID > 0 {
		line += " (mm " + strconv.FormatInt(resp.ID, 10) + ")"
	}
	return line
}
