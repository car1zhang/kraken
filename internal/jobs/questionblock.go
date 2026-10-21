package jobs

import (
	"strings"

	"kraken/internal/store"
)

// questionblock.go — the one daemon-side sign of a mid-build question round.
//
// Since job 629, a zeus that meets a product decision mid-/do files it as
// question rows and stops on the P0s (`.claude/skills/do/SKILL.md`, step 5).
// Kraken deliberately does NOT wake the cell when a row is answered — the
// operator's decision (Q1 of that job): carl answers on the board and then
// prompts the pane himself, and the recall hook carries the answers into that
// prompt. So the blocked notification is the only place the daemon can say
// WHAT the cell is waiting on, and a cell parked on a round needs two things
// from carl rather than one (an answer, then a steer), which is why its body
// names the open P0 instead of the generic "see the board".

// blockedNotifyBody picks the body of the "blocked" notification from the
// job's question rows: an unanswered P0 names itself, anything else — no rows,
// rows that are all answered, open rows that are only P1/P2 — is the generic
// line the daemon has always sent. Pure, so the table test needs no store.
func blockedNotifyBody(rows []store.DocQuestion) string {
	var open []string
	for _, q := range rows {
		if q.Importance == "P0" && strings.TrimSpace(q.Answer) == "" {
			open = append(open, q.QID)
		}
	}
	if len(open) == 0 {
		return "blocked — see the board"
	}
	return "waiting on " + strings.Join(open, ", ") + " (P0) — answer on the board, then steer"
}

// jobQuestionRows reads a job's question rows for the notification. Every
// failure — no store wired, a read error — reads as no rows, so the
// notification degrades to its generic body rather than going unsent.
func jobQuestionRows(j *Job) []store.DocQuestion {
	st := ActiveStore()
	if st == nil || j == nil {
		return nil
	}
	rows, err := st.ListQuestions(j.ID)
	if err != nil {
		return nil
	}
	return rows
}
