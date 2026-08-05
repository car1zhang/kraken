package core

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
)

// transcript.go — a bounded TAIL read of one claude transcript, for callers
// that want the conversation's most recent assistant turn and nothing else.
//
// The mid-conversation recall hook is the reason this exists: on a Stop event
// there is no prompt to retrieve against, so the query is whatever claude just
// said. mine.go's readers stream a whole file because a digest and a reduction
// are about the whole file; this one is about its last few kilobytes, and a
// transcript on this machine reaches 42 MB — reading one whole to answer "what
// was the last thing said?" would put a multi-hundred-megabyte read inside a
// hook that runs on every turn.
//
// Same defensive contract as mine.go and the rest of core for claude-owned
// data: a missing file, an unreadable one, a malformed line, a truncated final
// line, a shape that is not what was measured — all degrade to "" and none of
// them is an error. A hook that breaks a conversation is a bug by definition,
// so there is deliberately no error value to mishandle.

// lastAssistantTailBytes bounds how much of a transcript LastAssistantText
// looks at. 256 KiB is far more than a final assistant turn plus the records
// that follow it, and small enough that the read is a seek and one buffer
// rather than a scan of the file.
const lastAssistantTailBytes = 256 * 1024

// lastAssistantRecord is the slice of a transcript line this reader cares
// about. Deliberately its own type rather than mine.go's mineRecord: that one
// decodes the usage counters too, so a record with an unexpected shape THERE
// would fail to decode HERE for a reason having nothing to do with the text
// being read.
type lastAssistantRecord struct {
	Type    string `json:"type"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// LastAssistantText returns the text of the last assistant record in the
// transcript at path that actually said something: its `text` content blocks
// joined with newlines, in order. Thinking and tool_use blocks are skipped, so
// a record that only thought or only called a tool is not "the last thing
// said" and the search continues backwards through the ones before it.
//
// Everything that can go wrong returns "": no file, no permission, no
// qualifying record, unparseable JSON, a content shape that is neither the
// blocks array nor a plain string. "" means "no query available" to the one
// caller, which is a normal outcome rather than a failure.
//
// Only the final lastAssistantTailBytes of the file are examined. The seek
// lands mid-line except on a file smaller than the window, so the first line
// after a seek is discarded — decoding half a JSON record is at best a parse
// failure and at worst a truncated string that reads as real text.
func LastAssistantText(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	var start int64
	if fi.Size() > lastAssistantTailBytes {
		start = fi.Size() - lastAssistantTailBytes
	}
	if start > 0 {
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return ""
		}
	}

	sc := bufio.NewScanner(f)
	// Ceiling at the window size, since no whole line inside the window can be
	// longer than the window itself; the small initial buffer is what keeps the
	// ceiling a bound rather than a 256 KiB allocation on every hook invocation
	// (bufio allocates its initial capacity up front and only doubles). Without
	// a raised ceiling at all, bufio's 64 KB default would silently END the
	// scan at the first big inlined block — mineScanBufMax's argument, at this
	// reader's scale.
	sc.Buffer(make([]byte, 0, mineScanBufInit), lastAssistantTailBytes)
	if start > 0 {
		sc.Scan() // discard the partial line the seek landed inside
	}

	last := ""
	for sc.Scan() {
		var rec lastAssistantRecord
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		if rec.Type != "assistant" {
			continue
		}
		if text, ok := assistantText(rec.Message.Content); ok {
			last = text
		}
	}
	// sc.Err() is deliberately ignored: a mid-file read error or an oversized
	// line ends the scan, and whatever qualifying record was already seen is
	// still the best answer available. There is no error to report it through
	// and nothing a caller could do differently.
	return last
}

// assistantText joins one assistant message's text blocks. ok=false means the
// record said nothing quotable — no text blocks, or only empty ones — which is
// what makes a pure thinking/tool_use record fall through to the record before
// it rather than clearing the answer to "".
//
// Empty text blocks are JOINED rather than dropped: the join is the message's
// text as claude wrote it, and only the DECISION to use the record at all
// depends on some block being non-empty.
func assistantText(content json.RawMessage) (string, bool) {
	// The plain-string content shape is defensive rather than measured on
	// assistant records — the format is claude's, not kraken's, and mine.go
	// already carries a decoder for it because user records do use it.
	if s, ok := mineUserString(content); ok {
		return s, s != ""
	}
	var parts []string
	any := false
	for _, b := range mineBlocks(content) {
		if b.Type != "text" {
			continue
		}
		parts = append(parts, b.Text)
		if b.Text != "" {
			any = true
		}
	}
	if !any {
		return "", false
	}
	return strings.Join(parts, "\n"), true
}
