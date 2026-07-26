package kraken

import (
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"kraken/internal/jobs"
)

// skills_embed_test.go verifies the go:embed directive actually captured
// the four kraken-owned skills and that each SKILL.md's renamed frontmatter
// took: this can only be checked from package main, since the embed.FS
// itself (skillsFS) lives here, not in jobs (jobs only sees the fs.Sub'd
// jobs.SkillsFS assigned by init()). It also pins the three worktree-relative
// path helpers the aim skill depends on — jobs.HermesContractPath,
// jobs.LintSvgPath, and jobs.DiagramCardPath — to real files inside that
// embedded tree, and pins the diagram-card.html template's own placeholder
// counts and header-comment shape.
func TestSkillsFSContainsExpectedSkills(t *testing.T) {
	want := map[string]string{
		"info": "info",
		"aim":  "aim",
		"do":   "do",
		"pr":   "pr",
	}
	for dir, wantName := range want {
		data, err := fs.ReadFile(skillsFS, ".claude/skills/"+dir+"/SKILL.md")
		if err != nil {
			t.Fatalf("read embedded .claude/skills/%s/SKILL.md: %v", dir, err)
		}
		nameLine := ""
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "name:") {
				nameLine = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
				break
			}
		}
		if nameLine != wantName {
			t.Errorf(".claude/skills/%s/SKILL.md frontmatter name = %q, want %q", dir, nameLine, wantName)
		}
	}
}

// TestCrewPathsPinnedToEmbeddedTree guards against the exact failure the
// reviewer flagged: a rename of an embedded skill asset passes CI (nothing
// else references the literal path) but silently breaks every doc job, since
// jobs.HermesContractPath/LintSvgPath/DiagramCardPath are plain string
// builders with no compile-time link to the embedded tree.
//
// Both halves of the coupling are asserted, because the consumer moved. The
// paths used to be spelled out in zeus' brief; the brief now delegates the
// whole diagram pipeline to .claude/skills/aim/SKILL.md, which names them as
// literal markdown strings — so the check is that each path resolves to a real
// embedded file AND is still referenced by the skill that sends an agent to it.
func TestCrewPathsPinnedToEmbeddedTree(t *testing.T) {
	aim := readEmbedded(t, "aim/SKILL.md")
	for _, path := range []string{jobs.HermesContractPath(), jobs.LintSvgPath(), jobs.DiagramCardPath()} {
		rel := strings.TrimPrefix(path, ".claude/skills/")
		if _, err := fs.ReadFile(jobs.SkillsFS, rel); err != nil {
			t.Errorf("jobs.SkillsFS missing %q (from %s): %v", rel, path, err)
		}
		if !strings.Contains(aim, path) {
			t.Errorf("aim/SKILL.md no longer references %q — zeus' brief delegates the diagram "+
				"pipeline to that skill, so if the skill stops naming this asset nothing points at it "+
				"at all", path)
		}
	}
}

// TestSkillsOwnTheCellContract pins the procedure that moved OUT of zeus'
// brief and into these skills. The brief now carries only what no skill can
// know (identity, the cell's real paths, the crew, which skill governs each
// phase); everything below is the other side of that split, and
// TestZeusBriefDoesNotRestateSkills in internal/jobs asserts the brief does not
// keep a second copy of any of it.
//
// Without this test the dedupe is one-directional: deleting a rule from a
// SKILL.md would leave nothing anywhere, and the only symptom would be a doc
// job quietly doing the wrong thing.
func TestSkillsOwnTheCellContract(t *testing.T) {
	for _, c := range []struct {
		skill string
		want  []string
	}{
		{"aim", []string{
			// the artifact set's shape and its mode-scoped required floor
			"00-overview.md", "50-components.md", "51-code-map.md", "52-testing-plan.md",
			"10-<id>.html", "Spec mode", "Explain mode",
			// the diagram pipeline in full
			"hermes-contract.md", "lint_svg.py", "diagram-card.html",
			"{{CARD_TITLE}}", "{{CAPTION}}", "<!-- DIAGRAM: {{DIAGRAM_ID}} -->",
			"str.replace", "re.sub", "--marker-id", "model: opus",
			// every rendering agent is a NAMED teammate: the name is what
			// buys it a pane nestTeams can hang under the cell and keeps it
			// addressable for the fix round instead of respawned from scratch
			"hermes-<diagram-id>", "an Agent-tool call that passes a **`name`**",
			"`SendMessage` the line-numbered violations as a fix-round brief",
			"box → kind", "kind → shape table", "never dictate coordinates, markup, or a shape class",
			// the questions WRITE verbs (the brief keeps only the read verb
			// and the silent-update rule)
			"kraken questions set", "kraken questions add|update|remove",
			"P0", "answer", "answered-at",
			// the cell section: paths in <cellDir> terms, the stop line, the gate
			"Inside A Kraken Cell", "<cellDir>/artifacts/", "<cellDir>/doc/fragments/",
			"doc ready for review:", "Use `0` where a count is zero",
			"`/do` is carl's move", "Never declare the job done",
		}},
		{"info", []string{
			"Inside A Kraken Cell", "<cellDir>/artifacts/",
			"explanation ready:", "go deeper", "Use `0` where a count is zero",
			// /info's defining constraint: lighter than /aim, and never any questions
			"No open questions. Ever.", "never gets a `/do`", "never opens a PR",
			"Never run `kraken questions` write verbs",
			"kraken feedback addressed",
			// the fix round is /info's own copy of the rule — anchor the
			// sentence, not just the naming, or deleting the whole bullet
			// leaves the spawn rule matching and the rule itself gone from
			// the explanation path (which never reads /aim)
			"never spawn a fresh",
			"lints CLEAN",
			// /info's diagram section is a summary of /aim's, but it must
			// carry the same teammate naming — on the explanation path it
			// is read INSTEAD of /aim's, so a drop here drops it entirely
			"designed AND rendered by a TEAMMATE", "hermes-<diagram-id>",
		}},
		{"do", []string{
			"90-build-report.md", "Discrepancies vs. the plan", "Complications",
			"Deferred", "Verification",
			// the cell section: the crew, the gate, the handoff to /pr
			"Inside A Kraken Cell", "hephaestus", "athena-logic", "athena-edge", "athena-style",
			// the crew are TEAMMATES, but the skill must POINT at the brief
			// for the contract rather than copy it — pinning the copy here
			// is what would freeze a duplicate the brief later drifts from
			"The crew is the brief's crew, and the crew are TEAMMATES",
			"read it there rather than re-deriving it here",
			"stop each still-running teammate with `TaskStop`", "There is no team-delete step",
			"read-only exploration only",
			"manual test ready:", "`.claude/skills/pr/SKILL.md`", "do NOT invoke `/pr` by name",
			"JOB SPEC is the contract", "Never declare the job done",
		}},
		{"pr", []string{
			"Inside A Kraken Cell", "carl/", "gh pr create",
			"git fetch origin && git checkout -B", "NEVER ends the job",
			"awaiting checks", "Never poll",
		}},
	} {
		src := readEmbedded(t, c.skill+"/SKILL.md")
		for _, want := range c.want {
			if !strings.Contains(src, want) {
				t.Errorf(".claude/skills/%s/SKILL.md no longer contains %q — zeus' brief delegates "+
					"this to the skill and keeps no copy, so dropping it here drops it everywhere",
					c.skill, want)
			}
		}
	}
}

