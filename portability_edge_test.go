package kraken

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"kraken/internal/jobs"
)

// portability_edge_test.go — the EDGE dimension of artifacts/50-contract.md
// (kraken-portability-audit) as REPO-WIDE scans: the negative space slices N,
// P and S leave behind. Every scan reads the working tree (or, for skills, the
// embedded bundle that ships) at test time; the test author read none of it.
//
//   - no non-test, non-comment Go string literal under internal/, cmd/ or
//     web/*.go names carl (word-bounded, case-insensitive), outside the
//     contract's allowlist;
//   - no non-test Go source under internal/, cmd/ or web/ contains the bytes
//     `Documents` or `KRAKEN_PLUTO_DIR`;
//   - none of the retired pluto symbols or files exists;
//   - the embedded skills carry no carl, no malformed operator placeholder,
//     no pluto path, no he/him/his, and pr/SKILL.md no carl/ prefix;
//   - the Makefile's label knob reaches every launchctl target, and
//     install-completion never creates ~/.oh-my-zsh.

var edgeCarlRe = regexp.MustCompile(`(?i)\bcarl\b`)

// edgeGoFiles lists non-test .go files under internal/ and cmd/
// (recursively) plus web/*.go (top level only — web/_ui is the SPA).
func edgeGoFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	for _, root := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	web, err := filepath.Glob(filepath.Join("web", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range web {
		if !strings.HasSuffix(p, "_test.go") {
			files = append(files, p)
		}
	}
	if len(files) < 20 {
		t.Fatalf("found only %d Go source files — the scan is not looking at the repo", len(files))
	}
	return files
}

// edgeCarlAllowed is the contract's allowlist: "schema.go's historical
// migration strings and the dev.carl identifiers (label, bundle ids)". The
// literal "carl/" alone is ALSO allowed: the contract's branchFamily row
// requires the legacy carl/ names to keep matching, which needs the legacy
// prefix spelled somewhere (see the report: a contract silence).
func edgeCarlAllowed(file, lit string) bool {
	if filepath.Base(file) == "schema.go" {
		return true
	}
	if strings.Contains(lit, "dev.carl") {
		return true
	}
	return lit == "carl/"
}

// Contract (Slice N + S repo-wide): "grep -rniw 'carl' --include='*.go'
// internal cmd web/*.go over NON-comment, NON-test string literals is empty
// except schema.go's historical migration strings and the dev.carl
// identifiers".
func TestEdgeNoCarlInShippedGoStrings(t *testing.T) {
	fset := token.NewFileSet()
	for _, f := range edgeGoFiles(t) {
		file, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			val, err := strconv.Unquote(lit.Value)
			if err != nil {
				val = lit.Value
			}
			if edgeCarlRe.MatchString(val) && !edgeCarlAllowed(f, val) {
				short := val
				if len(short) > 120 {
					short = short[:120] + "…"
				}
				t.Errorf("%s: shipped string names carl: %q", fset.Position(lit.Pos()), short)
			}
			return true
		})
	}
}

// Contract (Slice P): "no non-test Go source under internal/, cmd/ or web/
// contains the literals Documents or KRAKEN_PLUTO_DIR" — read as raw bytes,
// comments included.
func TestEdgeNoPlutoLiteralsInGoSource(t *testing.T) {
	for _, f := range edgeGoFiles(t) {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			for _, bad := range []string{"Documents", "KRAKEN_PLUTO_DIR"} {
				if strings.Contains(line, bad) {
					t.Errorf("%s:%d contains %q: %s", f, i+1, bad, strings.TrimSpace(line))
				}
			}
		}
	}
}

