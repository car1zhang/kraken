package api

import (
	"os"
	"strings"
	"testing"
)

// TestNothingWakesACellForAQuestion pins the operator's decision on job 629:
// when a question row is written or answered, kraken types NOTHING into the
// cell's pane — carl answers on the board and then prompts the pane himself.
// The two routes that touch question rows are the cell-side verbs' proxy
// (questions.go) and the board's answer submit (webAnswerDocQuestion in
// actions.go); neither may reach a pane-typing primitive. The check is an
// absence assertion over the source, the TestOnlyDreamNamesTheFeedbackWriteVerb
// shape: a present-token test cannot say "this never happens".
func TestNothingWakesACellForAQuestion(t *testing.T) {
	paneWriters := []string{"SendToPeer", "SendMessage(", "send-keys", "SendToShell", "sendDigit("}
	check := func(label, src string) {
		for _, w := range paneWriters {
			if strings.Contains(src, w) {
				t.Errorf("%s reaches a pane-typing primitive (%q) — a question row change must never wake the cell; carl steers it himself", label, w)
			}
		}
	}
	qsrc, err := os.ReadFile("questions.go")
	if err != nil {
		t.Fatalf("read questions.go: %v", err)
	}
	check("questions.go", string(qsrc))

	asrc, err := os.ReadFile("actions.go")
	if err != nil {
		t.Fatalf("read actions.go: %v", err)
	}
	const fn = "func webAnswerDocQuestion("
	start := strings.Index(string(asrc), fn)
	if start < 0 {
		t.Fatalf("actions.go no longer declares %s", fn)
	}
	body := string(asrc)[start+len(fn):]
	if end := strings.Index(body, "\nfunc "); end >= 0 {
		body = body[:end]
	}
	check("webAnswerDocQuestion", body)
}
