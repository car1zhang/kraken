package jobs

import (
	"strconv"
	"strings"
	"testing"

	"kraken/internal/store"
)

// mindrun_closebrace_test.go — a mind answer that is a complete JSON object
// except for its outermost closing brace. Written by the lead alongside the
// change (no blind author); identifiers are prefixed `closeBrace`.

const closeBraceWhole = `{"ops":[{"op":"retire","target":7,"quote":"drop that one"}],"excluded":[{"rung":"2 hedge","quote":"maybe {later}","why":"trailing doubt"}],"coverage":{"prompts":[1,2,3]}}`

func closeBraceCut(t *testing.T) string {
	t.Helper()
	if !strings.HasSuffix(closeBraceWhole, "}") {
		t.Fatal("fixture does not end in a brace")
	}
	return closeBraceWhole[:len(closeBraceWhole)-1]
}

func TestCloseBraceAnswerMissingItsFinalBraceParses(t *testing.T) {
	cut := closeBraceCut(t)
	for name, out := range map[string]string{
		"bare":     cut,
		"fenced":   "```json\n" + cut + "\n```",
		"padded":   "\n  " + cut + "  \n",
		"envelope": `{"type":"result","result":` + closeBraceQuote(cut) + `,"usage":{"input_tokens":40,"output_tokens":9}}`,
	} {
		ans, raw, usage, err := ParseMindAnswer(out)
		if err != nil {
			t.Errorf("%s: ParseMindAnswer = %v, want the answer read", name, err)
			continue
		}
		if !ans.Repaired {
			t.Errorf("%s: Repaired = false on an answer that needed its brace supplied", name)
		}
		if len(ans.Ops) != 1 || ans.Ops[0].Op != "retire" || ans.Ops[0].Target != 7 || ans.Ops[0].Quote != "drop that one" {
			t.Errorf("%s: ops = %+v, want the one retire intact", name, ans.Ops)
		}
		if len(ans.Excluded) != 1 || ans.Excluded[0].Quote != "maybe {later}" {
			t.Errorf("%s: excluded = %+v", name, ans.Excluded)
		}
		if len(ans.Coverage) != 1 {
			t.Errorf("%s: coverage = %+v", name, ans.Coverage)
		}
		// raw is the answer as the model wrote it: the supplied brace is
		// never written back into what the run row stores
		if strings.Contains(raw, closeBraceWhole) {
			t.Errorf("%s: raw carries the supplied brace: %q", name, raw)
		}
		if name == "envelope" && (usage.In != 40 || usage.Out != 9) {
			t.Errorf("envelope usage = %+v", usage)
		}
	}
}

// closeBraceQuote renders s as a JSON string literal.
func closeBraceQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

func TestCloseBraceWholeAnswerIsNotMarkedRepaired(t *testing.T) {
	ans, _, _, err := ParseMindAnswer(closeBraceWhole)
	if err != nil {
		t.Fatalf("ParseMindAnswer(whole) = %v", err)
	}
	if ans.Repaired {
		t.Error("Repaired = true on an answer that was complete")
	}
}

func TestCloseBraceSuppliesOnlyTheOutermostBrace(t *testing.T) {
	for name, body := range map[string]string{
		"cut inside a string":          `{"ops":[],"coverage":{"note":"half a sen`,
		"cut after an escaped quote":   `{"ops":[],"note":"she said \"`,
		"array and object both open":   `{"ops":[{"op":"retire","target":7}`,
		"nested object still open":     `{"ops":[],"coverage":{"prompts":[1,2]`,
		"closer with nothing open":     `{"ops":[]}}`,
		"mismatched closer":            `{"ops":[}`,
		"top level is an array":        `[{"op":"retire"}`,
		"nothing open":                 `{"ops":[]}`,
		"empty":                        ``,
		"only prose":                   `no json here`,
		"brace only inside a string":   `"{"`,
		"two top-level values, 2 open": `{"ops":[]} {"ops":[`,
	} {
		if got, ok := mindCloseObject(body); ok {
			t.Errorf("%s: mindCloseObject(%q) = %q, want no repair", name, body, got)
		}
	}
	for name, body := range map[string]string{
		"plain":                         `{"ops":[]`,
		"braces inside strings ignored": `{"ops":[],"note":"uses } and { and ] freely"`,
		"escaped backslash then quote":  `{"ops":[],"note":"ends in a backslash \\"`,
		"multi-byte text":               `{"ops":[],"note":"café — naïve"`,
	} {
		got, ok := mindCloseObject(body)
		if !ok || got != body+"}" {
			t.Errorf("%s: mindCloseObject(%q) = (%q, %v), want one brace appended", name, body, got, ok)
		}
	}
}

