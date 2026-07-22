package util

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogEventAppendAndRead(t *testing.T) {
	t.Setenv("KRAKEN_LOGS_DIR", t.TempDir())
	LogEvent("daemon", "info", "daemon-start", "daemon up (pid 1)")
	LogEvent("web", "error", "action", "queue x: boom")
	files := ListLogFiles()
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %+v", files)
	}
	entries, err := ReadLogFile(files[0].Name)
	if err != nil || len(entries) != 2 {
		t.Fatalf("read: err=%v entries=%+v", err, entries)
	}
	if entries[0].Event != "daemon-start" || entries[0].Level != "info" || entries[0].Src != "daemon" {
		t.Fatalf("first entry mangled: %+v", entries[0])
	}
	if entries[1].Level != "error" || !strings.Contains(entries[1].Msg, "boom") {
		t.Fatalf("second entry mangled: %+v", entries[1])
	}
	if entries[0].TS <= 0 {
		t.Fatalf("ts missing: %+v", entries[0])
	}
}

func TestLogRotationBySize(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KRAKEN_LOGS_DIR", dir)
	// a file already at the cap forces the next append into a fresh file
	old := filepath.Join(dir, "20200101-000000.jsonl")
	if err := os.WriteFile(old, make([]byte, logMaxBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	LogEvent("cli", "info", "start", "started x")
	files := ListLogFiles()
	if len(files) != 2 {
		t.Fatalf("want rotation into a 2nd file, got %+v", files)
	}
	if files[0].Name != "20200101-000000.jsonl" || files[1].Name <= files[0].Name {
		t.Fatalf("files not in chronological name order: %+v", files)
	}
	// and while under the cap, appends reuse the newest file
	LogEvent("cli", "info", "start", "started y")
	if got := ListLogFiles(); len(got) != 2 {
		t.Fatalf("append under cap must not rotate: %+v", got)
	}
}

func TestReadLogFileRejectsNonNames(t *testing.T) {
	t.Setenv("KRAKEN_LOGS_DIR", t.TempDir())
	for _, name := range []string{"../../etc/passwd", "foo.jsonl", "20200101-000000.jsonl.bak", ""} {
		if _, err := ReadLogFile(name); err == nil {
			t.Errorf("name %q: expected error", name)
		}
	}
}

func TestReadLogFileSkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KRAKEN_LOGS_DIR", dir)
	name := newLogFileName(time.Now())
	content := `{"ts":1,"src":"cli","level":"info","event":"a","msg":"ok"}` + "\n" +
		"garbage not json\n" +
		`{"ts":2,"src":"cli","level":"info","event":"b","msg":"ok2"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadLogFile(name)
	if err != nil || len(entries) != 2 || entries[1].Event != "b" {
		t.Fatalf("err=%v entries=%+v", err, entries)
	}
}
