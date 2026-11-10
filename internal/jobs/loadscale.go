package jobs

// loadscale.go — the ruler behind a job's load estimate: the two 1-10
// ladders, one anchored rung each, plus the state map and the two pricing
// rules the judge applies before it reads a rung.
//
// The estimate used to be zeus' own, written from a skill file that carried
// this same text. It is now Jev's (loadjudge.go): after every turn the
// daemon hands Jev the lead's final message and this ruler, and Jev's
// yes/no answers land the two numbers. The ruler therefore lives in Go,
// where the judge can read it, and the web board (web/_ui/src/lib/format.ts)
// and the iOS app (ios/Kraken/Models.swift) carry the same twenty labels for
// their drilldowns — a cross-source test pins all three byte for byte, since
// a board that prints "Live E2E drive" where the ruler means something else
// is a board the operator cannot read the ladder off.

// LoadRung is one rung of a ladder: the label every surface prints, and the
// sentence the judge recognizes it by.
type LoadRung struct {
	Label     string
	Recognize string
}

// UnblockLadder is what unblocking the job costs the operator, rung 1 first.
var UnblockLadder = [10]LoadRung{
	{"Mechanical", "One-word reply, click a button, merge, run one pasted command. Zero thought. Review-only gates land here."},
	{"Read one page, pick", "A one-page artifact or short note, then a menu pick or yes/no — recommendations already given."},
	{"Answer a few concrete questions", "Each with a recommendation attached; a minute or two of thought total. Most spec-gate P0 batches land here."},
	{"Local smoke, or a credential", "Install the change locally and click around briefly; or create a secret, an account or an auth grant."},
	{"Live E2E drive", "A test server, a phone build via TestFlight, a real booking or dispatch watched end to end."},
	{"Genuine review", "A diff or artifact the operator must actually absorb before replying. Rare by policy — only when their eyes are truly required."},
	{"Decision batch needing new context", "They must read several artifacts or build understanding they do not have before they can answer."},
	{"Deep decision, real consequences", "An architecture pick, an irreversible cutover, a spend commitment."},
	{"Many deep decisions in one sitting", "A full set review where most questions are hard and coupled."},
	{"A day's thought", "The whole design is on them."},
}

// ScopeLadder is how big the claude work the operator's reply BUYS is —
// strictly the work still ahead — rung 1 first.
var ScopeLadder = [10]LoadRung{
	{"One-message close-out", "Open the PR, final report, stop."},
	{"One small round", "Apply one fix and push; rewrap one artifact; a small edit plus re-review."},
	{"One focused artifact or round", "An /info section doc plus diagram; one full review and fix round."},
	{"A single-slice implementation", "One coherent feature plus tests, no teammate fan-out."},
	{"A moderate slice", "Implementation plus athena review rounds; a day-scale build."},
	{"A full /aim set draft", "Overview plus diagrams plus section docs plus questions; or a multi-round solo build."},
	{"A delegated build", "Hephaestus fan-out plus blind tests plus join plus review rounds."},
	{"Full phase-2 of a substantial spec", "Several slices, several PRs likely."},
	{"A multi-PR program", "Repeated build, review and ship cycles on one job."},
	{"The largest coordinated effort", "Many teammates, a massive migration, sustained revision over days."},
}

// loadDirective is the standing instruction the judge is handed with every
// turn: the two pricing rules and the state map. The ladders are not in it;
// they ride as the rubric's own questions, one per rung.
const loadDirective = `You price what it will cost the operator to unblock a stopped kraken cell, and how much claude work their reply buys. The cell's lead has just ended a turn and the cell is blocked on the operator; its final message is in last_message, with the job's open questions, PRs and stack links beside it.
Two rules. (1) unblock prices the operator's ACTUAL move, never the nominal ask. They ship PRs on trust: a "review the diff and reply" gate costs them a one-word reply, not a review. Only three things genuinely cost them: driving a live environment, creating credentials or secrets, and decisions they must actually think about. (2) scope prices only the claude work the reply BUYS — strictly the work still ahead, never the work under review. At a manual-test gate the PR is already open, so a passing reply buys only shutting test services down and stopping, and a change request buys one revision round pushed to that PR, however large the feature behind the gate. Scope is legitimately big only at a spec gate, where the answers unlock the whole build.
State map, looked up before any ladder is read (unblock/scope): PR awaiting checks, nothing else needed 1/1 · manual-test gate, review-only 1/1 · manual-test gate, local install and drive 4/1 · manual-test gate, live E2E environment 5/2 · /info round, go-deeper pick 2/3 · /aim spec gate, P0s block /do 3/6-8 · credential, secret or auth need 4/the blocked work · mid-build question round with a P0 filed 3/the remaining build (7 if the operator must build context first) · two asks at once: price the EXPENSIVE ask.
A cell whose message says it is working, has teammates running or needs nothing is not blocked on the operator; the daemon never asks about one. Price honestly: an inflated scope takes the operator's attention from a cell that earned it, and an inflated unblock buries a cheap gate under work they cannot start.`
