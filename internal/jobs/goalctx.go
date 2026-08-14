package jobs

import (
	"strings"
	"time"

	"kraken/internal/store"
)

// goalctx.go — the second system-prompt injection: what a new cell's zeus is
// told about carl's CURRENT goals, staged beside graphctx.go's block.
//
// It runs on the same SPAWN path graphctx.go does, so it inherits that file's
// three rules verbatim (graphctx.go:13-34), and each one is why a piece of
// this file looks the way it does:
//
//  1. It NEVER calls a network. Goals are three indexed local reads — four
//     when a linked goal has to be resolved by id; nothing here embeds,
//     fetches or resolves anything off the machine.
//
//  2. It NEVER fails a spawn. Every error path — no store, an unreadable
//     list, a job row that will not load — returns "". A cell whose goals
//     table is sick and a cell with no goals at all get a BYTE-IDENTICAL
//     brief, which is what makes the whole feature safely revertible.
//
//  3. It never opens its own database handle. store.Open runs MIGRATIONS and
//     this repo's discipline is a single writer (`kraken serve`); a spawn path
//     opening a second handle would migrate the database from whatever process
//     happens to be spawning a cell. So: ActiveStore() or nothing.
//
// The renderer below has TWO call sites — goalContext here (spawn time) and
// Recall (recall.go, mid-conversation) — and it is one function rather than
// two for the reason keepRelevantFacts is: a second copy of the nesting, the
// exclusion rules and the marker would drift silently, and the injected block
// would start meaning slightly different things depending on which path
// produced it, with nothing that fails when it does.

// goalContextBytes caps the whole block including its header. The brief has a
// 13,500-byte ceiling of its own (TestZeusBriefStaysLean) and the graph block
// spends another 682 beside this one, so every byte here is a byte of
// attention spent before the job's own spec.
//
// 700 is roughly a dozen goal lines at realistic lengths — a list zeus reads
// at a glance, which is what a goals list is for. A week that needs more than
// that is a week whose goals are tasks.
const goalContextBytes = 700

// goalContextHeader opens the block. It states WHOSE goals these are (they are
// carl's, not the job's), and it names the pull verb once — the block is a
// snapshot, the verb is how zeus re-reads it after carl edits the board, and a
// snapshot that does not say where the live copy lives is a snapshot zeus has
// to trust forever. The leading newline separates it from whatever the brief's
// own last line was, exactly as graphContextHeader's does.
const goalContextHeader = "\nCarl's goals right now (re-read any time: `kraken goals get <slug>`):\n"

// goalContextMarker is the literal that flags the goal THIS job was linked to
// on the board. It is the one line in the block that is about this cell rather
// than about carl's week, so it is marked in place rather than hoisted into a
// section of its own.
const goalContextMarker = " ← this job's goal"

// goalNestPrefix indents a day goal under its weekly parent. The arrow is what
// makes the nesting visible in a plain-text brief, where indentation alone
// reads as accidental wrapping.
const goalNestPrefix = "    ↳ "

// goalByID is the seam onto the store's by-id goal read: the ONE place in this
// package that resolves a goal that is NOT in the current week's or today's
// list. It exists because the job's linked goal is included unconditionally —
// done, or left over from a period that has rolled — while every other goal in
// the block comes from the two current-period lists.
//
// Held as a var so a test can drive the marker and the out-of-period case
// without seeding a goal in a period the clock has moved past.
var goalByID = func(st *store.Store, id int64) *store.Goal {
	g, err := st.GetGoal(id)
	if err != nil {
		return nil
	}
	return g
}

// goalContext returns the block appended to zeus' brief for job j, or "" when
// there is no store and when carl has no goals worth stating.
//
// "" is the important answer and the common one, for graphContext's reason: a
// cell started in a week with nothing written down must get the brief it would
// have got before this feature existed, byte for byte.
func goalContext(j *Job) string {
	if j == nil {
		return ""
	}
	st := ActiveStore()
	if st == nil {
		return ""
	}
	return renderGoalsBlock(st, jobGoalID(st, j))
}

