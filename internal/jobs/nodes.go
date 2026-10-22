// Protocol parity: this file is part of the MULTIPLAYER protocol (CLAUDE.md
// § Protocol parity). The protocol is kraken-core's and identical in every
// fork; a change here lands in kraken-core first, carries "protocol:" in its
// PR title, and is named to the operator as a protocol change before it is
// pushed.

package jobs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
	"kraken/internal/util"
)

// nodes.go — the daemon's nodes pass: how this kraken finds its siblings on
// the tailnet and keeps their card lists. Three sub-passes on the one tick:
//
//   - discover (first tick, then every 8th — two minutes at the 15s tick):
//     read tailscale status, probe every online device the registry does not
//     know on this node's serve port, insert the ones that answer with a
//     kraken envelope;
//   - announce (first tick, and whenever this node's envelope changes): post
//     the envelope to every registered node, so a sibling learns of this one
//     without waiting for its own discover;
//   - poll (every tick for online nodes, discover ticks for offline ones):
//     fetch each node's /api/node/jobs and store the card list.
//
// There is no seed list and nothing to approve: a kraken joins the fleet by
// being on the tailnet. KRAKEN_NODES=off (FleetEnabled) is the one way out.

// nodeHTTPClient is the client every real network seam uses. Two seconds is
// the whole budget for one sibling: the board never waits on it (it reads the
// stored card list), so a slow sibling costs a stale list, not a slow board.
var nodeHTTPClient = &http.Client{Timeout: 2 * time.Second}

// nodeReplyMax bounds a sibling's reply. A node's card list is the same
// objects its own board carries, so this is generous; it exists so a
// misbehaving device cannot make this process read without end.
const nodeReplyMax = 16 << 20

// NodesDeps are the pass' network seams.
type NodesDeps struct {
	Status   func() (core.TailnetStatus, error)
	Probe    func(ip string, port int) (NodeEnvelope, error)
	Announce func(n *store.NodeRow, self NodeEnvelope) error
	Poll     func(n *store.NodeRow) ([]byte, error)
}

// NodesState is the pass' per-process memory: consecutive poll failures per
// node id, the envelope JSON last announced, and the single-flight flag.
type NodesState struct {
	Fails         map[string]int
	LastAnnounced string
	Running       atomic.Bool
}

// OnFleetChanged is called after a pass that changed what the board shows.
// jobs cannot import api, so api installs the callback into jobs (its board
// cache's invalidate), through SetOnFleetChanged.
var OnFleetChanged func()

// onFleetChangedMu guards OnFleetChanged: api installs it from the serve's
// HTTP setup while the tick goroutine may already be running a pass.
var onFleetChangedMu sync.Mutex

// SetOnFleetChanged installs the callback race-free.
func SetOnFleetChanged(f func()) {
	onFleetChangedMu.Lock()
	OnFleetChanged = f
	onFleetChangedMu.Unlock()
}

// fleetChanged runs the installed callback, if any.
func fleetChanged() {
	onFleetChangedMu.Lock()
	f := OnFleetChanged
	onFleetChangedMu.Unlock()
	if f != nil {
		f()
	}
}

// NodesToProbe is the discover step's work list: online peers, not this
// device, whose id no registered row carries — in status order.
func NodesToProbe(status core.TailnetStatus, known []*store.NodeRow) []core.TailnetDevice {
	have := make(map[string]bool, len(known))
	for _, n := range known {
		have[n.ID] = true
	}
	out := []core.TailnetDevice{}
	for _, d := range status.Peers {
		if !d.Online || d.Self || d.ID == status.Self.ID && d.ID != "" || have[d.ID] {
			continue
		}
		out = append(out, d)
	}
	return out
}

// RowFromEnvelope is the registry row for a node that answered from ip.
func RowFromEnvelope(env NodeEnvelope, ip string, now int64) *store.NodeRow {
	return &store.NodeRow{
		ID: env.ID, Label: env.Label, Operator: env.Operator, DNSName: env.DNSName,
		IP: ip, Port: env.Port, Build: env.Build, Contract: env.Contract,
		FirstSeen: now, LastSeen: now, Online: true,
	}
}

// nodesOfflineAfter is how many consecutive failed polls mark a node offline.
const nodesOfflineAfter = 3

