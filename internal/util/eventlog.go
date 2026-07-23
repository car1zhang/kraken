package util

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// Event log — kraken's single source-of-truth record of what happened:
// daemon startups/shutdowns and tick events, web-server lifecycle, every
// board action, CLI moves, installs, and errors. One JSONL entry per line,
// appended to the newest file in ~/.kraken/logs; when that file crosses
// logMaxBytes a new timestamp-named file starts. File-granularity is the
// viewer's pagination unit: clients load the newest file, then walk
// backwards one file at a time as the user scrolls.
//
// Writers are many concurrent processes (daemon, k web, CLI one-shots, make
// install), so appends use O_APPEND single-write lines — atomic enough for
// line-oriented logs — and rotation is race-tolerant: two processes rotating
// at once just create two files, both valid, both sorted by name. Logging is
// best-effort everywhere: a failure to log must never break the operation
// being logged.

// logMaxBytes caps one log file (~2-4k entries) — small enough that a whole
// file is one comfortable fetch on mobile, big enough to hold days of
// normal traffic.
const logMaxBytes = 512 * 1024

// LogEntry is one logged event. Level is "info" or "error"; Src names the
// writing surface (daemon | web | cli | install); Event is a short stable
// kebab-case id clients may badge/filter on; Msg is the human line.
type LogEntry struct {
	TS    int64  `json:"ts"` // unix millis
	Src   string `json:"src"`
	Level string `json:"level"`
	Event string `json:"event"`
	Msg   string `json:"msg"`
}

// LogFileInfo describes one log file for the viewer's pagination index.
type LogFileInfo struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // unix millis, matches jobMTime's encoding
}

func LogsDir() string {
	if d := os.Getenv("KRAKEN_LOGS_DIR"); d != "" {
		return d // tests point at a temp dir; daily use never sets this
	}
	return filepath.Join(Home(), ".kraken", "logs")
}

// logFileRe pins valid log file names: purely timestamp-derived, so a
// client-supplied name can be validated against this shape (plus a listing
// check) instead of ever touching path logic.
var logFileRe = regexp.MustCompile(`^\d{8}-\d{6}\.jsonl$`)

func newLogFileName(now time.Time) string {
	return now.Format("20060102-150405") + ".jsonl"
}

// ListLogFiles returns every log file, sorted oldest→newest by name (names
// are timestamp-derived, so lexicographic order IS chronological order).
// Missing dir reads as no files, never an error.
func ListLogFiles() []LogFileInfo {
	entries, err := os.ReadDir(LogsDir())
	if err != nil {
		return nil
	}
	var out []LogFileInfo
	for _, e := range entries {
		if e.IsDir() || !logFileRe.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, LogFileInfo{Name: e.Name(), Size: info.Size(), MTime: info.ModTime().UnixMilli()})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// currentLogFile picks the file the next entry appends to: the newest
// existing file while it's under the size cap, else a fresh timestamp-named
// one. A same-second rotation collision reuses the existing name — appends
// there are equivalent to having never rotated.
func currentLogFile(now time.Time) string {
	files := ListLogFiles()
	if n := len(files); n > 0 && files[n-1].Size < logMaxBytes {
		return filepath.Join(LogsDir(), files[n-1].Name)
	}
	return filepath.Join(LogsDir(), newLogFileName(now))
}

// LogEvent appends one entry to the event log. Best-effort by contract:
// every failure path returns silently — callers log as a side effect of
// real work, and that work must proceed unlogged rather than fail.
func LogEvent(src, level, event, msg string) {
	if err := os.MkdirAll(LogsDir(), 0o755); err != nil {
		return
	}
	now := time.Now()
	line, err := json.Marshal(LogEntry{TS: now.UnixMilli(), Src: src, Level: level, Event: event, Msg: msg})
	if err != nil {
		return
	}
	f, err := os.OpenFile(currentLogFile(now), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// ReadLogFile parses one log file by bare name (validated against the
// timestamp shape — never a path). Malformed lines are skipped, not fatal:
// a torn concurrent write must not blank the whole file for the viewer.
func ReadLogFile(name string) ([]LogEntry, error) {
	if !logFileRe.MatchString(name) {
		return nil, os.ErrNotExist
	}
	data, err := os.ReadFile(filepath.Join(LogsDir(), name))
	if err != nil {
		return nil, err
	}
	var out []LogEntry
	start := 0
	for i := 0; i <= len(data); i++ {
		if i != len(data) && data[i] != '\n' {
			continue
		}
		line := data[start:i]
		start = i + 1
		if len(line) == 0 {
			continue
		}
		var e LogEntry
		if json.Unmarshal(line, &e) == nil {
			out = append(out, e)
		}
	}
	return out, nil
}