// Contract (Slice P): the retired symbols "do not exist" and the files
// internal/jobs/import.go, internal/jobs/cutover.go, cmd/kraken/cmd_import.go
// are gone. Checked as top-level declarations of any kind (func, var, const,
// type) in the package that used to own each.
func TestEdgeRetiredPlutoSymbolsAreGone(t *testing.T) {
	for _, f := range []string{"internal/jobs/import.go", "internal/jobs/cutover.go", "cmd/kraken/cmd_import.go"} {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("%s still exists", f)
		}
	}
	gone := map[string][]string{
		"internal/jobs": {"preserveArtifacts", "PlutoDir", "PlutoSandboxed", "PlutoArtifactPrefix",
			"ArchiveDir", "LoadJobsFromFiles", "movePrefixed", "removeArchivedArtifactFiles",
			"archivedArtifactFiles", "isPlutoDayNote"},
		"internal/api": {"plutoFlatArtifacts", "mirrorPlutoActions"},
	}
	fset := token.NewFileSet()
	for dir, names := range gone {
		want := map[string]bool{}
		for _, n := range names {
			want[n] = true
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			p := filepath.Join(dir, e.Name())
			file, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s: %v", p, err)
			}
			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					if d.Recv == nil && want[d.Name.Name] {
						t.Errorf("%s still declares func %s", p, d.Name.Name)
					}
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						switch s := spec.(type) {
						case *ast.ValueSpec:
							for _, id := range s.Names {
								if want[id.Name] {
									t.Errorf("%s still declares %s", p, id.Name)
								}
							}
						case *ast.TypeSpec:
							if want[s.Name.Name] {
								t.Errorf("%s still declares type %s", p, s.Name.Name)
							}
						}
					}
				}
			}
		}
	}
}

// Contract (Slice P): "the import/cutover verbs … do not exist" — no
// dispatch string for either anywhere in cmd/kraken's shipped source.
func TestEdgeImportCutoverVerbsAreGone(t *testing.T) {
	fset := token.NewFileSet()
	for _, f := range edgeGoFiles(t) {
		if !strings.HasPrefix(f, filepath.Join("cmd", "kraken")) {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			c, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, e := range c.List {
				if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if v, _ := strconv.Unquote(lit.Value); v == "import" || v == "cutover" {
						t.Errorf("%s: still dispatches %q", fset.Position(lit.Pos()), v)
					}
				}
			}
			return true
		})
	}
}

// ---------------------------------------------------------------------------
// skills (Slice S), read from the embedded bundle that ships
// ---------------------------------------------------------------------------

func edgeEmbeddedSkills(t *testing.T) map[string]string {
	t.Helper()
	if jobs.SkillsFS == nil {
		t.Fatal("jobs.SkillsFS is nil — the embed was not wired")
	}
	out := map[string]string{}
	err := fs.WalkDir(jobs.SkillsFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".md") {
			return nil
		}
		data, err := fs.ReadFile(jobs.SkillsFS, p)
		if err != nil {
			return err
		}
		out[p] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, must := range []string{"aim/SKILL.md", "do/SKILL.md", "info/SKILL.md", "pr/SKILL.md",
		"dream/SKILL.md", "load/SKILL.md", "handoff/SKILL.md", "direct/SKILL.md", "aim/assets/hermes-contract.md"} {
		if _, ok := out[must]; !ok {
			t.Fatalf("embedded bundle lacks %s (have %d .md files)", must, len(out))
		}
	}
	return out
}

func edgeContext(s string, loc []int) string {
	lo, hi := loc[0]-50, loc[1]+50
	if lo < 0 {
		lo = 0
	}
	if hi > len(s) {
		hi = len(s)
	}
	return strings.ReplaceAll(s[lo:hi], "\n", " ")
}

// Contract: "no word carl (case-insensitive, word-bounded) remains" in any of
// the seven skills or hermes-contract.md — checked over EVERY .md in the
// bundle, since a missed asset ships just the same.
func TestEdgeEmbeddedSkillsNameNoCarl(t *testing.T) {
	for p, body := range edgeEmbeddedSkills(t) {
		for _, loc := range edgeCarlRe.FindAllStringIndex(body, -1) {
			t.Errorf("%s names carl: …%s…", p, edgeContext(body, loc))
		}
	}
}

