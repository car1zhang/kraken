package main

import (
	"kraken/jobs"
	"kraken/util"
	"kraken/web"
)

func cmdWeb(args []string) {
	util.InitSelfCheck()
	port := "7777"
	tailnet := false
	explicitAddr := ""
	forceFileMode := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "--port":
			i++
			if i >= len(args) {
				util.Die("-p needs a port")
			}
			port = args[i]
		case "--tailnet":
			tailnet = true
		case "-addr":
			i++
			if i >= len(args) {
				util.Die("-addr needs a host:port")
			}
			explicitAddr = args[i]
		case "--force-file-mode":
			forceFileMode = true
		default:
			util.Die("unknown argument: %s (usage: k web [-p port] [--tailnet] [-addr host:port] [--force-file-mode])", args[i])
		}
	}
	// E2: `k web` is a file-mode-only server (it never calls jobs.UseStore) —
	// refuse to start it against a machine that has already migrated onto
	// the sqlite store, where `kraken serve` is the authoritative writer.
	if err := jobs.GuardFileModeStart(forceFileMode); err != nil {
		util.Die("%v", err)
	}
	if err := web.Serve(web.Options{Port: port, Tailnet: tailnet, ExplicitAddr: explicitAddr}); err != nil {
		util.Die("%v", err)
	}
}
