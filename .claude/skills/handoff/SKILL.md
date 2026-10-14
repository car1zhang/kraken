---
name: handoff
description: "KRAKEN-LEVEL copy (repo-owned, materialized into cell worktrees). Hand your work over without losing it: write a handoff doc into the set root, then either compact yourself and read it back (default) or spawn the continuation agents carl describes and keep going. Use when `/handoff` is typed into your pane — by carl, or by kraken itself once your context passes 75%; `/handoff resume` is the step you type into your own pane and never carl's. When the arguments say the work goes to a NEW kraken cell or job, the skill files that job itself (`kraken job draft` + `kraken cell submit`) instead of waiting for carl to."
disable-model-invocation: true
argument-hint: "[resume | how to continue: agents, slices, instructions | hand <slice> to a new cell <slug>]"
---

# Handoff — Compaction With A Written Record

> **Kraken-level copy** — loaded from `.claude/skills/handoff/` in this worktree. If the harness header says `Base directory ... ~/.claude/skills/handoff`, a personal copy shadowed this one (personal beats project on a name collision).

A compaction keeps a summary; a handoff keeps a document. `/handoff` writes the document first, so whoever continues — you after compaction, or fresh teammates — starts from a record you chose rather than one a summarizer chose. It is not a phase: it changes no stage badge and no gate, and after it you are in exactly the phase you left. Four modes, by `$ARGUMENTS`:

| `$ARGUMENTS` | Mode | Who continues |
|---|---|---|
| empty | self | you, compacted |
| `resume` | resume | you, just compacted — read the doc and go on |
| names a NEW kraken cell, job or agent (`to a new cell`, `new kraken cell`, `another kraken agent`, `as its own job`) | cell | a cell you FILE yourself with `kraken job draft` + `kraken cell submit`; you keep your context and go on |
| anything else | spawn | the agents carl described, spawned INSIDE this cell; you keep your context and wait on them |

The rows are tried top down, so the cell row outranks the catch-all, and "another kraken agent" means a new cell, not a teammate: read as spawn mode, that phrasing got a code PR shipped from an /info job's own cell (2026-10-13). Spawn mode is for arguments that describe agents to run inside THIS cell.

Who typed it does not change the procedure: kraken's daemon types a bare `/handoff` into zeus' pane when the cell's statusline reports the context at or above 75%, and that runs self mode exactly as carl's would.

## 1. Write the handoff doc

Path: `<set root>/80-handoff.md` when you are zeus; `<set root>/80-handoff-<your agent name>.md` when you are a teammate. The set root is the SET ROOT your brief names (in a cell: `<cellDir>/artifacts/`). Overwrite any existing one. Sections in this order, each paragraph ONE source line (never hard-wrapped), absolute paths, question ids as `Q<n>`, never a secret's value:

1. `# Handoff — <job slug> — <ISO timestamp>` then one sentence: the FIRST thing the continuer must do. The timestamp is the output of `date -u +%FT%TZ`, pasted — never typed from memory: a typed one comes out rounded to a minute that is not the Write's, and the continuer reads it as the moment the doc was true.
2. **Goal** — the job in two sentences.
3. **Where we are** — phase, gate, the last line you put on the board.
4. **Done** — what shipped, with paths, PRs, commit SHAs.
5. **In flight** — every live teammate by name with its report path and what it owes; open question ids; feedback notes not yet acted on; any armed watch and what it waits on.
6. **Next steps** — in order, the first one concrete enough to start cold.
7. **Decisions and why** — what you chose, what would reverse it.
8. **Gotchas** — dead ends, flaky commands, anything that cost a round.
9. **Read first** — the files whose content the continuer needs in context, with line ranges.
10. Spawn mode only: `## Agent: <name>` per continuation agent — its slice, its brief's essentials, its report path.
11. Cell mode only: `## Agent: new cell <slug>` — the slice, the measured facts or evidence it must change, its constraints, and an acceptance test a cold reader can run; this section becomes the new job's spec.

## 2a. Self mode: compact and resume

Run `kraken load clear <slug>` (you are working, not blocked), then type two commands into your own pane with kraken's own send verb, one call each, in this order:

- `kraken s . "/compact Keep one sentence: the handoff doc is <absolute path to 80-handoff.md>; read it before anything else."`
- `kraken s . "/handoff resume"`

Then say the closing line (section 4) and END YOUR TURN with no further tool call. Claude Code holds both commands until the turn ends and runs them in order: the compaction, then this skill in resume mode. Never type the resume as plain prose — a plain message is delivered INSIDE the running turn, before the compaction, and is lost to it (measured 2026-10-08). `kraken s .` resolves the pane you are typing from through `$TMUX_PANE` and refuses while a permission or question prompt is open: answer or dismiss the prompt first, then retry.

## 2b. Spawn mode: hand to teammates and keep going

