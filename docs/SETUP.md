# Setting up kraken on your own Mac

Follow this top to bottom on a Mac you own and you end with the board at `http://127.0.0.1:7777`, a launchd daemon that survives reboots, and one job run through a cell; it takes about forty minutes, most of it Homebrew and the first Go build.

Every path is under your home directory; nothing here needs admin rights except Homebrew and the macOS prompts.

## 0. What kraken assumes

- A Mac you keep a terminal open on. The CLI and daemon have no stated minimum macOS; the optional macOS board app (`macos/project.yml`) needs macOS 15 or later.
- You drive Claude Code (and optionally Codex) sessions in **tmux** panes; kraken manages those panes. It does not create your tmux workflow for you.
- **Ghostty** is the terminal for `kraken o` and the board's "raise" buttons; everything else works in any terminal.
- Your git checkouts live as direct children of one directory (default `~/Developer`); every job names one of them by repo name.
- One GitHub account that can read and push to those repos, logged into `gh`.
- A phone on the same Tailscale network is optional and only for the iOS app.

## 1. Prerequisites

```sh
xcode-select --install                      # clang (cgo), make, git, python3
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
brew install go node tmux gh                # Go ≥ 1.24, Node ≥ 20.19 or ≥ 22.12
brew install --cask ghostty
curl -fsSL https://claude.ai/install.sh | bash     # Claude Code, lands in ~/.local/bin
gh auth login                               # the account that owns your repos
gh auth setup-git                           # git over https authenticates as gh's active account
git config --global user.name  "Your Name"
git config --global user.email "you@example.com"
```

Add to `~/.tmux.conf` (`kraken o` finds the Ghostty window holding a pane by its title, which tmux only sets with this on):

```
set -g set-titles on
```

Add to `~/.zshrc` if `~/.local/bin` is not already on your PATH:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

Do NOT install Claude Code through npm or nvm: the launchd daemon's PATH reaches `~/.local/bin` and Homebrew, not `~/.nvm`, so a cell spawned by the daemon would not find `claude`. If you must, put the directory in `PATH=` inside the env file in step 3.

## 2. Get the source

```sh
mkdir -p ~/Developer
git clone https://github.com/car1zhang/kraken ~/Developer/kraken
```

Keep it as a standalone clone on `main`, named `kraken`, directly under your repos directory: self-update and the TestFlight ship resolve kraken's own source by that name under `KRAKEN_REPOS_DIR` (or at the checkout `KRAKEN_SELF_REPO` names, an absolute path or a repo name under the repos dir), and a copy nested inside another repo cannot update itself.

## 3. Write your env file

`~/.kraken/env` is the ONE place for per-person settings. Every kraken process (the daemon, the CLI, the hooks inside cells) reads it at startup; a variable already in the environment wins over the file — except `PATH`, whose value from the file is PREPENDED to the inherited one (empty elements dropped, and nothing added when the inherited PATH already starts with it), since the daemon always inherits a PATH from its launchd plist. `KEY=VALUE` lines, `#` comments; no quoting, no `export`; a leading `~/` in a value is your home directory (the file is parsed, not sourced, so nothing else expands). Only `KRAKEN_*` keys, `CLAUDE_POOL_DIR`, `CODEX_POOL_DIR` and `PATH` are read from it; anything else is ignored.

```sh
mkdir -p ~/.kraken
cat > ~/.kraken/env <<'ENV'
# the name agents use for you (make install fills this in if you leave it out)
KRAKEN_OPERATOR=Ada Lovelace
# where your checkouts live (default ~/Developer)
KRAKEN_REPOS_DIR=~/Developer
# branch prefix for cell branches (default kraken/; a missing trailing / is added)
KRAKEN_BRANCH_PREFIX=ada/
# only if claude/codex/go live somewhere launchd's PATH does not reach
# (prepended to the inherited PATH, so name only the extra directories)
# PATH=~/.nvm/versions/node/v22.12.0/bin
ENV
```

