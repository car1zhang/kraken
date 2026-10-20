package jobs

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// fleet_edge_test.go — the EDGE dimension of 50-contract.md slice G3 for
// package jobs: visibility parsing refusals and defaults, the KRAKEN_NODES
// knob's spellings, the device-label ladder with empty and failing rungs,
// NodeSelf degrading to blank, ApplyPoll's three-strike offline and
// recovery, and every NodesPass no-op / skip path. Written blind from the
// contract: internal/jobs/job.go, node.go, nodes.go and daemon.go were not
// read. Happy paths are athena-logic's.
//
// Every package-level identifier is prefixed `edgeFleet`.

func edgeFleetStore(t *testing.T) *store.Store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_NODES", "")
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	UseStore(st)
	t.Cleanup(func() { UseStore(nil); st.Close() })
	return st
}

// ---------------------------------------------------------------------------
// visibility
// ---------------------------------------------------------------------------

// `ValidVisibility`: "True for exactly the three words; false for `""` and
// anything else (case-sensitive)."
func TestEdgeFleetValidVisibilityRefusesNearMisses(t *testing.T) {
	for _, v := range []string{"", "Public", "PRIVATE", "ReadOnly", "read-only", "read_only", " public", "public ", "pub", "publics", "none"} {
		if ValidVisibility(v) {
			t.Errorf("ValidVisibility(%q) = true", v)
		}
	}
}

// `ParseJob` "on any other value calls `j.addErr` with `bad visibility value
// "<v>"`".
func TestEdgeFleetParseJobBadVisibilityErrs(t *testing.T) {
	for _, v := range []string{"Public", "everyone", "read-only", "PRIVATE"} {
		src := "---\njob: x\nrepo: monolith\nvisibility: " + v + "\n---\n\n# Spec\n"
		j := ParseJob("x.job.md", []byte(src))
		want := `bad visibility value "` + v + `"`
		if !strings.Contains(j.Err, want) {
			t.Errorf("visibility %q: Err = %q, want it to contain %q", v, j.Err, want)
		}
	}
}

// Amended (14:40 log): "`visibility` arm strips comments like colour" — the
// template's own commented-value shape parses clean.
func TestEdgeFleetParseJobVisibilityTrailingComment(t *testing.T) {
	j := ParseJob("x.job.md", []byte("---\njob: x\nrepo: monolith\nvisibility: public   # other krakens may drive it\n---\n\n# Spec\n"))
	if j.Err != "" {
		t.Fatalf("commented visibility errs: %q", j.Err)
	}
	if j.Visibility != VisibilityPublic {
		t.Errorf("Visibility = %q, want public", j.Visibility)
	}
}

// "leaves `""` for an absent line", and `EffectiveVisibility`: "`Visibility`
// when non-empty, else `VisibilityReadOnly`."
func TestEdgeFleetParseJobAbsentVisibilityIsEmptyAndReadsReadonly(t *testing.T) {
	j := ParseJob("x.job.md", []byte("---\njob: x\nrepo: monolith\n---\n\n# Spec\n"))
	if j.Err != "" {
		t.Fatalf("job without a visibility line errs: %q", j.Err)
	}
	if j.Visibility != "" {
		t.Errorf("Visibility = %q, want \"\" for an absent line", j.Visibility)
	}
	if got := j.EffectiveVisibility(); got != VisibilityReadOnly {
		t.Errorf("EffectiveVisibility() = %q, want %q", got, VisibilityReadOnly)
	}
	if got := (&Job{}).EffectiveVisibility(); got != "readonly" {
		t.Errorf("zero Job EffectiveVisibility() = %q, want readonly", got)
	}
}

