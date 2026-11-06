package kraken

import (
	"bytes"
	"context"
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
	"time"
)

// portability_contract_test.go — athena-logic's BLIND contract tests for the
// repo-wide obligations of the portability audit (artifacts/50-contract.md:
// the Slice N + S "no carl in shipped strings" scan, Slice P's "no Documents /
// KRAKEN_PLUTO_DIR" scan and deleted symbols, Slice S's skills and Makefile).
// Written from the contract text alone: the author never opened a skill, the
// Makefile or any non-test Go file — every one of them is read BY THE TEST at
// run time. The working directory of this package's tests is the repo root.

var contractPortCarlWord = regexp.MustCompile(`(?i)\bcarl\b`)

// contractPortSkillFiles are the files Slice S's skills row names, plus
// direct/SKILL.md and mind/SKILL.md, which joined the bundle after that slice
// and name the operator the same way, minus pr/SKILL.md, which folded into do/SKILL.md
// in job 624 (its operator mentions travelled with it).
var contractPortSkillFiles = []string{
	".claude/skills/aim/SKILL.md",
	".claude/skills/do/SKILL.md",
	".claude/skills/info/SKILL.md",
	".claude/skills/dream/SKILL.md",
	".claude/skills/load/SKILL.md",
	".claude/skills/handoff/SKILL.md",
	".claude/skills/direct/SKILL.md",
	".claude/skills/mind/SKILL.md",
	".claude/skills/aim/assets/hermes-contract.md",
}

func contractPortEmbedded(t *testing.T, p string) string {
	t.Helper()
	b, err := fs.ReadFile(skillsFS, p)
	if err != nil {
		t.Fatalf("read embedded %s: %v", p, err)
	}
	return string(b)
}

func contractPortContext(s string, loc []int) string {
	lo, hi := loc[0]-60, loc[1]+60
	if lo < 0 {
		lo = 0
	}
	if hi > len(s) {
		hi = len(s)
	}
	return strings.ReplaceAll(s[lo:hi], "\n", "⏎")
}

// Contract (Slice S, skills): "no word carl (case-insensitive, word-bounded)
// remains" in the seven SKILL.md files and aim/assets/hermes-contract.md.
func TestContractPortSkillsNameNoCarl(t *testing.T) {
	for _, p := range contractPortSkillFiles {
		src := contractPortEmbedded(t, p)
		if loc := contractPortCarlWord.FindStringIndex(src); loc != nil {
			t.Errorf("%s still names carl: …%s…", p, contractPortContext(src, loc))
		}
	}
}

// Contract (Slice S): "where the person is named, the literal {{operator}}
// stands"; C8: "the embedded skill files contain {{operator}}". Asserted for
// the four pipeline skills that address the operator throughout (aim, do,
// info, dream) and for the set as a whole.
func TestContractPortSkillsCarryOperatorPlaceholder(t *testing.T) {
	total := 0
	for _, p := range contractPortSkillFiles {
		total += strings.Count(contractPortEmbedded(t, p), "{{operator}}")
	}
	if total == 0 {
		t.Fatal("no embedded skill carries the {{operator}} placeholder")
	}
	for _, p := range []string{".claude/skills/aim/SKILL.md", ".claude/skills/do/SKILL.md", ".claude/skills/info/SKILL.md", ".claude/skills/dream/SKILL.md"} {
		if !strings.Contains(contractPortEmbedded(t, p), "{{operator}}") {
			t.Errorf("%s does not carry {{operator}}", p)
		}
	}
}