// TestDiagramCardTemplateHeaderDoesNotSelfTerminate guards against the exact
// bug a prior review round caught: the diagram-card.html header comment used
// to embed the literal "<!-- DIAGRAM: {{DIAGRAM_ID}} -->" placeholder (and
// other stray "-->" sequences) inside its own explanatory comment. The first
// "-->" a browser (or a naive substring scan) sees closes the OUTER comment
// early, so everything between that point and the comment's real close
// renders as visible body text in every single card. Asserting exactly one
// DIAGRAM placeholder in the whole embedded file, and exactly one "-->"
// before <style> (the comment's own, real close), pins the fix.
func TestDiagramCardTemplateHeaderDoesNotSelfTerminate(t *testing.T) {
	data, err := fs.ReadFile(jobs.SkillsFS, "aim/assets/diagram-card.html")
	if err != nil {
		t.Fatalf("read embedded aim/assets/diagram-card.html: %v", err)
	}
	content := string(data)
	if n := strings.Count(content, "<!-- DIAGRAM: {{DIAGRAM_ID}} -->"); n != 1 {
		t.Errorf("diagram-card.html has %d occurrences of the DIAGRAM placeholder comment, want exactly 1", n)
	}
	if n := strings.Count(content, "{{CARD_TITLE}}"); n != 2 {
		t.Errorf("diagram-card.html has %d occurrences of {{CARD_TITLE}}, want exactly 2 (title + h1)", n)
	}
	if n := strings.Count(content, "{{CAPTION}}"); n != 1 {
		t.Errorf("diagram-card.html has %d occurrences of {{CAPTION}}, want exactly 1 (figcaption)", n)
	}
	styleIdx := strings.Index(content, "<style>")
	if styleIdx < 0 {
		t.Fatal("diagram-card.html missing <style> block")
	}
	head := content[:styleIdx]
	if n := strings.Count(head, "-->"); n != 1 {
		t.Errorf("diagram-card.html header (before <style>) contains %d occurrences of \"-->\", want exactly 1 (the header comment's own real close) — a second occurrence means the comment self-terminates early and everything after renders as visible body text", n)
	}
}

