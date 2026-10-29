package jobs

import (
	"os"
	"strings"
	"testing"
)

// A self question in a self thread may read the job's artifacts: the run
// carries `--add-dir <cell artifacts>`, because `--restricted` confines the
// file tools to the worktree cwd and the artifacts dir is its sibling. Every
// run without the read-only tools carries no such flag.
func TestAskArgvAddsArtifactsDirForSelfTools(t *testing.T) {
	st := askContractEnv(t)
	j := askContractJob(t, st)
	askContractWorktree(t, j)
	art := CellArtifacts(j)
	if err := os.MkdirAll(art, 0o755); err != nil {
		t.Fatal(err)
	}

	selfRow := askContractRow(j, askContractSelf, "")
	argv, _, _, err := AskArgv(j, selfRow, askContractSelf, false)
	if err != nil {
		t.Fatalf("AskArgv(self, self): %v", err)
	}
	if got, n := askContractFlag(argv, "--add-dir"); n != 1 || got != art {
		t.Errorf("self run: --add-dir = %q (present %d times), want exactly once naming %q", got, n, art)
	}
	// the brief names the dir, or the run looks for artifacts in its cwd
	if !strings.Contains(askThreadBrief(j, selfRow), art) {
		t.Errorf("self brief does not name the artifacts dir %q", art)
	}
	askContractCheckForkTail(t, argv, selfRow)

	for _, c := range []struct {
		name string
		row  ThreadOrigin
		from ThreadOrigin
	}{
		{"self row, remote sender", askContractSelf, askContractRemote},
		{"remote row, self sender", askContractRemote, askContractSelf},
		{"remote row, remote sender", askContractRemote, askContractRemote},
	} {
		row := askContractRow(j, c.row, "")
		argv, _, _, err := AskArgv(j, row, c.from, true)
		if err != nil {
			t.Fatalf("AskArgv(%s): %v", c.name, err)
		}
		if askContractHas(argv, "--add-dir") {
			t.Errorf("%s: a run with no tools carries --add-dir: %q", c.name, argv)
		}
	}
}

// A cell with no artifacts dir yet gets no `--add-dir`: the flag would name a
// path that does not exist.
func TestAskArgvNoArtifactsDirNoFlag(t *testing.T) {
	st := askContractEnv(t)
	j := askContractJob(t, st)
	askContractWorktree(t, j)
	if err := os.RemoveAll(CellArtifacts(j)); err != nil {
		t.Fatal(err)
	}
	row := askContractRow(j, askContractSelf, "")
	argv, _, _, err := AskArgv(j, row, askContractSelf, true)
	if err != nil {
		t.Fatalf("AskArgv: %v", err)
	}
	if askContractHas(argv, "--add-dir") {
		t.Errorf("no artifacts dir, yet argv carries --add-dir: %q", argv)
	}
	askContractCheckTools(t, argv, AskThreadTools)
}