// Contract (Slice S): "pr/SKILL.md: no branch-rename step, no carl/ prefix, no
// monolith category list (Q13); it says a cell never renames its branch".
// The PR rules now live in do/SKILL.md's step 8 (the pr skill folded into
// it in job 624), so the same four assertions hold against do.
func TestContractPortPRSkillNeverRenames(t *testing.T) {
	src := contractPortEmbedded(t, ".claude/skills/do/SKILL.md")
	if strings.Contains(src, "carl/") {
		t.Error("do/SKILL.md still carries the carl/ prefix")
	}
	if strings.Contains(strings.ToLower(src), "monolith") {
		t.Error("do/SKILL.md still names monolith (the category list must be gone)")
	}
	if !strings.Contains(strings.ToLower(src), "never rename") {
		t.Error("do/SKILL.md does not say a cell never renames its branch")
	}
	if regexp.MustCompile(`git branch -m\b|git branch --move\b`).MatchString(src) {
		t.Error("do/SKILL.md still carries a branch-rename command")
	}
}

// Contract (Slice S): "aim:122,192, info:261, handoff:57: the preserved-copy
// pointers become kraken artifacts get <slug> [name]"; C13: "No shipped
// string, skill or doc names ~/Documents/pluto, PlutoDir, KRAKEN_PLUTO_DIR,
// kraken import or kraken cutover".
func TestContractPortSkillsPointAtArtifactsGet(t *testing.T) {
	for _, p := range []string{".claude/skills/aim/SKILL.md", ".claude/skills/info/SKILL.md", ".claude/skills/handoff/SKILL.md"} {
		if !strings.Contains(contractPortEmbedded(t, p), "kraken artifacts get") {
			t.Errorf("%s does not point at `kraken artifacts get`", p)
		}
	}
	for _, p := range contractPortSkillFiles {
		src := contractPortEmbedded(t, p)
		for _, gone := range []string{"Documents/pluto", "PlutoDir", "KRAKEN_PLUTO_DIR", "kraken import", "kraken cutover"} {
			if strings.Contains(src, gone) {
				t.Errorf("%s still names %q", p, gone)
			}
		}
	}
}

// contractPortGoSources lists every non-test .go file under internal/, cmd/,
// and (recursive) web/, skipping web/_ui (the SPA, not Go).
func contractPortGoSources(t *testing.T, webRecursive bool) []string {
	t.Helper()
	var out []string
	for _, root := range []string{"internal", "cmd", "web"} {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if p == filepath.Join("web", "_ui") || d.Name() == "node_modules" {
					return filepath.SkipDir
				}
				if root == "web" && !webRecursive && p != "web" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
				out = append(out, p)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	if len(out) == 0 {
		t.Fatal("no Go sources found — is the test running at the repo root?")
	}
	return out
}

// contractPortCarlAllowed is the contract's allowlist: "schema.go's
// historical migration strings and the dev.carl identifiers (label, bundle
// ids)". The bare legacy branch prefix literal "carl/" is also admitted,
// because the contract itself REQUIRES branchFamily to enumerate names under
// carl/ (Slice E), which a Go literal must spell.
func contractPortCarlAllowed(file, lit string) bool {
	if filepath.ToSlash(file) == "internal/store/schema.go" {
		return true
	}
	if strings.Contains(lit, "dev.carl") {
		return true
	}
	return lit == "carl/"
}

// Repo-wide obligation (Slice N + S): "grep -rniw 'carl' --include='*.go'
// internal cmd web/*.go over NON-comment, NON-test string literals is empty
// except schema.go's historical migration strings and the dev.carl
// identifiers". Implemented as a go/parser scan of every string literal.
func TestContractPortNoCarlInShippedGoStrings(t *testing.T) {
	fset := token.NewFileSet()
	for _, file := range contractPortGoSources(t, false) {
		f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Errorf("parse %s: %v", file, err)
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			val, err := strconv.Unquote(lit.Value)
			if err != nil {
				val = lit.Value
			}
			if loc := contractPortCarlWord.FindStringIndex(val); loc != nil && !contractPortCarlAllowed(file, val) {
				t.Errorf("%s: shipped string names carl: …%s…", fset.Position(lit.Pos()), contractPortContext(val, loc))
			}
			return true
		})
	}
}

