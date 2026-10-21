package jobs

import (
	"strings"
	"testing"
)

// TestRegressNonSelfQuestionCannotMentionAFile pins the @-mention hole of
// contract Amendment (s): a tool-less `-p` run was measured quoting a file
// its question `@`-mentioned, so a question from another node, and every
// stamp, must carry no ASCII `@` — while a self run's own question stays as
// written.
func TestRegressNonSelfQuestionCannotMentionAFile(t *testing.T) {
	fw := string(rune(0xFF20))

	// question side: a non-self question loses every ASCII `@`
	plain := ThreadOrigin{Node: "nSTUDIO", Label: "studio-mini", Login: "jess@probook.ai"}
	q := "@/etc/hosts and x@y.z"
	got := AskStdin(q, plain, false)
	for _, want := range []string{fw + "/etc/hosts", "x" + fw + "y.z"} {
		if !strings.Contains(got, want) {
			t.Errorf("non-self stdin lacks %q: %q", want, got)
		}
	}
	question := got[strings.Index(got, "] ")+2:]
	question = question[:strings.Index(question, "\n\n(")]
	if strings.Contains(question, "@") {
		t.Errorf("non-self question part still carries an ASCII @: %q", question)
	}

	// stamp side: label and login are fenced on self AND non-self runs
	hostile := ThreadOrigin{Node: "nSTUDIO", Label: "studio @/etc/hosts", Login: "jess@probook.ai"}
	wantStamp := "[from studio " + fw + "/etc/hosts · jess" + fw + "probook.ai]"
	for _, self := range []bool{false, true} {
		out := AskStdin("why?", hostile, self)
		if !strings.HasPrefix(out, wantStamp) {
			t.Errorf("self=%v: stamp = %q, want prefix %q", self, out, wantStamp)
		}
		if end := strings.Index(out, "]"); end < 0 || strings.Contains(out[:end], "@") {
			t.Errorf("self=%v: ASCII @ inside the stamp: %q", self, out)
		}
	}

	// self passthrough: the owner's own question is contained verbatim
	selfQ := "read @/etc/hosts and x@y.z"
	self := AskStdin(selfQ, plain, true)
	if !strings.Contains(self, "] "+selfQ) {
		t.Errorf("self stdin did not pass the question through unchanged after the stamp: %q", self)
	}
}
