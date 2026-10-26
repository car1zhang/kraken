package jobs

import (
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
	"kraken/internal/util"
)

// The bind-done event names neither the account's email nor its slot: the
// event log is read from inside cells (an agent grepping it for its own
// slug), every line it reads is copied into a transcript that outlives the
// log, and a pool slot is usually the email run through util.Slugify, so the
// slot name reads as the address too. The row carries both for anything
// that needs them.
func TestBindDoneEventNamesNeitherTheEmailNorTheSlot(t *testing.T) {
	st := useTestStore(t)
	j := mustCreateJob(t, st, store.Job{Slug: "demo", Repo: "x"})

	var events []string
	util.SetEventSink(func(src, level, event, msg string) {
		if event == "bind-done" {
			events = append(events, msg)
		}
	})

	const email = "someone@example.com"
	slot := util.Slugify(email) // the shape kraken's own save-back slots have
	job := &Job{ID: j.ID, Slug: j.Slug}
	stampBinding(job, "start "+j.Slug, slot, email, time.Now())

	if len(events) != 1 {
		t.Fatalf("want one bind-done event, got %d: %q", len(events), events)
	}
	msg := events[0]
	if !strings.Contains(msg, "start demo") {
		t.Errorf("bind-done event %q does not carry the trigger", msg)
	}
	for _, leak := range []string{"@", "someone", "example", slot} {
		if strings.Contains(msg, leak) {
			t.Errorf("bind-done event %q carries %q — the log propagates into transcripts", msg, leak)
		}
	}
	// The stamp itself still carries both: the row is where they belong.
	if job.AccountEmail != email || job.AccountSlug != slot {
		t.Errorf("stamp = (%q, %q), want the slot and the email on the job", job.AccountSlug, job.AccountEmail)
	}
}