// Slice N substitutes ONLY the literal `{{operator}}`; any near-miss spelling
// in a skill would ship to every cell unsubstituted.
func TestEdgeEmbeddedSkillsHaveNoMalformedPlaceholder(t *testing.T) {
	near := regexp.MustCompile(`(?i)\{\{\s*operator\s*\}\}`)
	found := 0
	for p, body := range edgeEmbeddedSkills(t) {
		found += strings.Count(body, "{{operator}}")
		for _, loc := range near.FindAllStringIndex(body, -1) {
			if body[loc[0]:loc[1]] == "{{operator}}" {
				continue
			}
			t.Errorf("%s carries a malformed operator placeholder %q: …%s…", p, body[loc[0]:loc[1]], edgeContext(body, loc))
		}
	}
	if found == 0 {
		t.Errorf("no skill carries {{operator}} at all — the person is named nowhere, or named literally")
	}
}

// Contract: "pronouns they/them" for the operator in the skills.
func TestEdgeEmbeddedSkillsUseNoHeHimHis(t *testing.T) {
	re := regexp.MustCompile(`(?i)\b(he|him|his|himself)\b`)
	for p, body := range edgeEmbeddedSkills(t) {
		for _, loc := range re.FindAllStringIndex(body, -1) {
			t.Errorf("%s uses %q: …%s…", p, body[loc[0]:loc[1]], edgeContext(body, loc))
		}
	}
}

// C13: "No shipped string, skill or doc names ~/Documents/pluto, PlutoDir,
// KRAKEN_PLUTO_DIR, kraken import or kraken cutover".
func TestEdgeEmbeddedSkillsNameNoPluto(t *testing.T) {
	for p, body := range edgeEmbeddedSkills(t) {
		for _, bad := range []string{"Documents/pluto", "PlutoDir", "KRAKEN_PLUTO_DIR", "kraken import", "kraken cutover"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s still names %q", p, bad)
			}
		}
	}
}

// Contract: "pr/SKILL.md: no branch-rename step, no carl/ prefix, no monolith
// category list; it says a cell never renames its branch".
func TestEdgePRSkillHasNoCarlPrefixOrRename(t *testing.T) {
	pr := edgeEmbeddedSkills(t)["pr/SKILL.md"]
	if strings.Contains(strings.ToLower(pr), "carl/") {
		t.Errorf("pr/SKILL.md still carries a carl/ branch prefix")
	}
	if regexp.MustCompile(`git branch -[mM]\b|git branch --move\b`).MatchString(pr) {
		t.Errorf("pr/SKILL.md still carries a branch-rename command")
	}
	if !regexp.MustCompile(`(?i)never renames?\b[^.\n]*\bbranch|branch[^.\n]*\bnever (?:be )?renamed`).MatchString(pr) {
		t.Errorf("pr/SKILL.md does not say a cell never renames its branch")
	}
}

// ---------------------------------------------------------------------------
// Makefile (Slice S)
// ---------------------------------------------------------------------------

// Contract: "KRAKEN_LABEL ?= dev.carl.kraken names the plist file, its Label
// and every launchctl target". With an override, the dry-run recipe for
// install-serve must not name the default label anywhere.
func TestEdgeMakefileLabelOverrideReachesEveryTarget(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not on PATH")
	}
	home := t.TempDir()
	cmd := exec.Command("make", "-n", "install-serve", "KRAKEN_LABEL=edge.label.override")
	cmd.Env = append(edgeEnvWithout("HOME", "KRAKEN_LABEL"), "HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n install-serve: %v\n%s", err, out)
	}
	s := string(out)
	if strings.Contains(s, "dev.carl.kraken") {
		for i, line := range strings.Split(s, "\n") {
			if strings.Contains(line, "dev.carl.kraken") {
				t.Errorf("dry-run line %d still names the default label: %s", i+1, line)
			}
		}
	}
	if !strings.Contains(s, "edge.label.override") {
		t.Errorf("dry-run never names the overridden label:\n%s", s)
	}
}

