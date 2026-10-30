package jobs

// prwatchbot_contract_test.go — athena-logic's BLIND contract tests for
// slice B of dream-2026-10-30: pr-watch ignores a bot's bare approval
// (prActivityReview.State/Body, reviewNeedsAttention, latestActivityAt).
// Authored from artifacts/50-contract.md alone.
//
// LOGIC dimension. Every package-level identifier is prefixed
// `contractPRBot`.

import (
	"encoding/json"
	"testing"
)

func contractPRBotReview(at, login, state, body string) prActivityReview {
	r := prActivityReview{SubmittedAt: at, State: state, Body: body}
	r.Author.Login = login
	return r
}

func TestContractPRBotReviewJSON(t *testing.T) {
	var r prActivityReview
	if err := json.Unmarshal([]byte(`{"state":"CHANGES_REQUESTED","body":"please rename the helper"}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.State != "CHANGES_REQUESTED" || r.Body != "please rename the helper" {
		t.Errorf("decoded State=%q Body=%q", r.State, r.Body)
	}
	var empty prActivityReview
	if err := json.Unmarshal([]byte(`{}`), &empty); err != nil {
		t.Fatal(err)
	}
	if empty.State != "" || empty.Body != "" {
		t.Errorf("absent keys decoded State=%q Body=%q, want empty", empty.State, empty.Body)
	}
}

func TestContractPRBotReviewNeedsAttention(t *testing.T) {
	cases := []struct {
		name  string
		state string
		body  string
		want  bool
	}{
		{"approved, empty body", "APPROVED", "", true},
		{"approved, whitespace body", "APPROVED", "  \n\t ", true},
		{"approved, Auto-approved body", "APPROVED", "Auto-approved by bot-reviewer: all checks green", false},
		{"approved, padded Auto-approved body", "APPROVED", "\n  Auto-approved", false},
		{"approved, real text", "APPROVED", "LGTM, but rename the helper", true},
		{"changes requested, empty body", "CHANGES_REQUESTED", "", true},
		{"commented, empty body", "COMMENTED", "", true},
		{"commented, Auto-approved body", "COMMENTED", "Auto-approved", true},
		{"dismissed", "DISMISSED", "", true},
		{"empty state, empty body", "", "", true},
		{"empty state, Auto-approved body", "", "Auto-approved", true},
	}
	// Amended at the join (review rounds 1 and 2, contract defect): only the
	// `Auto-approved` template is a bare approval; an EMPTY body counts
	// whoever wrote it, since gh's review author carries no bot marker.
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := contractPRBotReview("2026-04-02T10:00:00Z", "bot-reviewer", c.state, c.body)
			if got := reviewNeedsAttention(r); got != c.want {
				t.Errorf("reviewNeedsAttention(state=%q body=%q) = %v, want %v", c.state, c.body, got, c.want)
			}
		})
	}
	t.Run("human approval with an empty body still needs attention", func(t *testing.T) {
		r := contractPRBotReview("2026-04-02T10:00:00Z", "alice", "APPROVED", "")
		if !reviewNeedsAttention(r) {
			t.Error("a human's empty-bodied approval was skipped; inline review comments ride no other signal")
		}
	})
	t.Run("human Auto-approved body is still the template", func(t *testing.T) {
		r := contractPRBotReview("2026-04-02T10:00:00Z", "alice", "APPROVED", "Auto-approved: role admin")
		if reviewNeedsAttention(r) {
			t.Error("the Auto-approved template counted as attention")
		}
	})
}

func TestContractPRBotLatestActivityAt(t *testing.T) {
	cases := []struct {
		name     string
		comments []prActivityComment
		reviews  []prActivityReview
		prAuthor string
		want     string
	}{
		{
			name: "bot bare approval newest never advances",
			reviews: []prActivityReview{
				contractPRBotReview("2026-04-01T10:00:00Z", "alice", "COMMENTED", "nit on line 3"),
				contractPRBotReview("2026-04-03T10:00:00Z", "bot-reviewer", "APPROVED", "Auto-approved"),
			},
			prAuthor: "zeus-author",
			want:     "2026-04-01T10:00:00Z",
		},
		{
			name: "Auto-approved body never advances",
			reviews: []prActivityReview{
				contractPRBotReview("2026-04-03T10:00:00Z", "bot-reviewer", "APPROVED", "Auto-approved: low-risk change"),
			},
			prAuthor: "zeus-author",
			want:     "",
		},
		{
			// amended at the join (review round 1): a human's empty-bodied
			// approval may carry inline nits gh's comment list never shows
			name: "human bare approval still advances",
			reviews: []prActivityReview{
				contractPRBotReview("2026-04-03T10:00:00Z", "alice", "APPROVED", ""),
			},
			comments: []prActivityComment{{CreatedAt: "2026-04-02T09:00:00Z"}},
			prAuthor: "zeus-author",
			want:     "2026-04-03T10:00:00Z",
		},
		{
			name: "approval with real text advances",
			reviews: []prActivityReview{
				contractPRBotReview("2026-04-03T10:00:00Z", "alice", "APPROVED", "approve once the test is renamed"),
			},
			prAuthor: "zeus-author",
			want:     "2026-04-03T10:00:00Z",
		},
		{
			name: "bot COMMENTED review advances",
			reviews: []prActivityReview{
				contractPRBotReview("2026-04-04T10:00:00Z", "bot-reviewer", "COMMENTED", ""),
			},
			prAuthor: "zeus-author",
			want:     "2026-04-04T10:00:00Z",
		},
		{
			name: "empty-state review advances",
			reviews: []prActivityReview{
				contractPRBotReview("2026-04-04T10:00:00Z", "bot-reviewer", "", ""),
			},
			prAuthor: "zeus-author",
			want:     "2026-04-04T10:00:00Z",
		},
		{
			name: "existing rules hold: viewer comment and PR-author review skipped",
			comments: []prActivityComment{
				{CreatedAt: "2026-04-01T00:00:00Z"},
				{CreatedAt: "2026-04-09T00:00:00Z", ViewerDidAuthor: true},
			},
			reviews: []prActivityReview{
				contractPRBotReview("2026-04-08T00:00:00Z", "zeus-author", "COMMENTED", "reply"),
				contractPRBotReview("2026-04-02T00:00:00Z", "alice", "CHANGES_REQUESTED", "fix it"),
			},
			prAuthor: "zeus-author",
			want:     "2026-04-02T00:00:00Z",
		},
		{
			name: "empty prAuthor fails open for a non-approval review",
			reviews: []prActivityReview{
				contractPRBotReview("2026-04-05T00:00:00Z", "zeus-author", "COMMENTED", "reply"),
			},
			prAuthor: "",
			want:     "2026-04-05T00:00:00Z",
		},
		{
			name: "nothing remains",
			want: "",
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
