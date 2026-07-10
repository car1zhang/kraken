package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// --- selection ---

func tokenize(s string) []string {
	var tokens []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, strings.ToLower(cur.String()))
			cur.Reset()
		}
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return tokens
}

func peerHaystack(p *Peer) string {
	return strings.ToLower(strings.Join([]string{
		p.Name, p.AgentName, projectLabel(p), p.TmuxSession, p.Pane, p.Team,
	}, " "))
}

func currentPanePeer(ordered []*Peer) *Peer {
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return nil
	}
	for _, p := range ordered {
		if p.Pane == pane {
			return p
		}
	}
	return nil
}

func selectPeer(sel string, ordered []*Peer) *Peer {
	if sel == "" {
		die("selector required")
	}
	if len(ordered) == 0 {
		die("no live sessions")
	}
	if sel == "." {
		if p := currentPanePeer(ordered); p != nil {
			return p
		}
		die("no session found in the current pane (is $TMUX_PANE set?)")
	}
	// exact pane id (%3): the one churn-proof selector — indexes shift as
	// sessions come and go, pane ids never do
	if strings.HasPrefix(sel, "%") {
		for _, p := range ordered {
			if p.Pane == sel {
				return p
			}
		}
		die("no session in pane %s", sel)
	}
	if n, err := strconv.Atoi(sel); err == nil && n >= 1 && n <= len(ordered) {
		return ordered[n-1]
		// out-of-range numbers fall through to name matching: tmux sessions
		// are often literally named "0", "1", ...
	}
	if len(sel) >= 4 {
		var matches []*Peer
		for _, p := range ordered {
			if p.SessionID != "" && strings.HasPrefix(p.SessionID, strings.ToLower(sel)) {
				matches = append(matches, p)
			}
		}
		if len(matches) == 1 {
			return matches[0]
		}
	}
	tokens := tokenize(sel)
	if len(tokens) == 0 {
		die("no session matches %q", sel)
	}
	var matches []*Peer
	for _, p := range ordered {
		hay := peerHaystack(p)
		ok := true
		for _, t := range tokens {
			if !strings.Contains(hay, t) {
				ok = false
				break
			}
		}
		if ok {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 0:
		die("no session matches %q", sel)
	case 1:
		return matches[0]
	}
	var names []string
	for _, m := range matches {
		names = append(names, fmt.Sprintf("%d:%s", m.Index, displayName(m)))
	}
	die("%q matches %d sessions: %s", sel, len(matches), strings.Join(names, ", "))
	return nil
}
