package jobs

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestArtifactPreserveListNestedHiddenFlatten covers the bug preserveArtifacts
// used to have: a flat os.ReadDir loop that skipped every subdirectory entry
// outright, silently dropping nested artifacts (e.g. figures/a.svg) at
// teardown. artifactPreserveList must recurse into subdirectories like
// ListArtifacts does, skip hidden files/dirs, and flatten nested rel paths
// into pluto's flat namespace by replacing "/" with "-".
func TestArtifactPreserveListNestedHiddenFlatten(t *testing.T) {
	root := t.TempDir()
	write := func(rel string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("top.md")
	write("figures/a.svg")
	write("figures/sub/b.svg")
	write(".hidden-file")
	write(".hidden-dir/inside.md") // must be skipped entirely: whole subtree hidden

	entries, err := artifactPreserveList(root)
	if err != nil {
		t.Fatalf("artifactPreserveList: %v", err)
	}

	var dests []string
	byDest := map[string]string{}
	for _, e := range entries {
		dests = append(dests, e.dest)
		byDest[e.dest] = e.src
	}
	sort.Strings(dests)
	want := []string{"figures-a.svg", "figures-sub-b.svg", "top.md"}
	if len(dests) != len(want) {
		t.Fatalf("want dests %v, got %v", want, dests)
	}
	for i, w := range want {
		if dests[i] != w {
			t.Fatalf("want dests %v, got %v", want, dests)
		}
	}
	if got := byDest["figures-a.svg"]; got != filepath.Join(root, "figures/a.svg") {
		t.Fatalf("figures-a.svg src mismatch: %q", got)
	}
	if got := byDest["figures-sub-b.svg"]; got != filepath.Join(root, "figures/sub/b.svg") {
		t.Fatalf("figures-sub-b.svg src mismatch: %q", got)
	}
}

// TestArtifactPreserveListMissingRoot mirrors the old os.ReadDir behavior: a
// nonexistent artifacts dir (no cell, or nothing produced yet) reads as
// "nothing to preserve", not an error.
func TestArtifactPreserveListMissingRoot(t *testing.T) {
	entries, err := artifactPreserveList(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing root must not error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("want no entries for a missing root, got %+v", entries)
	}
}

// TestArtifactPreserveListEmptyDir covers the plain (no subdirs) case still
// works the same as the old flat loop.
func TestArtifactPreserveListEmptyDir(t *testing.T) {
	root := t.TempDir()
	entries, err := artifactPreserveList(root)
	if err != nil {
		t.Fatalf("empty dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("want no entries for an empty dir, got %+v", entries)
	}
}

// TestArtifactPreserveListDestCollisionDeduped covers finding #5: a nested
// figures/a.svg and a top-level file literally named figures-a.svg both
// flatten to the same dest name. The old code let the second walk hit
// silently clobber the dest mapping in the caller's mind (both entries
// existed with the identical dest, and preserveArtifacts' never-overwrite
// keep() meant only the first-copied one ever reached pluto — the other was
// then destroyed at teardown with nothing preserved for it at all).
// artifactPreserveList must instead de-dupe deterministically so every file
// gets a distinct dest and nothing is silently dropped. A THIRD file,
// literally named figures-a-2.svg, additionally covers the closure bug in
// the original dedupeDestName rewrite: counting collisions only against the
// ORIGINAL name (not every candidate it hands out) would generate
// "figures-a-2.svg" for both the second and third file here, silently
// re-colliding on the very suffix meant to resolve the first collision.
func TestArtifactPreserveListDestCollisionDeduped(t *testing.T) {
	root := t.TempDir()
	write := func(rel string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// figures/a.svg flattens to "figures-a.svg"; a top-level file literally
	// named "figures-a.svg" collides with it, and a further top-level file
	// literally named "figures-a-2.svg" collides with the suffixed name the
	// second collision would otherwise be handed.
	write("figures/a.svg")
	write("figures-a.svg")
	write("figures-a-2.svg")

	entries, err := artifactPreserveList(root)
	if err != nil {
		t.Fatalf("artifactPreserveList: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("want 3 entries, got %+v", entries)
	}
	dests := map[string]bool{}
	for _, e := range entries {
		if dests[e.dest] {
			t.Fatalf("dest %q claimed twice: %+v", e.dest, entries)
		}
		dests[e.dest] = true
	}
	if len(dests) != 3 {
		t.Fatalf("want 3 distinct dests, got %+v", dests)
	}
	if !dests["figures-a.svg"] {
		t.Fatalf("want one entry to keep the plain dest name, got %+v", entries)
	}
	// the two collisions must both be de-duped with numeric suffixes before
	// the extension, not silently dropped or re-collided with each other.
	suffixed := 0
	for d := range dests {
		if d != "figures-a.svg" && strings.HasPrefix(d, "figures-a") && strings.HasSuffix(d, ".svg") {
			suffixed++
		}
	}
	if suffixed != 2 {
		t.Fatalf("want both colliding entries de-duped with distinct suffixes, got dests %+v", dests)
	}
}

// TestArtifactPreserveListSkipsNonRegularFiles covers findings #6 and #3.
// #6: a DANGLING symlink inside artifacts/ used to hard-block Done forever,
// since keep()'s os.ReadFile on it either followed it to who-knows-where or
// failed outright, either way tripping firstErr and refusing teardown —
// dangling symlinks (and other non-regular entries, e.g. a device) must be
// skipped instead. #3: a VALID symlink must NOT be skipped — ListArtifacts
// and the store sync both still preserve it (they follow it via ReadFile),
// so a blanket d.Type().IsRegular() check (lstat-based: false for every
// symlink, valid or not) silently dropped a legitimate artifact those other
// read paths still saw. artifactPreserveList must follow-then-check
// (os.Stat) instead, so only a symlink that fails to resolve to a regular
// file is skipped.
func TestArtifactPreserveListSkipsNonRegularFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "real.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// a dangling symlink: the target need not exist for this to matter — a
	// follow-then-check os.Stat errors on it just like the old lstat-based
	// check read false for it.
	if err := os.Symlink(filepath.Join(root, "does-not-exist"), filepath.Join(root, "dangling-link")); err != nil {
		t.Fatal(err)
	}
	// a VALID symlink to a regular file: must be preserved, not skipped.
	if err := os.Symlink(filepath.Join(root, "real.md"), filepath.Join(root, "valid-link.md")); err != nil {
		t.Fatal(err)
	}

	entries, err := artifactPreserveList(root)
	if err != nil {
		t.Fatalf("artifactPreserveList must not error on a symlink: %v", err)
	}
	dests := map[string]bool{}
	for _, e := range entries {
		dests[e.dest] = true
	}
	if len(entries) != 2 {
		t.Fatalf("want real.md and the valid symlink preserved, dangling symlink skipped, got %+v", entries)
	}
	if !dests["real.md"] {
		t.Fatalf("want real.md preserved, got %+v", entries)
	}
	if !dests["valid-link.md"] {
		t.Fatalf("want the valid symlink preserved, got %+v", entries)
	}
}

// TestArtifactPreserveListNonDirRoot covers the WalkDir-root-is-a-file guard
// (finding #6): a careless hand-edit could leave a plain file where the
// artifacts dir is expected. That must read as "nothing to preserve", not
// error the whole walk.
func TestArtifactPreserveListNonDirRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts-is-a-file")
	if err := os.WriteFile(root, []byte("oops"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := artifactPreserveList(root)
	if err != nil {
		t.Fatalf("non-dir root must not error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("want no entries for a non-dir root, got %+v", entries)
	}
}

// TestArtifactPreserveListPropagatesWalkErrors covers finding #7: an
// unreadable entry inside artifacts/ (e.g. a permission-denied subdir) used
// to be silently skipped mid-walk, with artifactPreserveList always
// returning a nil error regardless — the exact information
// preserveArtifacts needs, since it gates cell-dir DESTRUCTION on having
// preserved everything. The walk error must now propagate.
func TestArtifactPreserveListPropagatesWalkErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits don't block reads")
	}
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "secret.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(blocked, 0o755) // let t.TempDir's cleanup remove it

	if _, err := artifactPreserveList(root); err == nil {
		t.Fatalf("want a propagated walk error for an unreadable subdir")
	}
}

// TestPreserveArtifactsRefusesOnWalkError covers the other half of finding
// #7: preserveArtifacts must fold a non-nil artifactPreserveList error into
// firstErr so teardown is refused, instead of proceeding on a silently
// partial listing (which would then destroy the unread file at teardown
// with nothing preserved for it).
func TestPreserveArtifactsRefusesOnWalkError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits don't block reads")
	}
	t.Setenv("HOME", t.TempDir())
	plutoDir := t.TempDir()
	t.Setenv("KRAKEN_PLUTO_DIR", plutoDir)

	j := &Job{Slug: "preserve-walk-error"}
	blocked := filepath.Join(CellArtifacts(j), "blocked")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "secret.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(blocked, 0o755)

	if _, err := preserveArtifacts(j, "/unused-repo-for-build-jobs"); err == nil {
		t.Fatalf("want preserveArtifacts to refuse (return an error) on an unreadable artifacts subdir")
	}
}

// TestPreserveArtifactsSkipsSymlinkInArtifacts covers finding #6 end to end:
// a symlink sitting in artifacts/ alongside a real file must not block
// preservation (and, by extension, must not block Done) — only the real
// file is copied, and preserveArtifacts returns no error.
func TestPreserveArtifactsSkipsSymlinkInArtifacts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	plutoDir := t.TempDir()
	t.Setenv("KRAKEN_PLUTO_DIR", plutoDir)

	j := &Job{Slug: "preserve-symlink"}
	artDir := CellArtifacts(j)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artDir, "real.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(artDir, "does-not-exist"), filepath.Join(artDir, "dangling-link")); err != nil {
		t.Fatal(err)
	}

	copied, err := preserveArtifacts(j, "/unused-repo-for-build-jobs")
	if err != nil {
		t.Fatalf("preserveArtifacts must not be blocked by a symlink: %v", err)
	}
	if len(copied) != 1 {
		t.Fatalf("want only real.md preserved, got %+v", copied)
	}
}