// The Makefile no longer cites the Documents grant anywhere.
func TestEdgeMakefileLabelDefault(t *testing.T) {
	data, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	// (the default itself is pinned behaviourally by
	// TestEdgeMakefileLabelFallbackOrder's "no file" case, since the
	// amendment changed how the variable is spelled)
	// the signing comment no longer cites the Documents grant
	if strings.Contains(string(data), "Documents") {
		t.Errorf("Makefile still mentions Documents")
	}
}

func edgeEnvWithout(keys ...string) []string {
	drop := map[string]bool{}
	for _, k := range keys {
		drop[k] = true
	}
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			out = append(out, kv)
		}
	}
	return out
}

// edgeRunCompletion runs `make install-completion` under home, refusing to
// execute a recipe that would do more than copy a completion file (the
// dry-run is inspected first, so a recipe that also builds or installs the
// binary is never run from a test).
func edgeRunCompletion(t *testing.T, home string) {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not on PATH")
	}
	env := append(edgeEnvWithout("HOME", "ZSH", "ZSH_CUSTOM"), "HOME="+home)
	dry := exec.Command("make", "-n", "install-completion")
	dry.Env = env
	out, err := dry.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n install-completion: %v\n%s", err, out)
	}
	for _, risky := range []string{"go build", "launchctl", "codesign", "npm ", "xcodebuild", ".local/bin"} {
		if strings.Contains(string(out), risky) {
			t.Skipf("install-completion's recipe runs %q; not executing it from a test:\n%s", risky, out)
		}
	}
	run := exec.Command("make", "install-completion")
	run.Env = env
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("make install-completion: %v\n%s", err, out)
	}
}

func edgeExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Contract: "install-completion uses the first of
// ~/.oh-my-zsh/custom/completions, ~/.zsh/completions that ALREADY EXISTS and
// creates only ~/.zsh/completions as the fallback, never ~/.oh-my-zsh".
func TestEdgeMakefileCompletionDirChoice(t *testing.T) {
	t.Run("neither exists", func(t *testing.T) {
		home := t.TempDir()
		edgeRunCompletion(t, home)
		if !edgeExists(filepath.Join(home, ".zsh", "completions", "_k")) {
			t.Errorf("~/.zsh/completions/_k was not created as the fallback")
		}
		if edgeExists(filepath.Join(home, ".oh-my-zsh")) {
			t.Errorf("~/.oh-my-zsh was created")
		}
	})
	t.Run("oh-my-zsh custom exists", func(t *testing.T) {
		home := t.TempDir()
		omz := filepath.Join(home, ".oh-my-zsh", "custom", "completions")
		if err := os.MkdirAll(omz, 0o755); err != nil {
			t.Fatal(err)
		}
		edgeRunCompletion(t, home)
		if !edgeExists(filepath.Join(omz, "_k")) {
			t.Errorf("_k did not land in the existing oh-my-zsh completions dir")
		}
		if edgeExists(filepath.Join(home, ".zsh")) {
			t.Errorf("~/.zsh was created although the oh-my-zsh dir exists")
		}
	})
	t.Run("only zsh completions exists", func(t *testing.T) {
		home := t.TempDir()
		zc := filepath.Join(home, ".zsh", "completions")
		if err := os.MkdirAll(zc, 0o755); err != nil {
			t.Fatal(err)
		}
		edgeRunCompletion(t, home)
		if !edgeExists(filepath.Join(zc, "_k")) {
			t.Errorf("_k did not land in ~/.zsh/completions")
		}
		if edgeExists(filepath.Join(home, ".oh-my-zsh")) {
			t.Errorf("~/.oh-my-zsh was created")
		}
	})
	t.Run("oh-my-zsh root without custom/completions", func(t *testing.T) {
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, ".oh-my-zsh"), 0o755); err != nil {
			t.Fatal(err)
		}
		edgeRunCompletion(t, home)
		if edgeExists(filepath.Join(home, ".oh-my-zsh", "custom")) {
			t.Errorf("install-completion created ~/.oh-my-zsh/custom — only an EXISTING completions dir may be used")
		}
		if !edgeExists(filepath.Join(home, ".zsh", "completions", "_k")) {
			t.Errorf("fallback ~/.zsh/completions/_k missing")
		}
	})
}