// ApplyPoll records one poll of n. A reply that decodes, with a jobs array,
// stores the list and resets the failure count; anything else counts a
// failure, and the third in a row marks an online node offline. changed says
// whether what the board shows moved: a different card list, or a node going
// offline or coming back.
func ApplyPoll(st *store.Store, n *store.NodeRow, body []byte, err error, state *NodesState, now int64) (changed bool, applyErr error) {
	if state.Fails == nil {
		state.Fails = map[string]int{}
	}
	// the stored row, not n: n may be a snapshot from before this tick's
	// discover or an earlier poll, and "did the list change" is a question
	// about what the board is serving now
	cur, gerr := st.GetNode(n.ID)
	if gerr != nil {
		return false, gerr
	}
	var reply NodeJobsReply
	if err == nil {
		if derr := json.Unmarshal(body, &reply); derr != nil {
			err = fmt.Errorf("undecodable reply: %v", derr)
		} else if t := bytes.TrimSpace(reply.Jobs); len(t) == 0 || t[0] != '[' {
			err = fmt.Errorf("undecodable reply: no jobs array")
		}
	}
	if err == nil {
		jobsJSON := string(reply.Jobs)
		if serr := st.SetNodePoll(n.ID, true, "", jobsJSON, now); serr != nil {
			return false, serr
		}
		state.Fails[n.ID] = 0
		if !cur.Online {
			util.LogEvent("nodes", "info", "node-online", "node "+nodeName(cur)+" answered again")
		}
		return cur.JobsJSON != jobsJSON || !cur.Online, nil
	}
	if serr := st.SetNodePoll(n.ID, false, core.TruncateOneLine(err.Error(), 200), "", now); serr != nil {
		return false, serr
	}
	state.Fails[n.ID]++
	if state.Fails[n.ID] >= nodesOfflineAfter && cur.Online {
		if serr := st.SetNodeOnline(n.ID, false); serr != nil {
			return false, serr
		}
		util.LogEvent("nodes", "info", "node-offline", fmt.Sprintf("node %s offline after %d failed polls: %s", nodeName(cur), state.Fails[n.ID], core.TruncateOneLine(err.Error(), 200)))
		return true, nil
	}
	return false, nil
}

func nodeName(n *store.NodeRow) string {
	if n.Label != "" {
		return n.Label
	}
	return n.ID
}

// nodesDiscoverEvery is the discover cadence in ticks (2 minutes at 15s).
const nodesDiscoverEvery = 8

// NodesPass runs one tick of the fleet. It is single-flighted on
// state.Running, so a tick that arrives while the previous pass is still
// waiting on the network does nothing. Network calls within a sub-pass run
// concurrently; every store write happens on this goroutine afterwards.
func NodesPass(st *store.Store, self NodeEnvelope, deps NodesDeps, state *NodesState, tickN int, now time.Time) (changed bool) {
	if !FleetEnabled() || st == nil {
		return false
	}
	if !state.Running.CompareAndSwap(false, true) {
		return false
	}
	defer state.Running.Store(false)
	if state.Fails == nil {
		state.Fails = map[string]int{}
	}
	nowMs := now.UnixMilli()
	discover := tickN == 1 || tickN%nodesDiscoverEvery == 0

	if discover && deps.Status != nil && deps.Probe != nil {
		if status, err := deps.Status(); err == nil {
			known, kerr := st.ListNodes()
			if kerr == nil {
				changed = discoverNodes(st, self, deps, NodesToProbe(status, known), nowMs) || changed
			}
		}
	}

	rows, err := st.ListNodes()
	if err != nil {
		util.LogEvent("nodes", "error", "nodes-list", "list nodes: "+err.Error())
		return changed
	}

	if selfJSON, err := json.Marshal(self); err == nil && string(selfJSON) != state.LastAnnounced {
		if deps.Announce != nil {
			forEachConcurrent(rows, func(n *store.NodeRow) {
				if aerr := deps.Announce(n, self); aerr != nil {
					util.LogEvent("nodes", "info", "node-announce-failed", "announce to "+nodeName(n)+": "+core.TruncateOneLine(aerr.Error(), 200))
				}
			})
		}
		state.LastAnnounced = string(selfJSON)
	}

	if deps.Poll == nil {
		return changed
	}
	var due []*store.NodeRow
	for _, n := range rows {
		if n.Online || discover {
			due = append(due, n)
		}
	}
	type pollResult struct {
		body []byte
		err  error
	}
	results := make([]pollResult, len(due))
	forEachIndexed(len(due), func(i int) {
		b, perr := deps.Poll(due[i])
		results[i] = pollResult{b, perr}
	})
	for i, n := range due {
		c, aerr := ApplyPoll(st, n, results[i].body, results[i].err, state, nowMs)
		if aerr != nil {
			util.LogEvent("nodes", "error", "node-poll-apply", "record poll of "+nodeName(n)+": "+aerr.Error())
		}
		changed = changed || c
	}
	return changed
}

