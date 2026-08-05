package api

import "net/http"

// Register wires every JSON API route onto mux — the board, its move
// actions, the job editor, and the peers/health endpoints. Route strings are
// wire-pinned: the iOS app and web/app.js address them by these exact
// paths.
func Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/health", healthHandler)
	mux.HandleFunc("/api/peers", peersHandler(defaultPeerSource))
	mux.HandleFunc("/api/peers/read", peersReadHandler(defaultPeerSource))
	mux.HandleFunc("/api/peers/capture", peersCaptureHandler(defaultPeerSource))
	mux.HandleFunc("/api/peers/send", mirrorGated(peersSendHandler(defaultPeerSource)))
	mux.HandleFunc("/api/peers/spawn", mirrorGated(peersSpawnHandler))
	mux.HandleFunc("/api/peers/kill", mirrorGated(peersKillHandler(defaultPeerSource)))
	mux.HandleFunc("/api/peers/open", mirrorGated(peersOpenHandler(defaultPeerSource)))
	mux.HandleFunc("/api/board", boardHandler)
	mux.HandleFunc("/api/usage", usageHandler)
	mux.HandleFunc("/api/account/switch", accountSwitchHandler)
	mux.HandleFunc("/api/account/mode", accountModeHandler)
	mux.HandleFunc("/api/draft", draftHandler)
	mux.HandleFunc("/api/queue", webAction("queue", webQueue))
	mux.HandleFunc("/api/dequeue", webAction("dequeue", webDequeue))
	mux.HandleFunc("/api/done", webAction("done", webDone))
	mux.HandleFunc("/api/question", webAction("question", webAnswerQuestion))
	mux.HandleFunc("/api/doc-question", webAction("doc-question", webAnswerDocQuestion))
	mux.HandleFunc("/api/feedback", feedbackHandler)
	// The dream loop's two feedback reads. Both are exact patterns, so
	// neither shadows "/api/feedback" (a ServeMux exact pattern and a
	// different exact pattern never collide) and neither is a subtree.
	mux.HandleFunc("/api/feedback/sweep", feedbackSweepHandler)
	mux.HandleFunc("/api/feedback/snapshot", feedbackSnapshotHandler)
	// The watchlist's two write surfaces, split by column ownership — carl's
	// verdict on /api/watchlist, the agent's observation edits on
	// /api/watchlist/item (see watchlist.go's header). Both POSTs gate
	// themselves inline so their GET stays reachable under --mirror.
	mux.HandleFunc("/api/watchlist", watchlistHandler)
	mux.HandleFunc("/api/watchlist/item", watchItemHandler)
	mux.HandleFunc("/api/dream", dreamHandler)
	// The knowledge graph. Every one of these is an exact pattern, so none
	// shadows another and none is a subtree — the same property that lets
	// "/api/feedback" and "/api/feedback/sweep" coexist above. The four reads
	// stay open under --mirror (a read is a read; search is a POST only
	// because a query is arbitrary user text, and it writes nothing); the
	// three writes are gated with the same 403 every other agent-side write
	// gets.
	mux.HandleFunc("/api/graph", graphHandler)
	mux.HandleFunc("/api/graph/stats", graphStatsHandler)
	mux.HandleFunc("/api/graph/node", graphNodeHandler)
	mux.HandleFunc("/api/graph/pending", graphPendingHandler)
	mux.HandleFunc("/api/graph/search", graphSearchHandler)
	// Recall is a THIRD write despite reading like a search: it records what
	// it delivered in kg_recall, so it is gated with add and episode-done
	// rather than left open with the reads (recall.go).
	mux.HandleFunc("/api/graph/recall", mirrorGated(graphRecallHandler))
	mux.HandleFunc("/api/graph/add", mirrorGated(graphAddHandler))
	mux.HandleFunc("/api/graph/episode-done", mirrorGated(graphEpisodeDoneHandler))
	mux.HandleFunc("/api/questions", questionsHandler)
	mux.HandleFunc("/api/threads", threadsHandler)
	mux.HandleFunc("/api/thread/read", threadReadHandler)
	mux.HandleFunc("/api/retry", webAction("retry", webRetry))
	mux.HandleFunc("/api/kill", webAction("kill", webKill))
	mux.HandleFunc("/api/steer", webAction("steer", webSteer))
	mux.HandleFunc("/api/archive", webAction("archive", webArchive))
	mux.HandleFunc("/api/unarchive", webAction("unarchive", webUnarchive))
	mux.HandleFunc("/api/delete", webAction("delete", webDelete))
	mux.HandleFunc("/api/reactivate", webAction("reactivate", webReactivate))
	mux.HandleFunc("/api/start", webAction("start", webStart))
	mux.HandleFunc("/api/stop", webAction("stop", webStop))
	mux.HandleFunc("/api/reorder", webReorderHandler)
	mux.HandleFunc("/api/job/raw", webJobRaw)
	mux.HandleFunc("/api/job/create", webJobCreate)
	mux.HandleFunc("/api/job/save", webJobSave)
	mux.HandleFunc("/api/job/form", webJobForm)
	mux.HandleFunc("/api/job/saveform", webJobSaveForm)
	// Colour is its own one-field route rather than part of the form save:
	// it is cosmetic, so it is deliberately usable on a LIVE cell (see
	// webJobColor). Its own mirror gate lives inside the handler, beside the
	// PushJobColor call that is the only thing needing one.
	mux.HandleFunc("/api/job/color", webJobColor)
	// Load is a one-field route for the same reason colour is (see
	// jobcolor.go): the form save's editableJob gate would put it out of
	// reach on exactly the blocked, live-celled jobs it exists for. Its POST
	// gates itself inline so the GET stays reachable under --mirror.
	mux.HandleFunc("/api/job/load", jobLoadHandler)
	// The Slack intake bot (slack.go, jobdraft.go). Every one of these is an
	// exact pattern, so none shadows another and none is a subtree — the same
	// property that lets "/api/job/raw" and "/api/job/load" coexist above.
	// The three reads stay open under --mirror (a read is a read); the two
	// WRITES are gated via mirrorGated at registration rather than inline,
	// which is available here for the reason it is on /api/graph/add: both
	// are POST-ONLY paths, so there is no GET on the same path that a route
	// wrapper would take down with them.
	//
	// /api/job/draft is iris' only write surface on a job, and its gate list
	// is the safety property of the whole feature rather than a validation
	// nicety — see jobdraft.go's header.
	mux.HandleFunc("/api/slack/inbox", slackInboxHandler)
	mux.HandleFunc("/api/slack/status", slackStatusHandler)
	mux.HandleFunc("/api/slack/ack", mirrorGated(slackAckHandler))
	mux.HandleFunc("/api/job/list", jobListHandler)
	mux.HandleFunc("/api/job/draft", mirrorGated(jobDraftHandler))
	mux.HandleFunc("/api/upload", uploadHandler)
	mux.HandleFunc("/api/logs/files", webLogFiles)
	mux.HandleFunc("/api/logs", webLogRead)
	mux.HandleFunc("/api/pr-attach", prAttachHandler)
	mux.HandleFunc("/api/logevent", logEventHandler)
	// The update POST is mirror-gated for the same reason every tmux-driving
	// route is, only more so: a mirror board is read-only by policy, and
	// rebuilding + reinstalling + restarting the LIVE deployment's binary is
	// the most destructive action on this whole surface. Its status GET is a
	// read and stays open.
	mux.HandleFunc("/api/update", mirrorGated(updateHandler))
	mux.HandleFunc("/api/update/status", updateStatusHandler)
	// Ship-iOS is gated for the same reason: a mirror board is read-only by
	// policy, and this one spends ten minutes of the Mac's CPU and pushes a
	// build to carl's real App Store Connect account under a build number
	// that can never be reused. Its status GET is a read and stays open.
	mux.HandleFunc("/api/ship-ios", mirrorGated(shipIOSHandler))
	mux.HandleFunc("/api/ship-ios/status", shipIOSStatusHandler)
}