// `FleetEnabled`: "False iff `KRAKEN_NODES` (process env) equals `off`
// case-insensitively after trimming; any other value, including unset, is
// true."
func TestEdgeFleetFleetEnabledSpellings(t *testing.T) {
	for _, v := range []string{"off", "OFF", "Off", " off ", "\toff\n"} {
		t.Setenv("KRAKEN_NODES", v)
		if FleetEnabled() {
			t.Errorf("KRAKEN_NODES=%q: FleetEnabled() = true, want false", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "offline", "of", "o ff", "disabled", "on"} {
		t.Setenv("KRAKEN_NODES", v)
		if !FleetEnabled() {
			t.Errorf("KRAKEN_NODES=%q: FleetEnabled() = false, want true", v)
		}
	}
}

// ---------------------------------------------------------------------------
// label ladder + self envelope
// ---------------------------------------------------------------------------

type edgeFleetRungs struct {
	cn      string
	cnErr   error
	ts      core.TailnetStatus
	tsErr   error
	host    string
	hostErr error
}

func edgeFleetSwapRungs(t *testing.T, r edgeFleetRungs) {
	t.Helper()
	oldCN, oldTS, oldHost := computerName, tailscaleStatus, hostName
	t.Cleanup(func() { computerName, tailscaleStatus, hostName = oldCN, oldTS, oldHost })
	computerName = func() (string, error) { return r.cn, r.cnErr }
	tailscaleStatus = func() (core.TailnetStatus, error) { return r.ts, r.tsErr }
	hostName = func() (string, error) { return r.host, r.hostErr }
}

func edgeFleetTS(hostName string) core.TailnetStatus {
	return core.TailnetStatus{Self: core.TailnetDevice{ID: "nSELF", HostName: hostName, Self: true}}
}

// `DeviceLabel`: "`KRAKEN_DEVICE_NAME` (trimmed) when non-empty; else
// `computerName()`; else the tailscale `Self.HostName`; else `hostName()`;
// else `""`. Never errors." — each rung empty or failing falls through.
func TestEdgeFleetDeviceLabelLadderFallsThrough(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name string
		env  string
		r    edgeFleetRungs
		want string
	}{
		{"whitespace env is empty", "   \t", edgeFleetRungs{cn: "Carl's Mac", ts: edgeFleetTS("ts-host"), host: "os-host"}, "Carl's Mac"},
		{"env trimmed", "  Studio Mini  ", edgeFleetRungs{cn: "Carl's Mac", ts: edgeFleetTS("ts-host"), host: "os-host"}, "Studio Mini"},
		{"empty computer name", "", edgeFleetRungs{cn: "", ts: edgeFleetTS("ts-host"), host: "os-host"}, "ts-host"},
		{"failing computer name", "", edgeFleetRungs{cnErr: boom, ts: edgeFleetTS("ts-host"), host: "os-host"}, "ts-host"},
		{"failing status", "", edgeFleetRungs{cnErr: boom, tsErr: boom, host: "os-host"}, "os-host"},
		{"empty HostName", "", edgeFleetRungs{cnErr: boom, ts: edgeFleetTS(""), host: "os-host"}, "os-host"},
		{"every rung empty", "", edgeFleetRungs{cn: "", ts: edgeFleetTS(""), host: ""}, ""},
		{"every rung failing", "", edgeFleetRungs{cnErr: boom, tsErr: boom, hostErr: boom}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("KRAKEN_DEVICE_NAME", c.env)
			edgeFleetSwapRungs(t, c.r)
			if got := DeviceLabel(); got != c.want {
				t.Errorf("DeviceLabel() = %q, want %q", got, c.want)
			}
		})
	}
}

// `NodeEnvelope`: "`IPs` encodes as `[]` never `null`." — the zero value.
func TestEdgeFleetNodeEnvelopeZeroIPsEncodesEmptyArray(t *testing.T) {
	b, err := json.Marshal(NodeEnvelope{})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if got := string(m["ips"]); got != "[]" {
		t.Fatalf(`NodeEnvelope{} "ips" = %s, want []`, got)
	}
}

// `NodeSelf`: "`ID` = tailscale `Self.ID` (`""` when status fails)" — and a
// failing status leaves the DNS name blank and the IPs an empty array, with
// the port passed through.
func TestEdgeFleetNodeSelfStatusFailureDegradesToBlank(t *testing.T) {
	t.Setenv("KRAKEN_DEVICE_NAME", "")
	edgeFleetSwapRungs(t, edgeFleetRungs{cn: "Carl's Mac", tsErr: core.ErrNoTailscale})
	env := NodeSelf(7878)
	if env.ID != "" || env.DNSName != "" {
		t.Errorf("NodeSelf with failing status: ID=%q DNSName=%q, want blank", env.ID, env.DNSName)
	}
	if env.Port != 7878 {
		t.Errorf("Port = %d, want 7878", env.Port)
	}
	if env.Label != "Carl's Mac" {
		t.Errorf("Label = %q, want the computer name", env.Label)
	}
	b, _ := json.Marshal(env)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if got := string(m["ips"]); got != "[]" {
		t.Errorf(`"ips" = %s, want []`, got)
	}
}

