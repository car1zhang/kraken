package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"kraken/internal/core"
	"kraken/internal/util"
)

// --- k r ---

func cmdRead(args []string) {
	if len(args) == 0 {
		util.Die("usage: k r <sel> [n]")
	}
	// the last arg is n only when it parses and a selector precedes it, so
	// multi-word selectors like "k r asr tuning" still work
	sel, n := strings.Join(args, " "), 1
	if v, err := strconv.Atoi(args[len(args)-1]); err == nil && len(args) > 1 {
		if v < 1 {
			util.Die("n must be a positive integer")
		}
		n = v
		sel = strings.Join(args[:len(args)-1], " ")
	}
	// `<node>/<slug>`: a public cell on another kraken (see cmdSend)
	if node, slug, ok := remoteSelector(sel); ok {
		readRemote(node, slug, n)
		return
	}
	all, _ := core.OrderAll(core.DiscoverAll())
	p := core.SelectPeer(sel, all)
	path, err := core.ReadTranscriptPath(p)
	if err != nil {
		util.Die("%v", err)
	}
	blocks, counted, err := core.LastBlocks(path, n)
	if err != nil {
		util.Die("%v", err)
	}
	if counted == 0 {
		fmt.Println("no output blocks yet")
		return
	}
	if counted < n {
		fmt.Fprintf(os.Stderr, "kraken: only %d block(s) available\n", counted)
	}
	color := util.UseColor()
	for i, b := range blocks {
		if i > 0 {
			fmt.Println()
		}
		printReadBlock(color, b.Kind, b.Text, b.From)
	}
}

// printReadBlock prints one transcript block in k r's format — shared by the
// local read and the cross-node one (readRemote), so a remote cell reads the
// same as a local one.
func printReadBlock(color bool, kind, text, from string) {
	switch kind {
	case "user":
		fmt.Println(util.Paint(color, util.Cyan, "❯ user"))
		fmt.Println(text)
	case "thinking":
		fmt.Println(util.Paint(color, util.Dim, "· thinking"))
		fmt.Println(util.Paint(color, util.Dim, text))
	case "teammate":
		fmt.Println(util.Paint(color, util.Dim, "⇄ "+from))
		fmt.Println(util.Paint(color, util.Dim, text))
	case "tool":
		fmt.Println(util.Paint(color, util.Dim, "→ "+text))
	default:
		fmt.Println(text)
	}
}
