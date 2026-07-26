package web

import (
	"io/fs"
	"strings"
	"testing"
)

// boardsurface_test.go — the board card's question surface is a COUNT CHIP,
// and nothing else. The answerable panel lives one click into the job (the
// steer modal and the job editor).
//
// This is a string test over app.js because that is where the rule lives:
// there is no JS test harness in this repo, and the failure mode is silent —
// re-adding docQuestionsHTML to cardHTML restores a panel that renders fine,
// passes every Go test, and quietly re-buries a column behind one job's
// eight open questions (the exact regression this split fixed). The
// assertions are deliberately narrow: they pin which function the CARD
// calls, not how any of them are written.

// readAppJS returns the embedded app.js — the embedded copy, since that is
// what a built binary actually serves.
func readAppJS(t *testing.T) string {
	t.Helper()
	data, err := fs.ReadFile(webAssets, "static/app.js")
	if err != nil {
		t.Fatalf("read embedded static/app.js: %v", err)
	}
	return string(data)
}

// cardHTMLBody slices out cardHTML's own source, so a mention of
// docQuestionsHTML anywhere else in the file (the modals legitimately call
// it) can't satisfy or trip these checks.
func cardHTMLBody(t *testing.T, src string) string {
	t.Helper()
	const marker = "function cardHTML("
	start := strings.Index(src, marker)
	if start < 0 {
		t.Fatal("static/app.js has no cardHTML() — renamed? update this guard rather than deleting it")
	}
	depth := 0
	for i := strings.Index(src[start:], "{") + start; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start : i+1]
			}
		}
	}
	t.Fatal("static/app.js: cardHTML() body never closes — the brace scan is broken, fix this guard")
	return ""
}

func TestBoardCardRendersQuestionsAsChipOnly(t *testing.T) {
	src := readAppJS(t)
	card := cardHTMLBody(t, src)

	if !strings.Contains(card, "docQuestionsChipHTML(j)") {
		t.Error("cardHTML no longer renders docQuestionsChipHTML — the board's only question surface is that count chip")
	}
	if strings.Contains(card, "docQuestionsHTML(") {
		t.Error("cardHTML renders the full docQuestionsHTML panel again — questions belong in the drilldown " +
			"(fillSteerContext / fillEditorDocq); the card gets docQuestionsChipHTML's count and nothing more")
	}
	// the chip must not be a .chip: that class is the artifact-overlay click
	// target, so a chip-classed span would open an artifact with an undefined
	// url instead of falling through to the card's open-the-drilldown handler
	chip := funcBody(t, src, "docQuestionsChipHTML")
	if !strings.Contains(chip, `class="dq-chip`) {
		t.Error(`docQuestionsChipHTML no longer emits class="dq-chip" — style.css keys the chip's paint off it`)
	}
	if strings.Contains(chip, `class="chip`) {
		t.Error("docQuestionsChipHTML emits class=\"chip\" — that class is the artifact-overlay click target, " +
			"so the chip would try to open an artifact instead of the job's drilldown")
	}
	// open questions only: an answered question is a decision, not a standing
	// ask, and a settled job's questions have nobody waiting on them
	if !strings.Contains(chip, "!q.answer") {
		t.Error("docQuestionsChipHTML no longer filters to unanswered questions — the chip counts open asks only")
	}
	if !strings.Contains(chip, "settledJob(j)") {
		t.Error("docQuestionsChipHTML no longer skips settled jobs — a done/archived job gets no chip")
	}
}

// TestBoardHasNoDocQuestionInputs pins the consequence the draft-preservation
// machinery now assumes: #board renders no .dq-input at all, so
// DQ_INPUT_SELECTOR reaching for one would be dead weight pointing at a
// surface that no longer exists (and would mask a card panel creeping back).
func TestBoardHasNoDocQuestionInputs(t *testing.T) {
	src := readAppJS(t)
	if strings.Contains(src, "#board .dq-input") {
		t.Error("app.js still selects `#board .dq-input` — the questions panel (and its inputs) is modal-only now")
	}
	if !strings.Contains(src, "#steer-docq .dq-input") || !strings.Contains(src, "#editor-docq .dq-input") {
		t.Error("app.js no longer selects the modal .dq-inputs — the drilldown panels are where answers get typed, " +
			"and their in-progress text still needs snapshot/restore across a rebuild")
	}
}

// funcBody returns one named function's source, same brace scan as
// cardHTMLBody. Split out so the guards above can look inside a second
// function without duplicating the walk.
func funcBody(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "function "+name+"(")
	if start < 0 {
		t.Fatalf("static/app.js has no %s() — renamed? update this guard rather than deleting it", name)
	}
	depth := 0
	for i := strings.Index(src[start:], "{") + start; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start : i+1]
			}
		}
	}
	t.Fatalf("static/app.js: %s() body never closes — the brace scan is broken, fix this guard", name)
	return ""
}
