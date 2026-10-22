package jobs

// fleet_contract_test.go — athena-logic's BLIND contract tests for the
// multiplayer-kraken job, Slice G3 (jobs: visibility, fleet knob, node
// envelope, device label, the nodes pass, remote prefix). Authored from
// artifacts/50-contract.md alone; no implementation file was read. LOGIC
// dimension: the stated invariants and happy paths. Refusals and boundaries
// (a bad visibility value's error text, KRAKEN_NODES oddities, probe errors)
// are athena-edge's.

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
	"kraken/internal/util"
)

// --- visibility ----------------------------------------------------------

// Contract G3: "VisibilityPrivate, VisibilityReadOnly, VisibilityPublic —
// const … = "private", "readonly", "public"".
func TestContractFleetVisibilityConstants(t *testing.T) {
	if VisibilityPrivate != "private" || VisibilityReadOnly != "readonly" || VisibilityPublic != "public" {
		t.Errorf("visibility constants = %q %q %q, want private readonly public",
			VisibilityPrivate, VisibilityReadOnly, VisibilityPublic)
	}
}

// Contract G3 `ValidVisibility`: "True for exactly the three words".
func TestContractFleetValidVisibilityAcceptsTheThree(t *testing.T) {
	for _, v := range []string{"private", "readonly", "public"} {
		if !ValidVisibility(v) {
			t.Errorf("ValidVisibility(%q) = false, want true", v)
		}
	}
	if ValidVisibility("shared") {
		t.Error(`ValidVisibility("shared") = true, want false`)
	}
}

// Contract G3 `Job.Visibility`: "Parsed from the frontmatter key visibility;
// ParseJob accepts the three words, leaves "" for an absent line".
func TestContractFleetParseJobReadsVisibility(t *testing.T) {
	for _, v := range []string{"private", "readonly", "public"} {
		src := "---\njob: vis\nrepo: kraken\nvisibility: " + v + "\n---\n\nspec\n"
		j := ParseJob("vis"+JobSuffix, []byte(src))
		if j.Err != "" {
			t.Errorf("visibility %s: unexpected parse error %q", v, j.Err)
		}
		if j.Visibility != v {
			t.Errorf("visibility %s: parsed %q", v, j.Visibility)
		}
	}
	// AMENDED 14:40: "visibility arm strips comments like colour"
	c := ParseJob("vis"+JobSuffix, []byte("---\njob: vis\nrepo: kraken\nvisibility: public   # other krakens may drive it\n---\n\nspec\n"))
	if c.Err != "" || c.Visibility != "public" {
		t.Errorf("commented visibility line: Visibility %q, Err %q; want public, no error", c.Visibility, c.Err)
	}
	j := ParseJob("vis"+JobSuffix, []byte("---\njob: vis\nrepo: kraken\n---\n\nspec\n"))
	if j.Err != "" {
		t.Fatalf("absent visibility: unexpected parse error %q", j.Err)
	}
	if j.Visibility != "" {
		t.Errorf("absent visibility line parsed as %q, want \"\"", j.Visibility)
	}
}

