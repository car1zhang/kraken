package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// capturesource.go — the source pointer a mental-model item or a read mark
// keeps, and the two refusals every passage the board sends is checked
// against. It outlived the capture router (the mind thread): the ✓ read
// button (MarkRead, mindrun.go) and the mind run's items (ApplyMindOps) carry
// the same pointer, so `kraken mm source <id>` drills back down the same way
// whichever path wrote the row.

// ErrCaptureTooLong refuses a passage over captureMaxRunes; ErrCaptureBadSource
// refuses empty text or a malformed source. Both map to 400 at the API.
var (
	ErrCaptureTooLong   = errors.New("captured text is too long")
	ErrCaptureBadSource = errors.New("bad capture")
)

// captureMaxRunes caps one captured passage — the mm caps' decimal-runes
// convention (32000 for element and code marks).
const captureMaxRunes = 32000

// CaptureSource is where a passage came from — the pointer an item keeps (as
// mm_items.source_json) and a mark keeps (as mm_marks.source_json) so
// `kraken mm source <id>` can drill back down to it. Kind decides which other
// fields matter: an artifact by job slug, home and name (plus the sha of the
// bytes carl was reading), a transcript record by session and uuid (tid when
// it was a thread's), a repo directory by repo and dir, another mm item by
// id, or manual for text with no pointer.
type CaptureSource struct {
	Kind    string `json:"kind"`
	Slug    string `json:"slug,omitempty"`
	Home    string `json:"home,omitempty"`
	Name    string `json:"name,omitempty"`
	SHA     string `json:"sha,omitempty"`
	Session string `json:"session,omitempty"`
	UUID    string `json:"uuid,omitempty"`
	TID     string `json:"tid,omitempty"`
	Repo    string `json:"repo,omitempty"`
	Dir     string `json:"dir,omitempty"`
	ItemID  int64  `json:"item_id,omitempty"`
	// QID and FeedbackID are the evidence pointers (schema v37): a question
	// row by job slug + qid, a feedback row by id.
	QID        string `json:"qid,omitempty"`
	FeedbackID int64  `json:"feedback_id,omitempty"`
	// MarkID, SavedID and RunID are the mind run's pointers (schema v39): the
	// read mark an item was distilled from, the ★-saved library row it is
	// rooted in, and the run that wrote it.
	MarkID  int64 `json:"mark_id,omitempty"`
	SavedID int64 `json:"saved_id,omitempty"`
	RunID   int64 `json:"run_id,omitempty"`
}

// ValidCaptureSource refuses a kind outside the eight, and a kind missing the
// field that makes its pointer resolvable: an artifact's name, a transcript's
// session, a dir's repo, a job's slug, a feedback row's id, a question's slug
// and qid. job/feedback/question are the evidence kinds; a passage from the
// board never sends one, and accepting them here is harmless (the API's
// source resolver answers all eight).
func ValidCaptureSource(s CaptureSource) error {
	switch s.Kind {
	case "artifact":
		if strings.TrimSpace(s.Name) == "" {
			return errors.New("an artifact source needs a name")
		}
	case "transcript":
		if strings.TrimSpace(s.Session) == "" {
			return errors.New("a transcript source needs a session")
		}
	case "dir":
		if strings.TrimSpace(s.Repo) == "" {
			return errors.New("a dir source needs a repo")
		}
	case "job":
		if strings.TrimSpace(s.Slug) == "" {
			return errors.New("a job source needs a slug")
		}
	case "feedback":
		if s.FeedbackID <= 0 {
			return errors.New("a feedback source needs a feedback_id")
		}
	case "question":
		if strings.TrimSpace(s.Slug) == "" || strings.TrimSpace(s.QID) == "" {
			return errors.New("a question source needs a slug and a qid")
		}
	case "item", "manual":
	default:
		return fmt.Errorf("bad source kind %q: want artifact, transcript, dir, item, manual, job, feedback or question", s.Kind)
	}
	return nil
}

// captureSourceJSON is the source as a row stores it: plain encoding/json.
func captureSourceJSON(s CaptureSource) string {
	b, err := json.Marshal(s)
	if err != nil {
		return "{}" // unreachable for a struct of strings and ints
	}
	return string(b)
}
