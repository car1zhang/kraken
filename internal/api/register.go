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
	// A code ref clicked in an artifact (editor.go). Mirror-gated with the
	// peers open it is modelled on, and for the same reason: it drives a real
	// GUI app on this Mac regardless of which DB is wired. Its own
	// local-origin gate is a second, narrower refusal inside the handler.
	mux.HandleFunc("/api/editor/open", mirrorGated(editorOpenHandler))
	mux.HandleFunc("/api/board", boardHandler)
	mux.HandleFunc("/api/usage", usageHandler)
	mux.HandleFunc("/api/account/switch", accountSwitchHandler)
	mux.HandleFunc("/api/account/mode", accountModeHandler)
	mux.HandleFunc("/api/draft", draftHandler)
	// The submit: /api/queue is the URL a shipped iOS build already posts for
	// its draft→queued drag, so the path is kept (Q4) while the action behind
	// it is now "start this job's cell, now, synchronously". /api/dequeue and
	// /api/start are gone with the queued column they served — nothing can
	// reach a state that either one addressed.
	mux.HandleFunc("/api/queue", webAction("submit", webSubmit))
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
	// The mental model (mm.go): carl's own record of what he has read,
	// decided and noted. Every one of these is an exact pattern, so none
	// shadows another and none is a subtree — the same property that lets
	// "/api/feedback" and "/api/feedback/sweep" coexist above.
	//
	// The two GETs are ungated: a read is a read, and a mirror board must be
	// able to SHOW the model it may not write. The seven writes are gated via
	// mirrorGated at registration rather than inline, which is available here
	// for the reason it is on /api/slack/ack: every one of
	// them is a POST-ONLY path, so there is no GET on the same path that a
	// route wrapper would take down with it — which is also why the list and
	// the snapshot were given paths of their own rather than a GET arm on
	// /api/mm/... alongside a write.
	mux.HandleFunc("/api/mm", mmListHandler)
	mux.HandleFunc("/api/mm/snapshot", mmSnapshotHandler)
	mux.HandleFunc("/api/mm/decision", mirrorGated(mmDecisionHandler))
	mux.HandleFunc("/api/mm/artifact", mirrorGated(mmArtifactHandler))
	mux.HandleFunc("/api/mm/element", mirrorGated(mmElementHandler))
	mux.HandleFunc("/api/mm/code", mirrorGated(mmCodeHandler))
	mux.HandleFunc("/api/mm/note", mirrorGated(mmNoteHandler))
	mux.HandleFunc("/api/mm/detail", mirrorGated(mmDetailHandler))
	mux.HandleFunc("/api/mm/remove", mirrorGated(mmRemoveHandler))
	// Mid-conversation recall (recall.go): the cell hook's POST. Gated like
	// every other agent-side write, because a firing records what it
	// delivered (the per-job notes record) — it reads like a query and is not
	// one.
	mux.HandleFunc("/api/recall", mirrorGated(recallHandler))
	mux.HandleFunc("/api/questions", questionsHandler)
	// The artifact library (library.go). The first two are exact patterns, so
	// neither shadows the other — the same property that lets "/api/feedback"
	// and "/api/feedback/sweep" coexist above. The third is a SUBTREE, the one
	// on this surface beside /api/artifact/ and /api/upload/ over in web: its
	// path carries the row id and a display filename rather than a query
	// string, so a saved copy downloads under a real name with a real
	// extension. Both POSTs gate themselves INLINE (never mirrorGated here) —
	// /api/library's GET must stay reachable under --mirror, and the remove
	// gates the same way so a caller reaching the handler directly still hits
	// the refusal.
	mux.HandleFunc("/api/library", libraryHandler)
	mux.HandleFunc("/api/library/remove", libraryRemoveHandler)
	mux.HandleFunc("/api/library/artifact/", libraryArtifactHandler)
	// Goals (goals.go). Two exact patterns, so neither shadows the other and
	// neither is a subtree — the same property that lets "/api/job/raw" and
	// "/api/job/load" coexist below. /api/goals gates its POST inline so the
	// GET stays reachable under --mirror (a read is a read, and the phone's
	// drawer plus `kraken goals get` are both reads); /api/job/goal is
	// POST-only and gates itself inline too, so a caller reaching the handler
	// directly still hits the refusal.
	mux.HandleFunc("/api/goals", goalsHandler)
	// The job→goal link is a one-field route for the reason /api/job/color
	// and /api/job/load are (jobcolor.go, jobload.go): the form save's
	// editableJob gate would put labelling out of reach on exactly the live
	// cells it is most useful on, and a full form save from a client that
	// predates the field would clear it.
	mux.HandleFunc("/api/job/goal", jobGoalHandler)
	mux.HandleFunc("/api/threads", threadsHandler)
	mux.HandleFunc("/api/thread/read", threadReadHandler)
	mux.HandleFunc("/api/retry", webAction("retry", webRetry))
	mux.HandleFunc("/api/kill", webAction("kill", webKill))
	mux.HandleFunc("/api/steer", webAction("steer", webSteer))
	mux.HandleFunc("/api/archive", webAction("archive", webArchive))
	mux.HandleFunc("/api/unarchive", webAction("unarchive", webUnarchive))
	mux.HandleFunc("/api/delete", webAction("delete", webDelete))
	mux.HandleFunc("/api/reactivate", webAction("reactivate", webReactivate))
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
	// Rename is a one-field route for the same editableJob reason as those
	// two, plus one of its own: a slug is identity rather than a field, so it
	// is never written as a side effect of an editor save (jobrename.go). Its
	// mirror gate is inline rather than a mirrorGated wrapper here so a caller
	// reaching the handler directly still hits it.
	mux.HandleFunc("/api/job/rename", webJobRename)
	// The Slack intake bot (slack.go, jobdraft.go). Every one of these is an
	// exact pattern, so none shadows another and none is a subtree — the same
	// property that lets "/api/job/raw" and "/api/job/load" coexist above.
	// The three reads stay open under --mirror (a read is a read); the two
	// WRITES are gated via mirrorGated at registration rather than inline,
	// which is available here because both are POST-ONLY paths, so there is
	// no GET on the same path that a route wrapper would take down with them.
	//
	// /api/job/draft is iris' only write surface on a job, and its gate list
	// is the safety property of the whole feature rather than a validation
	// nicety — see jobdraft.go's header.
	mux.HandleFunc("/api/slack/inbox", slackInboxHandler)
	mux.HandleFunc("/api/slack/status", slackStatusHandler)
	mux.HandleFunc("/api/slack/ack", mirrorGated(slackAckHandler))
	// /api/cell/context is a cell claude's statusline report (`kraken
	// ctx-hook --slug`), feeding the daemon's auto-handoff pass. POSTed by a
	// HOOK running inside the cell's pane rather than by an agent deciding to
	// call it, and gated anyway because it WRITES.
	mux.HandleFunc("/api/cell/context", mirrorGated(cellContextHandler))
	mux.HandleFunc("/api/job/list", jobListHandler)
	mux.HandleFunc("/api/job/draft", mirrorGated(jobDraftHandler))
	mux.HandleFunc("/api/upload", uploadHandler)
	// The board's background image (background.go): GET serves it, POST
	// uploads/replaces it, DELETE removes it. Ungated because ITS write
	// touches nothing but the wired kraken.db — no tmux pane, no process, no
	// file on disk — which is a strictly weaker footprint than /api/upload
	// above, itself ungated while also materializing a real file under
	// ~/.kraken/uploads.
	mux.HandleFunc("/api/background", backgroundHandler)
	mux.HandleFunc("/api/logs/files", webLogFiles)
	mux.HandleFunc("/api/logs", webLogRead)
	mux.HandleFunc("/api/pr-attach", prAttachHandler)
	// The cell-side StopFailure hook's report (limithit.go). Ungated on
	// purpose, like its neighbour /api/pr-attach and unlike the mirror-gated
	// writes further up: a mirror must still RECORD that a cell hit a usage
	// limit — it is the rescue, not the observation, that a mirror must never
	// perform, and that gate lives in the daemon's own !mirror block.
	mux.HandleFunc("/api/limit-hit", limitHitHandler)
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