Read carl's `$ARGUMENTS` as the plan: how many agents, their names if given, how the remaining work splits, any instructions. Write the doc with one `## Agent:` section each. Spawn every continuation agent as a TEAMMATE — an Agent call with a `name` and `model: opus` — whose brief is: the doc's absolute path, its own section's name, the file-output contract (its report path, outside the repo), the zsh dialect line, and `kraken mm get --file` for every file it will cite. Names: the one carl gave; else the crew role the work matches (`hephaestus-<slice>` for code, `hermes-<id>` for a figure); else `relay-<n>`. Record each name and report path in the doc's **In flight** section. You do NOT compact yourself in this mode (carl's decision, Q2 of job 548): say the closing line, then wait on the report paths per the wait protocol in `.claude/skills/do/SKILL.md` step 4 — one background until-loop over every report path, then end the turn.

## 2c. Cell mode: file the new cell yourself and keep going

Carl's `$ARGUMENTS` say the remaining work goes to a NEW kraken cell, job or agent ("hand the X fix to a new kraken cell", "handoff X to another kraken agent", "spin this off as its own job"). Do not wait for carl to file it — a cell that stops at "carl files the new cell" has measured as a stall (job 570, 2026-10-13: the lead wrote the slice, set a load estimate and went idle; carl's next message was "do the handoff and amend the skill so that you remember to do so next time"). The procedure:

1. Write the doc with its `## Agent: new cell <slug>` section (section 1, item 11). Pick the slug yourself: `<system>-<slice>`, lowercase kebab, unique among `kraken job list`'s active slugs.
2. Write the job text to a scratch file — frontmatter then spec — and file it with `kraken job draft <slug> < <file>` (the full `kraken` binary, its own command). The frontmatter is exactly `job: <slug>`, `repo: <repo>`, `solo: false` between `---` lines (the route refuses `state:` and the retired keys). The spec is the `## Agent: new cell` section rewritten as a job spec: the slice in one paragraph, a "read first" paragraph naming the handoff artifacts by ABSOLUTE path — the cell's `artifacts/` path AND the flat copies that survive teardown (`~/Documents/pluto/<slug>-<id>.<file>`, archived under `archive/`), since the new cell may start after this one is gone — the measured facts the fix must change, constraints, and the acceptance test. Never a secret's value; a credential is named by where it lives.
3. Start the cell with `kraken cell submit <slug>` (its own command). If the server refuses with its ladder sentence, record the sentence in the doc's **In flight** and leave the draft for carl — the draft is still the deliverable.
4. Record the slug and the submit result under **In flight**, then say the closing line (section 4). You do NOT compact yourself; the handoff changed the board, not your context.

## 3. Resume mode

You have just been compacted. Read your handoff doc (zeus: `<set root>/80-handoff.md`; a teammate: the one carrying your name) — a SessionStart hook will also have named every handoff doc in the set root with its age; the doc wins over the compaction summary wherever they differ. Then re-read `kraken questions get <slug>` and `kraken feedback get <slug>`, re-check that every teammate named under **In flight** is still alive (a `SendMessage` to a dead name bounces) and that every armed watch still exists, and continue from **Next steps**. If the doc says you were parked at a gate (the manual-test gate, the `/do` gate, a PR awaiting checks), restate that gate's exact line, re-set the load estimate the state map in your brief gives for that gate (self mode cleared it), and stop again — the handoff changed your context, not the job's state. If no doc exists, say so in one line and continue from the summary.

## 4. Closing line

Before the turn ends, say exactly one of:

- self mode: `handoff written: <set root>/80-handoff.md; compacting`
- spawn mode: `handoff written: <set root>/80-handoff.md (+N agents spawned); waiting on them`
- cell mode: `handoff written: <set root>/80-handoff.md (+1 cell filed: <slug>); continuing` — or `(+1 cell drafted, submit refused: <slug>)` when the ladder said no

It is the board's detail line until your next turn speaks.

## Inside A Kraken Cell

- The set root is the brief's SET ROOT (`<cellDir>/artifacts/`); the doc is an artifact like any other and syncs to the board.
- The cell's `.claude/settings.json` carries a `SessionStart` hook (matcher `compact`, `kraken handoff-hook <slug>`) that lists every `80-handoff*.md` in the set root with its age after ANY compaction, including automatic ones you never asked for: a doc older than the work you remember is history, and the compaction summary is current.
- The same file carries a `statusLine` running `kraken ctx-hook --slug <slug>`, which reports your context fill to `kraken serve`; the daemon types `/handoff` into zeus' pane at 75%, re-arms when the fill drops, retries after 15 minutes if it did not (never while your turn is still running), and after three fires inside two hours stops with one notification until the streak is two hours old. Only zeus is targeted; a teammate is handed off only by carl.
- Never run `kraken feedback` write verbs here or anywhere: an ordinary cell reads its notes and acts on them, and the column is the nightly pass' alone.
- A teammate carl hands off reports to its lead as before; the lead's doc, not the teammate's, is the one the lead resumes from.