// `RemotePrefix`: "`fromCell != ""` → `[from <label> · cell <fromCell>]`;
// else `[from <label> · <login>]`; an empty `login` yields `[from <label>]`".
func TestEdgeFleetRemotePrefixForms(t *testing.T) {
	cases := []struct{ label, login, cell, want string }{
		{"studio-mini", "", "", "[from studio-mini]"},
		{"studio-mini", "jess@x.io", "tech-comms", "[from studio-mini · cell tech-comms]"},
		{"studio-mini", "", "tech-comms", "[from studio-mini · cell tech-comms]"},
		{"studio-mini", "jess@x.io", "", "[from studio-mini · jess@x.io]"},
	}
	for _, c := range cases {
		if got := RemotePrefix(c.label, c.login, c.cell); got != c.want {
			t.Errorf("RemotePrefix(%q,%q,%q) = %q, want %q", c.label, c.login, c.cell, got, c.want)
		}
	}
	// the separator is exactly space, U+00B7, space
	if got := RemotePrefix("a", "b", ""); got != "[from a · b]" {
		t.Errorf("separator: got %q", got)
	}
}

// ---------------------------------------------------------------------------
// NodesToProbe
// ---------------------------------------------------------------------------

// `NodesToProbe`: "Returns the online, non-self peers whose `ID` matches no
// `known` row's `ID`. Order preserved from `status.Peers`."
func TestEdgeFleetNodesToProbeFilters(t *testing.T) {
	status := core.TailnetStatus{
		Self: core.TailnetDevice{ID: "self", Self: true, Online: true, IP: "100.64.0.1"},
		Peers: []core.TailnetDevice{
			{ID: "z-new", Online: true, IP: "100.64.0.9"},
			{ID: "known", Online: true, IP: "100.64.0.2"},
			{ID: "offline", Online: false, IP: "100.64.0.3"},
			{ID: "a-new", Online: true, IP: "100.64.0.4"},
		},
	}
	got := NodesToProbe(status, []*store.NodeRow{{ID: "known"}, {ID: "self"}})
	if len(got) != 2 || got[0].ID != "z-new" || got[1].ID != "a-new" {
		var ids []string
		for _, d := range got {
			ids = append(ids, d.ID)
		}
		t.Fatalf("NodesToProbe = %v, want [z-new a-new]", ids)
	}
	if empty := NodesToProbe(core.TailnetStatus{Self: status.Self}, nil); len(empty) != 0 {
		t.Fatalf("NodesToProbe with no peers = %d devices, want 0", len(empty))
	}
}

// ---------------------------------------------------------------------------
// ApplyPoll
// ---------------------------------------------------------------------------

func edgeFleetSeedNode(t *testing.T, st *store.Store, id string, online bool) *store.NodeRow {
	t.Helper()
	r := &store.NodeRow{ID: id, Label: id, IP: "100.70.0.5", Port: 7777, Contract: 7, FirstSeen: 1, LastSeen: 1, Online: online}
	if err := st.UpsertNode(r); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	got, err := st.GetNode(id)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	return got
}

func edgeFleetReload(t *testing.T, st *store.Store, id string) *store.NodeRow {
	t.Helper()
	n, err := st.GetNode(id)
	if err != nil {
		t.Fatalf("GetNode(%s): %v", id, err)
	}
	return n
}

const edgeFleetReply = `{"node":{"node_id":"n1","label":"n1","operator":"Jess","dns_name":"","ips":[],"port":7777,"build":"x","contract":7},"jobs":[{"slug":"a"}]}`

