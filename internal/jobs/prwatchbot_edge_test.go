package jobs

// prwatchbot_edge_test.go — athena-edge's BLIND tests for slice B of
// dream-2026-10-30 (pr-watch ignores a bot's bare approval): the
// boundaries of reviewNeedsAttention and how latestActivityAt skips what it
// rejects. Authored from 50-contract.md alone. Every package-level
// identifier is prefixed edgePRW.

import (
	"encoding/json"
	"testing"
)

func edgePRWReview(at, login, state, body string) prActivityReview {
	r := prActivityReview{SubmittedAt: at, State: state, Body: body}
	r.Author.Login = login
	return r
}

func TestEdgeReviewNeedsAttention(t *testing.T) {
	cases := []struct {
		name  string
		state string
		body  string
		want  bool
	}{
		{"approved, empty body", "APPROVED", "", false},
		{"approved, whitespace-only body", "APPROVED", " \n\t  \r\n", false},
		{"approved, exactly Auto-approved", "APPROVED", "Auto-approved", false},
		{"approved, Auto-approved with tail", "APPROVED", "Auto-approved by bot-reviewer: all checks passed", false},
		{"approved, Auto-approved after leading whitespace", "APPROVED", "\n   Auto-approved\n", false},
		{"approved, Auto-approved glued suffix", "APPROVED", "Auto-approvedX", false},
		{"approved, lowercase auto-approved", "APPROVED", "auto-approved", true},
		{"approved, Auto-approve without d", "APPROVED", "Auto-approve", true},
		{"approved, Auto approved without hyphen", "APPROVED", "Auto approved", true},
		{"approved, Auto-approved not at start", "APPROVED", "Not Auto-approved", true},
		{"approved, real text", "APPROVED", "LGTM but rename the helper", true},
		{"approved, single char", "APPROVED", ".", true},
		{"lowercase state", "approved", "", true},
		{"padded state", " APPROVED", "", true},
		{"empty state, empty body", "", "", true},
		{"empty state, Auto-approved", "", "Auto-approved", true},
		{"commented, empty", "COMMENTED", "", true},
		{"commented, Auto-approved", "COMMENTED", "Auto-approved", true},
		{"changes requested, empty", "CHANGES_REQUESTED", "", true},
		{"dismissed, empty", "DISMISSED", "", true},
		{"pending, empty", "PENDING", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := edgePRWReview("2026-02-01T00:00:00Z", "bot-reviewer", c.state, c.body)
			if got := reviewNeedsAttention(r); got != c.want {
				t.Errorf("reviewNeedsAttention(state=%q, body=%q) = %v, want %v", c.state, c.body, got, c.want)
			}
		})
	}
}

func TestEdgeLatestActivityAtSkipsBareApprovals(t *testing.T) {
	cases := []struct {
		name     string
		comments []prActivityComment
		reviews  []prActivityReview
		prAuthor string
		want     string
	}{
		{
			name:    "only a bare approval",
			reviews: []prActivityReview{edgePRWReview("2026-02-03T00:00:00Z", "bot-reviewer", "APPROVED", "")},
			want:    "",
		},
		{
			name: "only bare approvals, from anyone",
			reviews: []prActivityReview{
				edgePRWReview("2026-02-03T00:00:00Z", "bot-reviewer", "APPROVED", "Auto-approved"),
				edgePRWReview("2026-02-04T00:00:00Z", "alice", "APPROVED", "  "),
			},
			want: "",
		},
		{
			name:     "bare approval skipped with an empty PR author too",
			reviews:  []prActivityReview{edgePRWReview("2026-02-05T00:00:00Z", "alice", "APPROVED", "")},
			prAuthor: "",
			want:     "",
		},
		{
			name:     "newest bare approval does not beat an older comment",
			comments: []prActivityComment{{CreatedAt: "2026-02-01T00:00:00Z"}},
			reviews:  []prActivityReview{edgePRWReview("2026-02-09T00:00:00Z", "bot-reviewer", "APPROVED", "Auto-approved: green")},
			want:     "2026-02-01T00:00:00Z",
		},
		{
			name: "newest bare approval does not beat an older real review",
			reviews: []prActivityReview{
				edgePRWReview("2026-02-02T00:00:00Z", "alice", "CHANGES_REQUESTED", "please fix"),
				edgePRWReview("2026-02-09T00:00:00Z", "bot-reviewer", "APPROVED", ""),
			},
			want: "2026-02-02T00:00:00Z",
		},
		{
			name: "approval carrying text still advances",
			reviews: []prActivityReview{
				edgePRWReview("2026-02-02T00:00:00Z", "alice", "COMMENTED", "q"),
				edgePRWReview("2026-02-09T00:00:00Z", "bot-reviewer", "APPROVED", "approved, one nit below"),
			},
			want: "2026-02-09T00:00:00Z",
		},
		{
			name:    "review with empty state advances",
			reviews: []prActivityReview{edgePRWReview("2026-02-07T00:00:00Z", "alice", "", "")},
			want:    "2026-02-07T00:00:00Z",
		},
		{
			name: "PR-author rule still applies to a non-bare review",
			reviews: []prActivityReview{
				edgePRWReview("2026-02-02T00:00:00Z", "alice", "COMMENTED", "x"),
				edgePRWReview("2026-02-08T00:00:00Z", "pr-owner", "COMMENTED", "reply"),
			},
			prAuthor: "pr-owner",
			want:     "2026-02-02T00:00:00Z",
		},
		{
			name: "viewer comment and bare approval both skipped",
			comments: []prActivityComment{
				{CreatedAt: "2026-02-08T00:00:00Z", ViewerDidAuthor: true},
			},
			reviews: []prActivityReview{edgePRWReview("2026-02-09T00:00:00Z", "bot-reviewer", "APPROVED", "")},
			want:    "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := latestActivityAt(c.comments, c.reviews, c.prAuthor); got != c.want {
				t.Errorf("latestActivityAt = %q, want %q", got, c.want)
			}
		})
	}
}

// The json keys are `state` and `body`; absent keys decode to "".
func TestEdgePRActivityReviewWire(t *testing.T) {
	var r prActivityReview
	if err := json.Unmarshal([]byte(`{"state":"APPROVED","body":"Auto-approved"}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.State != "APPROVED" || r.Body != "Auto-approved" {
		t.Errorf("decoded State=%q Body=%q, want APPROVED / Auto-approved", r.State, r.Body)
	}
	var empty prActivityReview
	if err := json.Unmarshal([]byte(`{}`), &empty); err != nil {
		t.Fatal(err)
	}
	if empty.State != "" || empty.Body != "" {
		t.Errorf("absent keys decoded State=%q Body=%q, want empty", empty.State, empty.Body)
	}
	if reviewNeedsAttention(empty) != true {
		t.Error("a review with no state must need attention")
	}
}
