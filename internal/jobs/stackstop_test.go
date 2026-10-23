package jobs

import (
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"kraken/internal/store"
)

// stackStopFakes swaps the three process seams for the test's duration:
// listeners by port, (start time, command) by pid, and a recorder for stops.
type stackStopFakes struct {
	listeners map[string][]int
	started   map[int]time.Time
	commands  map[int]string
	stopped   []int
}

func installStackStopFakes(t *testing.T, f *stackStopFakes) {
	t.Helper()
	origL, origI, origS := stackListeners, stackProcInfo, stackStop
	t.Cleanup(func() { stackListeners, stackProcInfo, stackStop = origL, origI, origS })
	stackListeners = func(port string) ([]int, error) { return f.listeners[port], nil }
	stackProcInfo = func(pid int) (time.Time, string, error) {
		c, ok := f.commands[pid]
		if !ok {
			return time.Time{}, "", errors.New("no such process")
		}
		return f.started[pid], c, nil
	}
	stackStop = func(pid int) error { f.stopped = append(f.stopped, pid); return nil }
}

// linkAt is the registration instant every fixture link carries; a listener
// started before it is "the one the cell registered".
var linkAt = time.Date(2030, 1, 2, 3, 4, 5, 0, time.Local)

func fixtureLink(url, label string) store.StackLink {
	return store.StackLink{URL: url, Label: label, At: linkAt.UnixMilli()}
}