// `ApplyPoll`: "`Fails[n.ID]++`; when it reaches 3 and the row was online,
// `SetNodeOnline(false)` and `changed = true`." — failures one and two flip
// nothing and report no change.
func TestEdgeFleetApplyPollThreeStrikes(t *testing.T) {
	st := edgeFleetStore(t)
	n := edgeFleetSeedNode(t, st, "n1", true)
	state := &NodesState{Fails: map[string]int{}}
	down := errors.New("dial tcp: connection refused")

	for i := 1; i <= 2; i++ {
		changed, err := ApplyPoll(st, edgeFleetReload(t, st, "n1"), nil, down, state, int64(100*i))
		if err != nil {
			t.Fatalf("strike %d: applyErr %v", i, err)
		}
		if changed {
			t.Errorf("strike %d: changed = true, want false", i)
		}
		if !edgeFleetReload(t, st, "n1").Online {
			t.Fatalf("strike %d: row went offline early", i)
		}
	}
	changed, err := ApplyPoll(st, edgeFleetReload(t, st, "n1"), nil, down, state, 300)
	if err != nil {
		t.Fatalf("strike 3: applyErr %v", err)
	}
	if !changed {
		t.Errorf("strike 3: changed = false, want true")
	}
	if edgeFleetReload(t, st, "n1").Online {
		t.Errorf("strike 3: row still online")
	}
	if state.Fails[n.ID] != 3 {
		t.Errorf("Fails = %d, want 3", state.Fails[n.ID])
	}
}

// "when it reaches 3 and the row was online" — a further failure on a row
// already offline reports no change and leaves it offline.
func TestEdgeFleetApplyPollFailureOnOfflineRowIsNoChange(t *testing.T) {
	st := edgeFleetStore(t)
	edgeFleetSeedNode(t, st, "n1", false)
	state := &NodesState{Fails: map[string]int{"n1": 3}}
	changed, err := ApplyPoll(st, edgeFleetReload(t, st, "n1"), nil, errors.New("timeout"), state, 500)
	if err != nil {
		t.Fatalf("applyErr %v", err)
	}
	if changed {
		t.Errorf("fourth failure on an offline row: changed = true")
	}
	if edgeFleetReload(t, st, "n1").Online {
		t.Errorf("offline row came online on a failure")
	}
}

// "A success on an offline row sets it online (`changed = true`)" and
// "`state.Fails[n.ID] = 0`", with `last_err` cleared by the ok poll.
func TestEdgeFleetApplyPollRecoveryOnOfflineRow(t *testing.T) {
	st := edgeFleetStore(t)
	edgeFleetSeedNode(t, st, "n1", true)
	if err := st.SetNodePoll("n1", true, "", `[{"slug":"a"}]`, 50); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodePoll("n1", false, "refused", "", 60); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodeOnline("n1", false); err != nil {
		t.Fatal(err)
	}
	state := &NodesState{Fails: map[string]int{"n1": 5}}
	// identical jobs to the stored list: only the online flip makes it a change
	changed, err := ApplyPoll(st, edgeFleetReload(t, st, "n1"), []byte(edgeFleetReply), nil, state, 700)
	if err != nil {
		t.Fatalf("applyErr %v", err)
	}
	if !changed {
		t.Errorf("success on an offline row: changed = false, want true")
	}
	got := edgeFleetReload(t, st, "n1")
	if !got.Online || got.LastErr != "" || got.LastOK != 700 {
		t.Errorf("after recovery: Online=%v LastErr=%q LastOK=%d", got.Online, got.LastErr, got.LastOK)
	}
	if state.Fails["n1"] != 0 {
		t.Errorf("Fails = %d, want 0", state.Fails["n1"])
	}
}

// "`changed` is true iff the stored `jobs_json` differed" — the same body
// twice on an online row is a change once, then not.
func TestEdgeFleetApplyPollIdenticalJobsIsNoChange(t *testing.T) {
	st := edgeFleetStore(t)
	edgeFleetSeedNode(t, st, "n1", true)
	state := &NodesState{Fails: map[string]int{}}
	if _, err := ApplyPoll(st, edgeFleetReload(t, st, "n1"), []byte(edgeFleetReply), nil, state, 100); err != nil {
		t.Fatal(err)
	}
	changed, err := ApplyPoll(st, edgeFleetReload(t, st, "n1"), []byte(edgeFleetReply), nil, state, 200)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Errorf("second identical poll: changed = true, want false")
	}
}

