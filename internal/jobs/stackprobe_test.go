package jobs

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
)

// stackprobe_test.go — ParseStackTarget's classification table, the probe's
// dial gate and write-on-change rule (stackprobe.go), the writers' cap and
// label handling (jobstack.go), and the teardown delete. The obligations are
// 52-testing-plan.md's probe and teardown rows.

func TestParseStackTargetTable(t *testing.T) {
	cases := []struct {
		raw      string
		host     string
		port     string
		loopback bool
		wantErr  string
	}{
		{"http://localhost:5173", "localhost", "5173", true, ""},
		{"http://127.0.0.1", "127.0.0.1", "80", true, ""},
		{"https://[::1]:8443/x", "::1", "8443", true, ""},
		{"http://0.0.0.0:3000", "0.0.0.0", "3000", true, ""},
		{"https://abc.ngrok.app", "abc.ngrok.app", "443", false, ""},
		{"http://192.168.1.20:8000", "192.168.1.20", "8000", false, ""},
		{"  http://LOCALHOST:9/path?q=1  ", "LOCALHOST", "9", true, ""},
		{"javascript:alert(1)", "", "", false, "scheme must be http or https"},
		{"file:///etc/passwd", "", "", false, "scheme must be http or https"},
		{"ftp://localhost:21", "", "", false, "scheme must be http or https"},
		{"http://u:p@localhost:5173", "", "", false, "must not carry credentials"},
		{"http://", "", "", false, "no host"},
		{"localhost:5173", "", "", false, "scheme must be http or https"},
		{"not a url at all ://", "", "", false, "not a url"},
	}
	for _, c := range cases {
		got, err := ParseStackTarget(c.raw)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%q: err = %v, want containing %q", c.raw, err, c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected err %v", c.raw, err)
			continue
		}
		if got.Host != c.host || got.Port != c.port || got.Loopback != c.loopback {
			t.Errorf("%q: got %+v, want host %q port %q loopback %v", c.raw, got, c.host, c.port, c.loopback)
		}
	}
}

// probeFixture wires a store, one live job with a live cell session, and a
// recording fake dialer. Returns the job and the slice of addresses dialled.
func probeFixture(t *testing.T, dialErr error) (*store.Store, *Job, map[string]bool, *[]string) {
	t.Helper()
	st := useTestStore(t)
	sj := mustCreateJob(t, st, store.Job{Slug: "probe-job", Repo: "kraken", State: "working"})
	j := &Job{ID: sj.ID, Slug: sj.Slug, Repo: sj.Repo, State: "working"}
	live := map[string]bool{CellSession(j): true}
	dialled := &[]string{}
	prev := stackDial
	stackDial = func(addr string) error {
		*dialled = append(*dialled, addr)
		return dialErr
	}
	t.Cleanup(func() { stackDial = prev })
	return st, j, live, dialled
}

func TestStackProbeDialsLoopbackOnlyAndWritesOnChange(t *testing.T) {
	st, j, live, dialled := probeFixture(t, nil)
	st.UpsertStack(j.ID, "http://localhost:5173", "ui", 1)
	st.UpsertStack(j.ID, "https://abc.ngrok.app", "tunnel", 1)

	now := time.UnixMilli(5000)
	stackProbePass(st, j, live, now)
	if len(*dialled) != 1 || (*dialled)[0] != "localhost:5173" {
		t.Fatalf("dialled = %v, want exactly [localhost:5173] (non-loopback never dialled)", *dialled)
	}
	links, _ := st.ListStacks(j.ID)
	if links[0].Status != StackUp || links[0].ProbedAt != 5000 {
		t.Fatalf("loopback row = %+v, want up @5000", links[0])
	}
	if links[1].Status != StackUnknown || links[1].ProbedAt != 0 {
		t.Fatalf("non-loopback row = %+v, want unknown, never probed", links[1])
	}

	// second pass, same answer: no write (probed_at must not move)
	stackProbePass(st, j, live, time.UnixMilli(9000))
	links, _ = st.ListStacks(j.ID)
	if links[0].ProbedAt != 5000 {
		t.Fatalf("unchanged status rewrote the row: probed_at %d, want 5000", links[0].ProbedAt)
	}
}

func TestStackProbeFlipsToDownOnRefusal(t *testing.T) {
	st, j, live, _ := probeFixture(t, errors.New("connection refused"))
	st.UpsertStack(j.ID, "http://127.0.0.1:7878", "board", 1)
	stackProbePass(st, j, live, time.UnixMilli(100))
	links, _ := st.ListStacks(j.ID)
	if links[0].Status != StackDown {
		t.Fatalf("status = %q, want down", links[0].Status)
	}
}