// discoverNodes probes devs and inserts every one that answers with an
// envelope carrying that device's own tailscale id and a diallable port.
func discoverNodes(st *store.Store, self NodeEnvelope, deps NodesDeps, devs []core.TailnetDevice, now int64) bool {
	envs := make([]NodeEnvelope, len(devs))
	oks := make([]bool, len(devs))
	forEachIndexed(len(devs), func(i int) {
		if devs[i].IP == "" {
			return
		}
		env, err := deps.Probe(devs[i].IP, self.Port)
		// the reply must name the device tailscale says lives at that
		// address (a row is bound to a verified identity, never to a claim)
		// and carry a port it can be dialled on, as announce checks
		if err == nil && env.ID != "" && env.ID == devs[i].ID && env.Port >= 1 && env.Port <= 65535 {
			envs[i], oks[i] = env, true
		}
	})
	inserted := false
	for i, d := range devs {
		if !oks[i] {
			continue
		}
		if err := st.UpsertNode(RowFromEnvelope(envs[i], d.IP, now)); err != nil {
			util.LogEvent("nodes", "error", "node-insert", "record node "+d.DNSName+": "+err.Error())
			continue
		}
		inserted = true
		util.LogEvent("nodes", "info", "node-new", "found kraken node "+envs[i].Label+" ("+d.DNSName+")")
	}
	return inserted
}

func forEachIndexed(n int, f func(i int)) {
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			f(i)
		}(i)
	}
	wg.Wait()
}

func forEachConcurrent(rows []*store.NodeRow, f func(n *store.NodeRow)) {
	forEachIndexed(len(rows), func(i int) { f(rows[i]) })
}

// nodeURL is the base URL of a sibling's serve.
func nodeURL(ip string, port int) string {
	return "http://" + net.JoinHostPort(ip, strconv.Itoa(port))
}

// nodeGet GETs url and returns the body of a 200, or an error naming the
// status. The body is bounded by nodeReplyMax.
func nodeGet(url string) ([]byte, error) {
	resp, err := nodeHTTPClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, nodeReplyMax))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return b, nil
}

// realNodesDeps are the production seams.
var realNodesDeps = NodesDeps{
	Status: func() (core.TailnetStatus, error) { return tailscaleStatus() },
	Probe: func(ip string, port int) (NodeEnvelope, error) {
		b, err := nodeGet(nodeURL(ip, port) + "/api/node")
		if err != nil {
			return NodeEnvelope{}, err
		}
		var env NodeEnvelope
		if err := json.Unmarshal(b, &env); err != nil {
			return NodeEnvelope{}, err
		}
		return env, nil
	},
	Announce: func(n *store.NodeRow, self NodeEnvelope) error {
		body, err := json.Marshal(self)
		if err != nil {
			return err
		}
		resp, err := nodeHTTPClient.Post(nodeURL(n.IP, n.Port)+"/api/node/announce", "application/json", bytes.NewReader(body))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, nodeReplyMax))
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return nil
	},
	Poll: func(n *store.NodeRow) ([]byte, error) {
		return nodeGet(nodeURL(n.IP, n.Port) + "/api/node/jobs")
	},
}

// notifyNewNodes fires one notification per node the first time it appears in
// the registry, deduped per stable id through d.Notified. It runs on the tick
// goroutine (d.Notified is the tick's own map, never touched by the pass'
// goroutine), so a node inserted by a pass — or by a sibling's announce — is
// announced to carl on the next tick. The first call seeds every row already
// registered without notifying: d.Notified is in-memory, and a serve restart
// must not re-announce the whole fleet.
func (d *daemonState) notifyNewNodes() {
	st := ActiveStore()
	if st == nil {
		return
	}
	rows, err := st.ListNodes()
	if err != nil {
		return
	}
	seeding := d.Notified["nodes:seeded"] == 0
	for _, n := range rows {
		key := "node:" + n.ID
		if d.Notified[key] != 0 {
			continue
		}
		d.Notified[key] = 1
		if !seeding {
			who := nodeName(n)
			if n.Operator != "" {
				who += " · " + n.Operator
			}
			notify("kraken: new node", who+" joined the fleet — its readonly and public jobs are on the board")
		}
	}
	d.Notified["nodes:seeded"] = 1
}

// nodesPass is the tick's call: the whole pass on its own goroutine, since it
// waits on the network and the tick must not. NodesPass' own single-flight is
// what keeps a slow pass from overlapping the next tick's.
func (d *daemonState) nodesPass(now time.Time) {
	// Only a process actually serving is a node: kraken serve records its
	// bound address (SetHookServeAddr) before its ticks start, and a process
	// that never did has nothing to announce and no port a sibling could
	// reach — the daemon tick run from a test is that case, and must not
	// shell out to tailscale or probe the real tailnet.
	if currentHookServeAddr() == "" {
		return
	}
	d.notifyNewNodes()
	if !FleetEnabled() || d.nodes.Running.Load() {
		return // NodeSelf below shells out; skip it when the pass would not run
	}
	tickN := d.tickN
	go func() {
		if NodesPass(ActiveStore(), NodeSelf(servePort()), realNodesDeps, &d.nodes, tickN, now) {
			fleetChanged()
		}
	}()
}
