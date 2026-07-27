package store

import (
	"errors"
	"reflect"
	"testing"
)

func TestReplacePrereqs(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "prereq-job"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.ReplacePrereqs(id, []Prereq{{Slug: "one", Gated: true}, {Slug: "two"}}); err != nil {
		t.Fatalf("ReplacePrereqs 1: %v", err)
	}
	got, err := s.ListPrereqs(id)
	if err != nil {
		t.Fatalf("ListPrereqs: %v", err)
	}
	want := []Prereq{{Slug: "one", Gated: true}, {Slug: "two"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	// replace with a smaller set — old rows must not linger
	if err := s.ReplacePrereqs(id, []Prereq{{Slug: "three"}}); err != nil {
		t.Fatalf("ReplacePrereqs 2: %v", err)
	}
	got, err = s.ListPrereqs(id)
	if err != nil {
		t.Fatalf("ListPrereqs 2: %v", err)
	}
	want = []Prereq{{Slug: "three"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestEventsAppendAndPaginate(t *testing.T) {
	s := openTest(t)
	for i := 0; i < 5; i++ {
		if _, err := s.AppendEvent(Event{TS: int64(i + 1), Src: "cli", Level: "info", Event: "e", Msg: "m"}); err != nil {
			t.Fatalf("AppendEvent %d: %v", i, err)
		}
	}
	page1, err := s.ListEvents(3, 0)
	if err != nil {
		t.Fatalf("ListEvents page1: %v", err)
	}
	if len(page1) != 3 {
		t.Fatalf("page1 len = %d, want 3", len(page1))
	}
	// newest first
	if page1[0].TS != 5 || page1[1].TS != 4 || page1[2].TS != 3 {
		t.Fatalf("page1 order wrong: %+v", page1)
	}
	page2, err := s.ListEvents(3, page1[len(page1)-1].ID)
	if err != nil {
		t.Fatalf("ListEvents page2: %v", err)
	}
	if len(page2) != 2 || page2[0].TS != 2 || page2[1].TS != 1 {
		t.Fatalf("page2 wrong: %+v", page2)
	}
}

func TestEventsJobSlugOptional(t *testing.T) {
	s := openTest(t)
	if _, err := s.AppendEvent(Event{Src: "daemon", Level: "info", Event: "start", Msg: "up"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	events, err := s.ListEvents(10, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 || events[0].JobSlug != "" {
		t.Fatalf("events = %+v", events)
	}
}

func TestFeedbackAddAndList(t *testing.T) {
	s := openTest(t)
	docJobID, err := s.CreateJob(&Job{Slug: "doc-job"})
	if err != nil {
		t.Fatalf("CreateJob doc-job: %v", err)
	}
	if _, err := s.CreateJob(&Job{Slug: "other-job"}); err != nil {
		t.Fatalf("CreateJob other-job: %v", err)
	}
	if _, err := s.AddFeedback(Feedback{JobID: docJobID, JobSlug: "doc-job", Artifact: "explain.html", Snapshot: []byte("<html>"), SnapshotExt: "html", Note: "confusing diagram", TS: 1}); err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}
	if _, err := s.AddFeedback(Feedback{JobSlug: "other-job", Artifact: "spec.html", Note: "n/a", TS: 2}); err != nil {
		t.Fatalf("AddFeedback other: %v", err)
	}
	list, err := s.ListFeedback(docJobID)
	if err != nil {
		t.Fatalf("ListFeedback: %v", err)
	}
	if len(list) != 1 || list[0].Note != "confusing diagram" {
		t.Fatalf("list = %+v", list)
	}
	all, err := s.ListFeedback(0)
	if err != nil {
		t.Fatalf("ListFeedback all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all = %+v", all)
	}
}

// The drafts table (and its round-trip test) was dropped in schema v11. It
// had zero non-test callers and zero rows on the live database: /api/draft is
// served entirely out of api/draft.go's in-memory draftStore, which is
// explicitly never persisted. Its behaviour lives in api/draft_test.go now.

func TestKVRoundTrip(t *testing.T) {
	s := openTest(t)
	if _, err := s.GetKV("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if err := s.SetKV("usage-snapshot", `{"tokens":123}`); err != nil {
		t.Fatalf("SetKV: %v", err)
	}
	v, err := s.GetKV("usage-snapshot")
	if err != nil {
		t.Fatalf("GetKV: %v", err)
	}
	if v != `{"tokens":123}` {
		t.Fatalf("v = %q", v)
	}
	if err := s.SetKV("usage-snapshot", `{"tokens":456}`); err != nil {
		t.Fatalf("SetKV overwrite: %v", err)
	}
	v, err = s.GetKV("usage-snapshot")
	if err != nil {
		t.Fatalf("GetKV after overwrite: %v", err)
	}
	if v != `{"tokens":456}` {
		t.Fatalf("v after overwrite = %q", v)
	}
}
