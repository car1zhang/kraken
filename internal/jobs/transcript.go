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

// transcriptMaxTurns bounds a turns request the same way, and
// transcriptMaxTurnBlocks caps the blocks a turns answer carries — a `/do`
// turn can run to hundreds of tool one-liners, and the panel polls every 3s.
// Whole oldest turns are dropped to fit, never a turn's head (core.LastTurns).
const (
	transcriptMaxTurns      = 50
	transcriptMaxTurnBlocks = 400
)

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
	sid, path := stampedZeusTranscript(j)
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

// ZeusTranscriptTurns is ZeusTranscriptBlocks counted in TURNS rather than
// blocks: the last n (clamped to 1..50) of carl's prompts and everything zeus
// did after each, at most transcriptMaxTurnBlocks blocks with the oldest
// whole turns dropped to fit. Same empty-answer and error rules.
func ZeusTranscriptTurns(j *Job, n int) ([]TranscriptBlock, string, error) {
	if n < 1 {
		n = 1
	}
	if n > transcriptMaxTurns {
		n = transcriptMaxTurns
	}
	sid, path := stampedZeusTranscript(j)
	if path == "" {
		return []TranscriptBlock{}, "", nil
	}
	blocks, _, err := core.LastTurns(path, n, transcriptMaxTurnBlocks)
	if err != nil {
		return nil, "", err
	}
	out := make([]TranscriptBlock, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, TranscriptBlock{Kind: b.Kind, Text: b.Text, From: b.From, UUID: b.UUID})
	}
	return out, sid, nil
}

// stampedZeusTranscript resolves the job's stamped zeus session to its
// transcript; "" for either half means nothing to read (no job, no stamped
// session, no transcript on disk yet).
func stampedZeusTranscript(j *Job) (sid, path string) {
	if j == nil {
		return "", ""
	}
	sid = strings.TrimSpace(j.Session)
	if sid == "" {
		return "", ""
	}
	return sid, core.TranscriptPathForSession(sid)
}
