package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
)

// spawnModelRow builds a usage row for the spawn-time fallback tests: the
// three windows that gate the switch, each a percent, plus the Fable window's
// reset (unix millis, 0 for none).
func spawnModelRow(fable, opus, session, weekly float64, fableResets int64) *core.AccountUsage {
	return &core.AccountUsage{
		Email:   "cell@example.com",
		Session: &core.UsageWindow{Percent: session},
		Weekly:  &core.UsageWindow{Percent: weekly},
		Models: []core.ModelWindow{
			{Name: "Fable", Percent: fable, ResetsAt: fableResets},
			{Name: "Opus", Percent: opus},
		},
	}
}

// TestSpawnModel pins the pure decision: a Fable-tier configured model is
// demoted to the fallback tier exactly when the account's Fable window is
// spent and every window the fallback would bill still has room; every
// unknown and every other exhaustion leaves the configured model alone.
func TestSpawnModel(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	fable := core.ZeusModel
	cases := []struct {
		name       string
		configured string
		acct       *core.AccountUsage
		want       string
	}{
		{"fable spent, room everywhere → fallback", fable, spawnModelRow(100, 40, 30, 50, 0), fableFallbackModel},
		{"fable past 100 still spent", fable, spawnModelRow(104, 40, 30, 50, 0), fableFallbackModel},
		{"fable short of the cap → configured", fable, spawnModelRow(99, 40, 30, 50, 0), fable},
		{"no row → configured", fable, nil, fable},
		{"no fable window at all → configured", fable, &core.AccountUsage{Session: &core.UsageWindow{Percent: 10}, Weekly: &core.UsageWindow{Percent: 10}}, fable},
		{"fable spent but window already reset → configured", fable, spawnModelRow(100, 40, 30, 50, now.Add(-time.Minute).UnixMilli()), fable},
		{"fable spent, reset still ahead → fallback", fable, spawnModelRow(100, 40, 30, 50, now.Add(time.Hour).UnixMilli()), fableFallbackModel},
		{"opus spent too → configured", fable, spawnModelRow(100, 100, 30, 50, 0), fable},
		{"session spent too → configured", fable, spawnModelRow(100, 40, 100, 50, 0), fable},
		{"weekly spent too → configured", fable, spawnModelRow(100, 40, 30, 100, 0), fable},
		{"hand-authored fable model: also falls", "claude-fable-5-1", spawnModelRow(100, 40, 30, 50, 0), fableFallbackModel},
		{"non-fable model: never touched", "sonnet", spawnModelRow(100, 40, 30, 50, 0), "sonnet"},
		{"opus configured, fable spent → untouched", core.DefaultModel, spawnModelRow(100, 0, 0, 0, 0), core.DefaultModel},
		{"empty configured → empty", "", spawnModelRow(100, 40, 30, 50, 0), ""},
	}
	for _, c := range cases {
		if got := spawnModel(c.configured, c.acct, now); got != c.want {
			t.Errorf("%s: spawnModel(%q) = %q, want %q", c.name, c.configured, got, c.want)
		}
	}
}

// TestSpawnModelNilWindowsAreRoom pins the nil-window convention the mid-run
// arm already uses: a row whose session or weekly pointer is nil is not read
// as exhausted, so a Fable-spent row with no session line still falls back.
func TestSpawnModelNilWindowsAreRoom(t *testing.T) {
	acct := &core.AccountUsage{Models: []core.ModelWindow{{Name: "Fable", Percent: 100}}}
	if got := spawnModel(core.ZeusModel, acct, time.Now()); got != fableFallbackModel {
		t.Fatalf("nil session/weekly and no Opus row must read as room: got %q", got)
	}
}