// ---------------------------------------------------------------------------
// Review-round-1 amendments — Makefile
// ---------------------------------------------------------------------------

// edgeMakeDry runs `make -n <target> [vars...]` under home with KRAKEN_LABEL
// either unset or set in the environment.
func edgeMakeDry(t *testing.T, home string, envLabel *string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not on PATH")
	}
	env := append(edgeEnvWithout("HOME", "KRAKEN_LABEL"), "HOME="+home)
	if envLabel != nil {
		env = append(env, "KRAKEN_LABEL="+*envLabel)
	}
	cmd := exec.Command("make", append([]string{"-n"}, args...)...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func edgeWriteHomeEnv(t *testing.T, home, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".kraken"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".kraken", "env"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Amended: "KRAKEN_LABEL falls back, in this order: the make line or
// environment, else the KRAKEN_LABEL= line of ~/.kraken/env, else
// dev.carl.kraken". Each rung, plus the lines the file rung must NOT read.
func TestEdgeMakefileLabelFallbackOrder(t *testing.T) {
	envVal := "edge.from.environment"
	cases := []struct {
		name     string
		file     string
		envLabel *string
		makeArg  string
		want     string
	}{
		{"file only", "KRAKEN_LABEL=edge.from.file\n", nil, "", "edge.from.file"},
		{"file with spaces", "  KRAKEN_LABEL = edge.spaced \n", nil, "", "edge.spaced"},
		{"make line beats file", "KRAKEN_LABEL=edge.from.file\n", nil, "KRAKEN_LABEL=edge.from.cli", "edge.from.cli"},
		{"environment beats file", "KRAKEN_LABEL=edge.from.file\n", &envVal, "", "edge.from.environment"},
		{"commented line ignored", "# KRAKEN_LABEL=edge.commented\n", nil, "", "dev.carl.kraken"},
		{"no file", "", nil, "", "dev.carl.kraken"},
		{"other keys only", "KRAKEN_OPERATOR=Ada\nKRAKEN_LABELX=edge.wrongkey\n", nil, "", "dev.carl.kraken"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			if c.file != "" {
				edgeWriteHomeEnv(t, home, c.file)
			}
			args := []string{"install-serve"}
			if c.makeArg != "" {
				args = append(args, c.makeArg)
			}
			out := edgeMakeDry(t, home, c.envLabel, args...)
			if !strings.Contains(out, "<string>"+c.want+"</string>") {
				t.Errorf("dry-run Label is not %q:\n%s", c.want, out)
			}
			for _, other := range []string{"dev.carl.kraken", "edge.from.file", "edge.from.cli", "edge.from.environment", "edge.commented", "edge.wrongkey", "edge.spaced"} {
				if other != c.want && strings.Contains(out, other) {
					t.Errorf("dry-run names %q although %q should win", other, c.want)
				}
			}
		})
	}
}

// Amended: "install-completion prints a note and succeeds when both
// candidate dirs are unusable (a file where a dir is expected)" — and leaves
// that file alone and never creates ~/.oh-my-zsh.
func TestEdgeMakefileCompletionBothDirsUnusable(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".zsh"), 0o755); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(home, ".zsh", "completions")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	edgeRunCompletion(t, home) // fails the test itself if make exits non-zero
	if b, err := os.ReadFile(blocker); err != nil || string(b) != "not a dir" {
		t.Errorf("the file at ~/.zsh/completions was changed: (%q, %v)", b, err)
	}
	if edgeExists(filepath.Join(home, ".oh-my-zsh")) {
		t.Errorf("~/.oh-my-zsh was created")
	}
}