Replace the example values; the two commented lines stay commented unless you need them. Secrets never go in this file: Slack, Linear and App Store Connect each have their own `~/.kraken/<name>.env` (step 7).

The launchd label is resolved by the Makefile in this order: `KRAKEN_LABEL` on the make line or in the environment, else the `KRAKEN_LABEL=` line of this file, else `dev.carl.kraken`. The file is read both by a `make install` you type and by the one the board's UPDATE runs from inside the daemon, so a label written here is the one both use; put a custom label here BEFORE the first install (a label given only on a hand-typed make line is not seen by the next UPDATE, which would install a second job under the default label). The rest of this guide writes the default label; substitute yours.

## 4. Install

```sh
cd ~/Developer/kraken
make install OPERATOR="Ada Lovelace"
```

What it does: builds the web UI (`npm ci`), builds the `kraken` binary, signs it with the first code-signing identity in your keychain (none is fine; you will just re-approve prompts after rebuilds), installs `~/.local/bin/kraken` and the `k` symlink, installs zsh completion (into `~/.oh-my-zsh/custom/completions` if that exists, else `~/.zsh/completions`, which it creates — that one is only read if it is on your `fpath`), writes `KRAKEN_OPERATOR` into the env file if the file has no such line (it never rewrites one), generates the launchd plist and starts `kraken serve`. `OPERATOR=` is optional: without it the name is your account's full name (`id -F`).

Approve the macOS prompts as they appear:

| prompt | why | when |
|---|---|---|
| notifications | kraken's notifications go through `osascript` | first blocked job |
| Accessibility for your terminal and Ghostty | `kraken o` focuses a window | first `kraken o` or board raise |
| Automation: control System Events | same | same |

There is no Documents-folder prompt: kraken keeps its state under `~/.kraken`.

## 5. Verify

```sh
kraken help | head -3
launchctl print gui/$(id -u)/dev.carl.kraken | grep -E 'state|pid'   # "running", a pid
curl -s http://127.0.0.1:7777/api/board | head -c 200
open http://127.0.0.1:7777
```

An empty board with the top bar rendered is success. `kraken ls` in a tmux pane with a running `claude` lists it.

## 6. First job

1. Clone the repo you want kraken to work on into `<KRAKEN_REPOS_DIR>/<name>` (any name; jobs match it by substring).
2. On the board, **New job**. The template's `repo:` already names your sole checkout (with two or more it is blank: fill it in). Write the spec under the frontmatter: what to build and how you will know it is done.
3. **Submit**. A cell comes up: a worktree at `~/.kraken/cells/<slug>-<id>/wt-<repo>` on branch `<prefix><slug>-<id>`, a tmux session named for it, and zeus, the lead agent, in its first pane. The card turns "working".
4. Watch it with `kraken v` (the TUI) or the card's **steer** panel; `kraken o <n>` jumps to its pane in Ghostty.
5. When the card turns "blocked", read zeus' last message on the card. Answer questions on the card, leave notes on artifacts with the feedback button, and type `/do` into the steer box to let it build once the design is right.
6. Zeus opens a PR on your repo and stops. Merge it however you merge things; the card shows the PR state.
7. **Done** on the card tears the cell down. Its artifacts stay readable on the card.

## 7. Optional pieces

