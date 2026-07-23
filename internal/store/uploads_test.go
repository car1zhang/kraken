package store

import (
	"errors"
	"testing"
)

func TestSaveUploadRejectsInvalidExt(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "bad-ext", Type: "build"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	for _, ext := range []string{"", "png/../etc", "P N G", "toolongextension"} {
		if _, err := s.SaveUpload(id, ext, "image/png", []byte("x")); !errors.Is(err, ErrInvalidExt) {
			t.Fatalf("SaveUpload(%q) err = %v, want ErrInvalidExt", ext, err)
		}
	}
}

func TestSaveUploadSequentialNaming(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "upload-job", Type: "build"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	u1, err := s.SaveUpload(id, "png", "image/png", []byte("one"))
	if err != nil {
		t.Fatalf("SaveUpload 1: %v", err)
	}
	if u1.Name != "img-001.png" {
		t.Fatalf("u1.Name = %q, want img-001.png", u1.Name)
	}

	u2, err := s.SaveUpload(id, "jpg", "image/jpeg", []byte("two"))
	if err != nil {
		t.Fatalf("SaveUpload 2: %v", err)
	}
	if u2.Name != "img-002.jpg" {
		t.Fatalf("u2.Name = %q, want img-002.jpg", u2.Name)
	}

	list, err := s.ListUploads(id)
	if err != nil {
		t.Fatalf("ListUploads: %v", err)
	}
	if len(list) != 2 || list[0].Name != "img-001.png" || list[1].Name != "img-002.jpg" {
		t.Fatalf("ListUploads = %+v", list)
	}

	got, err := s.GetUpload(id, "img-001.png")
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if string(got.Bytes) != "one" {
		t.Fatalf("bytes mismatch: %+v", got)
	}
}

func TestSaveUploadPerJobSequencing(t *testing.T) {
	s := openTest(t)
	id1, err := s.CreateJob(&Job{Slug: "job-a", Type: "build"})
	if err != nil {
		t.Fatalf("CreateJob 1: %v", err)
	}
	id2, err := s.CreateJob(&Job{Slug: "job-b", Type: "build"})
	if err != nil {
		t.Fatalf("CreateJob 2: %v", err)
	}
	if _, err := s.SaveUpload(id1, "png", "image/png", []byte("a")); err != nil {
		t.Fatalf("SaveUpload job1: %v", err)
	}
	u, err := s.SaveUpload(id2, "png", "image/png", []byte("b"))
	if err != nil {
		t.Fatalf("SaveUpload job2: %v", err)
	}
	// job2's sequence starts fresh at 001, independent of job1's
	if u.Name != "img-001.png" {
		t.Fatalf("job2 first upload name = %q, want img-001.png", u.Name)
	}
}

func TestUpsertUploadNamedPreservesNameAndUpserts(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "named-upload", Type: "build"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	// out-of-order name, unlike SaveUpload's always-next-sequential contract
	if err := s.UpsertUploadNamed(id, "img-007.png", "image/png", []byte("first"), 1000); err != nil {
		t.Fatalf("UpsertUploadNamed: %v", err)
	}
	got, err := s.GetUpload(id, "img-007.png")
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if string(got.Bytes) != "first" || got.CreatedAt != 1000 {
		t.Fatalf("upload after insert: %+v", got)
	}

	// same (job_id, name): must update in place, not duplicate
	if err := s.UpsertUploadNamed(id, "img-007.png", "image/png", []byte("second"), 2000); err != nil {
		t.Fatalf("UpsertUploadNamed (update): %v", err)
	}
	list, err := s.ListUploads(id)
	if err != nil {
		t.Fatalf("ListUploads: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("uploads after re-upsert = %+v, want 1 row", list)
	}
	if string(list[0].Bytes) != "second" || list[0].CreatedAt != 2000 {
		t.Fatalf("upload after update: %+v", list[0])
	}
}

func TestDeleteUploadsForJob(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "cleanup", Type: "build"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := s.SaveUpload(id, "png", "image/png", []byte("x")); err != nil {
		t.Fatalf("SaveUpload: %v", err)
	}
	if err := s.DeleteUploadsForJob(id); err != nil {
		t.Fatalf("DeleteUploadsForJob: %v", err)
	}
	list, err := s.ListUploads(id)
	if err != nil {
		t.Fatalf("ListUploads: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("uploads not deleted: %+v", list)
	}
}