func TestCloseBraceStillRefusesWhatABraceCannotFix(t *testing.T) {
	for name, out := range map[string]string{
		// passes the scan (one object open) but is not JSON with a brace added
		"dangling comma": `{"ops":[],`,
		"dangling colon": `{"ops":`,
		"dangling key":   `{"ops":[],"coverage"`,
		// closes into an object, but one that stops short of the contract's
		// three keys: the shape an answer cut off after a complete value has
		"no ops key":            `{"excluded":[],"coverage":{}`,
		"cut after the ops":     `{"ops":[{"op":"retire","target":7,"quote":"drop that one"}]`,
		"cut before coverage":   `{"ops":[],"excluded":[]`,
		"cut before excluded":   `{"ops":[],"coverage":{}`,
		"fenced, cut after ops": "```json\n{\"ops\":[]\n```",
	} {
		if ans, _, _, err := ParseMindAnswer(out); err == nil {
			t.Errorf("%s: ParseMindAnswer(%q) = %+v, want a refusal", name, out, ans)
		}
	}
}

// TestCloseBraceClosedAnswerMayStillOmitLists: an answer that arrives closed
// may still omit `excluded` and `coverage`; the three-key requirement belongs
// to the repair alone.
func TestCloseBraceClosedAnswerMayStillOmitLists(t *testing.T) {
	ans, _, _, err := ParseMindAnswer(`{"ops":[]}`)
	if err != nil {
		t.Fatalf("ParseMindAnswer(closed, ops only) = %v, want it read", err)
	}
	if ans.Repaired || ans.Excluded == nil || ans.Coverage == nil {
		t.Errorf("answer = %+v, want unrepaired with empty lists", ans)
	}
}

func TestCloseBraceRunFinishesAndSaysSo(t *testing.T) {
	st := edgeMREnv(t)
	run := edgeMRDaily(t, st)
	target := edgeMRAddItem(t, st, store.MMItem{Quote: "an old decision"})
	out := `{"ops":[{"op":"retire","target":` + strconv.FormatInt(target, 10) + `,"quote":"drop that one"}],"excluded":[],"coverage":{}`
	fail := func(err error) error { return err }
	// the answer is written onto the run's blob row, which a real run
	// creates when it stores its bundle
	if err := storeMindBlobs(st, run.ID, []byte("{}"), MindManifest{}); err != nil {
		t.Fatalf("storeMindBlobs: %v", err)
	}
	if err := finishMindRun(st, run, out, "test-model", "test-sha", MindManifest{}, fail); err != nil {
		t.Fatalf("finishMindRun = %v, want the run finished", err)
	}
	got := edgeMRGetRun(t, st, run.ID)
	if got.State != "done" || got.Retired != 1 {
		t.Errorf("run = state %q retired %d, want done with one retire", got.State, got.Retired)
	}
	if !strings.Contains(got.Report, "without its final closing brace") {
		t.Errorf("report does not say the answer was closed:\n%s", got.Report)
	}
	blobs, err := st.GetMindRunBlobs(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blobs.Answer != out {
		t.Errorf("stored answer = %q, want the text as it arrived", blobs.Answer)
	}
	if it, err := st.GetMMItem(target); err != nil || it.InvalidatedAt == 0 {
		t.Errorf("retire target = (%+v, %v), want it invalidated", it, err)
	}
}
