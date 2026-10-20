package jobs

import (
	"errors"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// Regression pin (review round 2, not a spec test): discover inserts nothing
// for a probe reply whose port could never be dialled.
func TestNodesRegressDiscoverDropsAnUndiallablePort(t *testing.T) {
	st := useTestStore(t)
	t.Setenv("KRAKEN_NODES", "")
	deps := NodesDeps{
		Status: func() (core.TailnetStatus, error) {
			return core.TailnetStatus{
				Self:  core.TailnetDevice{ID: "self", Self: true},
				Peers: []core.TailnetDevice{{ID: "p1", IP: "100.64.0.9", Online: true}},
			}, nil
		},
		Probe: func(string, int) (NodeEnvelope, error) {
			return NodeEnvelope{ID: "p1", Label: "studio", Port: 0}, nil
		},
		Announce: func(*store.NodeRow, NodeEnvelope) error { return nil },
		Poll:     func(*store.NodeRow) ([]byte, error) { return nil, errors.New("unused") },
	}
	var state NodesState
	if NodesPass(st, NodeEnvelope{ID: "self", Port: 7777}, deps, &state, 1, time.Now()) {
		t.Error("a pass that inserted nothing reported a change")
	}
	if _, err := st.GetNode("p1"); err == nil {
		t.Fatal("a probe reply on port 0 was inserted")
	}
}