func TestIsKrakenServeCommand(t *testing.T) {
	cases := map[string]bool{
		"/tmp/kraken-test serve -p 7801":               true,
		"/opt/scratch/kraken-scratch serve -p 7802":    true,
		"/Users/u/.local/bin/kraken serve":             true,
		"/Users/u/.local/bin/k serve -p 7801":          true,
		"kraken serve -p 7801 --tailnet":               true,
		"kraken ls":                                    false,
		"kraken":                                       false,
		"node /app/node_modules/.bin/vite --port 5199": false,
		"/usr/bin/python3 -m http.server 7801":         false,
		"/bin/zsh -c kraken serve":                     false,
		"/opt/krakenoid/bin/other serve":               false,
		"":                                             false,
	}
	for cmd, want := range cases {
		if got := isKrakenServeCommand(cmd); got != want {
			t.Errorf("isKrakenServeCommand(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestParseProcInfoLine(t *testing.T) {
	started, cmd, err := parseProcInfoLine("Wed Jan  2 03:04:05 2030 /tmp/kraken-test serve -p 7801")
	if err != nil {
		t.Fatal(err)
	}
	if !started.Equal(time.Date(2030, 1, 2, 3, 4, 5, 0, time.Local)) {
		t.Fatalf("started = %v", started)
	}
	if cmd != "/tmp/kraken-test serve -p 7801" {
		t.Fatalf("cmd = %q", cmd)
	}
	if _, _, err := parseProcInfoLine("garbage"); err == nil {
		t.Fatal("a short line must be an error")
	}
	if _, _, err := parseProcInfoLine("not a date at all here /bin/x"); err == nil {
		t.Fatal("an unparseable start time must be an error")
	}
}

// A loopback link whose listener is a kraken serve started before the link
// was registered is stopped; a dev server on another registered port is left
// running with a reason, not stopped.
func TestStackServesToStopStopsOnlyKrakenServes(t *testing.T) {
	f := &stackStopFakes{
		listeners: map[string][]int{"7801": {501}, "5199": {77}},
		started:   map[int]time.Time{501: linkAt.Add(-time.Hour), 77: linkAt.Add(-time.Hour)},
		commands:  map[int]string{501: "/tmp/kraken-test serve -p 7801", 77: "node /app/node_modules/.bin/vite --port 5199"},
	}
	installStackStopFakes(t, f)
	links := []store.StackLink{
		fixtureLink("http://127.0.0.1:7801", "test-serve"),
		fixtureLink("http://127.0.0.1:5199/page", "dev page"),
	}
	got := stackServesToStop(links, nil, "7777", 1)
	if len(got) != 2 {
		t.Fatalf("decisions = %+v, want 2", got)
	}
	byPID := map[int]stackStopDecision{}
	for _, d := range got {
		byPID[d.PID] = d
	}
	if d := byPID[501]; !d.Stop || d.URL != "http://127.0.0.1:7801" {
		t.Fatalf("kraken serve decision = %+v, want Stop on its link", d)
	}
	if d := byPID[77]; d.Stop || d.Reason != "not a kraken serve" {
		t.Fatalf("dev server decision = %+v, want left running as not a kraken serve", d)
	}
}

// Neither board is ever a target: the tearing-down daemon's own pid and bound
// port are excluded, and so is the default serve port whoever is tearing
// down — a parallel serve's cell registering the real board's address must
// not take the board down.
func TestStackServesToStopNeverTargetsABoard(t *testing.T) {
	f := &stackStopFakes{
		listeners: map[string][]int{"7777": {42}, "7801": {42, 9}, "7802": {11}},
		started:   map[int]time.Time{42: linkAt.Add(-time.Hour), 9: linkAt.Add(-time.Hour), 11: linkAt.Add(-time.Hour)},
		commands: map[int]string{
			42: "/Users/u/.local/bin/kraken serve",
			9:  "/tmp/kraken-test serve -p 7801",
			11: "/tmp/kraken-test serve -p 7802",
		},
	}
	installStackStopFakes(t, f)
	links := []store.StackLink{
		fixtureLink("http://localhost:7777", "board"),
		fixtureLink("http://127.0.0.1:7801", "test-serve"),
		fixtureLink("http://127.0.0.1:7802", "own"),
	}
	// A parallel serve bound on 7802 (pid 42 in this fixture is unrelated to
	// it, which is what makes the own-port exclusion load-bearing).
	got := stackServesToStop(links, nil, "7802", 42)
	if len(got) != 1 || got[0].PID != 9 || !got[0].Stop {
		t.Fatalf("decisions = %+v, want exactly pid 9 stopped", got)
	}
}

// A port another job's stack row still names is that job's to stop.
func TestStackServesToStopSkipsPortsOtherJobsRegister(t *testing.T) {
	f := &stackStopFakes{
		listeners: map[string][]int{"7801": {9}},
		started:   map[int]time.Time{9: linkAt.Add(-time.Hour)},
		commands:  map[int]string{9: "/tmp/kraken-test serve -p 7801"},
	}
	installStackStopFakes(t, f)
	links := []store.StackLink{fixtureLink("http://127.0.0.1:7801", "test-serve")}
	got := stackServesToStop(links, map[string]bool{"7801": true}, "7777", 1)
	if len(got) != 1 || got[0].Stop || got[0].Reason != "another job still registers this port" {
		t.Fatalf("decisions = %+v, want left running for the sibling", got)
	}
	if len(f.stopped) != 0 {
		t.Fatalf("stopped = %v, want nothing", f.stopped)
	}
}

// A kraken serve that started after the link was registered is not the one
// the cell registered: the recipe names one port, so a later cell's serve can
// be sitting on it.
func TestStackServesToStopSkipsListenersStartedAfterTheLink(t *testing.T) {
	f := &stackStopFakes{
		listeners: map[string][]int{"7801": {9, 10}},
		started:   map[int]time.Time{9: linkAt.Add(2 * time.Second), 10: linkAt.Add(-2 * time.Second)},
		commands:  map[int]string{9: "/tmp/kraken-test serve -p 7801", 10: "/tmp/kraken-test serve -p 7801"},
	}
	installStackStopFakes(t, f)
	links := []store.StackLink{fixtureLink("http://127.0.0.1:7801", "test-serve")}
	got := stackServesToStop(links, nil, "7777", 1)
	if len(got) != 2 {
		t.Fatalf("decisions = %+v, want 2", got)
	}
	for _, d := range got {
		switch d.PID {
		case 9:
			if d.Stop || d.Reason != "started after the link was registered" {
				t.Fatalf("late listener = %+v, want left running", d)
			}
		case 10:
			if !d.Stop {
				t.Fatalf("earlier listener = %+v, want stopped", d)
			}
		}
	}
}

// A non-loopback link is never inspected: nothing on this machine listens for
// an ngrok host, and a remote listener is not kraken's to stop.
func TestStackServesToStopSkipsNonLoopbackAndBadURLs(t *testing.T) {
	calls := 0
	origL := stackListeners
	t.Cleanup(func() { stackListeners = origL })
	stackListeners = func(port string) ([]int, error) { calls++; return nil, nil }
	links := []store.StackLink{
		fixtureLink("https://abc.ngrok.app", "tunnel"),
		fixtureLink("http://192.168.1.4:7801", "lan"),
		fixtureLink("not a url at all", "bad"),
	}
	if got := stackServesToStop(links, nil, "7777", 1); len(got) != 0 || calls != 0 {
		t.Fatalf("decisions = %+v, listener calls = %d; want none", got, calls)
	}
}

// A listener-listing failure on one link never blocks the others, and a pid
// whose process table entry cannot be read is left running with the reason.
func TestStackServesToStopSurvivesListingAndPsErrors(t *testing.T) {
	f := &stackStopFakes{
		listeners: map[string][]int{"7802": {11, 12}},
		started:   map[int]time.Time{11: linkAt.Add(-time.Hour)},
		commands:  map[int]string{11: "/tmp/kraken-test serve -p 7802"},
	}
	installStackStopFakes(t, f)
	origL := stackListeners
	t.Cleanup(func() { stackListeners = origL })
	stackListeners = func(port string) ([]int, error) {
		if port == "7801" {
			return nil, errors.New("lsof: boom")
		}
		return f.listeners[port], nil
	}
	links := []store.StackLink{fixtureLink("http://127.0.0.1:7801", "a"), fixtureLink("http://127.0.0.1:7802", "b")}
	got := stackServesToStop(links, nil, "7777", 1)
	if len(got) != 2 {
		t.Fatalf("decisions = %+v, want 2 for the 7802 listeners despite the 7801 error", got)
	}
	for _, d := range got {
		switch d.PID {
		case 11:
			if !d.Stop {
				t.Fatalf("pid 11 = %+v, want stopped", d)
			}
		case 12:
			if d.Stop || d.Reason == "" {
				t.Fatalf("pid 12 = %+v, want left running with a reason", d)
			}
		}
	}
}

// End to end through the store: StopStackServesForTeardown reads the job's
// links and every other job's, stops the kraken serve, leaves the dev server
// and the sibling's port, and a job with no links or no id is a quiet nil.
func TestStopStackServesForTeardownThroughTheStore(t *testing.T) {
	st := useTestStore(t)
	sj := mustCreateJob(t, st, store.Job{Slug: "demo", Repo: "kraken", State: "working"})
	sib := mustCreateJob(t, st, store.Job{Slug: "sibling", Repo: "kraken", State: "working"})
	j := &Job{ID: sj.ID, Slug: sj.Slug}
	for _, l := range []struct {
		job   int64
		url   string
		label string
	}{
		{sj.ID, "http://127.0.0.1:7801", "test-serve"},
		{sj.ID, "http://127.0.0.1:5199/page", "dev page"},
		{sj.ID, "http://127.0.0.1:7803", "shared"},
		{sib.ID, "http://127.0.0.1:7803", "mine too"},
	} {
		if err := st.UpsertStack(l.job, l.url, l.label, linkAt.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	f := &stackStopFakes{
		listeners: map[string][]int{"7801": {501}, "5199": {77}, "7803": {88}},
		started:   map[int]time.Time{501: linkAt.Add(-time.Hour), 77: linkAt.Add(-time.Hour), 88: linkAt.Add(-time.Hour)},
		commands:  map[int]string{501: "/tmp/kraken-test serve -p 7801", 77: "node vite", 88: "/tmp/kraken-test serve -p 7803"},
	}
	installStackStopFakes(t, f)
	if err := StopStackServesForTeardown(j); err != nil {
		t.Fatalf("StopStackServesForTeardown: %v", err)
	}
	if len(f.stopped) != 1 || f.stopped[0] != 501 {
		t.Fatalf("stopped = %v, want [501] (the dev server and the sibling's port left alone)", f.stopped)
	}
	if err := StopStackServesForTeardown(&Job{ID: 0, Slug: "none"}); err != nil {
		t.Fatalf("no-id job: %v", err)
	}
	if err := StopStackServesForTeardown(&Job{ID: sj.ID + 100, Slug: "bare"}); err != nil || len(f.stopped) != 1 {
		t.Fatalf("no-links job: err %v, stopped %v", err, f.stopped)
	}
}

// A revision round restarts the parallel serve after the first registration;
// the skill's re-add before the next gate line refreshes the row's time, so
// the restarted serve is still the one the cell registered and is stopped.
func TestStopStackServesForTeardownAfterARevisionRound(t *testing.T) {
	st := useTestStore(t)
	sj := mustCreateJob(t, st, store.Job{Slug: "demo", Repo: "kraken", State: "working"})
	j := &Job{ID: sj.ID, Slug: sj.Slug}
	first := linkAt.Add(-2 * time.Hour)
	restarted := linkAt.Add(-30 * time.Minute)
	if err := st.UpsertStack(j.ID, "http://127.0.0.1:7801", "test-serve", first.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	// the re-add before the second `manual test ready:` line
	if err := st.UpsertStack(j.ID, "http://127.0.0.1:7801", "test-serve", linkAt.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	f := &stackStopFakes{
		listeners: map[string][]int{"7801": {501}},
		started:   map[int]time.Time{501: restarted},
		commands:  map[int]string{501: "/tmp/kraken-test serve -p 7801"},
	}
	installStackStopFakes(t, f)
	if err := StopStackServesForTeardown(j); err != nil {
		t.Fatal(err)
	}
	if len(f.stopped) != 1 || f.stopped[0] != 501 {
		t.Fatalf("stopped = %v, want [501]: the restarted serve is the one the cell last registered", f.stopped)
	}
}

// The real stackListeners seam finds this process behind a port it listens
// on, through lsof at its resolved path — the one line the fakes above never
// exercise. Skipped where lsof is absent.
func TestStackListenersRealListener(t *testing.T) {
	if _, err := lsofPath(); err != nil {
		t.Skip("lsof not available:", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	pids, err := stackListeners(port)
	if err != nil {
		t.Fatalf("stackListeners(%s): %v", port, err)
	}
	found := false
	for _, pid := range pids {
		if pid == os.Getpid() {
			found = true
		}
	}
	if !found {
		t.Fatalf("stackListeners(%s) = %v, want to contain this process (%d)", port, pids, os.Getpid())
	}
	started, cmd, err := stackProcInfo(os.Getpid())
	if err != nil || cmd == "" || started.IsZero() {
		t.Fatalf("stackProcInfo(self) = %v %q %v", started, cmd, err)
	}
}