// Contract G3 `(*Job).EffectiveVisibility`: "Visibility when non-empty, else
// VisibilityReadOnly."
func TestContractFleetEffectiveVisibility(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", "readonly"},
		{"private", "private"},
		{"readonly", "readonly"},
		{"public", "public"},
	} {
		j := &Job{Visibility: c.in}
		if got := j.EffectiveVisibility(); got != c.want {
			t.Errorf("EffectiveVisibility(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Contract G3 `Job.Visibility` json tag: `json:"visibility,omitempty"`.
func TestContractFleetJobVisibilityJSONTag(t *testing.T) {
	b, err := json.Marshal(Job{Visibility: "public"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["visibility"] != "public" {
		t.Errorf(`marshalled Job carries visibility = %#v, want "public"`, m["visibility"])
	}
	b, err = json.Marshal(Job{})
	if err != nil {
		t.Fatal(err)
	}
	var empty map[string]any
	if err := json.Unmarshal(b, &empty); err != nil {
		t.Fatal(err)
	}
	if _, ok := empty["visibility"]; ok {
		t.Error("an empty Visibility is not omitted (omitempty)")
	}
}

// Contract G3 `JobTemplate`: "Emits a commented line `# visibility: readonly
// # optional: private (never leaves this machine) | readonly (default) |
// public (other krakens may drive it)` directly after the `# color:` block."
// The template must still parse clean, with no visibility set.
func TestContractFleetJobTemplateVisibilityLine(t *testing.T) {
	const want = "# visibility: readonly   # optional: private (never leaves this machine) | readonly (default) | public (other krakens may drive it)"
	src := JobTemplate("tmpl")
	lines := strings.Split(src, "\n")
	vis, color := -1, -1
	for i, l := range lines {
		if strings.TrimRight(l, " ") == want {
			if vis != -1 {
				t.Fatalf("visibility line appears twice:\n%s", src)
			}
			vis = i
		}
		if strings.HasPrefix(strings.TrimSpace(l), "# color:") {
			color = i
		}
	}
	if vis == -1 {
		t.Fatalf("template lacks the exact line %q:\n%s", want, src)
	}
	if color == -1 {
		t.Fatalf("template has no `# color:` line to anchor on:\n%s", src)
	}
	if vis < color {
		t.Fatalf("visibility line (line %d) precedes the # color: line (line %d)", vis, color)
	}
	// "directly after the # color: block": everything between the color line
	// and the visibility line is part of that comment block.
	for i := color + 1; i < vis; i++ {
		if !strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
			t.Errorf("non-comment line %d %q sits between the # color: block and the visibility line", i, lines[i])
		}
	}
	j := ParseJob("tmpl"+JobSuffix, []byte(src))
	if j.Err != "" {
		t.Errorf("template no longer parses clean: %s", j.Err)
	}
	if j.Visibility != "" {
		t.Errorf("template's commented line set Visibility = %q, want \"\"", j.Visibility)
	}
}

// Contract G3 `WriteJobFields(j, "visibility", v)`: "Lands the column;
// LoadJobs() then returns the job with Visibility == v."
func TestContractFleetWriteJobFieldsLandsVisibility(t *testing.T) {
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "vis-live", Repo: "kraken", State: "working"})
	for _, v := range []string{"public", "private", "readonly"} {
		j := LoadJobBySlug("vis-live")
		if j == nil {
			t.Fatal("fixture vanished")
		}
		if err := WriteJobFields(j, "visibility", v); err != nil {
			t.Fatalf("WriteJobFields(visibility=%s): %v", v, err)
		}
		got := JobBySlug(LoadJobs(), "vis-live")
		if got == nil {
			t.Fatal("LoadJobs lost the job")
		}
		if got.Visibility != v {
			t.Errorf("after WriteJobFields(%s), LoadJobs Visibility = %q", v, got.Visibility)
		}
		if got.State != "working" {
			t.Errorf("visibility write moved the state to %q", got.State)
		}
	}
}

// --- fleet knob ----------------------------------------------------------

// Contract G3 `FleetEnabled`: "False iff KRAKEN_NODES (process env) equals off
// case-insensitively after trimming; any other value, including unset, is
// true."
func TestContractFleetFleetEnabled(t *testing.T) {
	t.Setenv("KRAKEN_NODES", "")
	os.Unsetenv("KRAKEN_NODES")
	if !FleetEnabled() {
		t.Error("unset KRAKEN_NODES: FleetEnabled = false, want true")
	}
	for _, v := range []string{"off", "OFF", " Off "} {
		t.Setenv("KRAKEN_NODES", v)
		if FleetEnabled() {
			t.Errorf("KRAKEN_NODES=%q: FleetEnabled = true, want false", v)
		}
	}
	for _, v := range []string{"on", "1", "studio-mini"} {
		t.Setenv("KRAKEN_NODES", v)
		if !FleetEnabled() {
			t.Errorf("KRAKEN_NODES=%q: FleetEnabled = false, want true", v)
		}
	}
}

// --- NodeEnvelope --------------------------------------------------------

// Contract G3 `NodeEnvelope`: the eight json keys, and "IPs encodes as []
// never null".
func TestContractFleetNodeEnvelopeWire(t *testing.T) {
	b, err := json.Marshal(NodeEnvelope{ID: "n1", Label: "studio-mini", Operator: "Jess",
		DNSName: "studio-mini.tail072ad8.ts.net", IPs: []string{"100.70.1.2"}, Port: 7777, Build: "abc", Contract: 7})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"node_id": "n1", "label": "studio-mini", "operator": "Jess",
		"dns_name": "studio-mini.tail072ad8.ts.net", "ips": []any{"100.70.1.2"},
		"port": float64(7777), "build": "abc", "contract": float64(7),
	}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("NodeEnvelope JSON = %v\nwant              %v", m, want)
	}

	b, err = json.Marshal(NodeEnvelope{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"ips":[]`) {
		t.Errorf("zero NodeEnvelope encodes ips as %s; want []", b)
	}
}

// Contract G3: "Contract = NodeContract (const NodeContract = 7 …)".
func TestContractFleetNodeContractIsSeven(t *testing.T) {
	if NodeContract != 7 {
		t.Errorf("NodeContract = %d, want 7", NodeContract)
	}
}

// --- DeviceLabel / NodeSelf seams ----------------------------------------

// contractFleetSeams swaps the three label seams; nil leaves the source
// failing.
func contractFleetSeams(t *testing.T, comp func() (string, error), status func() (core.TailnetStatus, error), host func() (string, error)) {
	t.Helper()
	oc, os_, oh := computerName, tailscaleStatus, hostName
	fail := errors.New("contract fake: unavailable")
	if comp == nil {
		comp = func() (string, error) { return "", fail }
	}
	if status == nil {
		status = func() (core.TailnetStatus, error) { return core.TailnetStatus{}, fail }
	}
	if host == nil {
		host = func() (string, error) { return "", fail }
	}
	computerName, tailscaleStatus, hostName = comp, status, host
	t.Cleanup(func() { computerName, tailscaleStatus, hostName = oc, os_, oh })
}

func contractFleetStatus() core.TailnetStatus {
	return core.TailnetStatus{
		Self: core.TailnetDevice{
			ID: "nSELFstable11CNTRL", HostName: "Carls-MacBook-Bro",
			DNSName: "carls-macbook-bro.tail072ad8.ts.net", IP: "100.90.94.3",
			OS: "macOS", Login: "carl@github", Online: true, Self: true,
		},
		MagicDNSSuffix: "tail072ad8.ts.net",
	}
}

// Contract G3 `DeviceLabel`: "KRAKEN_DEVICE_NAME (trimmed) when non-empty;
// else computerName(); else the tailscale Self.HostName; else hostName(); else
// "". Never errors." Each rung is reached by FAILING (an error) the one above.
func TestContractFleetDeviceLabelLadder(t *testing.T) {
	ok := func(s string) func() (string, error) { return func() (string, error) { return s, nil } }
	st := func() (core.TailnetStatus, error) { return contractFleetStatus(), nil }

	t.Run("env override beats all", func(t *testing.T) {
		t.Setenv("KRAKEN_DEVICE_NAME", "  Kitchen Mac  ")
		contractFleetSeams(t, ok("Carl's MacBook Bro"), st, ok("carls-mbp.local"))
		if got := DeviceLabel(); got != "Kitchen Mac" {
			t.Errorf("DeviceLabel = %q, want Kitchen Mac", got)
		}
	})
	t.Run("ComputerName", func(t *testing.T) {
		t.Setenv("KRAKEN_DEVICE_NAME", "")
		contractFleetSeams(t, ok("Carl's MacBook Bro"), st, ok("carls-mbp.local"))
		if got := DeviceLabel(); got != "Carl's MacBook Bro" {
			t.Errorf("DeviceLabel = %q, want the ComputerName", got)
		}
	})
	t.Run("tailscale HostName", func(t *testing.T) {
		t.Setenv("KRAKEN_DEVICE_NAME", "")
		contractFleetSeams(t, nil, st, ok("carls-mbp.local"))
		if got := DeviceLabel(); got != "Carls-MacBook-Bro" {
			t.Errorf("DeviceLabel = %q, want tailscale Self.HostName", got)
		}
	})
	t.Run("os hostname", func(t *testing.T) {
		t.Setenv("KRAKEN_DEVICE_NAME", "")
		contractFleetSeams(t, nil, nil, ok("carls-mbp.local"))
		if got := DeviceLabel(); got != "carls-mbp.local" {
			t.Errorf("DeviceLabel = %q, want os.Hostname", got)
		}
	})
}

// Contract G3 `NodeSelf`: "ID = tailscale Self.ID …, Label = DeviceLabel(),
// Operator = Operator(), DNSName = Self.DNSName, IPs = Self's IPv4 then any
// others, Port = port, Build = util.Build(), Contract = NodeContract".
// TailnetDevice carries one address (IP), so Self's IPs are [Self.IP].
func TestContractFleetNodeSelf(t *testing.T) {
	t.Setenv("KRAKEN_DEVICE_NAME", "")
	t.Setenv("KRAKEN_OPERATOR", "Carl Zhang")
	contractFleetSeams(t,
		func() (string, error) { return "Carl's MacBook Bro", nil },
		func() (core.TailnetStatus, error) { return contractFleetStatus(), nil },
		nil)
	got := NodeSelf(7878)
	want := NodeEnvelope{
		ID: "nSELFstable11CNTRL", Label: "Carl's MacBook Bro", Operator: Operator(),
		DNSName: "carls-macbook-bro.tail072ad8.ts.net", IPs: []string{"100.90.94.3"},
		Port: 7878, Build: util.Build(), Contract: NodeContract,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NodeSelf = %+v\nwant     %+v", got, want)
	}
	if got.Operator != "Carl Zhang" {
		t.Errorf("Operator = %q, want the KRAKEN_OPERATOR value", got.Operator)
	}
}

// --- NodesToProbe / RowFromEnvelope -------------------------------------

// Contract G3 `NodesToProbe`: "Returns the online, non-self peers whose ID
// matches no known row's ID. Order preserved from status.Peers."
func TestContractFleetNodesToProbe(t *testing.T) {
	status := contractFleetStatus()
	status.Peers = []core.TailnetDevice{
		{ID: "p-new-1", IP: "100.70.0.1", Online: true},
		{ID: "p-offline", IP: "100.70.0.2", Online: false},
		{ID: "p-known", IP: "100.70.0.3", Online: true},
		{ID: "p-new-2", IP: "100.70.0.4", Online: true},
	}
	known := []*store.NodeRow{{ID: "p-known"}, {ID: "unrelated"}}
	got := NodesToProbe(status, known)
	var ids []string
	for _, d := range got {
		ids = append(ids, d.ID)
	}
	if !reflect.DeepEqual(ids, []string{"p-new-1", "p-new-2"}) {
		t.Errorf("NodesToProbe = %v, want [p-new-1 p-new-2]", ids)
	}
}

// Contract G3 `RowFromEnvelope`: "Maps envelope fields; IP = ip; FirstSeen =
// LastSeen = now; Online = true."
func TestContractFleetRowFromEnvelope(t *testing.T) {
	env := NodeEnvelope{ID: "n-b", Label: "studio-mini", Operator: "Jess",
		DNSName: "studio-mini.tail072ad8.ts.net", IPs: []string{"100.70.1.2"},
		Port: 7777, Build: "def", Contract: 7}
	got := RowFromEnvelope(env, "100.70.1.2", 1_760_000_000_000)
	want := store.NodeRow{ID: "n-b", Label: "studio-mini", Operator: "Jess",
		DNSName: "studio-mini.tail072ad8.ts.net", IP: "100.70.1.2", Port: 7777,
		Build: "def", Contract: 7, FirstSeen: 1_760_000_000_000, LastSeen: 1_760_000_000_000,
		Online: true}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("RowFromEnvelope = %+v\nwant            %+v", *got, want)
	}
}

// --- ApplyPoll -----------------------------------------------------------

func contractFleetSeedNode(t *testing.T, st *store.Store, id string, online bool) *store.NodeRow {
	t.Helper()
	r := &store.NodeRow{ID: id, Label: id, Operator: "Jess", DNSName: id + ".tail072ad8.ts.net",
		IP: "100.70.1.2", Port: 7777, Build: "b", Contract: 7, FirstSeen: 1, LastSeen: 1, Online: online}
	if err := st.UpsertNode(r); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetNode(id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func contractFleetReply(jobs string) []byte {
	return []byte(`{"node":{"node_id":"n-b","label":"studio-mini","operator":"Jess","dns_name":"x","ips":[],"port":7777,"build":"b","contract":7},"jobs":` + jobs + `}`)
}

// Contract G3 `ApplyPoll` success arm: "SetNodePoll(ok=true,
// jobsJSON=string(reply.Jobs)), state.Fails[n.ID] = 0, changed is true iff the
// stored jobs_json differed."
func TestContractFleetApplyPollSuccess(t *testing.T) {
	st := useTestStore(t)
	n := contractFleetSeedNode(t, st, "n-b", true)
	state := &NodesState{Fails: map[string]int{"n-b": 2}}
	jobsJSON := `[{"slug":"tech-comms","state":"working"}]`

	changed, err := ApplyPoll(st, n, contractFleetReply(jobsJSON), nil, state, 5_000)
	if err != nil {
		t.Fatalf("ApplyPoll: %v", err)
	}
	if !changed {
		t.Error("first successful poll with new cards: changed = false, want true")
	}
	if state.Fails["n-b"] != 0 {
		t.Errorf("Fails = %d after success, want 0", state.Fails["n-b"])
	}
	got, _ := st.GetNode("n-b")
	if got.JobsJSON != jobsJSON || got.LastOK != 5_000 || got.LastErr != "" || !got.Online {
		t.Errorf("stored row after success = %+v", got)
	}

	// identical cards: nothing changed
	n, _ = st.GetNode("n-b")
	changed, err = ApplyPoll(st, n, contractFleetReply(jobsJSON), nil, state, 6_000)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("a poll returning the same jobs_json reported changed = true")
	}
	got, _ = st.GetNode("n-b")
	if got.LastOK != 6_000 {
		t.Errorf("LastOK = %d, want 6000 (an unchanged success still records)", got.LastOK)
	}
}

// Contract G3 `ApplyPoll` failure arm: "err != nil …: SetNodePoll(ok=false,
// errText), Fails[n.ID]++; when it reaches 3 and the row was online,
// SetNodeOnline(false) and changed = true." Then "A success on an offline row
// sets it online (changed = true)."
func TestContractFleetApplyPollThreeStrikesThenRecover(t *testing.T) {
	st := useTestStore(t)
	contractFleetSeedNode(t, st, "n-b", true)
	if err := st.SetNodePoll("n-b", true, "", `[{"slug":"kept"}]`, 1_000); err != nil {
		t.Fatal(err)
	}
	state := &NodesState{Fails: map[string]int{}}
	down := errors.New("dial tcp 100.70.1.2:7777: connect: connection refused")

	for i := 1; i <= 3; i++ {
		n, _ := st.GetNode("n-b")
		changed, err := ApplyPoll(st, n, nil, down, state, int64(1_000+i))
		if err != nil {
			t.Fatalf("strike %d: ApplyPoll: %v", i, err)
		}
		if state.Fails["n-b"] != i {
			t.Errorf("strike %d: Fails = %d", i, state.Fails["n-b"])
		}
		got, _ := st.GetNode("n-b")
		if got.LastErr == "" {
			t.Errorf("strike %d: last_err not recorded", i)
		}
		if got.JobsJSON != `[{"slug":"kept"}]` {
			t.Errorf("strike %d: failed poll dropped the card list: %q", i, got.JobsJSON)
		}
		if i < 3 {
			if changed || !got.Online {
				t.Errorf("strike %d: changed=%v online=%v, want false/true", i, changed, got.Online)
			}
		} else {
			if !changed || got.Online {
				t.Errorf("strike 3: changed=%v online=%v, want true/false (offline flip)", changed, got.Online)
			}
		}
	}

	n, _ := st.GetNode("n-b")
	changed, err := ApplyPoll(st, n, contractFleetReply(`[{"slug":"kept"}]`), nil, state, 9_000)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("success on an offline row: changed = false, want true")
	}
	got, _ := st.GetNode("n-b")
	if !got.Online || got.LastErr != "" || got.LastOK != 9_000 {
		t.Errorf("after recovery: %+v", got)
	}
	if state.Fails["n-b"] != 0 {
		t.Errorf("Fails after recovery = %d, want 0", state.Fails["n-b"])
	}
}

// --- NodesPass -----------------------------------------------------------

// contractFleetDeps is a goroutine-safe recording fake of NodesDeps (the pass
// may probe/announce/poll concurrently).
type contractFleetDeps struct {
	mu        sync.Mutex
	statusN   int
	status    core.TailnetStatus
	statusErr error
	probes    []string
	probePort []int
	probeEnv  map[string]NodeEnvelope // by IP
	announced []string
	annSelf   []NodeEnvelope
	polled    []string
	pollBody  map[string][]byte
}

func (f *contractFleetDeps) deps() NodesDeps {
	return NodesDeps{
		Status: func() (core.TailnetStatus, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.statusN++
			return f.status, f.statusErr
		},
		Probe: func(ip string, port int) (NodeEnvelope, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.probes = append(f.probes, ip)
			f.probePort = append(f.probePort, port)
			if env, ok := f.probeEnv[ip]; ok {
				return env, nil
			}
			return NodeEnvelope{}, errors.New("contract fake: no kraken")
		},
		Announce: func(n *store.NodeRow, self NodeEnvelope) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.announced = append(f.announced, n.ID)
			f.annSelf = append(f.annSelf, self)
			return nil
		},
		Poll: func(n *store.NodeRow) ([]byte, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.polled = append(f.polled, n.ID)
			if b, ok := f.pollBody[n.ID]; ok {
				return b, nil
			}
			return contractFleetReply(`[]`), nil
		},
	}
}

func (f *contractFleetDeps) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusN, f.probes, f.probePort, f.announced, f.annSelf, f.polled = 0, nil, nil, nil, nil, nil
}

func contractFleetSorted(ss []string) []string {
	out := append([]string(nil), ss...)
	sort.Strings(out)
	return out
}

func contractFleetSelf() NodeEnvelope {
	return NodeEnvelope{ID: "nSELFstable11CNTRL", Label: "Carl's MacBook Bro", Operator: "Carl",
		DNSName: "carls-macbook-bro.tail072ad8.ts.net", IPs: []string{"100.90.94.3"}, Port: 7777,
		Build: "abc", Contract: 7}
}

func contractFleetPassEnv(t *testing.T) {
	t.Helper()
	t.Setenv("KRAKEN_NODES", "")
	os.Unsetenv("KRAKEN_NODES")
}

var contractFleetNow = time.UnixMilli(1_760_000_000_000)

// Contract G3 `NodesPass`: "Returns immediately (no deps called, nothing
// written) when !FleetEnabled()".
func TestContractFleetNodesPassOffDoesNothing(t *testing.T) {
	st := useTestStore(t)
	t.Setenv("KRAKEN_NODES", "off")
	contractFleetSeedNode(t, st, "n-b", true)
	f := &contractFleetDeps{status: contractFleetStatus()}
	state := &NodesState{Fails: map[string]int{}}
	if changed := NodesPass(st, contractFleetSelf(), f.deps(), state, 1, contractFleetNow); changed {
		t.Error("disabled pass reported changed")
	}
	if f.statusN != 0 || len(f.probes) != 0 || len(f.announced) != 0 || len(f.polled) != 0 {
		t.Errorf("disabled pass called deps: status=%d probes=%v announced=%v polled=%v",
			f.statusN, f.probes, f.announced, f.polled)
	}
}

// "… or !state.Running.CompareAndSwap(false, true); clears Running on return."
func TestContractFleetNodesPassSingleFlight(t *testing.T) {
	st := useTestStore(t)
	contractFleetPassEnv(t)
	contractFleetSeedNode(t, st, "n-b", true)
	f := &contractFleetDeps{status: contractFleetStatus()}
	state := &NodesState{Fails: map[string]int{}}

	state.Running.Store(true)
	NodesPass(st, contractFleetSelf(), f.deps(), state, 1, contractFleetNow)
	if f.statusN != 0 || len(f.announced) != 0 || len(f.polled) != 0 {
		t.Errorf("a pass while one is Running called deps: status=%d announced=%v polled=%v",
			f.statusN, f.announced, f.polled)
	}
	// AMENDED 14:40: "lost-CAS call leaves Running held"
	if !state.Running.Load() {
		t.Error("a pass that lost the CAS cleared Running; the holder's flag must stay set")
	}

	state.Running.Store(false)
	NodesPass(st, contractFleetSelf(), f.deps(), state, 2, contractFleetNow)
	if len(f.polled) == 0 {
		t.Error("a pass with Running clear did not run")
	}
	if state.Running.Load() {
		t.Error("Running still set after the pass returned")
	}
}

// "Discover runs when tickN == 1 || tickN % 8 == 0: deps.Status()".
func TestContractFleetNodesPassDiscoverCadence(t *testing.T) {
	st := useTestStore(t)
	contractFleetPassEnv(t)
	f := &contractFleetDeps{status: contractFleetStatus()}
	state := &NodesState{Fails: map[string]int{}}
	for tick := 1; tick <= 24; tick++ {
		f.reset()
		NodesPass(st, contractFleetSelf(), f.deps(), state, tick, contractFleetNow)
		want := tick == 1 || tick%8 == 0
		if (f.statusN > 0) != want {
			t.Errorf("tick %d: Status called %d times, want discover=%v", tick, f.statusN, want)
		}
	}
}

// "deps.Probe per device (… a reply inserts RowFromEnvelope(reply, device.IP,
// now))", only for NodesToProbe's devices. "A status listing only self writes
// nothing."
func TestContractFleetNodesPassDiscoverInsertsResponders(t *testing.T) {
	st := useTestStore(t)
	contractFleetPassEnv(t)

	// only-self first: nothing written
	f := &contractFleetDeps{status: contractFleetStatus()}
	state := &NodesState{Fails: map[string]int{}}
	if changed := NodesPass(st, contractFleetSelf(), f.deps(), state, 1, contractFleetNow); changed {
		t.Error("only-self status: changed = true")
	}
	if len(f.probes) != 0 {
		t.Errorf("only-self status probed %v", f.probes)
	}
	if rows, _ := st.ListNodes(); len(rows) != 0 {
		t.Fatalf("only-self status wrote rows: %+v", rows)
	}

	// now a real tailnet: one new kraken, one new non-kraken, one offline, one known
	contractFleetSeedNode(t, st, "p-known", true)
	status := contractFleetStatus()
	status.Peers = []core.TailnetDevice{
		{ID: "p-kraken", IP: "100.70.0.1", Online: true},
		{ID: "p-phone", IP: "100.70.0.2", Online: true},
		{ID: "p-offline", IP: "100.70.0.3", Online: false},
		{ID: "p-known", IP: "100.70.0.4", Online: true},
	}
	reply := NodeEnvelope{ID: "p-kraken", Label: "studio-mini", Operator: "Jess",
		DNSName: "studio-mini.tail072ad8.ts.net", IPs: []string{"100.70.0.1"}, Port: 7777, Build: "b", Contract: 7}
	f = &contractFleetDeps{status: status, probeEnv: map[string]NodeEnvelope{"100.70.0.1": reply}}
	state = &NodesState{Fails: map[string]int{}}
	changed := NodesPass(st, contractFleetSelf(), f.deps(), state, 8, contractFleetNow)
	if !changed {
		t.Error("a discover that inserted a row reported changed = false")
	}
	if got := contractFleetSorted(f.probes); !reflect.DeepEqual(got, []string{"100.70.0.1", "100.70.0.2"}) {
		t.Errorf("probed %v, want exactly the two unknown online devices", got)
	}
	for _, p := range f.probePort {
		if p != contractFleetSelf().Port {
			t.Errorf("probe on port %d, want the serve port %d", p, contractFleetSelf().Port)
		}
	}
	row, err := st.GetNode("p-kraken")
	if err != nil {
		t.Fatalf("responder not inserted: %v", err)
	}
	want := RowFromEnvelope(reply, "100.70.0.1", contractFleetNow.UnixMilli())
	if row.Label != want.Label || row.IP != want.IP || row.Operator != want.Operator ||
		row.FirstSeen != want.FirstSeen || row.Port != want.Port || !row.Online {
		t.Errorf("inserted row = %+v, want RowFromEnvelope = %+v", row, want)
	}
	if _, err := st.GetNode("p-phone"); err == nil {
		t.Error("a non-responding device was inserted")
	}
}

// "Announce runs for every registered row when state.LastAnnounced differs
// from the JSON of self (first tick included), then stores that JSON in
// LastAnnounced".
func TestContractFleetNodesPassAnnounceOnChangeOnly(t *testing.T) {
	st := useTestStore(t)
	contractFleetPassEnv(t)
	contractFleetSeedNode(t, st, "n-a", true)
	contractFleetSeedNode(t, st, "n-b", false)
	f := &contractFleetDeps{status: contractFleetStatus()}
	state := &NodesState{Fails: map[string]int{}}
	self := contractFleetSelf()

	NodesPass(st, self, f.deps(), state, 2, contractFleetNow)
	if got := contractFleetSorted(f.announced); !reflect.DeepEqual(got, []string{"n-a", "n-b"}) {
		t.Errorf("first pass announced to %v, want every registered row [n-a n-b]", got)
	}
	for _, s := range f.annSelf {
		if !reflect.DeepEqual(s, self) {
			t.Errorf("announced envelope %+v, want self %+v", s, self)
		}
	}
	selfJSON, _ := json.Marshal(self)
	if state.LastAnnounced != string(selfJSON) {
		t.Errorf("LastAnnounced = %q, want the JSON of self %q", state.LastAnnounced, selfJSON)
	}

	f.reset()
	NodesPass(st, self, f.deps(), state, 3, contractFleetNow)
	if len(f.announced) != 0 {
		t.Errorf("unchanged envelope re-announced to %v", f.announced)
	}

	f.reset()
	self.Build = "newbuild"
	NodesPass(st, self, f.deps(), state, 4, contractFleetNow)
	if got := contractFleetSorted(f.announced); !reflect.DeepEqual(got, []string{"n-a", "n-b"}) {
		t.Errorf("changed envelope announced to %v, want [n-a n-b]", got)
	}
}

// "Poll runs every tick for every row with Online true, and for offline rows
// only on discover ticks; each result goes through ApplyPoll. changed is the
// OR of every ApplyPoll".
func TestContractFleetNodesPassPollCadence(t *testing.T) {
	st := useTestStore(t)
	contractFleetPassEnv(t)
	contractFleetSeedNode(t, st, "n-on", true)
	contractFleetSeedNode(t, st, "n-off", false)
	f := &contractFleetDeps{status: contractFleetStatus(), pollBody: map[string][]byte{
		"n-on": contractFleetReply(`[{"slug":"live-card"}]`),
	}}
	state := &NodesState{Fails: map[string]int{}}

	changed := NodesPass(st, contractFleetSelf(), f.deps(), state, 2, contractFleetNow)
	if got := contractFleetSorted(f.polled); !reflect.DeepEqual(got, []string{"n-on"}) {
		t.Errorf("non-discover tick polled %v, want only the online row", got)
	}
	if !changed {
		t.Error("a poll that stored a new card list did not report changed")
	}
	row, _ := st.GetNode("n-on")
	if row.JobsJSON != `[{"slug":"live-card"}]` {
		t.Errorf("polled cards not stored: %q", row.JobsJSON)
	}

	f.reset()
	if changed := NodesPass(st, contractFleetSelf(), f.deps(), state, 3, contractFleetNow); changed {
		t.Error("an identical poll reported changed")
	}

	f.reset()
	NodesPass(st, contractFleetSelf(), f.deps(), state, 16, contractFleetNow)
	if got := contractFleetSorted(f.polled); !reflect.DeepEqual(got, []string{"n-off", "n-on"}) {
		t.Errorf("discover tick polled %v, want both rows", got)
	}
}

// --- RemotePrefix --------------------------------------------------------

// Contract G3 `RemotePrefix`: "fromCell != "" → [from <label> · cell
// <fromCell>]; else [from <label> · <login>]; an empty login yields [from
// <label>]; the separator is ` · ` (space, U+00B7, space)."
func TestContractFleetRemotePrefix(t *testing.T) {
	for _, c := range []struct{ label, login, cell, want string }{
		{"studio-mini", "jess@github", "", "[from studio-mini \u00b7 jess@github]"},
		{"studio-mini", "jess@github", "tech-comms", "[from studio-mini \u00b7 cell tech-comms]"},
		{"studio-mini", "", "", "[from studio-mini]"},
	} {
		if got := RemotePrefix(c.label, c.login, c.cell); got != c.want {
			t.Errorf("RemotePrefix(%q,%q,%q) = %q, want %q", c.label, c.login, c.cell, got, c.want)
		}
	}
}