| piece | how |
|---|---|
| Slack intake (iris drafts jobs from your Slack inbox) | `printf 'SLACK_USER_TOKEN=…\n' >> ~/.kraken/slack.env && chmod 600 ~/.kraken/slack.env`; absent means off |
| Linear intake | `~/.kraken/linear.env` with `LINEAR_API_KEY`, optional `LINEAR_DEFAULT_REPO`, `LINEAR_STATES`; absent means off |
| Account pools (`~/.claude-pool`, `~/.codex-pool`) | created by the separate `ccp` helper; without a pool every cell runs on your own login and nothing complains |
| iOS app | open `ios/Kraken.xcodeproj` in Xcode, select your own team for signing (a local change; do not commit it), run on your phone; first launch opens the pair screen, whose seed field takes `http://<your-mac>.<tailnet>.ts.net:7777` — see "Pair the phone" below. TestFlight uploads: `ios/TESTFLIGHT.md` |
| Pair the phone | on the Mac's board open ◉ Devices, set a pairing password (12+ characters), and either scan the QR it shows from the app's pair screen or type the Mac's `http://<your-mac>.<tailnet>.ts.net:7777` there, then the password; the app binds to the one kraken whose password matches. Log out is on the app's Settings screen, Revoke on the Devices overlay; changing the password logs every phone out |
| Several Macs | nothing to configure: every `kraken serve` on the tailnet finds the others through `tailscale status` and shows their `readonly`/`public` jobs on its own board with an owner chip. Per job, `visibility: private \| readonly \| public` in the job file or the steer modal; `KRAKEN_NODES=off` in `~/.kraken/env` keeps a machine out of the fleet entirely |
| macOS board app | `make install-mac` (needs `brew install xcodegen` and an Apple team) |
| Zed editor marks | `docs/zed-mental-model.md` (Rust toolchain) |
| Updating | the board's **UPDATE** button, or `kraken update` from any shell (the checkout must be clean and on `main`): fetch, ff-only merge, build, install, restart the daemon; the mac-app phase runs only if `/Applications/Kraken.app` is installed |

## 8. Troubleshooting

| symptom | cause | fix |
|---|---|---|
| submit refused: "no repo under … matches" | the repo is not a direct child of the repos dir, or `KRAKEN_REPOS_DIR` is wrong | move the clone or fix the env file, then `launchctl kickstart -k gui/$(id -u)/dev.carl.kraken` |
| the cell comes up and dies in seconds | the daemon's PATH cannot find `claude` (or `codex`, `go`) | `PATH=` line in the env file, then kickstart as above |
| `kraken o` does nothing | Accessibility or Automation not granted, or `set-titles` missing from tmux.conf | grant, add, restart tmux |
| the board's UPDATE says "Repository not found" | gh's active account cannot read the kraken origin | `gh auth switch --user <login>` and retry |
| after `make install` every prompt asks again | the binary is ad-hoc signed | create a self-signed code-signing certificate in Keychain Access and `make install KRAKEN_SIGN_ID=<its name>` |
| a knob in the env file has no effect | the daemon was started before the file changed | kickstart as above (every process reads the file at startup) |

## 9. Uninstall

```sh
launchctl bootout gui/$(id -u)/dev.carl.kraken
rm -f ~/Library/LaunchAgents/dev.carl.kraken.plist ~/.local/bin/kraken ~/.local/bin/k
rm -rf ~/.kraken          # the database, cells, logs, env file
```

Cells hold worktrees of your repos; `git worktree prune` in each repo afterwards.

## 10. Upgrading an existing install

If kraken was already installed on this Mac before the env file existed, three things change on the first `make install` (or board UPDATE) after it:

- **Branch names stay as they are.** When `~/.kraken/kraken.db` already exists and the env file has no `KRAKEN_BRANCH_PREFIX=` line, the installer appends `KRAKEN_BRANCH_PREFIX=carl/` (with a `#` comment saying why), so live cells, their PR tracking and reactivation keep the `carl/…` branches they already have. Delete or change that line only once no job depends on the old names. A fresh install writes no such line and uses `kraken/`.
- **Linear jobs no longer default to `monolith`.** On a machine with more than one checkout under the repos dir, set `LINEAR_DEFAULT_REPO=<repo>` in `~/.kraken/linear.env`, or every job the Linear intake creates arrives with an empty `repo:`. With exactly one checkout, that one is used.
- **The iOS app asks for its server once** if it never had a server URL saved (it used to come preset to someone else's address). Enter yours on the Settings screen; an app that already saved one keeps it.
