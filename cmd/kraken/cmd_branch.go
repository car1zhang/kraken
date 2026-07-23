package main

import (
	"os"
	"os/exec"
	"strings"

	"kraken/internal/core"
	"kraken/internal/util"
)

// --- k b ---

func cmdBranch(args []string) {
	if os.Getenv("TMUX") == "" && os.Getenv("TMUX_PANE") == "" {
		util.Die("k b must run inside tmux")
	}
	all, _ := core.OrderAll(core.DiscoverAll())
	var p *core.Peer
	if len(args) > 0 {
		p = core.SelectPeer(strings.Join(args, " "), all)
	} else {
		p = core.CurrentPanePeer(all)
		if p == nil {
			util.Die("no claude in the current pane — pass a selector (k b <sel>)")
		}
	}
	if err := core.BranchGuard(p); err != nil {
		util.Die("%v", err)
	}
	cwd := core.PeerCWD(p)
	shellCmd := core.ForkCmdString(p)
	// split next to the caller, not the target — k b <sel> must not hijack
	// the peer's screen space
	target := os.Getenv("TMUX_PANE")
	if target == "" {
		target = p.Pane
	}
	cmd := exec.Command("tmux", "split-window", "-h", "-t", target, "-c", cwd, shellCmd)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		util.Die("tmux split-window: %v", err)
	}
}
