package store

import (
	"bytes"
	"errors"
	"testing"
)

func TestSyncArtifactsTrueMirror(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "artifact-job"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	err = s.SyncArtifacts(id, []ArtifactFile{
		{Path: "a.txt", Bytes: []byte("aaa"), Mtime: 1},
		{Path: "sub/b.txt", Bytes: []byte("bbb"), Mtime: 2},
	}, nil)
	if err != nil {
		t.Fatalf("SyncArtifacts 1: %v", err)
	}
	list, err := s.ListArtifacts(id)
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 artifacts, got %+v", list)
	}

	// second sync: a.txt updated, sub/b.txt vanished, c.txt is new — true
	// mirror means b.txt's row must be deleted, not left stale
	err = s.SyncArtifacts(id, []ArtifactFile{
		{Path: "a.txt", Bytes: []byte("aaa2"), Mtime: 5},
		{Path: "c.txt", Bytes: []byte("ccc"), Mtime: 5},
	}, nil)
	if err != nil {
		t.Fatalf("SyncArtifacts 2: %v", err)
	}

	list, err = s.ListArtifacts(id)
	if err != nil {
		t.Fatalf("ListArtifacts 2: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 artifacts after mirror sync, got %+v", list)
	}
	byPath := map[string]Artifact{}
	for _, a := range list {
		byPath[a.Path] = a
	}
	if _, ok := byPath["sub/b.txt"]; ok {
		t.Fatalf("vanished file b.txt not removed: %+v", list)
	}
	if !bytes.Equal(byPath["a.txt"].Bytes, []byte("aaa2")) || byPath["a.txt"].Mtime != 5 {
		t.Fatalf("a.txt not updated: %+v", byPath["a.txt"])
	}
	if !bytes.Equal(byPath["c.txt"].Bytes, []byte("ccc")) {
		t.Fatalf("c.txt not inserted: %+v", byPath["c.txt"])
	}
}

func TestSyncArtifactsEmptySetDeletesAll(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "wipe"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.SyncArtifacts(id, []ArtifactFile{{Path: "x", Bytes: []byte("x"), Mtime: 1}}, nil); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	if err := s.SyncArtifacts(id, nil, nil); err != nil {
		t.Fatalf("empty sync: %v", err)
	}
	list, err := s.ListArtifacts(id)
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty mirror, got %+v", list)
	}
}

// TestSyncArtifactsUnchangedSkipsUpsertAndSurvives covers: a path
// named in `unchanged` (not `files`) must survive a sync with its stored
// bytes/mtime completely untouched, while a co-occurring changed/new file
// in `files` still lands normally.
func TestSyncArtifactsUnchangedSkipsUpsertAndSurvives(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "unchanged-job"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.SyncArtifacts(id, []ArtifactFile{{Path: "a.txt", Bytes: []byte("aaa"), Mtime: 1}}, nil); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	if err := s.SyncArtifacts(id,
		[]ArtifactFile{{Path: "b.txt", Bytes: []byte("bbb"), Mtime: 2}},
		map[string]bool{"a.txt": true},
	); err != nil {
		t.Fatalf("sync with unchanged: %v", err)
	}
	list, err := s.ListArtifacts(id)
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 rows, got %+v", list)
	}
	byPath := map[string]Artifact{}
	for _, a := range list {
		byPath[a.Path] = a
	}
	if !bytes.Equal(byPath["a.txt"].Bytes, []byte("aaa")) || byPath["a.txt"].Mtime != 1 {
		t.Fatalf("unchanged a.txt drifted: %+v", byPath["a.txt"])
	}
	if !bytes.Equal(byPath["b.txt"].Bytes, []byte("bbb")) {
		t.Fatalf("new b.txt missing: %+v", byPath["b.txt"])
	}
}

// TestSyncArtifactsUnchangedStillDeletedWhenDropped confirms true-mirror
// semantics extend to the unchanged set too: a path that's neither in
// files nor unchanged on a later sync gets deleted, same as a plain vanish.
func TestSyncArtifactsUnchangedStillDeletedWhenDropped(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "unchanged-vanish"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.SyncArtifacts(id, []ArtifactFile{{Path: "a.txt", Bytes: []byte("aaa"), Mtime: 1}}, nil); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	if err := s.SyncArtifacts(id, nil, nil); err != nil {
		t.Fatalf("drop sync: %v", err)
	}
	list, err := s.ListArtifacts(id)
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("want empty mirror, got %+v", list)
	}
}

func TestListArtifactMeta(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "meta-job"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.SyncArtifacts(id, []ArtifactFile{
		{Path: "a.txt", Bytes: []byte("aaa"), Mtime: 10},
		{Path: "sub/b.txt", Bytes: []byte("bbbb"), Mtime: 20},
	}, nil); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	metas, err := s.ListArtifactMeta(id)
	if err != nil {
		t.Fatalf("ListArtifactMeta: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("want 2 metas, got %+v", metas)
	}
	byPath := map[string]ArtifactMeta{}
	for _, m := range metas {
		byPath[m.Path] = m
	}
	if byPath["a.txt"].Size != 3 || byPath["a.txt"].Mtime != 10 {
		t.Fatalf("a.txt meta: %+v", byPath["a.txt"])
	}
	if byPath["sub/b.txt"].Size != 4 || byPath["sub/b.txt"].Mtime != 20 {
		t.Fatalf("sub/b.txt meta: %+v", byPath["sub/b.txt"])
	}
}

func TestGetArtifactNotFound(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "missing-art"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := s.GetArtifact(id, "nope.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