// TestPreserveArtifactsReservedQuestionsDestFlattensAside covers finding #2:
// an artifact literally named artifacts/questions.yaml flattens (via
// artifactPreserveList) to the SAME pluto dest, <slug>.questions.yaml, that
// preserveArtifacts already uses for the cell's real doc-questions file.
// The real questions.yaml copy runs first, and keep() never overwrites — so
// pre-fix, the artifact's content was silently discarded at teardown with
// nothing preserved for it. artifactPreserveList must pre-claim
// "questions.yaml" so this artifact is de-duped aside to "questions-2.yaml",
// and preserveArtifacts must end up preserving BOTH files.
func TestPreserveArtifactsReservedQuestionsDestFlattensAside(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	plutoDir := t.TempDir()
	t.Setenv("KRAKEN_PLUTO_DIR", plutoDir)

	j := &Job{Slug: "reserved-questions"}
	artDir := CellArtifacts(j)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artDir, "questions.yaml"), []byte("artifact content"), 0o644); err != nil {
		t.Fatal(err)
	}
	qPath := filepath.Join(cellDir(j), "questions.yaml")
	if err := os.WriteFile(qPath, []byte("- q: real\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	copied, err := preserveArtifacts(j, "/unused-repo-for-build-jobs")
	if err != nil {
		t.Fatalf("preserveArtifacts: %v", err)
	}
	if len(copied) != 2 {
		t.Fatalf("want both the real questions file and the artifact preserved, got %+v", copied)
	}

	realDst := filepath.Join(plutoDir, "reserved-questions.questions.yaml")
	artDst := filepath.Join(plutoDir, "reserved-questions.questions-2.yaml")
	if data, err := os.ReadFile(realDst); err != nil || string(data) != "- q: real\n" {
		t.Fatalf("real questions file not preserved: data=%q err=%v", data, err)
	}
	if data, err := os.ReadFile(artDst); err != nil || string(data) != "artifact content" {
		t.Fatalf("artifact aliased to questions.yaml not preserved aside: data=%q err=%v", data, err)
	}
}

// TestPreserveArtifactsCopiesQuestionsFile passes a bogus repo path (Q14:
// the root-level *.md sweep is unconditional now, but ExistingWorktree
// resolves to "" for a nonexistent repo, so the sweep itself is a no-op
// here) exercising the full function: nested artifacts and questions.yaml
// both land in the pluto dir under <slug>.<name>, and a second call never
// overwrites either.
func TestPreserveArtifactsCopiesQuestionsFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	plutoDir := t.TempDir()
	t.Setenv("KRAKEN_PLUTO_DIR", plutoDir)

	j := &Job{Slug: "preserve-me"}
	artDir := CellArtifacts(j)
	if err := os.MkdirAll(filepath.Join(artDir, "figures"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artDir, "figures", "a.svg"), []byte("svg"), 0o644); err != nil {
		t.Fatal(err)
	}
	qPath := filepath.Join(cellDir(j), "questions.yaml")
	if err := os.WriteFile(qPath, []byte("- q: one\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	copied, err := preserveArtifacts(j, "/unused-repo-for-build-jobs")
	if err != nil {
		t.Fatalf("preserveArtifacts: %v", err)
	}
	if len(copied) != 2 {
		t.Fatalf("want 2 files copied, got %+v", copied)
	}

	figDst := filepath.Join(plutoDir, "preserve-me.figures-a.svg")
	qDst := filepath.Join(plutoDir, "preserve-me.questions.yaml")
	if data, err := os.ReadFile(figDst); err != nil || string(data) != "svg" {
		t.Fatalf("figures-a.svg not preserved: data=%q err=%v", data, err)
	}
	if data, err := os.ReadFile(qDst); err != nil || string(data) != "- q: one\n" {
		t.Fatalf("questions.yaml not preserved: data=%q err=%v", data, err)
	}

	// change both sources, run again: never-overwrite means the pluto
	// copies must stay exactly as they were the first time.
	if err := os.WriteFile(filepath.Join(artDir, "figures", "a.svg"), []byte("svg-v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(qPath, []byte("- q: two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := preserveArtifacts(j, "/unused-repo-for-build-jobs"); err != nil {
		t.Fatalf("preserveArtifacts (second pass): %v", err)
	}
	if data, _ := os.ReadFile(figDst); string(data) != "svg" {
		t.Fatalf("figures-a.svg was overwritten: %q", data)
	}
	if data, _ := os.ReadFile(qDst); string(data) != "- q: one\n" {
		t.Fatalf("questions.yaml was overwritten: %q", data)
	}
}
