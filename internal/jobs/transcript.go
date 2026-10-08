package jobs

import (
	"strings"

	"kraken/internal/core"
)

// transcript.go — zeus' recent conversation, for the steer modal's read-only
// transcript panel (Q5). The panel exists so carl can highlight a passage of
// what zeus said and route it to the mind thread like any artifact text, which
// is why every block carries its record uuid: that uuid is the pointer a
// transcript-sourced capture stores.

// TranscriptBlock is one rendered block of a transcript, the wire shape of
// core's unexported block: user/thinking/text/tool/teammate Kind, its Text,
// the teammate id in From, and the record UUID it came out of.
type TranscriptBlock struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
	From string `json:"from,omitempty"`
	UUID string `json:"uuid"`
}

// transcriptMaxBlocks bounds n: the panel is a recent tail, and LastBlocks'
// backward scan grows with the request.
const transcriptMaxBlocks = 200

// ZeusTranscriptBlocks returns the last n blocks (clamped to 1..200) of the
// job's stamped zeus session and that session id. A job with no stamped
// session, or whose transcript cannot be found, is an empty answer with nil
// error — a cell in its first seconds, or a job that never ran, has nothing
// to show, which is not a failure. A transcript that exists and cannot be
// read is the one error.
func ZeusTranscriptBlocks(j *Job, n int) ([]TranscriptBlock, string, error) {
	if n < 1 {
		n = 1
	}
	if n > transcriptMaxBlocks {
		n = transcriptMaxBlocks
	}
	if j == nil {
		return []TranscriptBlock{}, "", nil
	}
	sid := strings.TrimSpace(j.Session)
	if sid == "" {
		return []TranscriptBlock{}, "", nil
	}
	path := core.TranscriptPathForSession(sid)
	if path == "" {
		return []TranscriptBlock{}, "", nil
	}
	blocks, _, err := core.LastBlocks(path, n)
	if err != nil {
		return nil, "", err
	}
	// LastBlocks counts only non-tool blocks against n and returns the tool
	// one-liners interleaved after them too, so the tail is cut here to keep
	// "the last n blocks" literal.
	if len(blocks) > n {
		blocks = blocks[len(blocks)-n:]
	}
	out := make([]TranscriptBlock, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, TranscriptBlock{Kind: b.Kind, Text: b.Text, From: b.From, UUID: b.UUID})
	}
	return out, sid, nil
}