func TestJobsSkillsFSWiredByInit(t *testing.T) {
	// init() must have already run by the time tests execute; jobs.SkillsFS
	// should be a non-nil sub-FS rooted at .claude/skills (each top-level
	// entry a skill dir directly), not the raw embed.FS (whose top-level
	// entry is ".claude").
	if jobs.SkillsFS == nil {
		t.Fatal("jobs.SkillsFS is nil — init() did not wire it")
	}
	entries, err := fs.ReadDir(jobs.SkillsFS, ".")
	if err != nil {
		t.Fatalf("read jobs.SkillsFS root: %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	for _, want := range []string{"info", "aim", "do", "pr"} {
		if !names[want] {
			t.Errorf("jobs.SkillsFS root missing skill dir %q, got %v", want, names)
		}
	}
}

// ---------------------------------------------------------------------------
// Shape-kit vocabulary parity (C8 / U1, U2 in the cell's testing plan).
//
// The sanctioned class vocabulary is duplicated across FIVE files: the
// contract hermes reads (hermes-contract.md), the linter that enforces it
// (lint_svg.py), and THREE copies of the same stylesheet (diagram-card.html,
// spec-template.html, explain-template.html). A class added to the linter but
// missing from one stylesheet renders unstyled in exactly one artifact type,
// silently — nothing fails, nothing logs, and it only surfaces when a human
// happens to open a rendered card of that one type. A human reading the diff
// will not notice a single missing selector among ~120. These tests are the
// only mechanical link between the five copies.
//
// Everything below reads the EMBEDDED tree (jobs.SkillsFS), not the working
// tree: the embedded copy is what actually ships into a cell worktree, so
// that is the copy that must agree with itself.
// ---------------------------------------------------------------------------

// canonicalShapeClasses is the vocabulary of record: the 45 sanctioned class
// tokens. Held here as a Go literal on purpose — a test that derived the list
// from one of the files it checks would agree with itself by construction and
// catch nothing.
var canonicalShapeClasses = []string{
	// base kit
	"node", "accent", "group", "store", "lane",
	"edge", "dashed", "edgeLabel", "arrowhead", "flow-dot",
	"ok", "warn", "danger",
	"cell", "step",
	"pill", "decision", "actor", "badge", "bar", "axis", "region",
	"title", "note", "dim",
	// shape kit: the 17 silhouettes
	"proc", "cli", "thread", "fn", "agent", "llm", "timer",
	"iface", "event", "ui",
	"table", "file", "queue", "type",
	"pkg", "machine", "cloud",
	// shape-kit modifiers + the sparkle paint class
	"head", "holds", "spark",
}

// shapeKitStylesheets are the three files that must each resolve every
// canonical class to paint. Kept in one place so a fourth copy (if one is ever
// added) only has to be listed once.
var shapeKitStylesheets = []string{
	"aim/assets/diagram-card.html",
	"aim/assets/spec-template.html",
	"aim/assets/explain-template.html",
}

func readEmbedded(t *testing.T, path string) string {
	t.Helper()
	data, err := fs.ReadFile(jobs.SkillsFS, path)
	if err != nil {
		t.Fatalf("read embedded %s: %v", path, err)
	}
	return string(data)
}

// pyStringLiteralRe pulls the quoted tokens out of a python set body. Class
// tokens are plain identifiers/hyphenated words, so a narrow character class
// keeps stray quoted text (a message, a docstring) from being read as a token.
var pyStringLiteralRe = regexp.MustCompile(`"([A-Za-z][A-Za-z0-9_-]*)"`)

// pyIdentByte reports whether b can appear inside a python identifier, so a
// name search can reject a match that is really the tail of a longer name.
func pyIdentByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// pyBraceBody returns the text between the `{` that opens the module-level
// assignment `name = {…}` in `src` and that literal's first closing `}`, with
// `#` comments stripped line by line.
//
// It matches the ASSIGNMENT, not the first textual occurrence of the name:
// lint_svg.py discusses its own tables at length in comments (NESTABLE_SHAPES,
// SHAPE_RECT_INSET and friends are all named in prose above and below their
// definitions), and a plain substring search would happily start reading in the
// middle of a paragraph. Tolerant of reflow — any spacing around `=`, the brace
// on the same line or the next, trailing commas, and interleaved `#` section
// comments all parse the same. Intolerant of finding nothing: ok=false, so every
// caller can fail loudly instead of vacuously passing on an empty table.
func pyBraceBody(src, name string) (string, bool) {
	for i := 0; ; {
		j := strings.Index(src[i:], name)
		if j < 0 {
			return "", false
		}
		at := i + j
		i = at + len(name)
		if at > 0 && pyIdentByte(src[at-1]) {
			continue // the tail of some longer identifier
		}
		rest := strings.TrimLeft(src[i:], " \t")
		if !strings.HasPrefix(rest, "=") {
			continue // a mention in a comment or an expression, not the assignment
		}
		rest = strings.TrimLeft(rest[1:], " \t")
		if !strings.HasPrefix(rest, "{") {
			continue // assigned something that is not a brace literal
		}
		var body strings.Builder
		for _, line := range strings.Split(rest[1:], "\n") {
			if k := strings.Index(line, "#"); k >= 0 { // strip trailing/section comments
				line = line[:k]
			}
			if k := strings.Index(line, "}"); k >= 0 { // literal closes here
				body.WriteString(line[:k])
				return body.String(), true
			}
			body.WriteString(line)
			body.WriteString("\n")
		}
		return "", false // unterminated literal
	}
}

// parsePySet extracts the string members of a python set literal named `name`
// from `src`. It is deliberately tolerant of reformatting — the set may span
// any number of lines, carry trailing commas, and (as ALLOWED_CLASSES does)
// contain `#` section comments between groups of tokens — because a purely
// cosmetic reflow of the linter must not turn this test red. It is NOT
// tolerant of finding nothing: an empty parse returns ok=false so the caller
// can fail loudly rather than vacuously pass, which is the failure mode that
// would quietly disable the whole parity check.
func parsePySet(src, name string) (map[string]bool, bool) {
	body, ok := pyBraceBody(src, name)
	if !ok {
		return nil, false
	}
	out := map[string]bool{}
	for _, m := range pyStringLiteralRe.FindAllStringSubmatch(body, -1) {
		out[m[1]] = true
	}
	return out, len(out) > 0
}

// styleBlocks returns the concatenated bodies of every <style> element in an
// HTML file. Scoping the class scan to the stylesheet keeps prose in the
// template's header comment or body markup from counting as "styled".
func styleBlocks(html string) string {
	var b strings.Builder
	rest := html
	for {
		i := strings.Index(rest, "<style>")
		if i < 0 {
			break
		}
		rest = rest[i+len("<style>"):]
		j := strings.Index(rest, "</style>")
		if j < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:j])
		b.WriteString("\n")
		rest = rest[j+len("</style>"):]
	}
	return b.String()
}

var cssCommentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)

// cssSelectorText returns only the selector preludes of a stylesheet: comments
// stripped, then every run of text that sits between the previous rule's `}`
// and the next `{`. A token mentioned only in a comment or only inside a
// declaration value therefore does not count as styled — which is the point,
// since neither paints anything.
func cssSelectorText(css string) string {
	css = cssCommentRe.ReplaceAllString(css, " ")
	var b strings.Builder
	for _, chunk := range strings.Split(css, "{") {
		prelude := chunk
		if k := strings.LastIndex(chunk, "}"); k >= 0 {
			prelude = chunk[k+1:]
		}
		b.WriteString(prelude)
		b.WriteString("\n")
	}
	return b.String()
}

// classTokenRe matches `.tok` only when the next character cannot continue a
// class name, so `.ok` does not match `.okay` and `.cell` does not match
// `.cellhead`. RE2 has no lookahead, hence the explicit trailing character
// class rather than (?![\w-]).
func classTokenRe(tok string) *regexp.Regexp {
	return regexp.MustCompile(`\.` + regexp.QuoteMeta(tok) + `($|[^0-9A-Za-z_-])`)
}