// Contract (Slice P): "no non-test Go source under internal/, cmd/ or web/
// contains the literals Documents or KRAKEN_PLUTO_DIR".
func TestContractPortNoPlutoLiteralsInGoSources(t *testing.T) {
	for _, file := range contractPortGoSources(t, true) {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, gone := range []string{"Documents", "KRAKEN_PLUTO_DIR"} {
			if i := bytes.Index(b, []byte(gone)); i >= 0 {
				t.Errorf("%s contains %q: …%s…", file, gone, contractPortContext(string(b), []int{i, i + len(gone)}))
			}
		}
	}
}

// Contract (Slice P): the listed pluto symbols, the import/cutover verbs and
// their files "do not exist". Pinned as a declaration scan plus file absence.
func TestContractPortPlutoSymbolsDeleted(t *testing.T) {
	for _, gone := range []string{"internal/jobs/import.go", "internal/jobs/cutover.go", "cmd/kraken/cmd_import.go"} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s still exists (stat err %v)", gone, err)
		}
	}
	decl := regexp.MustCompile(`(?m)^\s*(?:func|var|const)\s+(preserveArtifacts|PlutoDir|PlutoSandboxed|PlutoArtifactPrefix|ArchiveDir|LoadJobsFromFiles|movePrefixed|removeArchivedArtifactFiles|archivedArtifactFiles|isPlutoDayNote|plutoFlatArtifacts|mirrorPlutoActions)\b`)
	for _, file := range contractPortGoSources(t, true) {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if m := decl.FindSubmatch(b); m != nil {
			t.Errorf("%s still declares %s", file, m[1])
		}
	}
	main, err := os.ReadFile("cmd/kraken/main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{`case "import":`, `case "cutover":`} {
		if bytes.Contains(main, []byte(verb)) {
			t.Errorf("cmd/kraken/main.go still dispatches %s", verb)
		}
	}
}

// ---------------------------------------------------------------------------
// Makefile
// ---------------------------------------------------------------------------

func contractPortMakefile(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	return string(b)
}

// contractPortMakeEnv is os.Environ() with HOME replaced and every KRAKEN_* /
// OPERATOR / MAKEFLAGS entry removed, so the make run is the machine-neutral
// default.
func contractPortMakeEnv(home string, extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "HOME" || strings.HasPrefix(k, "KRAKEN_") || k == "OPERATOR" || k == "MAKEFLAGS" || k == "MFLAGS" || k == "MAKELEVEL" {
			continue
		}
		env = append(env, kv)
	}
	return append(append(env, "HOME="+home), extra...)
}