func TestStackProbeSkipsJobsWithoutLiveCell(t *testing.T) {
	st, j, _, dialled := probeFixture(t, nil)
	st.UpsertStack(j.ID, "http://127.0.0.1:7878", "board", 1)
	stackProbePass(st, j, map[string]bool{}, time.Now())
	if len(*dialled) != 0 {
		t.Fatalf("dialled %v for a job with no live cell", *dialled)
	}
	// nil store is a no-op too
	stackProbePass(nil, j, map[string]bool{CellSession(j): true}, time.Now())
}

// TestStackProbeRealListener runs the real dialer against a real loopback
// listener, open and then closed: the one test that proves the dial itself.
func TestStackProbeRealListener(t *testing.T) {
	st := useTestStore(t)
	sj := mustCreateJob(t, st, store.Job{Slug: "probe-real", Repo: "kraken", State: "working"})
	j := &Job{ID: sj.ID, Slug: sj.Slug, Repo: sj.Repo, State: "working"}
	live := map[string]bool{CellSession(j): true}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	url := "http://" + ln.Addr().String()
	st.UpsertStack(j.ID, url, "", 1)

	stackProbePass(st, j, live, time.Now())
	if links, _ := st.ListStacks(j.ID); links[0].Status != StackUp {
		t.Fatalf("with listener: status %q, want up", links[0].Status)
	}
	ln.Close()
	stackProbePass(st, j, live, time.Now())
	if links, _ := st.ListStacks(j.ID); links[0].Status != StackDown {
		t.Fatalf("after close: status %q, want down", links[0].Status)
	}
}

func TestAddStackLinkEnforcesCapAndAllowsReAdd(t *testing.T) {
	st := useTestStore(t)
	sj := mustCreateJob(t, st, store.Job{Slug: "cap-job", Repo: "kraken", State: "working"})
	j := &Job{ID: sj.ID, Slug: sj.Slug}
	for i := 0; i < StackLinkMax; i++ {
		if err := AddStackLink(j, "http://localhost:"+string(rune('1'+i))+"000", ""); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if err := AddStackLink(j, "http://localhost:9999", ""); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("ninth add = %v, want a cap refusal", err)
	}
	// re-adding an existing url at the cap is a relabel, not a ninth link
	if err := AddStackLink(j, "http://localhost:1000", "first"); err != nil {
		t.Fatalf("re-add at cap: %v", err)
	}
	if links := ListStackLinks(j); len(links) != StackLinkMax || links[0].Label != "first" {
		t.Fatalf("after re-add: %d links, first label %q", len(links), links[0].Label)
	}
}

func TestStackWritersRefuseWithoutStore(t *testing.T) {
	UseStore(nil)
	j := &Job{ID: 1, Slug: "nostore"}
	for name, fn := range map[string]func() error{
		"add":   func() error { return AddStackLink(j, "http://localhost:1", "") },
		"rm":    func() error { return RemoveStackLink(j, "x") },
		"clear": func() error { return ClearStackLinks(j) },
	} {
		if err := fn(); err == nil || !strings.Contains(err.Error(), "no store") {
			t.Errorf("%s without store = %v, want a no-store refusal", name, err)
		}
	}
	if links := ListStackLinks(j); links != nil {
		t.Errorf("list without store = %v, want nil", links)
	}
	if err := DeleteStacksForTeardown(j); err != nil {
		t.Errorf("teardown delete without store = %v, want nil", err)
	}
}

func TestRemoveStackLinkNotFoundIsErrNotFound(t *testing.T) {
	st := useTestStore(t)
	sj := mustCreateJob(t, st, store.Job{Slug: "rm-job", Repo: "kraken", State: "working"})
	j := &Job{ID: sj.ID, Slug: sj.Slug}
	if err := RemoveStackLink(j, "nothing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want wrapping store.ErrNotFound", err)
	}
}

func TestDeleteStacksForTeardownClearsOnlyThatJob(t *testing.T) {
	st := useTestStore(t)
	a := mustCreateJob(t, st, store.Job{Slug: "td-a", Repo: "kraken", State: "working"})
	b := mustCreateJob(t, st, store.Job{Slug: "td-b", Repo: "kraken", State: "working"})
	st.UpsertStack(a.ID, "http://localhost:1", "", 1)
	st.UpsertStack(b.ID, "http://localhost:2", "", 1)
	if err := DeleteStacksForTeardown(&Job{ID: a.ID, Slug: a.Slug}); err != nil {
		t.Fatalf("DeleteStacksForTeardown: %v", err)
	}
	if got, _ := st.ListStacks(a.ID); len(got) != 0 {
		t.Fatalf("a still has %v", got)
	}
	if got, _ := st.ListStacks(b.ID); len(got) != 1 {
		t.Fatalf("b lost its row: %v", got)
	}
}