// TestShapeKitClassParity is the drift guard over the five copies of the
// sanctioned vocabulary. It asserts, against the embedded tree:
//
//  1. canonicalShapeClasses ≡ ALLOWED_CLASSES in lint_svg.py, in BOTH
//     directions — one-way would let a class the linter accepts but nothing
//     styles slip in unnoticed, which is the same silent-unstyled bug from the
//     other end.
//  2. every token has at least one selector in each of the three stylesheets.
//  3. every token is documented in hermes-contract.md, the catalog hermes
//     actually reads — a class no contract mentions is a class no diagram
//     will ever use.
//
// Every failure names the token AND the file, so a red run points straight at
// the edit instead of costing the next person a bisect.
func TestShapeKitClassParity(t *testing.T) {
	// --- 1. linter parity, both directions ---
	lint := readEmbedded(t, "aim/assets/lint_svg.py")
	allowed, ok := parsePySet(lint, "ALLOWED_CLASSES")
	if !ok {
		t.Fatalf("could not parse ALLOWED_CLASSES out of aim/assets/lint_svg.py (found no tokens) — " +
			"the set was renamed or reshaped; fix this parser rather than leaving the parity check silently dead")
	}
	canonical := map[string]bool{}
	for _, tok := range canonicalShapeClasses {
		canonical[tok] = true
	}
	if len(canonical) != len(canonicalShapeClasses) {
		t.Errorf("canonicalShapeClasses has duplicates: %d entries, %d unique", len(canonicalShapeClasses), len(canonical))
	}
	for _, tok := range canonicalShapeClasses {
		if !allowed[tok] {
			t.Errorf("class %q is canonical but missing from ALLOWED_CLASSES in aim/assets/lint_svg.py — the linter will reject markup that uses it", tok)
		}
	}
	for tok := range allowed {
		if !canonical[tok] {
			t.Errorf("class %q is in ALLOWED_CLASSES in aim/assets/lint_svg.py but not in canonicalShapeClasses — either add it to the canonical list (and to all three stylesheets + the contract) or drop it from the linter", tok)
		}
	}

	// --- 2. every token styled in every stylesheet ---
	for _, path := range shapeKitStylesheets {
		selectors := cssSelectorText(styleBlocks(readEmbedded(t, path)))
		if strings.TrimSpace(selectors) == "" {
			t.Fatalf("%s: found no CSS selectors — the <style> block moved or the extractor broke", path)
		}
		for _, tok := range canonicalShapeClasses {
			if !classTokenRe(tok).MatchString(selectors) {
				t.Errorf("class %q has no selector in %s — a fragment using it renders unstyled in that artifact type only", tok, path)
			}
		}
	}

	// --- 3. every token documented in the contract ---
	contract := readEmbedded(t, "aim/assets/hermes-contract.md")
	for _, tok := range canonicalShapeClasses {
		if !strings.Contains(contract, tok) {
			t.Errorf("class %q never appears in aim/assets/hermes-contract.md — hermes has no way to learn it exists", tok)
		}
	}
}

// sharedSVGStart / sharedSVGEnd bound the block of SVG-styling rules that all
// three stylesheets must carry identically. The bounds are anchors, not the
// rules themselves: the last rule in the block has already moved once during
// the shape-kit change, so pinning "through the closing brace of rule X" would
// have to be re-edited every time the block grows.
const (
	sharedSVGStart = "/* ===== Shared SVG styling"
	sharedSVGEnd   = "@media (prefers-reduced-motion: reduce)"
)

// sharedSVGRegion returns the shared SVG-styling block of a stylesheet: from
// the start marker through the last line before the reduced-motion block.
func sharedSVGRegion(t *testing.T, path string) string {
	t.Helper()
	content := readEmbedded(t, path)
	start := strings.Index(content, sharedSVGStart)
	if start < 0 {
		t.Fatalf("%s: missing the %q marker that opens the shared SVG-styling block", path, sharedSVGStart)
	}
	// Search for the end marker AFTER start: the legacy templates also mention
	// prefers-reduced-motion in their header comment, well above the block.
	rel := strings.Index(content[start:], sharedSVGEnd)
	if rel < 0 {
		t.Fatalf("%s: missing the %q block that closes the shared SVG-styling region", path, sharedSVGEnd)
	}
	end := start + rel
	if nl := strings.LastIndex(content[start:end], "\n"); nl >= 0 {
		end = start + nl + 1 // cut at the start of the @media line
	}
	return content[start:end]
}

// TestStylesheetsIdentical pins C6: the legacy templates carry a byte-identical
// copy of the card template's SVG rules, so a fragment renders the same in a
// legacy combined doc as in a standalone card. Token-presence parity
// (TestShapeKitClassParity) does not catch this — a copy can have every class
// yet drift in ordering, specificity, or a single value, and the only symptom
// is a diagram that looks subtly different in one artifact type.
func TestStylesheetsIdentical(t *testing.T) {
	base := shapeKitStylesheets[0]
	want := sharedSVGRegion(t, base)
	if strings.TrimSpace(want) == "" {
		t.Fatalf("%s: extracted shared SVG-styling region is empty", base)
	}
	for _, path := range shapeKitStylesheets[1:] {
		got := sharedSVGRegion(t, path)
		if got == want {
			continue
		}
		t.Errorf("shared SVG-styling block differs between %s and %s:\n%s", base, path, diffReport(base, want, path, got))
	}
}

// diffReport renders the first divergence between two versions of the shared
// block as a unified-style excerpt. "not identical" on its own would leave the
// reader diffing 100 lines of CSS by eye.
func diffReport(nameA, a, nameB, b string) string {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	n := len(la)
	if len(lb) < n {
		n = len(lb)
	}
	i := 0
	off := 0
	for ; i < n && la[i] == lb[i]; i++ {
		off += len(la[i]) + 1
	}
	var out strings.Builder
	fmt.Fprintf(&out, "  first difference at block line %d (byte offset %d); %s has %d lines, %s has %d lines\n",
		i+1, off, nameA, len(la), nameB, len(lb))
	for c := i - 2; c < i; c++ {
		if c >= 0 && c < len(la) {
			fmt.Fprintf(&out, "   %s\n", la[c])
		}
	}
	if i < len(la) {
		fmt.Fprintf(&out, "  -%s: %s\n", nameA, la[i])
	} else {
		fmt.Fprintf(&out, "  -%s: <end of block>\n", nameA)
	}
	if i < len(lb) {
		fmt.Fprintf(&out, "  +%s: %s\n", nameB, lb[i])
	} else {
		fmt.Fprintf(&out, "  +%s: <end of block>\n", nameB)
	}
	return out.String()
}