// TestOpusHasRoomSharedWithMidRunArm pins that the mid-run fallback and the
// spawn-time one read headroom through one function: a row the spawn arm
// refuses on headroom is a row the mid-run arm refuses too, for every window.
func TestOpusHasRoomSharedWithMidRunArm(t *testing.T) {
	now := time.Now()
	hit := LimitHit{At: now.Unix() - 10, Error: "rate_limit"}
	for _, row := range []*core.AccountUsage{
		spawnModelRow(100, 100, 30, 50, 0),
		spawnModelRow(100, 40, 100, 50, 0),
		spawnModelRow(100, 40, 30, 100, 0),
	} {
		if opusHasRoom(row, now) {
			t.Fatalf("row %+v reads as room", row)
		}
		if got := spawnModel(core.ZeusModel, row, now); got != core.ZeusModel {
			t.Errorf("spawn arm switched on a row with no room: %q", got)
		}
		if got := fableFallbackDecide(hit, core.ZeusModel, row, fallbackRecord{}, 0, "", now); got != fallbackNo {
			t.Errorf("mid-run arm switched on a row with no room: %q", got)
		}
	}
	open := spawnModelRow(100, 40, 30, 50, 0)
	if !opusHasRoom(open, now) {
		t.Fatal("a row with every window short reads as no room")
	}
	if got := fableFallbackDecide(hit, core.ZeusModel, open, fallbackRecord{}, 0, "", now); got != fallbackSwitch {
		t.Errorf("mid-run arm on an open row = %q, want switch", got)
	}
}

// TestOpusHasRoomHonoursAPassedReset pins the reset rule on the session and
// weekly windows: a row still reading 100% from before a reset that has
// passed is room (the snapshot is a poll), a reset still ahead is not, and an
// unknown reset is read as spent.
func TestOpusHasRoomHonoursAPassedReset(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	past, ahead := now.Add(-time.Minute).UnixMilli(), now.Add(time.Hour).UnixMilli()
	cases := []struct {
		name    string
		session *core.UsageWindow
		weekly  *core.UsageWindow
		want    bool
	}{
		{"session spent, reset passed → room", &core.UsageWindow{Percent: 100, ResetsAt: past}, &core.UsageWindow{Percent: 10}, true},
		{"session spent, reset ahead → no room", &core.UsageWindow{Percent: 100, ResetsAt: ahead}, &core.UsageWindow{Percent: 10}, false},
		{"session spent, reset unknown → no room", &core.UsageWindow{Percent: 100}, &core.UsageWindow{Percent: 10}, false},
		{"weekly spent, reset passed → room", &core.UsageWindow{Percent: 10}, &core.UsageWindow{Percent: 100, ResetsAt: past}, true},
		{"weekly spent, reset ahead → no room", &core.UsageWindow{Percent: 10}, &core.UsageWindow{Percent: 100, ResetsAt: ahead}, false},
		{"both short → room", &core.UsageWindow{Percent: 99, ResetsAt: ahead}, &core.UsageWindow{Percent: 99}, true},
	}
	for _, c := range cases {
		acct := &core.AccountUsage{Session: c.session, Weekly: c.weekly, Models: []core.ModelWindow{{Name: "Fable", Percent: 100}, {Name: "Opus", Percent: 20}}}
		if got := opusHasRoom(acct, now); got != c.want {
			t.Errorf("%s: opusHasRoom = %v, want %v", c.name, got, c.want)
		}
		wantModel := core.ZeusModel
		if c.want {
			wantModel = fableFallbackModel
		}
		if got := spawnModel(core.ZeusModel, acct, now); got != wantModel {
			t.Errorf("%s: spawnModel = %q, want %q", c.name, got, wantModel)
		}
	}
}

// TestCodexBriefNeverConsultsTheClaudeSnapshot pins the branch order in
// zeusBrief: a codex zeus runs on neither claude tier, so its brief must
// state codexZeusModel without reading the usage row, memoizing a claude
// model on the Job, or logging a fallback for a pane with no Fable window.
func TestCodexBriefNeverConsultsTheClaudeSnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	asked := swapLaunchAccount(t, spawnModelRow(100, 40, 30, 50, 0))
	// a sentinel, since an unbound job under a bare temp HOME would ask for
	// "" and the assertion below would pass on a read that happened
	*asked = "UNASKED"
	j := &Job{ID: 16, Slug: "codex-brief", Agent: "codex"}
	brief := zeusBrief(j)
	if !strings.Contains(brief, "kraken spawned you on `"+core.CodexZeusModel+"`") {
		t.Fatal("a codex brief must state core.CodexZeusModel")
	}
	if *asked != "UNASKED" {
		t.Fatalf("a codex brief read the claude usage snapshot (asked for %q)", *asked)
	}
	if j.launchModel != "" {
		t.Fatalf("a codex brief memoized a claude model on the Job: %q", j.launchModel)
	}
}

