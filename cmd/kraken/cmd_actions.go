package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"kraken/internal/core"
	"kraken/internal/util"
)

// --- k n / k c ---

// splitDirFlag peels an optional "-c <dir>" off the front of args, returning
// the start directory (caller's cwd by default) and the remaining args.
func splitDirFlag(args []string) (string, []string) {
	if len(args) >= 1 && args[0] == "-c" {
		if len(args) < 2 {
			util.Die("-c needs a directory")
		}
		return args[1], args[2:]
	}
	dir, err := os.Getwd()
	if err != nil {
		dir = util.Home()
	}
	return dir, args
}

func cmdNewShell(args []string) {
	dir, rest := splitDirFlag(args)
	var argv []string
	if len(rest) > 0 {
		// run the command, then drop into an interactive shell so the pane
		// (and the command's output) survives the command exiting; exec goes
		// on its own line so a trailing comment, ';' or '&' can't eat it
		sh := core.UserShell()
		argv = []string{sh, "-c", strings.Join(rest, " ") + "\nexec " + sh}
	}
	at := core.NewWindow(dir, argv)
	if len(rest) > 0 {
		fmt.Printf("running %q in new window %s\n", strings.Join(rest, " "), at)
	} else {
		fmt.Printf("opened shell in new window %s\n", at)
	}
}

func cmdNewClaude(args []string) {
	dir, rest := splitDirFlag(args)
	if len(rest) == 0 {
		util.Die("usage: k c [-c dir] <name> [prompt...]")
	}
	name := rest[0]
	argv := []string{core.ClaudeLauncher(), "--name", name}
	argv = append(argv, core.ModelArgs("")...)
	if prompt := strings.Join(rest[1:], " "); prompt != "" {
		// "--" so a prompt starting with "-" can't be read as a claude flag
		argv = append(argv, "--", prompt)
	}
	at := core.NewWindow(dir, argv)
	fmt.Printf("started claude %q in new window %s\n", name, at)
}

// --- k o ---

func cmdOpen(args []string) {
	if len(args) == 0 {
		util.Die("usage: k o <sel>")
	}
	all, _ := core.OrderAll(core.DiscoverAll())
	p := core.SelectPeer(strings.Join(args, " "), all)
	msg, _, err := core.OpenPeer(p)
	if err != nil {
		util.Die("%v", err)
	}
	fmt.Println(msg)
}

// --- k s ---

func cmdSend(args []string) {
	if len(args) == 0 {
		util.Die("usage: k s <sel> <message...>")
	}
	// `<node>/<slug>`: a public cell on another kraken, reached through this
	// machine's serve. Only a selector with a "/" whose left half names a
	// registry node goes this way; everything else resolves locally as before.
	if n, slug, ok := remoteSelector(args[0]); ok {
		sendRemote(n, slug, sendMessage(args[1:]))
		return
	}
	all, _ := core.OrderAll(core.DiscoverAll())
	p := core.SelectPeer(args[0], all)
	if p.Role == "shell" {
		// checked again in SendToPeer, but early-exit here so a shell target
		// never blocks on a stdin read that would otherwise follow
		util.Die("%s is a shell pane — the text would run as a shell command", core.DisplayName(p))
	}
	msg := sendMessage(args[1:])
	queued, err := core.SendToPeer(p, msg)
	if err != nil {
		util.Die("%v", err)
	}
	if queued {
		fmt.Fprintf(os.Stderr, "kraken: %s is mid-turn — the message queued\n", core.DisplayName(p))
	}
	fmt.Printf("sent to %s (%s)\n", core.DisplayName(p), p.Pane)
	reportCellMessage(os.Getenv("KRAKEN_JOB_SLUG"), p)
}

// cellMessageReportTimeout bounds the best-effort report below: it runs after
// the send has already been printed, so a dead serve may delay k s' exit by
// at most this much and changes nothing else about it.
const cellMessageReportTimeout = 2 * time.Second

// cellMessageTarget is the agent name a send from cell selfSlug to p is
// reported under: only a cell's own send (selfSlug, the KRAKEN_JOB_SLUG every
// claude zeus runs with, is set) to another cell's lead (a foreground agent
// named zeus-…) counts as a cell-to-cell message.
func cellMessageTarget(selfSlug string, p *core.Peer) (to string, ok bool) {
	if strings.TrimSpace(selfSlug) == "" || p == nil || p.Depth != 0 || !strings.HasPrefix(p.Name, "zeus-") {
		return "", false
	}
	return p.Name, true
}

// reportCellMessage tells kraken serve that this cell just messaged another
// cell's zeus, for the agent map's message threads (/api/job/message). Every
// failure is swallowed: the send itself already succeeded, and the record is
// bookkeeping about it.
func reportCellMessage(selfSlug string, p *core.Peer) {
	to, ok := cellMessageTarget(selfSlug, p)
	if !ok {
		return
	}
	headers := map[string]string{fromCellHeader: strings.TrimSpace(selfSlug)}
	_, _ = qfPostWith("/api/job/message", map[string]string{"to": to}, headers, cellMessageReportTimeout)
}

// sendMessage is k s' message: the remaining args joined, else stdin.
func sendMessage(rest []string) string {
	msg := strings.Join(rest, " ")
	if msg == "" {
		if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			util.Die("no message given (pass it as args or pipe it on stdin)")
		}
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			util.Die("read stdin: %v", err)
		}
		msg = strings.TrimRight(string(data), "\n")
	}
	return msg
}

// --- k k ---

func cmdKill(args []string) {
	if len(args) == 0 {
		util.Die("usage: k k <sel>")
	}
	all, _ := core.OrderAll(core.DiscoverAll())
	p := core.SelectPeer(strings.Join(args, " "), all)
	msg, err := core.KillPeer(p)
	if err != nil {
		util.Die("%v", err)
	}
	fmt.Println(msg)
}
