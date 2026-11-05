package jobs

import (
	"fmt"
	"time"

	"kraken/internal/store"
	"kraken/internal/util"
)

// jobmsg.go — the jobs-level side of the CELL-TO-CELL MESSAGE record: how
// often one job's cell has messaged another job's zeus through `kraken s`.
// The sending `kraken s` knows its own job from KRAKEN_JOB_SLUG and the target
// pane's agent name, and reports the pair to kraken serve
// (internal/api/jobmsg.go), which bumps the row here. The agent map draws one
// thread per pair of cards from the rows (webJob.talks).
//
// Like spawn links, nothing goes through WriteJobFields: the rows live in
// store.job_messages (schema v40) so neither job's `rev` or `updated_at`
// moves, and only a hard delete removes them.

// RecordCellMessage counts one message from `from`'s cell to `to`'s zeus at
// the current time. A nil store refuses rather than degrading (LinkSpawn's
// reason: a silent no-op is a message that was never recorded), as do a job
// with no id and a job messaging itself.
func RecordCellMessage(from, to *Job) error {
	if from == nil || to == nil {
		return fmt.Errorf("record cell message: a job is missing")
	}
	st := ActiveStore()
	if st == nil {
		return fmt.Errorf("record cell message %s -> %s: no store is wired", from.Slug, to.Slug)
	}
	if from.ID == 0 || to.ID == 0 {
		return fmt.Errorf("record cell message %s -> %s: a job with no id cannot be recorded", from.Slug, to.Slug)
	}
	if from.ID == to.ID {
		return fmt.Errorf("record cell message %s: a cell messaging itself is not recorded", from.Slug)
	}
	if err := st.BumpJobMessage(from.ID, to.ID, time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("record cell message %s -> %s: %w", from.Slug, to.Slug, err)
	}
	util.LogEvent("jobs", "info", "cell-message", from.Slug+" -> "+to.Slug)
	return nil
}

// JobForZeusName is the active job whose zeus answers to name, through
// ZeusNameCandidates so a renamed job's zeus (still running under its old
// --name) resolves too; nil when none does. A job whose CURRENT zeus name
// matches wins over one that merely used to answer to it, the precedence
// LoadJobBySlug gives a current slug over a former one.
func JobForZeusName(name string) *Job {
	if name == "" {
		return nil
	}
	js := LoadJobs()
	for _, j := range js {
		if ZeusName(j) == name {
			return j
		}
	}
	for _, j := range js {
		if isZeusName(j, name) {
			return j
		}
	}
	return nil
}

// ListCellMessages is the read the board goes through: the whole table. No
// store means no rows, never an error.
func ListCellMessages() []store.JobMessage {
	st := ActiveStore()
	if st == nil {
		return nil
	}
	rows, err := st.ListJobMessages()
	if err != nil {
		util.LogEvent("jobs", "error", "cell-message-list", err.Error())
		return nil
	}
	return rows
}
