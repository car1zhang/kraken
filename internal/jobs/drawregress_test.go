package jobs

import (
	"strings"
	"testing"
)

// drawregress_test.go pins two defects fixed in mapdraw.go after review: a
// text made only of U+FEFF passed the server's text rule while the board
// refuses it, and a failed kv read made SetMapDrawings replace the saved row.

// drawRegressBlankTexts are texts made only of White_Space code points and
// U+FEFF, each of which the drawing rule must refuse. They are built from
// rune values so the file holds no invisible characters.
var drawRegressBlankTexts = []string{
	string([]rune{0xFEFF}),
	string([]rune{0x20, 0xFEFF, 0x0A}),
	string([]rune{0x85}),
	string([]rune{0xA0, 0x2028, 0x3000}),
}

func TestDrawRegressBlankTextRefused(t *testing.T) {
	st := useTestStore(t)
	for _, text := range drawRegressBlankTexts {
		if err := st.DeleteKV(mapDrawingsKey); err != nil {
			t.Fatalf("clear: %v", err)
		}
		d := MapDrawing{Kind: "text", Pts: []float64{1, 2}, Size: 12, Text: text}
		if err := SetMapDrawings(map[string]MapDrawing{"t": d}, nil); err == nil {
			t.Errorf("SetMapDrawings accepted the blank text %q", text)
		}
		if got := MapDrawings(); len(got) != 0 {
			t.Errorf("after the blank text %q, MapDrawings = %v, want none", text, got)
		}
	}
}

// TestDrawRegressReadErrorRefuses makes the kv row unreadable while leaving
// it writable: kv becomes a view over the real table whose value column
// raises a SQL error on read (json() of a string that is not JSON), with an
// INSTEAD OF trigger passing writes through. A save must then refuse and
// leave the saved row as it was.
func TestDrawRegressReadErrorRefuses(t *testing.T) {
	st := useTestStore(t)
	saved := `{"a":{"kind":"line","pts":[0,0,1,1],"size":2}}`
	if err := st.SetKV(mapDrawingsKey, saved); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, q := range []string{
		`ALTER TABLE kv RENAME TO kv_real`,
		`CREATE VIEW kv AS SELECT key, json(value || 'x') AS value FROM kv_real`,
		`CREATE TRIGGER kv_write INSTEAD OF INSERT ON kv BEGIN
			INSERT OR REPLACE INTO kv_real (key, value) VALUES (NEW.key, NEW.value);
		END`,
	} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := st.GetKV(mapDrawingsKey); err == nil {
		t.Fatal("the view did not make the row unreadable")
	}
	d := MapDrawing{Kind: "line", Pts: []float64{5, 5, 6, 6}, Size: 2}
	err := SetMapDrawings(map[string]MapDrawing{"b": d}, nil)
	if err == nil {
		t.Fatal("SetMapDrawings succeeded although the row could not be read")
	}
	// SQLite refuses an upsert into a view, so here a write attempt fails
	// too and the row survives either way; what tells a refusal from a
	// replace that happened to fail is that the error is the read's.
	if strings.Contains(err.Error(), "set kv") {
		t.Errorf("SetMapDrawings tried to write after a failed read: %v", err)
	}
	var got string
	if err := st.DB().QueryRow(`SELECT value FROM kv_real WHERE key = ?`, mapDrawingsKey).Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != saved {
		t.Errorf("the saved row changed to %s, want %s", got, saved)
	}
}