// TestContractSectionsReferencedBySkills pins the contract anchors that
// .claude/skills/aim/SKILL.md sends an agent to by NAME.
// TestCrewPathsPinnedToEmbeddedTree above pins that hermes-contract.md exists;
// nothing pinned what is inside it, so renaming a heading kept CI green while
// every doc job pointed zeus at a section that no longer existed.
//
// The dependent strings all live in aim/SKILL.md's Core Rules now (zeus' brief
// used to carry its own copy and no longer does — see
// TestZeusBriefDoesNotRestateSkills): "...taken from the sanctioned kind list
// in the Shape Kit section of `.claude/skills/aim/assets/hermes-contract.md` —
// that section's kind → shape table is the source of truth..." and "...a
// component you cannot honestly name a kind for falls back to the plain `node`
// rounded rect..."
//
// So: the "Shape Kit" section heading, the kind → shape lookup heading beneath
// it, and the fallback row that gives an unlisted kind a shape must all exist.
func TestContractSectionsReferencedBySkills(t *testing.T) {
	contract := readEmbedded(t, "aim/assets/hermes-contract.md")
	for _, anchor := range []struct{ what, text string }{
		{"the Shape Kit section heading (brief + SKILL.md say \"the Shape Kit section of\" the contract)", "\n## Shape Kit\n"},
		{"the kind → shape lookup heading (brief says hermes derives the shape via \"the contract's kind → shape table\")", "\n### Kind → shape lookup\n"},
		{"the `node` fallback row (brief: a component with no listed kind still gets a silhouette; SKILL.md: \"falls back to the plain `node` rounded rect\")", "| anything genuinely outside the list above | `node` |"},
	} {
		if !strings.Contains(contract, anchor.text) {
			t.Errorf("aim/assets/hermes-contract.md is missing %s: no %q — aim/SKILL.md points at it by name, so a rename here silently breaks every doc job", anchor.what, anchor.text)
		}
	}
	// The fallback bullet in the Shape Kit prose is the same contract stated in
	// words; keep both, since the skill leans on the behaviour and the table row
	// alone would not tell hermes the kind text survives the fallback.
	if !strings.Contains(contract, "falls back to plain `node`") {
		t.Error("aim/assets/hermes-contract.md is missing the `node` fallback rule (\"falls back to plain `node`\") — aim/SKILL.md tells zeus an unnamed kind still gets a shape, and this is where hermes learns which")
	}
}

// ---------------------------------------------------------------------------
// Shape-kit TABLE parity: the numbers, not the vocabulary.
//
// TestShapeKitClassParity above guards WHICH class tokens exist. It says
// nothing about the three policy tables that are ALSO duplicated between
// hermes-contract.md (prose tables hermes reads) and lint_svg.py (python
// literals that gate every fragment) — content wells, usable label widths, and
// the never-a-parent list. Both files were edited by hand in the same change and
// both diverged, twice, caught by a human reviewer rather than by CI:
//
//   - `lane` sat in NESTABLE_SHAPES while the contract said twice it may never be
//     a parent — and the linter's own NEST_PARENT message advertised `lane` as a
//     legal destination to move a child into.
//   - `machine`'s well was 2–4px looser in the linter than the published row, so
//     a child hanging over the front face's edge linted clean.
//
// Neither shows up as a test failure, a stack trace, or a rendering artifact:
// the fragment lints green and the diagram is subtly wrong. These assertions are
// the only mechanical link between the two copies of the numbers.
//
// Both sides are PARSED (unlike canonicalShapeClasses, which is a Go literal on
// purpose): the question here is not "what should the numbers be" but "do the
// two files agree", so a third hardcoded copy would only add a third thing to
// forget. The vacuity risk that trade brings — a parser that silently matches
// nothing and passes — is answered by failing loudly on an empty parse, a
// missing table, an unparseable row, and a missing sentinel row.
// ---------------------------------------------------------------------------

// mdSepRowRe matches a markdown table's `|---|:--:|` separator line.
var mdSepRowRe = regexp.MustCompile(`^[|\s:-]+$`)

// splitMDRow splits one `| a | b | c |` line into trimmed cells. Column padding
// is therefore invisible to every caller, which is half of what makes these
// checks survive a reflow of the contract.
func splitMDRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	cells := strings.Split(line, "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

// mdTableRows returns the body rows (separator dropped, cells trimmed) of the
// first markdown table in `src` whose header row mentions every string in
// `headerCells`. Located by header text rather than by line number or by the
// surrounding heading, so re-wording the prose around a table, moving it within
// its section, or adding a column all leave the lookup working.
func mdTableRows(src string, headerCells ...string) [][]string {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		head := strings.TrimSpace(line)
		if !strings.HasPrefix(head, "|") {
			continue
		}
		match := true
		for _, want := range headerCells {
			if !strings.Contains(head, want) {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		var rows [][]string
		for _, l := range lines[i+1:] {
			l = strings.TrimSpace(l)
			if !strings.HasPrefix(l, "|") {
				break // table ended
			}
			if mdSepRowRe.MatchString(l) {
				continue
			}
			rows = append(rows, splitMDRow(l))
		}
		return rows
	}
	return nil
}

// mdToken strips the backticks and spaces a markdown cell wraps a code token in,
// so “ `node` “ and `node` both read as "node" — the contract adds and drops
// those backticks freely.
func mdToken(cell string) string {
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(cell), "`"))
}

// contractWellRe reads a content-well cell — `X+14, Y+26, W-28, H-38` — into its
// four signed insets. Whitespace around every sign and comma is optional, and
// the surrounding backticks are already gone, so only the NUMBERS can break it.
var contractWellRe = regexp.MustCompile(
	`X\s*([+-]\s*\d+)\s*,\s*Y\s*([+-]\s*\d+)\s*,\s*W\s*([+-]\s*\d+)\s*,\s*H\s*([+-]\s*\d+)`)

// contractWidthRe reads a usable-width cell — `W - 24` — into its allowance.
var contractWidthRe = regexp.MustCompile(`W\s*-\s*(\d+)`)

// pyWellRowRe reads one `"machine": (12, 26, -36, -38),` row of NESTABLE_WELLS.
var pyWellRowRe = regexp.MustCompile(
	`"([A-Za-z][A-Za-z0-9_-]*)"\s*:\s*\(\s*(-?\d+)\s*,\s*(-?\d+)\s*,\s*(-?\d+)\s*,\s*(-?\d+)\s*\)`)

// pyIntRowRe reads one `"event": 44,` row of SHAPE_WIDTH_ALLOWANCE.
var pyIntRowRe = regexp.MustCompile(`"([A-Za-z][A-Za-z0-9_-]*)"\s*:\s*(-?\d+)`)

// atoiSigned parses a `+14` / `- 28` capture. The regexes above guarantee the
// shape, so a failure here is a bug in this file, not in the file under test.
func atoiSigned(raw string) int {
	n, err := strconv.Atoi(strings.ReplaceAll(strings.TrimSpace(raw), " ", ""))
	if err != nil {
		return 0
	}
	return n
}

func fmtWell(w [4]int) string {
	return fmt.Sprintf("(%d, %d, %d, %d)", w[0], w[1], w[2], w[3])
}