// "`err != nil` or undecodable body: `SetNodePoll(ok=false, errText)`,
// `Fails[n.ID]++`" — a nil error with a body that is not a NodeJobsReply is a
// failure, and the stored jobs survive it.
func TestEdgeFleetApplyPollUndecodableBodyIsFailure(t *testing.T) {
	st := edgeFleetStore(t)
	edgeFleetSeedNode(t, st, "n1", true)
	if err := st.SetNodePoll("n1", true, "", `[{"slug":"kept"}]`, 50); err != nil {
		t.Fatal(err)
	}
	state := &NodesState{Fails: map[string]int{}}
	for i, body := range []string{`<html>404 page not found</html>`, `{"jobs":`, ``} {
		if _, err := ApplyPoll(st, edgeFleetReload(t, st, "n1"), []byte(body), nil, state, int64(100+i)); err != nil {
			t.Fatalf("body %q: applyErr %v", body, err)
		}
		if state.Fails["n1"] != i+1 {
			t.Errorf("body %q: Fails = %d, want %d", body, state.Fails["n1"], i+1)
		}
	}
	got := edgeFleetReload(t, st, "n1")
	if got.JobsJSON != `[{"slug":"kept"}]` || got.LastOK != 50 {
		t.Errorf("undecodable bodies overwrote the poll: JobsJSON=%q LastOK=%d", got.JobsJSON, got.LastOK)
	}
	if got.LastErr == "" {
		t.Errorf("undecodable body recorded no last_err")
	}
}

// The daemon passes `&d.nodes`, a zero NodesState whose Fails map is nil
// (the contract names no initialiser — disclosed silence): ApplyPoll must
// count the failure rather than panic on the nil map.
func TestEdgeFleetApplyPollZeroState(t *testing.T) {
	st := edgeFleetStore(t)
	edgeFleetSeedNode(t, st, "n1", true)
	var state NodesState
	if _, err := ApplyPoll(st, edgeFleetReload(t, st, "n1"), nil, errors.New("down"), &state, 100); err != nil {
		t.Fatalf("applyErr %v", err)
	}
	if state.Fails["n1"] != 1 {
		t.Fatalf("Fails = %d, want 1", state.Fails["n1"])
	}
}

// ---------------------------------------------------------------------------
// NodesPass
// ---------------------------------------------------------------------------

type edgeFleetCalls struct {
	mu                            sync.Mutex
	status, probe, announce, poll int
	polled                        []string
}

func edgeFleetDeps(c *edgeFleetCalls, status core.TailnetStatus, statusErr error,
	probe func(ip string, port int) (NodeEnvelope, error), announceErr error) NodesDeps {
	return NodesDeps{
		Status: func() (core.TailnetStatus, error) {
			c.mu.Lock()
			c.status++
			c.mu.Unlock()
			return status, statusErr
		},
		Probe: func(ip string, port int) (NodeEnvelope, error) {
			c.mu.Lock()
			c.probe++
			c.mu.Unlock()
			if probe == nil {
				return NodeEnvelope{}, errors.New("no probe configured")
			}
			return probe(ip, port)
		},
		Announce: func(n *store.NodeRow, self NodeEnvelope) error {
			c.mu.Lock()
			c.announce++
			c.mu.Unlock()
			return announceErr
		},
		Poll: func(n *store.NodeRow) ([]byte, error) {
			c.mu.Lock()
			c.poll++
			c.polled = append(c.polled, n.ID)
			c.mu.Unlock()
			return []byte(edgeFleetReply), nil
		},
	}
}

func (c *edgeFleetCalls) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status + c.probe + c.announce + c.poll
}

var edgeFleetSelf = NodeEnvelope{ID: "nSELF", Label: "carls-mac", Operator: "Carl", IPs: []string{"100.90.94.3"}, Port: 7777, Build: "abc", Contract: 7}

func edgeFleetOnlyPeer() core.TailnetStatus {
	return core.TailnetStatus{
		Self:  core.TailnetDevice{ID: "nSELF", IP: "100.90.94.3", Online: true, Self: true},
		Peers: []core.TailnetDevice{{ID: "nPEER", IP: "100.90.94.4", Online: true}},
	}
}

func edgeFleetNow() time.Time { return time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC) }

