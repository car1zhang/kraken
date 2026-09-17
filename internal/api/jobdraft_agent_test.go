package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestJobDraftCarriesTheAgentField pins a defect the codex account pool's live
// gate found on this route: the create path built its store.Job without the
// parsed Agent, so a job whose text said `agent: codex` was stored with an
// empty agent column and spawned as a claude cell. The draft route is iris'
// only write surface on a job, so without this pin every codex job iris ever
// drafts would silently run on the wrong agent.
func TestJobDraftCarriesTheAgentField(t *testing.T) {
	useTestStore(t)

	content := "---\njob: draft-agent\nrepo: kraken\nagent: codex\n---\n\nreply with the single word pong\n"
	body := mustJSON(t, map[string]any{"slug": "draft-agent", "content": content})
	rec := httptest.NewRecorder()
	jobDraftHandler(rec, httptest.NewRequest(http.MethodPost, "/api/job/draft", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/job/draft (create): status %d, body %s", rec.Code, rec.Body.String())
	}
	j, _, err := webJobFor("draft-agent")
	if err != nil {
		t.Fatalf("reload drafted job: %v", err)
	}
	if j.Agent != "codex" {
		t.Fatalf("drafted job's agent = %q, want %q — the create path dropped the parsed agent", j.Agent, "codex")
	}

	// The EDIT arm had the same gap: re-sending the text with a changed agent
	// answered 200 while the row kept the old value. The stored value is the
	// parser's normalised one, so a mixed-case spelling lands lowercased.
	content = "---\njob: draft-agent\nrepo: kraken\nagent: Claude\n---\n\nreply with the single word pong\n"
	body = mustJSON(t, map[string]any{"slug": "draft-agent", "content": content, "mtime": 0})
	rec = httptest.NewRecorder()
	jobDraftHandler(rec, httptest.NewRequest(http.MethodPost, "/api/job/draft", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/job/draft (edit): status %d, body %s", rec.Code, rec.Body.String())
	}
	j, _, err = webJobFor("draft-agent")
	if err != nil {
		t.Fatalf("reload edited job: %v", err)
	}
	if j.Agent != "claude" {
		t.Fatalf("edited job's agent = %q, want %q — the edit arm dropped the parsed agent", j.Agent, "claude")
	}

	// An agent the parser refuses is a 400 on either arm, and nothing changes.
	content = "---\njob: draft-agent\nrepo: kraken\nagent: aider\n---\n\nreply with the single word pong\n"
	body = mustJSON(t, map[string]any{"slug": "draft-agent", "content": content, "mtime": 0})
	rec = httptest.NewRecorder()
	jobDraftHandler(rec, httptest.NewRequest(http.MethodPost, "/api/job/draft", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/job/draft (agent: aider): status %d, want 400; body %s", rec.Code, rec.Body.String())
	}
	if j, _, _ = webJobFor("draft-agent"); j.Agent != "claude" {
		t.Fatalf("a refused edit changed the agent to %q", j.Agent)
	}
}