// parseContractWells reads the contract's content-well table. Rows whose well
// cell is prose rather than geometry (`table`'s "its own `cell` children only",
// `region`'s "anywhere inside") come back in `prose` instead of `wells` — they
// are real rows making a real claim, just not a numeric one, and the caller
// asserts the claim the linter makes about them rather than skipping them.
func parseContractWells(t *testing.T, contract string) (wells map[string][4]int, prose map[string]string) {
	t.Helper()
	rows := mdTableRows(contract, "Parent shape", "Content well")
	if len(rows) == 0 {
		t.Fatal("aim/assets/hermes-contract.md: could not find the content-well table " +
			"(no markdown table whose header names both \"Parent shape\" and \"Content well\") — " +
			"the heading or the column names changed; fix this parser rather than leaving the " +
			"well parity check silently dead")
	}
	wells = map[string][4]int{}
	prose = map[string]string{}
	for _, row := range rows {
		if len(row) < 2 {
			t.Fatalf("aim/assets/hermes-contract.md: content-well row %q has %d columns, want at least 2 "+
				"(shape, well) — fix this parser", strings.Join(row, " | "), len(row))
		}
		shape := mdToken(row[0])
		if shape == "" {
			t.Fatalf("aim/assets/hermes-contract.md: content-well row %q has an empty shape column — fix this parser",
				strings.Join(row, " | "))
		}
		m := contractWellRe.FindStringSubmatch(row[1])
		if m == nil {
			prose[shape] = row[1]
			continue
		}
		wells[shape] = [4]int{atoiSigned(m[1]), atoiSigned(m[2]), atoiSigned(m[3]), atoiSigned(m[4])}
	}
	if len(wells) == 0 {
		t.Fatal("aim/assets/hermes-contract.md: the content-well table parsed to zero geometric rows — " +
			"the `X+14, Y+26, W-28, H-38` cell format changed; fix this parser rather than leaving the " +
			"well parity check silently dead")
	}
	// Sentinel: `node` is the fallback box and the first row of the table. If it
	// stopped parsing, the parse is broken even though it found other rows.
	if _, ok := wells["node"]; !ok {
		t.Fatalf("aim/assets/hermes-contract.md: the content-well table parsed %d rows but none of them is `node`, "+
			"the fallback box — the parse is picking up the wrong table; fix this parser", len(wells))
	}
	return wells, prose
}

// storeCapBulge is the ONE documented, deliberate divergence between the
// contract's well table and NESTABLE_WELLS, and the linter states it in its own
// comment above the table: `store` is the only parent whose box the linter
// MEASURES (a cylinder draws no body <rect>) rather than reconstructs, and
// path_points sees on-curve endpoints only, so the bottom cap's 10px arc bulge
// is invisible and a contract-conformant cylinder measures 10px shorter than it
// draws. The linter's `dh` is therefore 10 less negative, which lands the well
// on exactly the contract's rectangle — the claim asserted below, rather than
// the four numbers being equal.
//
// `machine` — the other row the linter calls out as non-literal — needs no such
// adjustment, and that is worth stating because it is where a real defect lived:
// its box is likewise reconstructed rather than drawn (SHAPE_RECT_INSET grows the
// front-face <rect> back to the full box), but that reconstruction happens in
// silhouette_box BEFORE the insets apply, so the four inset numbers still
// transcribe the contract's row verbatim. The bug this catches is exactly the one
// review found: pre-paying the front face's 12px offset a second time here, which
// loosens the well by 2–4px against the published numbers.
const storeCapBulge = 10