// `NodesPass`: "Returns immediately (no deps called, nothing written) when
// … `!state.Running.CompareAndSwap(false, true)`". The flag belongs to the
// pass that is in flight, so the refused call must leave it held (disclosed
// reading of "clears `Running` on return").
func TestEdgeFleetNodesPassSingleFlight(t *testing.T) {
	st := edgeFleetStore(t)
	edgeFleetSeedNode(t, st, "n1", true)
	var c edgeFleetCalls
	state := &NodesState{Fails: map[string]int{}}
	state.Running.Store(true)
	if changed := NodesPass(st, edgeFleetSelf, edgeFleetDeps(&c, edgeFleetOnlyPeer(), nil, nil, nil), state, 1, edgeFleetNow()); changed {
		t.Errorf("held pass reported changed")
	}
	if c.total() != 0 {
		t.Errorf("held pass called deps: %+v", &c)
	}
	if !state.Running.Load() {
		t.Errorf("a refused pass cleared the in-flight holder's Running flag")
	}
	if state.LastAnnounced != "" {
		t.Errorf("held pass recorded LastAnnounced %q", state.LastAnnounced)
	}
}

// "Returns immediately … when `!FleetEnabled()`".
func TestEdgeFleetNodesPassFleetOff(t *testing.T) {
	st := edgeFleetStore(t)
	edgeFleetSeedNode(t, st, "n1", true)
	t.Setenv("KRAKEN_NODES", "OFF")
	var c edgeFleetCalls
	state := &NodesState{Fails: map[string]int{}}
	if changed := NodesPass(st, edgeFleetSelf, edgeFleetDeps(&c, edgeFleetOnlyPeer(), nil, nil, nil), state, 1, edgeFleetNow()); changed {
		t.Errorf("fleet-off pass reported changed")
	}
	if c.total() != 0 {
		t.Errorf("fleet-off pass called deps: %+v", &c)
	}
	if got := edgeFleetReload(t, st, "n1"); got.LastOK != 0 || got.JobsJSON != "" {
		t.Errorf("fleet-off pass wrote the row: %+v", got)
	}
}

// "Returns immediately … when … `st == nil`".
func TestEdgeFleetNodesPassNilStore(t *testing.T) {
	t.Setenv("KRAKEN_NODES", "")
	var c edgeFleetCalls
	state := &NodesState{Fails: map[string]int{}}
	if changed := NodesPass(nil, edgeFleetSelf, edgeFleetDeps(&c, edgeFleetOnlyPeer(), nil, nil, nil), state, 1, edgeFleetNow()); changed {
		t.Errorf("nil-store pass reported changed")
	}
	if c.total() != 0 {
		t.Errorf("nil-store pass called deps: %+v", &c)
	}
}

// "A status listing only self writes nothing." — on a discover tick.
func TestEdgeFleetNodesPassOnlySelfWritesNothing(t *testing.T) {
	st := edgeFleetStore(t)
	var c edgeFleetCalls
	state := &NodesState{Fails: map[string]int{}}
	onlySelf := core.TailnetStatus{Self: core.TailnetDevice{ID: "nSELF", IP: "100.90.94.3", Online: true, Self: true}}
	if changed := NodesPass(st, edgeFleetSelf, edgeFleetDeps(&c, onlySelf, nil, nil, nil), state, 1, edgeFleetNow()); changed {
		t.Errorf("only-self pass reported changed")
	}
	if c.probe != 0 || c.announce != 0 || c.poll != 0 {
		t.Errorf("only-self pass probed/announced/polled: %+v", &c)
	}
	rows, err := st.ListNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("only-self pass wrote %d registry rows", len(rows))
	}
	if state.Running.Load() {
		t.Errorf("Running still held after the pass returned")
	}
}

// "an error or a reply whose `ID` is empty inserts nothing".
func TestEdgeFleetNodesPassProbeFailuresInsertNothing(t *testing.T) {
	for name, probe := range map[string]func(string, int) (NodeEnvelope, error){
		"error":    func(string, int) (NodeEnvelope, error) { return NodeEnvelope{}, errors.New("404 page not found") },
		"empty id": func(string, int) (NodeEnvelope, error) { return NodeEnvelope{Label: "not-kraken", Port: 7777}, nil },
	} {
		t.Run(name, func(t *testing.T) {
			st := edgeFleetStore(t)
			var c edgeFleetCalls
			state := &NodesState{Fails: map[string]int{}}
			changed := NodesPass(st, edgeFleetSelf, edgeFleetDeps(&c, edgeFleetOnlyPeer(), nil, probe, nil), state, 1, edgeFleetNow())
			if changed {
				t.Errorf("failed probe reported changed")
			}
			if c.probe != 1 {
				t.Errorf("probe calls = %d, want 1", c.probe)
			}
			if rows, _ := st.ListNodes(); len(rows) != 0 {
				t.Errorf("failed probe inserted %d rows", len(rows))
			}
		})
	}
}

