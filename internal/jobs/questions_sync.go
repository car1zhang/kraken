package jobs

// questions_sync.go — a thin exported entrypoint onto sync.go's block-event
// sync machinery (kept unexported there, trigger-bound to the daemon's
// working→blocked roll-call and teardownCell), for the one caller outside
// this package that needs a single question's answer to become durable in
// the store promptly rather than waiting for the next tick: the web API's
// answer-a-doc-question handler.

// SyncQuestionsNow re-mirrors j's live questions.yaml into the store's
// questions table immediately — a no-op when no store is wired or j is
// file-backed (ID == 0), same contract as syncQuestionsFile/syncArtifactsNow.
// Call it right after a successful jobs.AnswerDocQuestion so the answer
// doesn't sit unmirrored until the cell's next working→blocked cycle or
// teardown. Errors are soft-failed (logged, swallowed) — same reasoning as
// syncArtifactsNow: a sync hiccup here must never turn a successful file
// write into a failed API response, since questions.yaml itself is already
// the durable record.
func SyncQuestionsNow(j *Job) {
	st := ActiveStore()
	if st == nil || j.ID == 0 {
		return
	}
	if err := syncQuestionsFile(st, j.ID, j); err != nil {
		dlog("%s: questions sync (answer) failed: %v", j.Slug, err)
	}
}