// TestShapeKitTableParity is the drift guard over the three policy tables the
// class-vocabulary test does not reach. Against the embedded tree it asserts:
//
//  1. every content-well row in hermes-contract.md resolves to the same
//     rectangle as NESTABLE_WELLS in lint_svg.py, and vice versa — with `store`
//     compared through its documented measurement offset, and the contract's two
//     prose rows (`table`, `region`) asserted to carry no geometry in the linter.
//  2. every usable-label-width row equals its SHAPE_WIDTH_ALLOWANCE entry, in
//     BOTH directions, and the two rows with no numeric allowance (`llm`,
//     `timer`, whose labels sit outside the shape) have no entry at all.
//  3. the contract's "Never a parent" list is a subset of the linter's leaf set,
//     with `table` as the single intended extra.
//
// Every failure names the shape, the table, and both values.
func TestShapeKitTableParity(t *testing.T) {
	contract := readEmbedded(t, "aim/assets/hermes-contract.md")
	lint := readEmbedded(t, "aim/assets/lint_svg.py")

	// --- 1. content wells ---
	contractWells, contractProse := parseContractWells(t, contract)

	wellBody, ok := pyBraceBody(lint, "NESTABLE_WELLS")
	if !ok {
		t.Fatal("aim/assets/lint_svg.py: could not find the `NESTABLE_WELLS = {…}` assignment — " +
			"the table was renamed or reshaped; fix this parser rather than leaving the well parity " +
			"check silently dead")
	}
	lintWells := map[string][4]int{}
	for _, m := range pyWellRowRe.FindAllStringSubmatch(wellBody, -1) {
		lintWells[m[1]] = [4]int{atoiSigned(m[2]), atoiSigned(m[3]), atoiSigned(m[4]), atoiSigned(m[5])}
	}
	if len(lintWells) == 0 {
		t.Fatal("aim/assets/lint_svg.py: NESTABLE_WELLS parsed to zero rows — the `\"shape\": (dx, dy, dw, dh)` " +
			"row format changed; fix this parser rather than leaving the well parity check silently dead")
	}

	for shape, want := range contractWells {
		got, present := lintWells[shape]
		if !present {
			t.Errorf("content well for %q: hermes-contract.md publishes %s, but NESTABLE_WELLS in "+
				"lint_svg.py has no row for it — the linter cannot check children of that parent at all",
				shape, fmtWell(want))
			continue
		}
		expect := want
		if shape == "store" {
			expect[3] = want[3] + storeCapBulge
		}
		if got != expect {
			extra := ""
			if shape == "store" {
				extra = fmt.Sprintf(" (the contract's %s plus the documented +%d cap-bulge offset on dh, "+
					"since a cylinder measures 10px shorter than it draws)", fmtWell(want), storeCapBulge)
			}
			t.Errorf("content well for %q disagrees: hermes-contract.md's table says %s → expected linter "+
				"row %s%s, but NESTABLE_WELLS in lint_svg.py has %s — a child that lints clean under one "+
				"lands outside the other; fix whichever number is wrong, in both files",
				shape, fmtWell(want), fmtWell(expect), extra, fmtWell(got))
		}
	}
	for shape, got := range lintWells {
		if _, present := contractWells[shape]; present {
			continue
		}
		if cell, isProse := contractProse[shape]; isProse {
			t.Errorf("content well for %q: hermes-contract.md's table gives it no geometry (%q), yet "+
				"NESTABLE_WELLS in lint_svg.py has %s — the linter is enforcing a rectangle the contract "+
				"never published, so hermes has no way to satisfy it", shape, cell, fmtWell(got))
			continue
		}
		t.Errorf("content well for %q: NESTABLE_WELLS in lint_svg.py has %s but hermes-contract.md's "+
			"content-well table has no row for it — hermes is never told this shape can hold children",
			shape, fmtWell(got))
	}
	// The contract's prose rows are claims too: "its own `cell` children only"
	// and "anywhere inside" both mean "no rectangle to enforce", which the linter
	// expresses by omitting the row. The loop above catches the reverse (a row
	// appearing); this pins that the omission stays deliberate rather than the
	// rows quietly vanishing from the contract.
	for _, shape := range []string{"table", "region"} {
		if _, present := contractProse[shape]; !present {
			t.Errorf("content well for %q: hermes-contract.md no longer has a prose (non-geometric) row for it "+
				"in the content-well table — it is the row that tells hermes this parent has no enforced "+
				"rectangle, and NESTABLE_WELLS in lint_svg.py still omits it on that basis", shape)
		}
	}

	// --- 2. usable label width ---
	widthRows := mdTableRows(contract, "Usable inner width", "Min height")
	if len(widthRows) == 0 {
		t.Fatal("aim/assets/hermes-contract.md: could not find the sizing table (no markdown table whose " +
			"header names both \"Usable inner width\" and \"Min height\") — the column names changed; fix " +
			"this parser rather than leaving the width parity check silently dead")
	}
	contractWidths := map[string]int{}
	widthProse := map[string]string{}
	for _, row := range widthRows {
		if len(row) < 2 {
			t.Fatalf("aim/assets/hermes-contract.md: sizing row %q has %d columns, want at least 2 "+
				"(shape, usable width) — fix this parser", strings.Join(row, " | "), len(row))
		}
		shape := mdToken(row[0])
		if shape == "" {
			t.Fatalf("aim/assets/hermes-contract.md: sizing row %q has an empty shape column — fix this parser",
				strings.Join(row, " | "))
		}
		if m := contractWidthRe.FindStringSubmatch(row[1]); m != nil {
			contractWidths[shape] = atoiSigned(m[1])
		} else {
			widthProse[shape] = row[1]
		}
	}
	if len(contractWidths) == 0 {
		t.Fatal("aim/assets/hermes-contract.md: the sizing table parsed to zero `W - N` rows — the usable-width " +
			"cell format changed; fix this parser rather than leaving the width parity check silently dead")
	}
	if _, present := contractWidths["node"]; !present {
		t.Fatalf("aim/assets/hermes-contract.md: the sizing table parsed %d usable-width rows but none of them "+
			"is `node` — the parse is picking up the wrong table; fix this parser", len(contractWidths))
	}

	allowanceBody, ok := pyBraceBody(lint, "SHAPE_WIDTH_ALLOWANCE")
	if !ok {
		t.Fatal("aim/assets/lint_svg.py: could not find the `SHAPE_WIDTH_ALLOWANCE = {…}` assignment — the " +
			"table was renamed or reshaped; fix this parser rather than leaving the width parity check " +
			"silently dead")
	}
	lintWidths := map[string]int{}
	for _, m := range pyIntRowRe.FindAllStringSubmatch(allowanceBody, -1) {
		lintWidths[m[1]] = atoiSigned(m[2])
	}
	if len(lintWidths) == 0 {
		t.Fatal("aim/assets/lint_svg.py: SHAPE_WIDTH_ALLOWANCE parsed to zero rows — the `\"shape\": N` row " +
			"format changed; fix this parser rather than leaving the width parity check silently dead")
	}

	for shape, want := range contractWidths {
		got, present := lintWidths[shape]
		if !present {
			t.Errorf("usable label width for %q: hermes-contract.md's sizing table says `W - %d`, but "+
				"SHAPE_WIDTH_ALLOWANCE in lint_svg.py has no entry — SHAPE_TEXT_FIT never checks a label "+
				"in this silhouette", shape, want)
			continue
		}
		if got != want {
			t.Errorf("usable label width for %q disagrees: hermes-contract.md's sizing table says `W - %d`, "+
				"SHAPE_WIDTH_ALLOWANCE in lint_svg.py says %d — hermes sizes boxes to one number and the "+
				"linter measures against the other; fix whichever is wrong, in both files", shape, want, got)
		}
	}
	for shape, got := range lintWidths {
		if _, present := contractWidths[shape]; present {
			continue
		}
		if cell, isProse := widthProse[shape]; isProse {
			t.Errorf("usable label width for %q: hermes-contract.md's sizing table publishes no numeric "+
				"allowance (%q — the label sits outside the shape), yet SHAPE_WIDTH_ALLOWANCE in lint_svg.py "+
				"has %d; either the contract gained an interior or the linter is measuring one that does not "+
				"exist", shape, cell, got)
			continue
		}
		t.Errorf("usable label width for %q: SHAPE_WIDTH_ALLOWANCE in lint_svg.py has %d but "+
			"hermes-contract.md's sizing table has no row for it — hermes is never told how much of the box "+
			"it can fill", shape, got)
	}
	// `llm` and `timer` draw their labels outside the silhouette, so the contract
	// gives them prose instead of a `W - N`. TEXT_FIT_EXEMPT_SHAPES is the
	// linter's word for the same thing; a numeric allowance appearing for either
	// would mean the two files stopped agreeing about what has an interior.
	for _, shape := range []string{"llm", "timer"} {
		if _, present := widthProse[shape]; !present {
			t.Errorf("usable label width for %q: hermes-contract.md's sizing table no longer carries a prose "+
				"(non-numeric) width cell for it — that cell is what says the label sits outside the shape, "+
				"which is why SHAPE_WIDTH_ALLOWANCE in lint_svg.py omits it", shape)
		}
	}

	// --- 3. never a parent ---
	neverParent := parseNeverAParent(t, contract)
	shapeClasses, ok := parsePySet(lint, "SHAPE_CLASSES")
	if !ok {
		t.Fatal("aim/assets/lint_svg.py: could not parse SHAPE_CLASSES (found no tokens) — the set was " +
			"renamed or reshaped; fix this parser rather than leaving the leaf parity check silently dead")
	}
	nestable, ok := parsePySet(lint, "NESTABLE_SHAPES")
	if !ok {
		t.Fatal("aim/assets/lint_svg.py: could not parse NESTABLE_SHAPES (found no tokens) — the set was " +
			"renamed or reshaped; fix this parser rather than leaving the leaf parity check silently dead")
	}
	// Recomputed the way the linter computes it: LEAF_SHAPES is not a literal
	// there, it is `SHAPE_CLASSES - NESTABLE_SHAPES`, so the subtraction is the
	// thing to mirror. Reading a literal that does not exist would just be a
	// parser that always finds nothing.
	leaf := map[string]bool{}
	for tok := range shapeClasses {
		if !nestable[tok] {
			leaf[tok] = true
		}
	}
	if len(leaf) == 0 {
		t.Fatal("aim/assets/lint_svg.py: SHAPE_CLASSES - NESTABLE_SHAPES came out empty — one of the two sets " +
			"parsed wrong; fix this parser rather than leaving the leaf parity check silently dead")
	}

	// The relationship is subset-plus-one, not equality: the contract lists 18
	// silhouettes under "Never a parent" and gives `table` its own well row
	// ("its own `cell` children only"), while the linter has no per-parent
	// carve-out — it leaves `table` out of NESTABLE_SHAPES and lets the blanket
	// `cell` exemption in NEST_PARENT allow the children it does accept. So
	// `table` is a leaf in lint_svg.py that the contract does not name, on
	// purpose, and that is the ONLY such shape.
	const tableIsTheIntendedExtra = "table"
	for tok := range neverParent {
		if !shapeClasses[tok] {
			t.Errorf("never-a-parent list names %q, which is not in SHAPE_CLASSES in lint_svg.py — either the "+
				"contract invented a silhouette the linter has never heard of, or this parser is reading the "+
				"wrong words out of the paragraph", tok)
			continue
		}
		if !leaf[tok] {
			t.Errorf("never-a-parent: hermes-contract.md says %q may never hold children, but lint_svg.py lists "+
				"it in NESTABLE_SHAPES, so LEAF_SHAPES excludes it — NEST_PARENT will accept a component "+
				"nested inside a %q and even advertise it as a legal destination in its failure message",
				tok, tok)
		}
	}
	for tok := range leaf {
		if neverParent[tok] || tok == tableIsTheIntendedExtra {
			continue
		}
		t.Errorf("never-a-parent: lint_svg.py treats %q as leaf-only (in SHAPE_CLASSES, absent from "+
			"NESTABLE_SHAPES) but hermes-contract.md never says so — either add it to the contract's "+
			"\"Never a parent\" list or give it a content-well row and a NESTABLE_SHAPES entry; %q is the "+
			"only sanctioned extra, since its well row admits `cell` children only",
			tok, tableIsTheIntendedExtra)
	}
	if leaf[tableIsTheIntendedExtra] && neverParent[tableIsTheIntendedExtra] {
		t.Errorf("never-a-parent: hermes-contract.md now lists %q under \"Never a parent\" as well as giving it "+
			"a content-well row — pick one; the linter's leaf set carries it as the one intended extra",
			tableIsTheIntendedExtra)
	}
	if !leaf[tableIsTheIntendedExtra] {
		t.Errorf("never-a-parent: %q is no longer leaf-only in lint_svg.py (it gained a NESTABLE_SHAPES entry) — "+
			"the contract's well row still says it holds `cell` children only, which the linter expresses by "+
			"leaving it out of NESTABLE_SHAPES and letting the blanket `cell` exemption in NEST_PARENT do the "+
			"work", tableIsTheIntendedExtra)
	}
}