// "A status error on a discover tick skips discover only." — the poll of a
// registered online row still runs.
func TestEdgeFleetNodesPassStatusErrorSkipsDiscoverOnly(t *testing.T) {
	st := edgeFleetStore(t)
	edgeFleetSeedNode(t, st, "n1", true)
	var c edgeFleetCalls
	state := &NodesState{Fails: map[string]int{}}
	NodesPass(st, edgeFleetSelf, edgeFleetDeps(&c, core.TailnetStatus{}, core.ErrNoTailscale, nil, nil), state, 1, edgeFleetNow())
	if c.probe != 0 {
		t.Errorf("probe ran despite a status error: %d", c.probe)
	}
	if c.poll != 1 {
		t.Errorf("poll calls = %d, want 1 (only discover is skipped)", c.poll)
	}
}

// "an announce error is logged and ignored" and "then stores that JSON in
// `LastAnnounced`" — the failed announce still records the envelope, so the
// next tick with an unchanged envelope does not re-announce, and polling is
// unaffected.
func TestEdgeFleetNodesPassAnnounceErrorIgnored(t *testing.T) {
	st := edgeFleetStore(t)
	edgeFleetSeedNode(t, st, "n1", true)
	var c edgeFleetCalls
	state := &NodesState{Fails: map[string]int{}}
	deps := edgeFleetDeps(&c, edgeFleetOnlyPeer(), nil, nil, errors.New("announce: connection reset"))
	// tick 2: not a discover tick, so only announce + poll run
	NodesPass(st, edgeFleetSelf, deps, state, 2, edgeFleetNow())
	if c.announce != 1 {
		t.Fatalf("announce calls after first tick = %d, want 1", c.announce)
	}
	want, _ := json.Marshal(edgeFleetSelf)
	if state.LastAnnounced != string(want) {
		t.Errorf("LastAnnounced = %q, want %q despite the error", state.LastAnnounced, want)
	}
	if c.poll != 1 {
		t.Errorf("poll calls = %d, want 1", c.poll)
	}
	NodesPass(st, edgeFleetSelf, deps, state, 3, edgeFleetNow().Add(15*time.Second))
	if c.announce != 1 {
		t.Errorf("unchanged envelope re-announced: %d calls", c.announce)
	}
	if c.poll != 2 {
		t.Errorf("poll calls after two ticks = %d, want 2", c.poll)
	}
}

// "Poll runs every tick for every row with `Online` true, and for offline
// rows only on discover ticks" (`tickN == 1 || tickN % 8 == 0`).
func TestEdgeFleetNodesPassOfflineRowsPolledOnDiscoverTicksOnly(t *testing.T) {
	st := edgeFleetStore(t)
	edgeFleetSeedNode(t, st, "on", true)
	edgeFleetSeedNode(t, st, "off", false)
	onlySelf := core.TailnetStatus{Self: core.TailnetDevice{ID: "nSELF", IP: "100.90.94.3", Online: true, Self: true}}
	state := &NodesState{Fails: map[string]int{}, LastAnnounced: func() string { b, _ := json.Marshal(edgeFleetSelf); return string(b) }()}

	for _, tc := range []struct {
		tick       int
		wantOffPol bool
	}{{2, false}, {7, false}, {8, true}, {9, false}, {16, true}} {
		var c edgeFleetCalls
		// keep "off" offline: re-flip after any successful poll of it
		NodesPass(st, edgeFleetSelf, edgeFleetDeps(&c, onlySelf, nil, nil, nil), state, tc.tick, edgeFleetNow())
		polledOff := false
		polledOn := false
		for _, id := range c.polled {
			if id == "off" {
				polledOff = true
			}
			if id == "on" {
				polledOn = true
			}
		}
		if polledOff != tc.wantOffPol {
			t.Errorf("tick %d: offline row polled = %v, want %v", tc.tick, polledOff, tc.wantOffPol)
		}
		if !polledOn {
			t.Errorf("tick %d: online row not polled", tc.tick)
		}
		if err := st.SetNodeOnline("off", false); err != nil {
			t.Fatal(err)
		}
	}
}