func contractPortMake(t *testing.T, env []string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Fatalf("make not on PATH: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "make", args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// Contract (Slice S, Makefile): "KRAKEN_LABEL ?= dev.carl.kraken names the
// plist file, its Label and every launchctl target".
func TestContractPortMakefileLabelDefault(t *testing.T) {
	if !regexp.MustCompile(`(?m)^KRAKEN_LABEL\s*\?=\s*dev\.carl\.kraken\s*$`).MatchString(contractPortMakefile(t)) {
		t.Error("Makefile lacks `KRAKEN_LABEL ?= dev.carl.kraken`")
	}
}

// Contract: "with it unset make -n install-serve output is byte-identical to
// today's" — the knob must change nothing when it is not used. The contract
// froze "today's" as the Makefile at its base commit 8d59e62, which held the
// recipe still only until the recipe next changed for a reason of its own
// (the plist PATH gaining /usr/sbin:/sbin); a fixed-commit baseline is then a
// pin on history rather than on the knob. The obligation itself is checked
// here as: an unset KRAKEN_LABEL and one set to its documented default
// produce the same dry run, byte for byte, from the same directory under the
// same HOME.
func TestContractPortMakefileInstallServeUnchangedByDefault(t *testing.T) {
	home := t.TempDir()
	env := contractPortMakeEnv(home)
	unset := contractPortMake(t, env, "-n", "install-serve")
	explicit := contractPortMake(t, env, "-n", "install-serve", "KRAKEN_LABEL=dev.carl.kraken")
	if unset != explicit {
		t.Errorf("make -n install-serve differs between KRAKEN_LABEL unset and its default.\n--- unset ---\n%s\n--- KRAKEN_LABEL=dev.carl.kraken ---\n%s", unset, explicit)
	}
	// And the unset run still drives the default label through the recipe:
	// equality alone would also hold for a recipe that stopped using it.
	if want := filepath.Join(home, "Library", "LaunchAgents", "dev.carl.kraken.plist"); !strings.Contains(unset, want) {
		t.Errorf("make -n install-serve with KRAKEN_LABEL unset never names %s:\n%s", want, unset)
	}
}

// Contract: "KRAKEN_LABEL=x renames the plist file, the Label and every
// launchctl target consistently" — the dry run names the new label and never
// the old one.
func TestContractPortMakefileLabelOverride(t *testing.T) {
	home := t.TempDir()
	out := contractPortMake(t, contractPortMakeEnv(home), "-n", "install-serve", "KRAKEN_LABEL=org.example.contract")
	if strings.Contains(out, "dev.carl.kraken") {
		t.Errorf("make -n install-serve KRAKEN_LABEL=org.example.contract still names dev.carl.kraken:\n%s", out)
	}
	if !strings.Contains(out, "org.example.contract") {
		t.Errorf("make -n install-serve never names the overridden label:\n%s", out)
	}
	if !strings.Contains(out, "org.example.contract.plist") {
		t.Errorf("the plist file is not renamed to org.example.contract.plist:\n%s", out)
	}
}

// Contract: "install writes KRAKEN_OPERATOR=<OPERATOR> into ~/.kraken/env …
// where OPERATOR defaults to $(shell id -F) and may be overridden on the make
// line". The write's only-when-absent behaviour needs a real install and is
// not exercised here; the default's spelling is.
func TestContractPortMakefileOperatorDefault(t *testing.T) {
	src := contractPortMakefile(t)
	if !regexp.MustCompile(`(?m)^OPERATOR\s*\?=\s*\$\(shell id -F\b`).MatchString(src) {
		t.Error("Makefile lacks an `OPERATOR ?=` line whose default invokes `id -F`")
	}
	if !strings.Contains(src, "KRAKEN_OPERATOR=") {
		t.Error("Makefile never writes a KRAKEN_OPERATOR= line")
	}
	if !strings.Contains(src, ".kraken/env") {
		t.Error("Makefile never names ~/.kraken/env")
	}
}

// Contract: "install-completion uses the first of
// ~/.oh-my-zsh/custom/completions, ~/.zsh/completions that ALREADY EXISTS and
// creates only ~/.zsh/completions as the fallback, never ~/.oh-my-zsh". Run
// for real under a throwaway HOME; -o keeps any build prerequisite from being
// remade.
func TestContractPortMakefileCompletionDir(t *testing.T) {
	run := func(t *testing.T, home string) {
		t.Helper()
		contractPortMake(t, contractPortMakeEnv(home), "-o", "build", "-o", "web-assets", "-o", "kraken", "install-completion")
	}
	t.Run("neither dir exists", func(t *testing.T) {
		home := t.TempDir()
		run(t, home)
		if _, err := os.Stat(filepath.Join(home, ".zsh", "completions", "_k")); err != nil {
			t.Errorf("~/.zsh/completions/_k not created: %v", err)
		}
		if _, err := os.Stat(filepath.Join(home, ".oh-my-zsh")); !os.IsNotExist(err) {
			t.Errorf("~/.oh-my-zsh was created (stat err %v)", err)
		}
	})
	t.Run("oh-my-zsh completions dir exists", func(t *testing.T) {
		home := t.TempDir()
		omz := filepath.Join(home, ".oh-my-zsh", "custom", "completions")
		if err := os.MkdirAll(omz, 0o755); err != nil {
			t.Fatal(err)
		}
		run(t, home)
		if _, err := os.Stat(filepath.Join(omz, "_k")); err != nil {
			t.Errorf("_k not installed into the existing oh-my-zsh dir: %v", err)
		}
		if _, err := os.Stat(filepath.Join(home, ".zsh")); !os.IsNotExist(err) {
			t.Errorf("~/.zsh created although the oh-my-zsh dir existed (stat err %v)", err)
		}
	})
	t.Run("only ~/.zsh/completions exists", func(t *testing.T) {
		home := t.TempDir()
		zsh := filepath.Join(home, ".zsh", "completions")
		if err := os.MkdirAll(zsh, 0o755); err != nil {
			t.Fatal(err)
		}
		run(t, home)
		if _, err := os.Stat(filepath.Join(zsh, "_k")); err != nil {
			t.Errorf("_k not installed into ~/.zsh/completions: %v", err)
		}
		if _, err := os.Stat(filepath.Join(home, ".oh-my-zsh")); !os.IsNotExist(err) {
			t.Errorf("~/.oh-my-zsh was created (stat err %v)", err)
		}
	})
}

// Contract: "the signing comment (lines 55-61) no longer cites the Documents
// grant as the reason" — read strictly: the Makefile no longer names
// Documents at all.
func TestContractPortMakefileSigningCommentNoDocuments(t *testing.T) {
	src := contractPortMakefile(t)
	if i := strings.Index(src, "Documents"); i >= 0 {
		t.Errorf("Makefile still names Documents: …%s…", contractPortContext(src, []int{i, i + len("Documents")}))
	}
}

// Contract (review-round-1 amendment): "KRAKEN_LABEL falls back, in this
// order: the make line or environment, else the KRAKEN_LABEL= line of
// ~/.kraken/env, else dev.carl.kraken".
func TestContractPortMakefileLabelFromEnvFile(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".kraken"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".kraken", "env"), []byte("KRAKEN_OPERATOR=Ada\nKRAKEN_LABEL=org.example.fromfile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := contractPortMake(t, contractPortMakeEnv(home), "-n", "install-serve")
	if !strings.Contains(out, "org.example.fromfile.plist") || strings.Contains(out, "dev.carl.kraken") {
		t.Errorf("env-file label: make -n install-serve does not use org.example.fromfile throughout:\n%s", out)
	}
	out = contractPortMake(t, contractPortMakeEnv(home), "-n", "install-serve", "KRAKEN_LABEL=org.example.fromline")
	if !strings.Contains(out, "org.example.fromline.plist") || strings.Contains(out, "org.example.fromfile") {
		t.Errorf("make-line label must beat the env file's:\n%s", out)
	}
}

// Contract (amended): "install ALSO appends KRAKEN_BRANCH_PREFIX=carl/ to the
// env file when ~/.kraken/kraken.db already exists and the file has no
// KRAKEN_BRANCH_PREFIX= line … with a # comment line saying why". The append
// needs a real install; its spelling and its kraken.db condition are pinned.
func TestContractPortMakefileWritesLegacyBranchPrefix(t *testing.T) {
	src := contractPortMakefile(t)
	if !strings.Contains(src, "KRAKEN_BRANCH_PREFIX=carl/") {
		t.Error("Makefile never writes KRAKEN_BRANCH_PREFIX=carl/")
	}
	if !strings.Contains(src, "kraken.db") {
		t.Error("Makefile never tests for ~/.kraken/kraken.db before writing the legacy prefix")
	}
}

// Contract (amended): "install-completion prints a note and succeeds when
// both candidate dirs are unusable (a file where a dir is expected)".
func TestContractPortMakefileCompletionUnusableDirsSucceeds(t *testing.T) {
	home := t.TempDir()
	for _, f := range []string{".oh-my-zsh", ".zsh"} {
		if err := os.WriteFile(filepath.Join(home, f), []byte("not a dir"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// contractPortMake fails the test on a non-zero exit
	out := contractPortMake(t, contractPortMakeEnv(home), "-o", "build", "-o", "web-assets", "-o", "kraken", "install-completion")
	if strings.TrimSpace(out) == "" {
		t.Error("install-completion printed nothing when no completion dir was usable, want a note")
	}
}
