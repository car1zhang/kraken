package core

// redactgoogle_edge_test.go — athena-edge's BLIND tests for slice C of
// dream-2026-10-30 (the transcript redactor covers Google API key shapes):
// length floors, character classes and word boundaries of the AIza and AQ.
// arms. Authored from 50-contract.md alone. Every package-level identifier
// is prefixed edgeRG. Key bodies are INVENTED filler, never real keys.

import (
	"strings"
	"testing"
)

// edgeRGBody returns n characters cycling through the arm's class.
func edgeRGBody(n int) string {
	const class = "Zq7_x-K3mPw9Lr2Tn5Yb8Hc4Vd6Gf1Js0"
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteByte(class[i%len(class)])
	}
	return b.String()
}

func TestEdgeRedactGoogleShapes(t *testing.T) {
	aiza35 := "AIza" + edgeRGBody(35)
	aiza34 := "AIza" + edgeRGBody(34)
	aq30 := "AQ." + edgeRGBody(30)
	aq29 := "AQ." + edgeRGBody(29)
	aq80 := "AQ." + edgeRGBody(80)
	// "first mineRedactKeep (5) runes plus …[redacted]"
	aizaRed := aiza35[:5] + "…[redacted]"
	aqRed := aq30[:5] + "…[redacted]"

	cases := []struct {
		name, in, want string
	}{
		// AIza arm
		{"AIza + 35 alone", aiza35, aizaRed},
		{"AIza + 35 in prose", "key " + aiza35 + " end", "key " + aizaRed + " end"},
		{"AIza + 35 after =", "val=" + aiza35, "val=" + aizaRed},
		{"AIza + 35 in double quotes", `"` + aiza35 + `"`, `"` + aizaRed + `"`},
		{"AIza + 35 after a hyphen boundary", "x-" + aiza35, "x-" + aizaRed},
		{"AIza + 35 in parens", "(" + aiza35 + ")", "(" + aizaRed + ")"},
		{"AIza + 34 is too short", aiza34, aiza34},
		{"AIza + 34 then a space", aiza34 + " tail", aiza34 + " tail"},
		{"xAIza + 35 is not at a word boundary", "x" + aiza35, "x" + aiza35},
		{"digit-glued AIza + 35", "9" + aiza35, "9" + aiza35},
		{"underscore-glued AIza + 35", "_" + aiza35, "_" + aiza35},
		{"aiza lowercase prefix", "aiza" + edgeRGBody(35), "aiza" + edgeRGBody(35)},
		{"AIza broken by a dot", "AIza" + edgeRGBody(10) + "." + edgeRGBody(24), "AIza" + edgeRGBody(10) + "." + edgeRGBody(24)},
		{"AIza prose", "the AIza prefix", "the AIza prefix"},
		{"AIza bare", "AIza", "AIza"},

		// AQ. arm
		{"AQ. + 30 alone", aq30, aqRed},
		{"AQ. + 30 in prose", "token " + aq30 + " here", "token " + aqRed + " here"},
		{"AQ. + 80 redacted whole", aq80, aq80[:5] + "…[redacted]"},
		{"AQ. + 80 then a space", aq80 + " ok", aq80[:5] + "…[redacted] ok"},
		{"AQ. + 30 after =", "t=" + aq30, "t=" + aqRed},
		{"AQ. + 29 is too short", aq29, aq29},
		{"FAQ. + 30 is not at a word boundary", "F" + aq30, "F" + aq30},
		{"lowercase aq. + 30", "aq." + edgeRGBody(30), "aq." + edgeRGBody(30)},
		{"AQ without the dot", "AQ" + edgeRGBody(30), "AQ" + edgeRGBody(30)},
		{"AQ. broken by a slash", "AQ." + edgeRGBody(12) + "/" + edgeRGBody(20), "AQ." + edgeRGBody(12) + "/" + edgeRGBody(20)},
		{"see FAQ.", "see FAQ.", "see FAQ."},
		{"FAQ. then prose", "read the FAQ. It explains", "read the FAQ. It explains"},
		{"AQ. bare", "AQ.", "AQ."},

		// both in one string
		{"one of each", aiza35 + " and " + aq30, aizaRed + " and " + aqRed},
		{"two AIza keys", aiza35 + "," + aiza35, aizaRed + "," + aizaRed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := redactSecrets(c.in); got != c.want {
				t.Errorf("redactSecrets(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestEdgeRedactGoogleKeepIsFive(t *testing.T) {
	if mineRedactKeep != 5 {
		t.Errorf("mineRedactKeep = %d, want 5", mineRedactKeep)
	}
}

// The redaction never lets any 6-rune fragment of the key body through.
func TestEdgeRedactGoogleNoLeak(t *testing.T) {
	for _, key := range []string{"AIza" + edgeRGBody(35), "AQ." + edgeRGBody(44)} {
		got := redactSecrets("before " + key + " after")
		if strings.Contains(got, key[:6]) {
			t.Errorf("redactSecrets leaked %q in %q", key[:6], got)
		}
		if strings.Contains(got, key[len(key)-6:]) {
			t.Errorf("redactSecrets leaked the key's tail %q in %q", key[len(key)-6:], got)
		}
	}
}
