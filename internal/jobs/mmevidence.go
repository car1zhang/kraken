package jobs

import (
	"errors"
	"strings"
	"time"

	"kraken/internal/store"
)

// mmevidence.go — `kraken mm evidence`: the operator-authored rows the mind
// pass reads beside the transcripts — job specs, artifact feedback and answers
// to questions, inside one inclusive window. A job spec counts only when the
// operator wrote it: jobs.author (schema v37) is what tells a spec typed on
// the board from one an agent, Linear or kraken itself drafted, since the body
// alone cannot. Pre-v37 rows read "unknown" and are kept.

// MMEvidence is the window's evidence; every slice is non-nil.
type MMEvidence struct {
	Since    int64                `json:"since"`
	Until    int64                `json:"until"`
	Jobs     []MMEvidenceJob      `json:"jobs"`
	Feedback []MMEvidenceFeedback `json:"feedback"`
	Answers  []MMEvidenceAnswer   `json:"answers"`
}

// MMEvidenceJob is one job spec, Mid-run log stripped (JobSpecText).
type MMEvidenceJob struct {
	Slug      string `json:"slug"`
	ID        int64  `json:"id"`
	Author    string `json:"author"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	BodySHA   string `json:"body_sha"`
	Body      string `json:"body"`
}

// MMEvidenceFeedback is one artifact note.
type MMEvidenceFeedback struct {
	ID       int64  `json:"id"`
	Slug     string `json:"slug"`
	Artifact string `json:"artifact"`
	TS       int64  `json:"ts"`
	Note     string `json:"note"`
}

// MMEvidenceAnswer is one answered question. AcceptedRec marks an answer that
// only accepted the recommendation, which says less about the operator.
type MMEvidenceAnswer struct {
	Slug        string `json:"slug"`
	QID         string `json:"qid"`
	Text        string `json:"text"`
	Answer      string `json:"answer"`
	AnsweredAt  string `json:"answered_at"`
	AcceptedRec bool   `json:"accepted_rec"`
}

// CollectMMEvidence gathers the window [since, until] (until 0 = no ceiling).
func CollectMMEvidence(st *store.Store, since, until int64) (MMEvidence, error) {
	if st == nil {
		return MMEvidence{}, errors.New("no store")
	}
	if since <= 0 {
		return MMEvidence{}, errors.New("since must be a positive unix-millis floor")
	}
	ev := MMEvidence{
		Since: since, Until: until,
		Jobs:     []MMEvidenceJob{},
		Feedback: []MMEvidenceFeedback{},
		Answers:  []MMEvidenceAnswer{},
	}
	inWindow := func(t int64) bool { return t >= since && (until <= 0 || t <= until) }

	linear, err := st.ListLinearIssues()
	if err != nil {
		return MMEvidence{}, err
	}
	linearJob := map[int64]bool{}
	for _, li := range linear {
		if li.JobID != 0 {
			linearJob[li.JobID] = true
		}
	}
	touched, err := st.ListJobsTouchedSince(since, until)
	if err != nil {
		return MMEvidence{}, err
	}
	for _, j := range touched {
		switch j.Author {
		case "agent", "linear", "kraken":
			continue
		}
		if strings.HasPrefix(j.Slug, "dream-") || strings.HasPrefix(j.Slug, "mind-") || linearJob[j.ID] {
			continue
		}
		body := JobSpecText(j.Body)
		ev.Jobs = append(ev.Jobs, MMEvidenceJob{
			Slug: j.Slug, ID: j.ID, Author: j.Author,
			CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt,
			BodySHA: store.SHA256Hex([]byte(body)), Body: body,
		})
	}

	fb, err := st.FeedbackSince(since)
	if err != nil {
		return MMEvidence{}, err
	}
	for _, f := range fb {
		if !inWindow(f.TS) {
			continue
		}
		ev.Feedback = append(ev.Feedback, MMEvidenceFeedback{
			ID: f.ID, Slug: f.JobSlug, Artifact: f.Artifact, TS: f.TS, Note: f.Note,
		})
	}

	qs, err := st.ListQuestionsAll()
	if err != nil {
		return MMEvidence{}, err
	}
	slugOf := map[int64]string{}
	for _, q := range qs {
		if q.Answer == "" {
			continue
		}
		at, perr := time.Parse(time.RFC3339, q.AnsweredAt)
		if perr != nil || !inWindow(at.UnixMilli()) {
			continue
		}
		slug, seen := slugOf[q.JobID]
		if !seen {
			if j, gerr := st.GetJobByID(q.JobID); gerr == nil && j != nil {
				slug = j.Slug
			}
			slugOf[q.JobID] = slug
		}
		ev.Answers = append(ev.Answers, MMEvidenceAnswer{
			Slug: slug, QID: q.QID, Text: q.Text, Answer: q.Answer, AnsweredAt: q.AnsweredAt,
			AcceptedRec: strings.TrimSpace(q.Answer) == strings.TrimSpace(q.Recommendation),
		})
	}
	return ev, nil
}
