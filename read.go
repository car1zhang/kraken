package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"kraken/core"
	"kraken/util"
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
		switch b.Kind {
		case "user":
			fmt.Println(util.Paint(color, util.Cyan, "❯ user"))
			fmt.Println(b.Text)
		case "thinking":
			fmt.Println(util.Paint(color, util.Dim, "· thinking"))
			fmt.Println(util.Paint(color, util.Dim, b.Text))
		case "teammate":
			fmt.Println(util.Paint(color, util.Dim, "⇄ "+b.From))
			fmt.Println(util.Paint(color, util.Dim, b.Text))
		case "tool":
			fmt.Println(util.Paint(color, util.Dim, "→ "+b.Text))
		default:
			fmt.Println(b.Text)
		}
	}
}
