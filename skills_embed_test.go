package kraken

import (
	"fmt"
	"io/fs"
	"regexp"
	"sort"
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
		"info":  "info",
		"aim":   "aim",
		"do":    "do",
		"pr":    "pr",
		"dream": "dream",
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
			// the researcher spawn rule. It is stated in aim, info AND dream
			// because each is read INSTEAD of the others, so each copy is the
			// only one its own path ever sees — which is exactly why all three
			// are pinned: deleting it from any one file is otherwise silent,
			// and the symptom is a lead waiting forever on a report an
			// `Explore` agent had no tool to write.
			"never `Explore`",
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
			// /info acts on its feedback and never closes it. The token is the
			// PROHIBITION rather than the verb: the verb belongs to dream alone,
			// which TestOnlyDreamNamesTheFeedbackWriteVerb below enforces for the
			// four non-dream skills and crew_test.go's forbidden-token list
			// enforces for the brief.
			"never MARKED addressed",
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
			// see the same token on aim above — /info's copy is the only one
			// the explanation path reads
			"never `Explore`",
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
			// The blind-authorship split, anchored on both sides of the line.
			// The first four are skill-ONLY: crew_test.go's forbidden-token
			// list pins the brief not to name the artifact, its columns, or
			// the per-dimension filenames, so if they vanish from here they
			// exist nowhere. The three verdict names are deliberately in BOTH
			// — the brief has to be able to classify a failure without
			// re-reading a skill, and this file owns how the classification is
			// applied — so these three anchor the procedure, not the naming.
			// The last two pin the parts a wrong edit could delete while every
			// noun above still matched: the barrier, and author-without-running.
			"50-contract.md", "symbol · signature · obligations · owner",
			"_contract_test.go", "_edge_test.go",
			"code bug", "test bug", "contract defect",
			// crew-neutral wording on purpose: this half of the file is what a
			// standalone /do reads, where hephaestus and athena do not exist.
			// The cell section below binds the roles to the names.
			"author but do not run", "EVERY implementation slice is done",
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
		// dream owns the nightly corroboration pass end to end: the
		// destination rule (which is the only mistake here that is hard to
		// undo), the order of operations, how carl's verdicts are read, the
		// two-sighting default and when judgment may go below it, the report
		// shape, and its own closing line. None of it is in zeus' brief.
		{"dream", []string{
			"Inside A Kraken Cell", "<cellDir>/artifacts/", "dream swept:",
			"Use `0` where a count is zero", "Never declare the job done",
			"Findings never enter git", "00-dream-report.md",
			"kraken watchlist get|add|update|remove", "kraken feedback sweep --since",
			// this pass is the SOLE writer of `addressed`, which is why the
			// verb appears in no other skill and in no brief. Three tokens
			// because three separate claims carry the rule: who may write it,
			// what it now means, and that an open note is not an unhandled one
			// — a reader that loses the middle claim starts closing notes for
			// artifact-local fixes again, which is the state this replaced.
			"kraken feedback addressed", "only writer of `addressed`",
			"a DURABLE fix shipped", "does not mean nobody",
			// the three window properties, one token each. They sit in their own
			// passage well below the Evidence 1 paragraphs the four tokens above
			// pin, so without these the whole seam correction — once-only, the
			// clamp that sweeps zero, and what an `addressed` note MEANS on a
			// re-swept row — deletes cleanly with every test still green. The
			// third is the one whose loss is silent: the pass simply stops
			// trusting its own previous verdict, which looks like nothing.
			"the floor only moves forwards", "swept zero times", "from a previous night",
			"two independent sightings", "judgment calls",
			"suggested_fix", "prompting",
			"unnamed subagents", "solo: true",
			// see the same token on aim above — the scouts are this skill's
			// researchers and hit the same wall
			"never `Explore`",
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

// TestOnlyDreamNamesTheFeedbackWriteVerb is the ABSENT half of the rule above,
// and it is the direction that table cannot express: TestSkillsOwnTheCellContract
// only ever asserts a token is present, so an invariant of the form "exactly one
// file may say this" is half-pinned by it at best.
//
// The invariant: `addressed` is written by the nightly corroboration pass and by
// nothing else. An ordinary cell reads its feedback and acts on it, deliberately
// leaving the column empty, so that a note reaches the pass saying what carl
// asked for rather than saying one artifact was patched. crew_test.go's
// forbidden-token list keeps the verb out of zeus' brief; this keeps it out of
// the four skills an ordinary cell actually reads.
//
// Without it the regression is silent and the reversal is one plausible edit:
// `info/SKILL.md` DID name this verb until the change that added this test
// removed it, so a later hand re-adding "close it with `kraken feedback
// addressed <slug> <id>`" to `aim` — the skill every spec-path cell reads — is
// exactly the shape to expect, and every existing test stays green while every
// /aim cell resumes closing its own notes.
//
// The token deliberately omits the binary name. `k` is a first-class spelling
// of the same verb and is the one kraken's OWN usage strings use
// (cmd/kraken/cmd_feedback.go's feedbackAddressedUsage, cmd/kraken/main.go's
// help), so it is the spelling a future editor is most likely to copy — and a
// test matching only `kraken feedback addressed` would wave through
// "close it with `k feedback addressed <slug> <id>`" in aim/SKILL.md, which is
// the same silent partial reversal one spelling over. The bare substring
// appears in no non-dream skill today, so the looser match costs nothing.
func TestOnlyDreamNamesTheFeedbackWriteVerb(t *testing.T) {
	const verb = "feedback addressed"
	for _, skill := range []string{"aim", "info", "do", "pr"} {
		if strings.Contains(readEmbedded(t, skill+"/SKILL.md"), verb) {
			t.Errorf(".claude/skills/%s/SKILL.md names %q — only dream/SKILL.md may. "+
				"That column is the nightly pass' alone; a skill an ordinary cell reads "+
				"must state the prohibition, never the call", skill, verb)
		}
	}
	// the other side, so this test fails if the verb is moved rather than copied
	if !strings.Contains(readEmbedded(t, "dream/SKILL.md"), verb) {
		t.Errorf("dream/SKILL.md no longer names %q — it is the only writer, so the verb "+
			"now exists in no skill at all and nothing closes a feedback note", verb)
	}
}

// TestWaitProtocolPointersResolve pins the one cross-SKILL reference in the
// bundle. `aim`, `info` and `dream` each fan agents out and none of them
// states how to wait for the results; all three send the reader to `do`'s step
// 4 instead, by heading and bullet title. That is deliberate — the protocol was
// rewritten five commits running while it lived in one file, and a fourth copy
// is a fourth thing to drift — but it makes those two strings load-bearing
// text in a file whose own author has no reason to know they are quoted
// elsewhere.
//
// The repo already tests brief-vs-skill drift in both directions
// (TestZeusBriefDoesNotRestateSkills, TestSkillsOwnTheCellContract); this is
// the same guard for skill-vs-skill. Without it, retitling do's bullet leaves
// three skills pointing at a heading that no longer exists, and the failure is
// silent: the reader Reads the file, does not find the words, and improvises
// the wait — which is precisely the ls-in-a-loop the pointers exist to end.
func TestWaitProtocolPointersResolve(t *testing.T) {
	do := readEmbedded(t, "do/SKILL.md")
	for _, anchor := range []string{
		"### 4. Plan The Implementation", // the "step 4" every pointer names
		"**How to wait:",                 // the bullet title they name inside it
	} {
		if !strings.Contains(do, anchor) {
			t.Errorf(".claude/skills/do/SKILL.md no longer contains %q — aim, info and dream all send "+
				"a reader to it by those exact words, and a pointer that misses fails silently: the "+
				"agent improvises the wait instead", anchor)
		}
	}
	for _, skill := range []string{"aim", "info", "dream"} {
		src := readEmbedded(t, skill+"/SKILL.md")
		if !strings.Contains(src, ".claude/skills/do/SKILL.md") {
			t.Errorf(".claude/skills/%s/SKILL.md no longer points at do/SKILL.md for the wait protocol "+
				"— it fans agents out and states no protocol of its own, so dropping the pointer leaves "+
				"the reader with nothing", skill)
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
	for _, want := range []string{"info", "aim", "do", "pr", "dream"} {
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

// canonicalShapeClasses is the vocabulary of record: the sanctioned class
// tokens, sanctionedClassCount of them. Held here as a Go literal on purpose —
// a test that derived the list from one of the files it checks would agree with
// itself by construction and catch nothing. This slice is the FIFTH copy of the
// vocabulary, alongside lint_svg.py's ALLOWED_CLASSES, the three stylesheets,
// and hermes-contract.md's Ban List; adding or renaming a class means editing
// all five, and TestShapeKitClassParity/TestAllowlistMatchesContract are what
// make forgetting one of them a red run instead of a silently unstyled diagram.
var canonicalShapeClasses = []string{
	// base kit
	"node", "group", "store", "lane",
	"edge", "dashed", "edgeLabel", "arrowhead", "flow-dot",
	// the six treatments — colour names in a list of structural classes because
	// a treatment IS a class, applied on top of whatever silhouette a box already
	// has. Each is a factual claim about the thing it is drawn on (absent, wrong,
	// fixed, new, counter-intuitive), each pairs with a glyph, and
	// hermes-contract.md § Colour Semantics is where those claims are written
	// down. They replaced accent/ok/warn/danger in a clean cutover — no aliases.
	"grey", "red", "yellow", "green", "blue", "purple",
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

// ---------------------------------------------------------------------------
// Palette parity: the COLOUR half of the same duplication problem.
//
// TestShapeKitClassParity/TestStylesheetsIdentical above cover the shape
// vocabulary and the shared SVG rule block. Nothing covered the palette itself:
// the `:root` custom properties and the dark-mode overrides are a THIRD verbatim
// copy in each of the three stylesheets, and a one-hex drift in one of them
// renders every diagram in exactly one artifact type in a colour no other
// artifact uses — with no failure, no warning, and a diff no human reads through.
//
// Nor was there any link between the linter's class list and the WRITTEN meaning
// of a treatment. A treatment added to ALLOWED_CLASSES with no `### <treatment>`
// section in hermes-contract.md is a class zeus can legally assign and no agent
// can correctly use, because nothing anywhere says what it claims.
// ---------------------------------------------------------------------------

// sanctionedClassCount is the size of the class vocabulary of record: 6
// treatments + the base kit + the 17 silhouettes + the modifiers. Pinned as a
// number on purpose. Adding a class is a five-file edit (linter, contract, three
// stylesheets) and bumping this line is the cheapest part of it — a test that
// derived the count from one of those files would agree with itself and never
// notice the other four.
const sanctionedClassCount = 47

// cssBraceBlock returns the first block in `css` introduced by `opener`: from the
// opener through the `}` that balances its `{`. Brace-matched rather than
// line-counted so a reflow of the block's contents cannot truncate the extract
// (and so the nested `:root` inside the dark-mode `@media` comes along with it).
func cssBraceBlock(css, opener string) (string, bool) {
	i := strings.Index(css, opener)
	if i < 0 {
		return "", false
	}
	open := strings.Index(css[i:], "{")
	if open < 0 {
		return "", false
	}
	depth := 0
	for k := i + open; k < len(css); k++ {
		switch css[k] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return css[i : k+1], true
			}
		}
	}
	return "", false
}

// svgRules returns every CSS rule in `css` whose selector prelude starts with
// `svg `, each flattened to a single space-joined line. Rules are collected by
// brace balance, not by line, because the stylesheets wrap long selector lists
// onto a continuation line (`svg .store > rect, … \n { fill: … }`) — a
// line-oriented scan would compare the selector and silently skip the
// declarations under it, which is exactly where a colour drift would hide.
//
// Leading indentation is trimmed, so the reduced-motion `@media` block's
// deeper-indented `svg .flow-dot` rule compares equal across files that agree on
// content. Comments are stripped first: prose about a rule is not the rule.
func svgRules(css string) []string {
	css = cssCommentRe.ReplaceAllString(css, "")
	var rules []string
	var cur []string
	depth := 0
	opened := false
	for _, line := range strings.Split(css, "\n") {
		trimmed := strings.TrimSpace(line)
		if len(cur) == 0 {
			if trimmed != "svg" && !strings.HasPrefix(trimmed, "svg ") && !strings.HasPrefix(trimmed, "svg{") {
				continue
			}
		}
		cur = append(cur, trimmed)
		for _, c := range trimmed {
			switch c {
			case '{':
				depth++
				opened = true
			case '}':
				depth--
			}
		}
		if opened && depth <= 0 {
			rules = append(rules, strings.Join(cur, " "))
			cur, depth, opened = nil, 0, false
		}
	}
	return rules
}

// palette is the part of a stylesheet that MUST be byte-identical across the
// three templates. Everything else in them — body, h1-h4, code/pre, table,
// blockquote.meta, figure — legitimately differs: a card has no prose, a spec
// doc has four heading levels, and comparing those would just be noise.
type palette struct {
	root string
	dark string
	svg  []string
}

func readPalette(t *testing.T, path string) palette {
	t.Helper()
	css := styleBlocks(readEmbedded(t, path))
	if strings.TrimSpace(css) == "" {
		t.Fatalf("%s: no <style> block — the extractor broke or the template stopped carrying its own CSS", path)
	}
	css = cssCommentRe.ReplaceAllString(css, "")
	root, ok := cssBraceBlock(css, ":root")
	if !ok {
		t.Fatalf("%s: no `:root { … }` block — the custom properties moved or were renamed; fix this "+
			"parser rather than leaving the palette parity check silently dead", path)
	}
	dark, ok := cssBraceBlock(css, "@media (prefers-color-scheme: dark)")
	if !ok {
		t.Fatalf("%s: no `@media (prefers-color-scheme: dark)` block — dark mode moved or was renamed; "+
			"fix this parser rather than leaving the palette parity check silently dead", path)
	}
	rules := svgRules(css)
	if len(rules) == 0 {
		t.Fatalf("%s: found no `svg …` rules — the SVG styling moved or the extractor broke; fix this "+
			"parser rather than leaving the palette parity check silently dead", path)
	}
	return palette{root: root, dark: dark, svg: rules}
}

// TestPaletteInSync pins the three stylesheets' palette to one value. The six
// treatment hues are defined once per file and consumed by ~40 `svg .<treatment>`
// rules per file, so a single drifted hex in one template silently repaints every
// diagram in that one artifact type — a card and a spec doc showing the same
// fragment in different reds, with nothing failing anywhere.
//
// Only the three shared regions are compared: `:root`, the dark-mode override,
// and the `svg …` rules. The prose CSS around them is meant to differ.
func TestPaletteInSync(t *testing.T) {
	base := shapeKitStylesheets[0]
	want := readPalette(t, base)
	for _, path := range shapeKitStylesheets[1:] {
		got := readPalette(t, path)
		if got.root != want.root {
			t.Errorf("`:root` custom properties differ between %s and %s:\n%s",
				base, path, diffReport(base, want.root, path, got.root))
		}
		if got.dark != want.dark {
			t.Errorf("dark-mode `@media (prefers-color-scheme: dark)` block differs between %s and %s:\n%s",
				base, path, diffReport(base, want.dark, path, got.dark))
		}
		if strings.Join(got.svg, "\n") != strings.Join(want.svg, "\n") {
			t.Errorf("`svg …` rule set differs between %s and %s:\n%s", base, path,
				diffReport(base, strings.Join(want.svg, "\n"), path, strings.Join(got.svg, "\n")))
		}
	}
}

// paletteReferencePath is the swatch sheet zeus is told to LOOK at whenever a
// treatment call is not obvious — the one palette artifact meant for human eyes
// rather than for a renderer.
//
// Spelled as a literal rather than reached through a jobs.* helper because,
// unlike hermes-contract.md / lint_svg.py / diagram-card.html, it has no helper:
// nothing in the Go code opens it, only the two SKILL.md files point at it. That
// is precisely why it needs pinning here — a rename breaks nothing the compiler
// can see and leaves both skills sending agents to a dead path.
const paletteReferencePath = ".claude/skills/aim/assets/palette-reference.html"

// paletteHues are the five hue custom properties every palette copy must agree
// on. `grey` is deliberately absent: it has no hue of its own — it paints from
// `--muted` and signals through a dotted border, which is what makes it the one
// treatment that survives greyscale.
var paletteHues = []string{"red", "yellow", "green", "blue", "purple"}

// cssVarDeclRe matches a `--name: #hex` custom-property declaration. Only hex
// values are read: a `var(--x)` indirection is not a palette definition, and a
// non-colour property is not a hue.
var cssVarDeclRe = regexp.MustCompile(`--([a-zA-Z][a-zA-Z0-9-]*)\s*:\s*(#[0-9a-fA-F]{3,8})`)

// cssVars reads every `--name: #hex` declaration in a block into a map. Keyed by
// NAME, never by position, so the order the hues are declared in is free — the
// swatch sheet lists them in reading order and the templates list them in
// severity order, and neither is wrong.
func cssVars(block string) map[string]string {
	out := map[string]string{}
	for _, m := range cssVarDeclRe.FindAllStringSubmatch(block, -1) {
		out[m[1]] = strings.ToLower(m[2])
	}
	return out
}

// paletteBlockVars extracts one named block from a stylesheet and reads its
// custom properties. Fails loudly when the block is missing rather than
// returning an empty map, which would make every comparison below pass vacuously.
func paletteBlockVars(t *testing.T, path, css, opener, what string) map[string]string {
	t.Helper()
	block, ok := cssBraceBlock(css, opener)
	if !ok {
		t.Fatalf("%s: no %s block (looked for %q) — the palette moved or was renamed; fix this parser "+
			"rather than leaving the swatch-sheet parity check silently dead", path, what, opener)
	}
	vars := cssVars(block)
	if len(vars) == 0 {
		t.Fatalf("%s: the %s block declares no `--name: #hex` custom properties — fix this parser rather "+
			"than leaving the swatch-sheet parity check silently dead", path, what)
	}
	return vars
}

// TestPaletteReferenceInSync pins the FOURTH copy of the palette.
//
// palette-reference.html is not a template — it renders no fragment and carries
// no `svg …` rules — so it cannot join shapeKitStylesheets and TestPaletteInSync's
// whole-block comparison, which is exactly how it slipped through: it defines the
// same ten hex values in a different shape (its own `:root`, plus `.lt`/`.dk`
// mirror vars that paint its forced side-by-side light/dark panels, since a
// single page cannot render both modes from one `prefers-color-scheme`).
//
// A drift here is the worst kind in the whole palette, because this is the sheet
// zeus is told to LOOK at when a call is not obvious: the swatch would show one
// red and every rendered diagram would use another, and the reference would be
// actively lying rather than merely stale.
//
// Compared per HUE NAME against diagram-card.html, the copy of record, so the
// declaration order in any file is free to change.
func TestPaletteReferenceInSync(t *testing.T) {
	const base = "aim/assets/diagram-card.html"
	ref := strings.TrimPrefix(paletteReferencePath, ".claude/skills/")

	baseCSS := cssCommentRe.ReplaceAllString(styleBlocks(readEmbedded(t, base)), "")
	refCSS := cssCommentRe.ReplaceAllString(styleBlocks(readEmbedded(t, ref)), "")

	for _, mode := range []struct{ name, opener string }{
		{"light", ":root"},
		{"dark", "@media (prefers-color-scheme: dark)"},
	} {
		want := paletteBlockVars(t, base, baseCSS, mode.opener, mode.name+" palette")
		got := paletteBlockVars(t, ref, refCSS, mode.opener, mode.name+" palette")
		for _, hue := range paletteHues {
			w, ok := want[hue]
			if !ok {
				t.Errorf("%s: its %s block defines no `--%s` — that file is the copy of record for the "+
					"palette, so this check has nothing to compare against", base, mode.name, hue)
				continue
			}
			g, ok := got[hue]
			if !ok {
				t.Errorf("%s: its %s block defines no `--%s`, but %s does (%s) — the swatch sheet is "+
					"missing a treatment zeus is told to look up there", ref, mode.name, hue, base, w)
				continue
			}
			if g != w {
				t.Errorf("hue `--%s` disagrees in %s mode: %s says %s, %s says %s — the sheet zeus LOOKS at "+
					"to make a colour call would show a different %s from every rendered diagram",
					hue, mode.name, base, w, ref, g, hue)
			}
		}
	}

	// The `.lt`/`.dk` mirrors are a second copy of the same ten values INSIDE the
	// swatch sheet, and they are the copy that actually paints its chips — the
	// `:root` hues above only reach the small table swatches. A drift between the
	// two halves of one file is just as silent as a drift between two files.
	light := paletteBlockVars(t, ref, refCSS, ":root", "light palette")
	dark := paletteBlockVars(t, ref, refCSS, "@media (prefers-color-scheme: dark)", "dark palette")
	for _, mirror := range []struct {
		name, opener string
		want         map[string]string
	}{
		{"light panel (`.lt`)", ".lt {", light},
		{"dark panel (`.dk`)", ".dk {", dark},
	} {
		got := paletteBlockVars(t, ref, refCSS, mirror.opener, mirror.name)
		// The neutrals ride along: `grey` paints from `--muted` and the panels
		// reproduce the page background/foreground, so a drift there mis-renders
		// the one treatment that has no hue.
		for _, name := range append(append([]string{}, paletteHues...), "bg", "fg", "muted") {
			w, ok := mirror.want[name]
			if !ok {
				continue // not every neutral is mirrored; only compare what both define
			}
			g, ok := got["p-"+name]
			if !ok {
				t.Errorf("%s: the %s block defines no `--p-%s`, so that panel paints from an undefined "+
					"variable — its chips fall back to the browser's initial value, not to %s", ref, mirror.name, name, w)
				continue
			}
			if g != w {
				t.Errorf("%s: `--p-%s` in the %s block is %s but the file's own palette says %s — the two "+
					"halves of one page disagree, so the swatch and the table below it show different colours",
					ref, name, mirror.name, g, w)
			}
		}
	}
}

// TestPaletteReferenceReachable pins the swatch sheet's existence and its two
// pointers, the same coupling TestCrewPathsPinnedToEmbeddedTree pins for the
// three assets that DO have Go path helpers.
//
// Without it, renaming or deleting the file is green everywhere: no Go code
// opens it, so the compiler sees nothing, and both SKILL.md files keep telling
// agents to LOOK at a path that no longer resolves. A pointer to nothing is
// worse than no pointer — it reads as an instruction that was followed.
func TestPaletteReferenceReachable(t *testing.T) {
	rel := strings.TrimPrefix(paletteReferencePath, ".claude/skills/")
	if _, err := fs.ReadFile(jobs.SkillsFS, rel); err != nil {
		t.Fatalf("jobs.SkillsFS missing %q (from %s): %v — installSkills materializes only what is "+
			"embedded, so a cell would have no swatch sheet at the path both skills name",
			rel, paletteReferencePath, err)
	}
	for _, skill := range []string{"aim", "info"} {
		src := readEmbedded(t, skill+"/SKILL.md")
		if !strings.Contains(src, paletteReferencePath) {
			t.Errorf(".claude/skills/%s/SKILL.md no longer references %q — that is the artifact zeus is "+
				"told to LOOK at when a treatment call is not obvious, and this skill is the only thing "+
				"that points at it", skill, paletteReferencePath)
		}
	}
}

// mdSection returns the body of the `## <name>` section of a markdown file: from
// the heading line through the line before the next `## ` heading (or EOF).
// Anchored on a heading at the START of a line, so the many inline `## Colour
// Semantics` cross-references in the contract's prose cannot be mistaken for the
// section itself.
func mdSection(src, name string) (string, bool) {
	heading := "\n## " + name + "\n"
	i := strings.Index(src, heading)
	if i < 0 {
		return "", false
	}
	body := src[i+len(heading):]
	if j := strings.Index(body, "\n## "); j >= 0 {
		body = body[:j]
	}
	return body, true
}

// mdSubsectionRe matches a `### <tok>` heading whose token is exactly tok — the
// headings continue past the token (`### grey — missing, absent, removed`), so
// the trailing character class stands in for the lookahead RE2 does not have.
func mdSubsectionRe(tok string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^### ` + regexp.QuoteMeta(tok) + `($|[^0-9A-Za-z_-])`)
}

// pyStrMapRowRe reads one `"grey": "∅",` row of a python str→str dict.
var pyStrMapRowRe = regexp.MustCompile(`"([A-Za-z][A-Za-z0-9_-]*)"\s*:\s*"([^"]*)"`)

// parseTreatments pulls the treatment vocabulary out of lint_svg.py. It prefers
// `TREATMENT_GLYPHS` (a token→glyph map, which lets the caller check the glyphs
// too) and falls back to `STATUS_CLASSES` (a bare set, glyphs unknown). Which one
// it found is returned so failures can name it.
//
// Deliberately parsed rather than hardcoded: the question here is whether the
// linter's treatments are DOCUMENTED, so the linter has to be the side that
// speaks first. Vacuity is guarded by failing loudly when neither table parses.
func parseTreatments(t *testing.T, lint string) (glyphs map[string]string, source string) {
	t.Helper()
	if body, ok := pyBraceBody(lint, "TREATMENT_GLYPHS"); ok {
		out := map[string]string{}
		for _, m := range pyStrMapRowRe.FindAllStringSubmatch(body, -1) {
			out[m[1]] = m[2]
		}
		if len(out) > 0 {
			return out, "TREATMENT_GLYPHS"
		}
		t.Fatal("aim/assets/lint_svg.py: `TREATMENT_GLYPHS = {…}` parsed to zero rows — the " +
			"`\"treatment\": \"glyph\"` row format changed; fix this parser rather than leaving the " +
			"treatment documentation check silently dead")
	}
	set, ok := parsePySet(lint, "STATUS_CLASSES")
	if !ok {
		t.Fatal("aim/assets/lint_svg.py: found neither `TREATMENT_GLYPHS` nor a parseable " +
			"`STATUS_CLASSES` — the treatment vocabulary was renamed or reshaped; fix this parser " +
			"rather than leaving the treatment documentation check silently dead")
	}
	out := map[string]string{}
	for tok := range set {
		out[tok] = ""
	}
	return out, "STATUS_CLASSES"
}

// contractTreatmentGlyphs reads the `| class | means | glyph |` table out of the
// contract's Colour Semantics section. Scoped to that section so the Parts Index
// (whose header also says "class") can never be picked up by mistake.
func contractTreatmentGlyphs(t *testing.T, section string) map[string]string {
	t.Helper()
	rows := mdTableRows(section, "class", "glyph")
	if len(rows) == 0 {
		t.Fatal("aim/assets/hermes-contract.md: the `## Colour Semantics` section has no markdown table " +
			"whose header names both \"class\" and \"glyph\" — that table is where every treatment's glyph " +
			"is published; fix this parser rather than leaving the glyph check silently dead")
	}
	out := map[string]string{}
	for _, row := range rows {
		if len(row) < 3 {
			t.Fatalf("aim/assets/hermes-contract.md: Colour Semantics row %q has %d columns, want at least 3 "+
				"(class, means, glyph) — fix this parser", strings.Join(row, " | "), len(row))
		}
		out[mdToken(row[0])] = mdToken(row[2])
	}
	return out
}

// TestContractDefinesEveryTreatment closes the loop between "the linter accepts
// this class" and "an agent can find out what it claims". A treatment is a
// factual assertion about the thing it is drawn on; a treatment with no written
// meaning is a class zeus will assign by vibe, which is the exact failure the
// Colour Semantics section exists to prevent.
//
// For every treatment the LINTER recognises, the contract must carry a
// `### <treatment>` subsection with real prose under it, and a glyph — the glyph
// being what survives greyscale, a printout, and red–green CVD, so a documented
// treatment without one is undocumented in the way that matters.
func TestContractDefinesEveryTreatment(t *testing.T) {
	lint := readEmbedded(t, "aim/assets/lint_svg.py")
	treatments, source := parseTreatments(t, lint)
	const wantTreatments = 6
	if len(treatments) != wantTreatments {
		t.Errorf("aim/assets/lint_svg.py: %s names %d treatments (%v), want %d — the palette is "+
			"grey/red/yellow/green/blue/purple", source, len(treatments), sortedKeys(treatments), wantTreatments)
	}

	allowed, ok := parsePySet(lint, "ALLOWED_CLASSES")
	if !ok {
		t.Fatal("aim/assets/lint_svg.py: could not parse ALLOWED_CLASSES (found no tokens) — the set was " +
			"renamed or reshaped; fix this parser rather than leaving this check silently dead")
	}
	for _, tok := range sortedKeys(treatments) {
		if !allowed[tok] {
			t.Errorf("treatment %q is in %s but not in ALLOWED_CLASSES in aim/assets/lint_svg.py — the "+
				"linter would reject every fragment that uses it", tok, source)
		}
	}

	contract := readEmbedded(t, "aim/assets/hermes-contract.md")
	section, ok := mdSection(contract, "Colour Semantics")
	if !ok {
		t.Fatal("aim/assets/hermes-contract.md: no `## Colour Semantics` section — that section is where " +
			"every treatment's meaning, glyph, bans and decision procedure live, and both SKILL.md files " +
			"point agents at it by name")
	}
	tableGlyphs := contractTreatmentGlyphs(t, section)

	for _, tok := range sortedKeys(treatments) {
		loc := mdSubsectionRe(tok).FindStringIndex(section)
		if loc == nil {
			t.Errorf("treatment %q (from %s in aim/assets/lint_svg.py) has no `### %s` subsection in "+
				"hermes-contract.md's Colour Semantics — the linter accepts a class whose claim is written "+
				"down nowhere, so hermes and zeus can only guess at what it asserts", tok, source, tok)
			continue
		}
		// A heading with nothing under it documents nothing. Bound the body at the
		// next `### ` (or the end of the section) and require real prose.
		body := section[loc[1]:]
		if j := strings.Index(body, "\n### "); j >= 0 {
			body = body[:j]
		}
		const minBody = 80
		if len(strings.TrimSpace(body)) < minBody {
			t.Errorf("treatment %q: the `### %s` subsection in hermes-contract.md is %d characters of prose, "+
				"under the %d-character floor — a heading with no claim under it is not a definition",
				tok, tok, len(strings.TrimSpace(body)), minBody)
		}
		glyph, present := tableGlyphs[tok]
		if !present {
			t.Errorf("treatment %q has no row in hermes-contract.md's Colour Semantics `| class | means | "+
				"glyph |` table — that table is the only place its glyph is published, and the glyph is what "+
				"the treatment reads as in greyscale, in print, and under red–green CVD", tok)
			continue
		}
		if glyph == "" || glyph == "—" || glyph == "-" {
			t.Errorf("treatment %q states no glyph in hermes-contract.md's Colour Semantics table (glyph cell "+
				"is %q) — every treated element must carry one, so a treatment without one cannot be used at all",
				tok, glyph)
		}
		if want := treatments[tok]; want != "" && want != glyph {
			t.Errorf("glyph for treatment %q disagrees: %s in aim/assets/lint_svg.py says %q, "+
				"hermes-contract.md's Colour Semantics table says %q — the linter would reject the very glyph "+
				"the contract told hermes to draw", tok, source, want, glyph)
		}
	}
}

// sortedKeys gives every failure message above a stable order, so a red run reads
// the same twice and a diff of two runs means something.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// parseBanListClasses reads the class allowlist the contract publishes in prose:
// the "any class outside this catalog — N tokens" bullet in the Ban List, whose
// indented sub-bullets group every sanctioned token by the element it must sit
// on. Returns the tokens and the count the prose CLAIMS, which is itself a fact
// that can go stale.
//
// Anchored on the bullet's own wording and terminated by the next unindented
// `- ` bullet, so re-grouping the tokens, re-wrapping the lines, or adding an
// element family all still parse. Fails loudly on a missing bullet or an empty
// parse rather than passing vacuously.
func parseBanListClasses(t *testing.T, contract string) (tokens map[string]bool, declared int) {
	t.Helper()
	section, ok := mdSection(contract, "Ban List")
	if !ok {
		t.Fatal("aim/assets/hermes-contract.md: no `## Ban List` section — that section publishes the " +
			"sanctioned class vocabulary and says to edit it together with lint_svg.py; fix this parser " +
			"rather than leaving the allowlist parity check silently dead")
	}
	const marker = "any class outside this catalog"
	lines := strings.Split(section, "\n")
	start := -1
	for i, line := range lines {
		if strings.Contains(line, marker) {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("aim/assets/hermes-contract.md: the Ban List has no %q bullet — that bullet and its "+
			"sub-bullets ARE the published allowlist; fix this parser rather than leaving the allowlist "+
			"parity check silently dead", marker)
	}
	if m := regexp.MustCompile(`(\d+)\s+tokens`).FindStringSubmatch(lines[start]); m != nil {
		declared, _ = strconv.Atoi(m[1])
	}
	var region strings.Builder
	region.WriteString(lines[start])
	region.WriteString("\n")
	for _, line := range lines[start+1:] {
		if strings.HasPrefix(line, "- ") { // the next top-level ban; the list ended
			break
		}
		region.WriteString(line)
		region.WriteString("\n")
	}
	tokens = map[string]bool{}
	for _, m := range backtickTokenRe.FindAllStringSubmatch(region.String(), -1) {
		tokens[m[1]] = true
	}
	if len(tokens) == 0 {
		t.Fatalf("aim/assets/hermes-contract.md: parsed zero class tokens out of the %q bullet (%q) — fix "+
			"this parser rather than leaving the allowlist parity check silently dead", marker, region.String())
	}
	return tokens, declared
}

// TestAllowlistMatchesContract enforces the instruction the contract gives in its
// own words — "enforced by lint_svg.py, which carries the second copy of this
// vocabulary (edit both together)" — which until now was an honour system.
//
// Both directions matter and they fail differently. A token in the contract but
// not the linter is markup hermes is invited to write and the linter then
// rejects, mid-fan-out, after the fragment is drawn. A token in the linter but
// not the contract is a class that lints clean and no agent knows exists — dead
// vocabulary that nothing will ever emit.
func TestAllowlistMatchesContract(t *testing.T) {
	contract := readEmbedded(t, "aim/assets/hermes-contract.md")
	lint := readEmbedded(t, "aim/assets/lint_svg.py")

	published, declared := parseBanListClasses(t, contract)
	allowed, ok := parsePySet(lint, "ALLOWED_CLASSES")
	if !ok {
		t.Fatal("aim/assets/lint_svg.py: could not parse ALLOWED_CLASSES (found no tokens) — the set was " +
			"renamed or reshaped; fix this parser rather than leaving the allowlist parity check silently dead")
	}

	var missingFromLinter, missingFromContract []string
	for _, tok := range sortedKeys(published) {
		if !allowed[tok] {
			missingFromLinter = append(missingFromLinter, tok)
		}
	}
	for _, tok := range sortedKeys(allowed) {
		if !published[tok] {
			missingFromContract = append(missingFromContract, tok)
		}
	}
	if len(missingFromLinter) > 0 {
		t.Errorf("hermes-contract.md's Ban List publishes %v, which ALLOWED_CLASSES in lint_svg.py does not "+
			"accept — hermes is told it may use these and the linter rejects the fragment that does",
			missingFromLinter)
	}
	if len(missingFromContract) > 0 {
		t.Errorf("ALLOWED_CLASSES in lint_svg.py accepts %v, which hermes-contract.md's Ban List never "+
			"publishes — dead vocabulary no agent knows exists", missingFromContract)
	}

	if len(published) != sanctionedClassCount {
		t.Errorf("hermes-contract.md's Ban List publishes %d class tokens, want %d — if the vocabulary "+
			"genuinely changed, bump sanctionedClassCount here as the last step of the five-file edit "+
			"(linter, contract, three stylesheets)", len(published), sanctionedClassCount)
	}
	if len(allowed) != sanctionedClassCount {
		t.Errorf("ALLOWED_CLASSES in lint_svg.py has %d tokens, want %d — see sanctionedClassCount",
			len(allowed), sanctionedClassCount)
	}
	if declared == 0 {
		t.Error("hermes-contract.md's Ban List no longer states a token COUNT (\"— N tokens\") in its " +
			"class bullet — that number is what tells a human reader the list is complete")
	} else if declared != len(published) {
		t.Errorf("hermes-contract.md's Ban List says %d tokens but lists %d — the prose count went stale "+
			"against its own list", declared, len(published))
	}
}