// jobGoalID reads the goal this job is linked to, or 0.
//
// The link lives on the store's job row (store.Job.GoalID) rather than on
// jobs.Job, because it is set by a route (`/api/job/goal`) that deliberately
// touches neither `rev` nor `updated_at` — so a jobs.Job in hand may predate
// the link by any amount of time, and re-reading the row is the only way to
// see it. Every failure — a file-backed job with no row, a missing row, a
// read error — is 0, which is the unlinked job's answer.
func jobGoalID(st *store.Store, j *Job) int64 {
	var (
		row *store.Job
		err error
	)
	switch {
	case j.ID != 0:
		row, err = st.GetJobByID(j.ID)
	case strings.TrimSpace(j.Slug) != "":
		row, err = st.GetJobBySlug(j.Slug)
	default:
		return 0
	}
	if err != nil || row == nil {
		return 0
	}
	return row.GoalID
}

// renderGoalsBlock is THE renderer, shared by goalContext above and Recall
// (recall.go). Given a store and the id of the goal this job is linked to (0
// for none), it returns the block or "".
//
// What goes in, and why each rule is a rule:
//
//   - Every UNDONE goal in the CURRENT week and CURRENT day (store.GoalPeriods
//     off the local clock, matching the dream loop's day boundary). A done goal
//     is a goal that no longer steers anything, and the block is what zeus
//     steers by.
//   - A day goal carrying a parent renders INDENTED under that parent's line.
//     The nesting is the whole reason parents exist: "today: land the PR" means
//     something different under "this week: ship goal tracking" than it does
//     alone.
//   - The job's linked goal is included WHATEVER its state — done, or from a
//     period that has since rolled — and marked. It is the one goal the cell
//     was pointed at deliberately, and dropping it because carl ticked it off
//     would silently remove the only line that says what this cell is for.
func renderGoalsBlock(st *store.Store, linkedID int64) string {
	if st == nil {
		return ""
	}
	week, day := store.GoalPeriods(time.Now())
	weekGoals, err := st.ListGoals("week", week)
	if err != nil {
		return ""
	}
	dayGoals, err := st.ListGoals("day", day)
	if err != nil {
		return ""
	}

	// Which week goals survive the done-filter, resolved BEFORE the day goals
	// are bucketed: a day goal whose parent is not being rendered cannot be
	// nested under a line that is not there, and it still has to appear (it is
	// an undone goal for today). It degrades to the unassigned group, which is
	// the same degradation store.DeleteGoal gives a day goal whose parent row
	// is deleted outright.
	shown := map[int64]bool{}
	var weekShown []store.Goal
	for _, g := range weekGoals {
		if g.Done && g.ID != linkedID {
			continue
		}
		shown[g.ID] = true
		weekShown = append(weekShown, g)
	}

	children := map[int64][]store.Goal{}
	var unassigned []store.Goal
	inBlock := map[int64]bool{}
	for _, g := range weekShown {
		inBlock[g.ID] = true
	}
	for _, g := range dayGoals {
		if g.Done && g.ID != linkedID {
			continue
		}
		inBlock[g.ID] = true
		if g.ParentID != 0 && shown[g.ParentID] {
			children[g.ParentID] = append(children[g.ParentID], g)
			continue
		}
		unassigned = append(unassigned, g)
	}

	// Lines are assembled as GROUPS — one top-level goal followed by whatever
	// nests under it — because the byte cap below treats the two positions
	// differently, and it can only do that if it can still tell them apart.
	var groups [][]string
	// The linked goal LEADS when it is not already in the current lists — first
	// group under the header, so it is the one line the cap is least likely to
	// reach. (Least likely, not immune: an in-list linked goal is an ordinary
	// line in ordinary position and the cap binds on it exactly as on any
	// other.)
	if linkedID != 0 && !inBlock[linkedID] {
		if g := goalByID(st, linkedID); g != nil {
			groups = append(groups, []string{goalLine(*g, false, true)})
		}
	}
	for _, g := range weekShown {
		group := []string{goalLine(g, false, g.ID == linkedID)}
		for _, c := range children[g.ID] {
			group = append(group, goalLine(c, true, c.ID == linkedID))
		}
		groups = append(groups, group)
	}
	for _, g := range unassigned {
		groups = append(groups, []string{goalLine(g, false, g.ID == linkedID)})
	}
	if len(groups) == 0 {
		return ""
	}

	// The cap stops BETWEEN goals, never inside one: a half-written goal reads
	// as a complete (and wrong) instruction, which is the one way this block
	// could actively mislead. Which whole goals it drops depends on position,
	// and the asymmetry is the point:
	//
	//   - A top-level goal that does not fit is SKIPPED and the next group is
	//     tried, exactly as renderGraphBlock skips an over-long fact. Breaking
	//     here would let ONE goal written as a paragraph blank every goal
	//     ranked below it — and, when it is the first, blank the whole block.
	//     Its children go with it: a nested line whose parent line was never
	//     written would read as nesting under whatever line precedes it.
	//   - Inside a group the loop BREAKS, because there the lines are not
	//     independent: children are ordered under one head, and taking a later
	//     child over an earlier one only changes which day goal is silently
	//     missing from a group that claims to list them.
	var b strings.Builder
	b.WriteString(goalContextHeader)
	written := 0
	for _, group := range groups {
		if b.Len()+len(group[0]) > goalContextBytes {
			continue
		}
		b.WriteString(group[0])
		written++
		for _, child := range group[1:] {
			if b.Len()+len(child) > goalContextBytes {
				break
			}
			b.WriteString(child)
			written++
		}
	}
	if written == 0 {
		// A header with nothing under it announces that carl has goals and
		// then declines to say what they are.
		return ""
	}
	return b.String()
}

