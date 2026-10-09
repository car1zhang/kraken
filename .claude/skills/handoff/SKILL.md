---
name: handoff
description: "KRAKEN-LEVEL copy (repo-owned, materialized into cell worktrees). Hand your work over without losing it: write a handoff doc into the set root, then either compact yourself and read it back (default) or spawn the continuation agents carl describes and keep going. Use when `/handoff` is typed into your pane — by carl, or by kraken itself once your context passes 75%; `/handoff resume` is the step you type into your own pane and never carl's."
disable-model-invocation: true
argument-hint: "[resume | how to continue: agents, slices, instructions]"
---

# Handoff — Compaction With A Written Record

> **Kraken-level copy** — loaded from `.claude/skills/handoff/` in this worktree. If the harness header says `Base directory ... ~/.claude/skills/handoff`, a personal copy shadowed this one (personal beats project on a name collision).

A compaction keeps a summary; a handoff keeps a document. `/handoff` writes the document first, so whoever continues — you after compaction, or fresh teammates — starts from a record you chose rather than one a summarizer chose. It is not a phase: it changes no stage badge and no gate, and after it you are in exactly the phase you left. Three modes, by the argument text you were invoked with. Claude Code substitutes that text wherever the placeholder appears, so it appears exactly once, on the next line, and the rest of this file names it as "the arguments" (a second placeholder anywhere else would be replaced by the argument text too):

Arguments: $ARGUMENTS

| Arguments | Mode | Who continues |
|---|---|---|
| empty | self | you, compacted |
| `resume` | resume | you, just compacted — read the doc and go on |
| anything else | spawn | the agents carl described; you keep your context and wait on them |

Who typed it does not change the procedure: kraken's daemon types a bare `/handoff` into zeus' pane when the cell's statusline reports the context at or above 75%, and that runs self mode exactly as carl's would.

## 1. Write the handoff doc

Path: `<set root>/80-handoff.md` when you are zeus; `<set root>/80-handoff-<your agent name>.md` when you are a teammate. The set root is the SET ROOT your brief names (in a cell: `<cellDir>/artifacts/`). Overwrite any existing one. Sections in this order, each paragraph ONE source line (never hard-wrapped), absolute paths, question ids as `Q<n>`, never a secret's value:

1. `# Handoff — <job slug> — <ISO timestamp>` then one sentence: the FIRST thing the continuer must do.
2. **Goal** — the job in two sentences.
3. **Where we are** — phase, gate, the last line you put on the board.
4. **Done** — what shipped, with paths, PRs, commit SHAs.
5. **In flight** — every live teammate by name with its report path and what it owes; open question ids; feedback notes not yet acted on; any armed watch and what it waits on.
6. **Next steps** — in order, the first one concrete enough to start cold.
7. **Decisions and why** — what you chose, what would reverse it.
8. **Gotchas** — dead ends, flaky commands, anything that cost a round.
9. **Read first** — the files whose content the continuer needs in context, with line ranges.
10. Spawn mode only: `## Agent: <name>` per continuation agent — its slice, its brief's essentials, its report path.

## 2a. Self mode: compact and resume

Run `kraken load clear <slug>` (you are working, not blocked), then type two commands into your own pane with kraken's own send verb, one call each, in this order:

- `kraken s . "/compact Keep one sentence: the handoff doc is <absolute path to 80-handoff.md>; read it before anything else."`
- `kraken s . "/handoff resume"`

Then say the closing line (section 4) and END YOUR TURN with no further tool call. Claude Code holds both commands until the turn ends and runs them in order: the compaction, then this skill in resume mode. Never type the resume as plain prose — a plain message is delivered INSIDE the running turn, before the compaction, and is lost to it (measured 2026-10-08). `kraken s .` resolves the pane you are typing from through `$TMUX_PANE` and refuses while a permission or question prompt is open: answer or dismiss the prompt first, then retry.

## 2b. Spawn mode: hand to teammates and keep going

Read carl's arguments (the Arguments line at the top) as the plan: how many agents, their names if given, how the remaining work splits, any instructions. Write the doc with one `## Agent:` section each. Spawn every continuation agent as a TEAMMATE — an Agent call with a `name` and `model: opus` — whose brief is: the doc's absolute path, its own section's name, the file-output contract (its report path, outside the repo), the zsh dialect line, and `kraken mm get --file` for every file it will cite. Names: the one carl gave; else the crew role the work matches (`hephaestus-<slice>` for code, `hermes-<id>` for a figure); else `relay-<n>`. Record each name and report path in the doc's **In flight** section. You do NOT compact yourself in this mode (carl's decision, Q2 of job 548): say the closing line, then wait on the report paths per the wait protocol in `.claude/skills/do/SKILL.md` step 4 — one background until-loop over every report path, then end the turn.

## 3. Resume mode

You have just been compacted. Read your handoff doc (zeus: `<set root>/80-handoff.md`; a teammate: the one carrying your name) — a SessionStart hook will also have named every handoff doc in the set root with its age; the doc wins over the compaction summary wherever they differ. Then re-read `kraken questions get <slug>` and `kraken feedback get <slug>`, re-check that every teammate named under **In flight** is still alive (a `SendMessage` to a dead name bounces) and that every armed watch still exists, and continue from **Next steps**. If the doc says you were parked at a gate (the manual-test gate, the `/do` gate, a PR awaiting checks), restate that gate's exact line, re-set the load estimate the state map in your brief gives for that gate (self mode cleared it), and stop again — the handoff changed your context, not the job's state. If no doc exists, say so in one line and continue from the summary.

## 4. Closing line

Before the turn ends, say exactly one of:

- self mode: `handoff written: <set root>/80-handoff.md; compacting`
- spawn mode: `handoff written: <set root>/80-handoff.md (+N agents spawned); waiting on them`

It is the board's detail line until your next turn speaks.

## Inside A Kraken Cell

- The set root is the brief's SET ROOT (`<cellDir>/artifacts/`); the doc is an artifact like any other and syncs to the board.
- The cell's `.claude/settings.json` carries a `SessionStart` hook (matcher `compact`, `kraken handoff-hook <slug>`) that lists every `80-handoff*.md` in the set root with its age after ANY compaction, including automatic ones you never asked for: a doc older than the work you remember is history, and the compaction summary is current.
- The same file carries a `statusLine` running `kraken ctx-hook --slug <slug>`, which reports your context fill to `kraken serve`; the daemon types `/handoff` into zeus' pane at 75%, re-arms when the fill drops, retries after 15 minutes if it did not (never while your turn is still running), and after three fires inside two hours stops with one notification until the streak is two hours old. Only zeus is targeted; a teammate is handed off only by carl.
- Never run `kraken feedback` write verbs here or anywhere: an ordinary cell reads its notes and acts on them, and the column is the nightly pass' alone.
- A teammate carl hands off reports to its lead as before; the lead's doc, not the teammate's, is the one the lead resumes from.