// backtickTokenRe pulls the `tok` code spans out of a run of markdown prose.
var backtickTokenRe = regexp.MustCompile("`([A-Za-z][A-Za-z0-9_-]*)`")

// sentenceBreakRe marks the end of the never-a-parent list sentence.
var sentenceBreakRe = regexp.MustCompile(`\.\s`)

// parseNeverAParent reads the contract's "Never a parent" list — a comma-joined
// run of backticked shape names inside one prose paragraph, not a table.
//
// The parse is anchored three times so a reflow cannot move it: the bolded
// marker finds the paragraph, the blank line ends it, and within it the list is
// the span between the LAST colon before the first code span (the sentences
// before the list contain their own periods, so cutting on the first `. ` would
// truncate them, and an earlier colon in re-worded prose would over-read) and
// the first sentence break after it. Re-ordering the names, re-wrapping the
// lines, or re-wording the sentences on either side all still parse.
func parseNeverAParent(t *testing.T, contract string) map[string]bool {
	t.Helper()
	const marker = "**Never a parent**"
	start := strings.Index(contract, marker)
	if start < 0 {
		t.Fatalf("aim/assets/hermes-contract.md: no %q paragraph — the never-a-parent rule was renamed or "+
			"removed; fix this parser rather than leaving the leaf parity check silently dead", marker)
	}
	region := contract[start:]
	if end := strings.Index(region, "\n\n"); end >= 0 {
		region = region[:end]
	}
	firstTick := strings.Index(region, "`")
	if firstTick < 0 {
		t.Fatalf("aim/assets/hermes-contract.md: the %q paragraph names no backticked shape at all — "+
			"fix this parser rather than leaving the leaf parity check silently dead", marker)
	}
	colon := strings.LastIndex(region[:firstTick], ":")
	if colon < 0 {
		t.Fatalf("aim/assets/hermes-contract.md: the %q paragraph has no \":\" introducing its shape list — "+
			"fix this parser rather than leaving the leaf parity check silently dead", marker)
	}
	list := region[colon+1:]
	if m := sentenceBreakRe.FindStringIndex(list); m != nil {
		list = list[:m[0]] // stop at the end of the list sentence
	}
	out := map[string]bool{}
	for _, m := range backtickTokenRe.FindAllStringSubmatch(list, -1) {
		out[m[1]] = true
	}
	if len(out) == 0 {
		t.Fatalf("aim/assets/hermes-contract.md: parsed zero shapes out of the %q list (%q) — fix this parser "+
			"rather than leaving the leaf parity check silently dead", marker, list)
	}
	return out
}
