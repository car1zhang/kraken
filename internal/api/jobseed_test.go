package api

import (
	"errors"
	"path/filepath"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// seedJobText stores a job whose text is src (frontmatter + body, the shape
// a job editor shows), creating the row or — when slug already has one —
// overwriting its fields, PRs included. It wires a temp store first when
// none is: kraken.db is the only place jobs live, so a fixture that used to
// write a <slug>.job.md now writes the row the board reads. Unlike
// useTestStore it leaves HOME alone, so a caller that built a world under
// its own HOME (a ~/Developer checkout) keeps it.
func seedJobText(t *testing.T, slug, src string) *store.Job {
	t.Helper()
	st := jobs.ActiveStore()
	if st == nil {
		var err error
		st, err = store.Open(filepath.Join(t.TempDir(), "kraken.db"))
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		jobs.UseStore(st)
		t.Cleanup(func() {
			jobs.UseStore(nil)
			st.Close()
		})
	}
	p := jobs.ParseJob(slug+jobs.JobSuffix, []byte(src))
	run := int64(p.Run)
	if run == 0 {
		run = 1
	}
	existing, err := st.GetJobBySlug(slug)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	var id int64
	if existing == nil {
		id, err = st.CreateJob(&store.Job{
			Slug: slug, Repo: p.Repo, Base: p.Base,
			Solo: p.Solo, Model: p.Model, Color: p.Color, Agent: p.Agent,
			Body: p.Body, State: p.State, Outcome: p.Outcome,
			Session: p.Session, Teardown: p.Teardown, Run: run, Err: p.Err,
		})
		if err != nil {
			t.Fatal(err)
		}
	} else {
		id = existing.ID
		if _, err := st.UpdateJobFields(existing.ID, existing.Rev, map[string]any{
			"repo": p.Repo, "base": p.Base, "solo": p.Solo, "model": p.Model,
			"color": p.Color, "agent": p.Agent, "body": p.Body, "state": p.State,
			"outcome": p.Outcome, "session": p.Session, "teardown": p.Teardown,
			"run": run, "err": p.Err,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, pr := range p.PRs {
		if err := st.UpsertPR(id, pr.URL, pr.State); err != nil {
			t.Fatal(err)
		}
		if pr.LastCommentAt != "" || pr.BumpedSHA != "" || pr.ConflictState != "" {
			if err := st.SetPRWatermark(id, pr.URL, pr.LastCommentAt, pr.BumpedSHA, pr.ConflictState); err != nil {
				t.Fatal(err)
			}
		}
	}
	row, err := st.GetJobByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

// savedJobText is a stored job's full text as the raw editor renders it
// (composeRawJobContent) — what a test that used to read the saved
// <slug>.job.md back off disk now compares.
func savedJobText(t *testing.T, slug string) []byte {
	t.Helper()
	j := jobs.JobBySlug(jobs.LoadJobs(), slug)
	if j == nil {
		t.Fatalf("job %s not found", slug)
	}
	return []byte(composeRawJobContent(j))
}