// goalLine renders one goal: `- [week] <text>` at the top level, `    ↳ <text>`
// when nested under its weekly parent, plus `(done)` for the one done goal the
// block ever carries and the linked-goal marker.
//
// The scope label is on the TOP-LEVEL line only — a nested line's arrow already
// says it is a day goal, and the label there would be noise on every child.
// Done is stated rather than implied because the only done goal that reaches
// here is the job's own linked goal, and "the goal this cell serves is already
// ticked off" is exactly the kind of thing zeus must not have to infer.
//
// The text is ONE-LINED first (goalOneLine), for factLine's reason: a newline
// inside a goal forges extra block lines, and a forged line can carry the
// nesting arrow or the linked-goal marker while belonging to neither.
func goalLine(g store.Goal, nested, linked bool) string {
	var b strings.Builder
	if nested {
		b.WriteString(goalNestPrefix)
	} else {
		b.WriteString("- [")
		b.WriteString(g.Scope)
		b.WriteString("] ")
	}
	b.WriteString(goalOneLine(g.Text))
	if g.Done {
		b.WriteString(" (done)")
	}
	if linked {
		b.WriteString(goalContextMarker)
	}
	b.WriteString("\n")
	return b.String()
}

// goalOneLine collapses a goal's text onto one line: control characters
// (newlines, tabs, a stray \x1b) become spaces and whitespace runs collapse to
// one, which is core.TruncateOneLine's normalization exactly.
//
// What it deliberately does NOT do is core.TruncateOneLine's other half, the
// rune cap. A goal is carl's own sentence and it appears WHOLE or not at all:
// the only budget this block enforces is its byte cap, and that one drops whole
// goals rather than cutting one short. An ellipsis here would put a goal on the
// wire that says something carl did not write, which is worse than a goal the
// block had no room for.
func goalOneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}
