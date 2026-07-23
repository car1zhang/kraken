package main

import (
	"kraken/internal/util"
	"kraken/web"
)

func cmdWeb(args []string) {
	util.InitSelfCheck()
	port := "7777"
	tailnet := false
	explicitAddr := ""
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
		default:
			util.Die("unknown argument: %s (usage: k web [-p port] [--tailnet] [-addr host:port])", args[i])
		}
	}
	if err := web.Serve(web.Options{Port: port, Tailnet: tailnet, ExplicitAddr: explicitAddr}); err != nil {
		util.Die("%v", err)
	}
}