// swapLaunchAccount points the spawn-time read at a fixed row for one test,
// recording the email it was asked for.
func swapLaunchAccount(t *testing.T, row *core.AccountUsage) *string {
	t.Helper()
	prev := launchAccount
	var asked string
	launchAccount = func(email string) *core.AccountUsage {
		asked = email
		return row
	}
	t.Cleanup(func() { launchAccount = prev })
	return &asked
}

// TestZeusLaunchModel pins the launch resolution end to end: the argv and the
// brief both come out on the fallback tier when the bound account's Fable is
// spent, the row consulted is the job's OWN binding, and with no row the
// spawn is the configured model byte for byte.
func TestZeusLaunchModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	asked := swapLaunchAccount(t, spawnModelRow(100, 40, 30, 50, 0))

	j := &Job{ID: 7, Slug: "fable-spent", AccountSlug: "slot-a", AccountEmail: "slot-a@example.com"}
	argv := strings.Join(zeusSpawnArgv(j), " ")
	if !strings.Contains(argv, "--model "+fableFallbackModel) {
		t.Fatalf("a zeus on a Fable-spent account must spawn on the fallback tier: %q", argv)
	}
	if strings.Contains(argv, "--model "+core.ZeusModel) {
		t.Fatalf("argv still names the configured Fable tier: %q", argv)
	}
	if *asked != "slot-a@example.com" {
		t.Fatalf("the row consulted is not the job's binding: asked for %q", *asked)
	}
	brief := zeusBrief(j)
	if !strings.Contains(brief, "kraken spawned you on `"+fableFallbackModel+"`") {
		t.Fatal("the brief must state the tier the argv actually passed, not the configured one")
	}
	if strings.Contains(brief, "spawned you on `"+core.ZeusModel+"`") {
		t.Fatal("the brief names the configured Fable tier for a zeus spawned on Opus")
	}
	// the resume path takes the same resolution
	resumed := strings.Join(zeusSpawnArgv(&Job{ID: 8, Slug: "fable-spent-r", AccountEmail: "slot-a@example.com"}, "-r", "sess-1"), " ")
	if !strings.Contains(resumed, "--model "+fableFallbackModel) || !strings.HasSuffix(resumed, "-r sess-1") {
		t.Fatalf("reactivation argv wrong: %q", resumed)
	}

	// an unbound job asks for the machine's main identity — seeded under the
	// temp HOME so the assertion cannot pass on two empty strings
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"main@example.com"}}`), 0o644); err != nil {
		t.Fatalf("seed ~/.claude.json: %v", err)
	}
	unbound := &Job{ID: 9, Slug: "fable-unbound"}
	zeusLaunchModel(unbound)
	if *asked != "main@example.com" {
		t.Fatalf("an unbound job must consult main's row: asked for %q", *asked)
	}

	// no row: the configured model, exactly as before the feature
	swapLaunchAccount(t, nil)
	fresh := strings.Join(zeusSpawnArgv(&Job{ID: 10, Slug: "no-row"}), " ")
	if !strings.Contains(fresh, "--model "+core.ZeusModel) {
		t.Fatalf("with no usage row a fresh zeus must spawn on core.ZeusModel: %q", fresh)
	}
	// a hand-authored non-Fable model: is never consulted against the snapshot
	swapLaunchAccount(t, spawnModelRow(100, 40, 30, 50, 0))
	pinned := strings.Join(zeusSpawnArgv(&Job{ID: 11, Slug: "pinned", Model: "sonnet"}), " ")
	if !strings.Contains(pinned, "--model sonnet") {
		t.Fatalf("job model: override dropped on the spawn path: %q", pinned)
	}
}

// TestZeusLaunchModelMemoizesPerSpawn pins that one spawn chain reads the
// snapshot once: the argv and the brief agree even when the row changes
// between them, and a fresh Job (the next spawn) reads the snapshot anew.
func TestZeusLaunchModelMemoizesPerSpawn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	swapLaunchAccount(t, spawnModelRow(100, 40, 30, 50, 0))
	j := &Job{ID: 12, Slug: "memo", AccountEmail: "slot-a@example.com"}
	argv := strings.Join(zeusSpawnArgv(j), " ")
	// the usage pass lands a fresh window between the argv and the brief
	swapLaunchAccount(t, spawnModelRow(0, 40, 30, 50, 0))
	if !strings.Contains(zeusBrief(j), "spawned you on `"+fableFallbackModel+"`") {
		t.Fatalf("brief re-resolved and now disagrees with the argv %q", argv)
	}
	// the next spawn is a fresh Job and sees the fresh window
	if got := zeusLaunchModel(&Job{ID: 12, Slug: "memo", AccountEmail: "slot-a@example.com"}); got != core.ZeusModel {
		t.Fatalf("a fresh Job must read the snapshot anew: got %q", got)
	}
}

// TestSpawnCommandStampsTheModel pins the stamp: spawnCommand records the
// argv's --model in the cell dir, zeusLaunchedModel reads it back (the limit
// rescue's view of the running process), every spawn rewrites it, and a cell
// with no stamp — spawned before it existed — reads as its configured model.
func TestSpawnCommandStampsTheModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{ID: 13, Slug: "stamp"}
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatalf("make cell dir: %v", err)
	}
	if got := zeusLaunchedModel(j); got != core.ZeusModel {
		t.Fatalf("no stamp must read as the configured model: %q", got)
	}
	if _, err := spawnCommand(j, []string{"claude", "--model", fableFallbackModel, "--name", "zeus-stamp"}, "brief", "prompt"); err != nil {
		t.Fatalf("spawnCommand: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(cellDir(j), "zeus-model"))
	if err != nil {
		t.Fatalf("stamp not written: %v", err)
	}
	if strings.TrimSpace(string(data)) != fableFallbackModel {
		t.Fatalf("stamp = %q, want %q", data, fableFallbackModel)
	}
	if got := zeusLaunchedModel(j); got != fableFallbackModel {
		t.Fatalf("zeusLaunchedModel = %q, want the stamped %q", got, fableFallbackModel)
	}
	// the next spawn (a rescue respawn on an account with Fable room, say)
	// overwrites it
	if _, err := spawnCommand(j, []string{"claude", "--model", core.ZeusModel, "-r", "sess"}, "brief", "prompt"); err != nil {
		t.Fatalf("spawnCommand: %v", err)
	}
	if got := zeusLaunchedModel(j); got != core.ZeusModel {
		t.Fatalf("a respawn must restamp: got %q", got)
	}
	// an argv with no --model (nothing kraken builds, but the stamp must not
	// invent one) leaves the previous stamp in place rather than blanking it
	if _, err := spawnCommand(j, []string{"claude"}, "", ""); err != nil {
		t.Fatalf("spawnCommand: %v", err)
	}
	if got := zeusLaunchedModel(j); got != core.ZeusModel {
		t.Fatalf("a model-less argv must not disturb the stamp: got %q", got)
	}
	// a job whose model: is pinned and never stamped reads its pin
	if got := zeusLaunchedModel(&Job{ID: 14, Slug: "pinned-no-stamp", Model: "sonnet"}); got != "sonnet" {
		t.Fatalf("an unstamped pinned job must read its model: field: %q", got)
	}
}

// TestLaunchedModelGatesTheMidRunFallback pins why the rescue reads the stamp
// rather than the configured model: a zeus the spawn arm started on Opus is
// not a Fable fallback candidate, so its hits go to the ordinary chain, while
// a zeus stamped on Fable still is.
func TestLaunchedModelGatesTheMidRunFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	hit := LimitHit{At: time.Now().Unix(), Error: "rate_limit"}
	j := &Job{ID: 15, Slug: "gate"}
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatalf("make cell dir: %v", err)
	}
	if _, err := spawnCommand(j, []string{"claude", "--model", fableFallbackModel}, "", ""); err != nil {
		t.Fatalf("spawnCommand: %v", err)
	}
	if fableFallbackCandidate(hit, zeusLaunchedModel(j)) {
		t.Fatal("a zeus spawned on Opus must not be a Fable fallback candidate")
	}
	if _, err := spawnCommand(j, []string{"claude", "--model", core.ZeusModel}, "", ""); err != nil {
		t.Fatalf("spawnCommand: %v", err)
	}
	if !fableFallbackCandidate(hit, zeusLaunchedModel(j)) {
		t.Fatal("a zeus spawned on Fable must stay a Fable fallback candidate")
	}
}
