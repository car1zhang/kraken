package jobs

import (
	"fmt"
	"net"
	"time"

	"kraken/internal/store"
	"kraken/internal/util"
)

// stackprobe.go — the daemon pass that keeps a stack link's `status` honest.
// Every tick, for every LIVE cell, each loopback link is dialled once with a
// short timeout: a connect is `up`, anything else is `down`. The verdict is
// written only when it CHANGES (tokensPass' rule: no store write when nothing
// moved), and only transitions reach the event log, so a long manual-test
// gate does not fill either with a steady state.
//
// Why dial at all when the cell is told to remove its links: a cell forgets,
// and a chip that still reads live over a port that connection-refuses is a
// button that lies. Dimming it (the clients render `down` muted and
// struck-through, still clickable because the probe can be one tick stale
// after a restart) costs one loopback SYN per link per tick, which on this
// machine is sub-millisecond either way.
//
// Why loopback only: a non-loopback host (an ngrok tunnel, a LAN ip) says
// nothing about a process on THIS Mac, and a dropped-packet host would hold
// the tick for the whole timeout. Those links stay `unknown` forever and the
// chip renders neutral.

// Stack statuses on the wire. StackUnknown is the store's own default
// (store.StackStatusUnknown), re-exported here so the three spellings live
// in one package's vocabulary.
const (
	StackUp      = "up"
	StackDown    = "down"
	StackUnknown = store.StackStatusUnknown
)

// stackDialTimeout bounds one dial. Loopback either connects or refuses
// within a millisecond; the bound exists for the pathological case (a
// listener that accepted the SYN and then stalled its backlog), so that case
// costs a quarter second rather than the tick.
const stackDialTimeout = 250 * time.Millisecond

// stackDial is the probe's one side effect, swappable in tests so a pass can
// be run against a fake network (and so a test can assert a non-loopback
// link is NEVER handed to it).
var stackDial = func(addr string) error {
	c, err := net.DialTimeout("tcp", addr, stackDialTimeout)
	if err != nil {
		return err
	}
	return c.Close()
}

// stackProbePass probes one job's links. Skips jobs whose cell is not live:
// a dead cell's rows are about to be deleted by teardown, or already describe
// a stack that died with it, and re-dialling them every tick would churn a
// card nobody can act on.
func stackProbePass(st *store.Store, j *Job, live map[string]bool, now time.Time) {
	if st == nil || j == nil || j.ID == 0 {
		return
	}
	if _, isLive := FindCellSession(j, live); !isLive {
		return
	}
	links, err := st.ListStacks(j.ID)
	if err != nil {
		util.LogEvent("jobs", "error", "stack-probe", fmt.Sprintf("%s: list: %v", j.Slug, err))
		return
	}
	for _, l := range links {
		tgt, err := ParseStackTarget(l.URL)
		if err != nil || !tgt.Loopback {
			// unparseable rows cannot exist (the API refused them) and
			// non-loopback ones are never dialled; both stay as they are.
			continue
		}
		status := StackUp
		if err := stackDial(net.JoinHostPort(tgt.Host, tgt.Port)); err != nil {
			status = StackDown
		}
		if status == l.Status {
			continue
		}
		if err := st.SetStackStatus(j.ID, l.URL, status, now.UnixMilli()); err != nil {
			util.LogEvent("jobs", "error", "stack-probe", fmt.Sprintf("%s: %s: %v", j.Slug, l.URL, err))
			continue
		}
		util.LogEvent("jobs", "info", "stack-"+status, fmt.Sprintf("%s: %s", j.Slug, l.URL))
	}
}
