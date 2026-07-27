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
