package api

import "net/http"

// Register wires every JSON API route onto mux — the board, its move
// actions, the job editor, and the peers/health endpoints. Route strings are
// unchanged from when they lived in cmdWeb: the iOS app and web/app.js
// address them by these exact paths.
func Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/health", healthHandler)
	mux.HandleFunc("/api/peers", peersHandler(defaultPeerSource))
	mux.HandleFunc("/api/peers/read", peersReadHandler(defaultPeerSource))
	mux.HandleFunc("/api/peers/capture", peersCaptureHandler(defaultPeerSource))
	mux.HandleFunc("/api/peers/send", peersSendHandler(defaultPeerSource))
	mux.HandleFunc("/api/peers/spawn", peersSpawnHandler)
	mux.HandleFunc("/api/peers/kill", peersKillHandler(defaultPeerSource))
	mux.HandleFunc("/api/peers/open", peersOpenHandler(defaultPeerSource))
	mux.HandleFunc("/api/board", boardHandler)
	mux.HandleFunc("/api/draft", draftHandler)
	mux.HandleFunc("/api/queue", webAction("queue", webQueue))
	mux.HandleFunc("/api/dequeue", webAction("dequeue", webDequeue))
	mux.HandleFunc("/api/done", webAction("done", webDone))
	mux.HandleFunc("/api/question", webAction("question", webAnswerQuestion))
	mux.HandleFunc("/api/retry", webAction("retry", webRetry))
	mux.HandleFunc("/api/kill", webAction("kill", webKill))
	mux.HandleFunc("/api/steer", webAction("steer", webSteer))
	mux.HandleFunc("/api/archive", webAction("archive", webArchive))
	mux.HandleFunc("/api/unarchive", webAction("unarchive", webUnarchive))
	mux.HandleFunc("/api/delete", webAction("delete", webDelete))
	mux.HandleFunc("/api/reactivate", webAction("reactivate", webReactivate))
	mux.HandleFunc("/api/job/raw", webJobRaw)
	mux.HandleFunc("/api/job/create", webJobCreate)
	mux.HandleFunc("/api/job/save", webJobSave)
	mux.HandleFunc("/api/job/form", webJobForm)
	mux.HandleFunc("/api/job/saveform", webJobSaveForm)
	mux.HandleFunc("/api/upload", uploadHandler)
	mux.HandleFunc("/api/logs/files", webLogFiles)
	mux.HandleFunc("/api/logs", webLogRead)
}
