.PHONY: build web-assets install install-completion install-serve ios-testflight mac install-mac clean

# launchd label of the `kraken serve` job: names the plist file, its Label key
# and every launchctl target below. Resolved in this order:
#   1. the make line or the environment (`make install KRAKEN_LABEL=x`);
#   2. else the first KRAKEN_LABEL= line of ~/.kraken/env (a missing or
#      unreadable file reads as no line);
#   3. else dev.carl.kraken, the label existing installs already have loaded,
#      so leaving it unset everywhere changes nothing on those machines.
# Step 2 is what makes a hand-typed `make install` and the daemon's (`k update`
# runs make from serve's environment, which ApplyEnvFile has already filled
# from the same file) resolve the SAME label: put a custom label in the env
# file and both paths agree. A machine that switches labels has to bootout the
# old one by hand once.
KRAKEN_ENV_FILE := $(HOME)/.kraken/env
# The lookup reads the file the way util.ApplyEnvFile does: a UTF-8 BOM at the
# start of the file and a trailing CR (a CRLF file) are ignored, the key and
# value are trimmed, and the FIRST KRAKEN_LABEL= line wins (quotes are kept,
# exactly as the loader keeps them; the one quote rule is below).
KRAKEN_ENV_LABEL := $(strip $(shell LC_ALL=C awk 'NR==1{sub(/^\357\273\277/,"")} {sub(/\r$$/,"")} /^[ \t]*KRAKEN_LABEL[ \t]*=/{v=$$0; sub(/^[^=]*=[ \t]*/,"",v); sub(/[ \t]+$$/,"",v); print v; exit}' '$(KRAKEN_ENV_FILE)' 2>/dev/null))
ifneq ($(KRAKEN_ENV_LABEL),)
KRAKEN_LABEL ?= $(KRAKEN_ENV_LABEL)
endif
KRAKEN_LABEL ?= dev.carl.kraken
# The quote rule, applied to the RESOLVED label whatever its origin (make line,
# environment, or the file): a value wrapped in matching ' or " quotes is
# refused with a note and falls through to the default. The env loader keeps
# quotes verbatim, so a quoted line in ~/.kraken/env reaches the daemon's
# environment quoted and from there the board UPDATE's `make install`; used
# as-is it would name a plist and a launchd job with quote characters in them.
# `override`, because a make-line value cannot be reassigned without it.
KRAKEN_LABEL_QUOTED := $(and $(filter '%,$(firstword $(KRAKEN_LABEL))),$(filter %',$(lastword $(KRAKEN_LABEL))))$(and $(filter "%,$(firstword $(KRAKEN_LABEL))),$(filter %",$(lastword $(KRAKEN_LABEL))))
ifneq ($(KRAKEN_LABEL_QUOTED),)
$(info note: ignoring the quoted KRAKEN_LABEL (origin: $(origin KRAKEN_LABEL); "file" means ~/.kraken/env) — write it unquoted; using dev.carl.kraken)
override KRAKEN_LABEL := dev.carl.kraken
endif
# a set-but-empty KRAKEN_LABEL (environment or make line) would name a plist
# called ".plist" and target gui/<uid>/ — treat it as unset
ifeq ($(strip $(KRAKEN_LABEL)),)
override KRAKEN_LABEL := dev.carl.kraken
endif
PLIST := $(HOME)/Library/LaunchAgents/$(KRAKEN_LABEL).plist

# Whether kraken.db existed BEFORE this make ran. Evaluated once at parse time
# on purpose: install-serve (a prerequisite of install) bootstraps a fresh
# serve, which creates the database within moments, so a check made inside
# the install recipe would read every first install as an upgrade.
KRAKEN_DB_PREEXISTED := $(shell test -f '$(or $(KRAKEN_DB_PATH),$(HOME)/.kraken/kraken.db)' && echo yes)

# the name kraken's agents use for the person running it. `install` writes it
# into ~/.kraken/env as KRAKEN_OPERATOR only when that file has no such line
# yet; an existing line is never rewritten. Defaults to the account's full name.
OPERATOR ?= $(shell id -F 2>/dev/null)

# short commit of the tree being built, stamped into the binary so a running
# kraken can report what it is (util.Build(), the board's build chip) and the
# updater can tell "already up to date" from "a restart is pending".
SHA := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

# web-assets builds the React board into web/_ui/dist, which web/embed.go
# pulls into the binary with `//go:embed all:_ui/dist`.
#
# This is a PREREQUISITE of build, and that edge is the only thing making the
# ordering correct: go:embed reads the directory from disk at compile time
# with no dependency tracking of its own, so a bare `go build` would happily
# embed whatever stale bundle happened to be sitting there — or fail outright,
# since go:embed cannot embed an empty directory and dist/ is gitignored. On a
# clean clone `make build` is therefore the only entrypoint that works; a bare
# `go build` (or `go test ./...`) works only after this target has run once.
#
# `npm ci` rather than `npm install`: it installs exactly the lockfile, fails
# instead of silently resolving a new version, and is what makes a board-click
# self-update reproducible. It needs node on PATH — see cmd/kraken/cmd_update.go's
# toolchainEnv and jobs.PreflightUpdate, which resolve npm to an absolute path
# for exactly the launchd-PATH reason they already do it for go.
web-assets:
	cd web/_ui && npm ci --no-audit --no-fund && npm run build

# the -X path is the FULL package path (module `kraken` + package dir): a
# wrong one is silently ignored by the linker, leaving Build() == "dev".
#
# -tags sqlite_fts5 compiles FTS5 into mattn/go-sqlite3 (its
# sqlite3_opt_fts5.go is behind `//go:build sqlite_fts5 || fts5`), which the
# retired knowledge graph's v15 migration still needs: it creates an FTS5
# virtual table on every fresh database, and migration history is never
# edited. A binary built without the tag fails LOUDLY on open ("no such
# module: fts5") — same deliberate-failure stance as web-assets above.
# `go test ./...` needs the tag for the same reason; the commands in
# CLAUDE.md carry it.
build: web-assets
	CGO_ENABLED=1 go build -tags sqlite_fts5 -ldflags "-X kraken/internal/util.BuildSHA=$(SHA)" -o kraken ./cmd/kraken

# install always deploys `kraken serve` — the only runtime.
install: build install-completion install-serve
	mkdir -p $(HOME)/.local/bin
	# Record the operator's name in ~/.kraken/env (parsed by every kraken
	# process at startup, util.ApplyEnvFile) — but only when the file has no
	# KRAKEN_OPERATOR= line: a name the operator wrote or edited by hand is
	# never rewritten. The file is created if absent, a missing trailing newline
	# is repaired before appending, and an empty OPERATOR writes nothing (the
	# runtime then falls back to $USER). This runs before the restart below, so
	# the daemon comes up already knowing the name.
	#
	# Same rule for KRAKEN_BRANCH_PREFIX, on an UPGRADE only: when kraken.db
	# existed before this make (KRAKEN_DB_PREEXISTED, read at parse time) and
	# the file has no KRAKEN_BRANCH_PREFIX= line, pin `carl/` — the prefix every
	# branch had before it became a knob — so live cells, their PR tracking and
	# reactivation keep the branches they already have instead of moving to the
	# new `kraken/` default. A fresh install writes no prefix line.
	#
	# Any failed write to the file (mkdir, touch, unreadable file, append)
	# FAILS the recipe: reporting "wrote" over a write that did not happen
	# would leave agents nameless with nothing saying why.
	@op='$(subst ','\'',$(OPERATOR))'; env="$(HOME)/.kraken/env"; \
	fail() { echo "error: could not write $$env — $$1" >&2; exit 1; }; \
	mkdir -p "$(HOME)/.kraken" || fail "mkdir failed"; \
	touch "$$env" || fail "touch failed"; \
	[ -r "$$env" ] || fail "file is not readable"; \
	nl() { if [ -s "$$env" ] && [ -n "$$(tail -c 1 "$$env")" ]; then echo >> "$$env" || fail "append failed"; fi; }; \
	if grep -q '^[[:space:]]*KRAKEN_OPERATOR[[:space:]]*=' "$$env"; then \
	  echo "~/.kraken/env already names the operator — left as is"; \
	elif [ -z "$$op" ]; then \
	  echo "note: no operator name (set OPERATOR=...) — agents will use \$$USER"; \
	else \
	  nl; printf 'KRAKEN_OPERATOR=%s\n' "$$op" >> "$$env" || fail "append failed"; \
	  echo "wrote KRAKEN_OPERATOR=$$op → ~/.kraken/env"; \
	fi; \
	if [ "$(KRAKEN_DB_PREEXISTED)" = yes ] && ! grep -q '^[[:space:]]*KRAKEN_BRANCH_PREFIX[[:space:]]*=' "$$env"; then \
	  nl; printf '%s\n' '# pinned by make install: this install predates the branch-prefix knob, so it keeps the carl/ branches it has' 'KRAKEN_BRANCH_PREFIX=carl/' >> "$$env" || fail "append failed"; \
	  echo "wrote KRAKEN_BRANCH_PREFIX=carl/ → ~/.kraken/env (existing install keeps its branch names)"; \
	fi
	# Sign the fresh binary HERE — in the build dir, before it is copied into
	# ~/.local/bin. codesign rewrites the file it signs, so signing the
	# installed path in place would modify a live binary and SIGKILL every
	# running kraken: the exact hazard the cp-to-.tmp + mv dance below exists
	# to avoid.
	#
	# Why sign at all: a Go binary is ad-hoc signed by default, so its
	# designated requirement is `cdhash H"..."` — a hash of its own bytes,
	# invalidated by every rebuild. macOS TCC keys any privacy grant it makes
	# to the binary on that requirement, so with an ad-hoc signature each `make
	# install` looks like a brand new app and a granted prompt comes back. Signing with a real
	# identity plus a stable --identifier makes the requirement name WHO signed
	# it instead of WHAT the bytes are, so a grant survives rebuilds.
	#
	# The --identifier is deliberately NOT $(KRAKEN_LABEL): it is the code
	# identity existing grants are keyed on, not the launchd job name, and
	# moving it would re-prompt every grant once.
	#
	# Identity: $(KRAKEN_SIGN_ID) if set, else the first codesigning identity in
	# the keychain (swapping to a self-signed cert later is config, not code).
	# Signing NEVER fails the install — a warning and today's ad-hoc behavior is
	# always better than a machine with no fresh kraken on it.
	#
	# Deliberately NO --options runtime: hardened runtime can interfere with a
	# Go binary that shells out to tmux/git/gh/claude constantly, and it buys
	# nothing for local TCC identity stability. Do not "helpfully" add it.
	@id="$(KRAKEN_SIGN_ID)"; \
	if [ -z "$$id" ]; then \
	  id=$$(security find-identity -v -p codesigning 2>/dev/null | awk '/^ *[0-9]+\) / {print $$2; exit}'); \
	fi; \
	if [ -z "$$id" ]; then \
	  echo "warning: no codesigning identity found (set KRAKEN_SIGN_ID) — leaving the binary ad-hoc signed; expect a TCC re-prompt"; \
	elif codesign --force --sign "$$id" --identifier dev.carl.kraken ./kraken; then \
	  echo "signed kraken → identifier dev.carl.kraken ($$id)"; \
	else \
	  echo "warning: codesign failed — continuing with the ad-hoc signature"; \
	fi
	# mv, not cp: overwriting a running binary in place gets every live
	# kraken (k v!) SIGKILLed by macOS code signing; rename swaps the
	# directory entry and leaves running processes on the old inode
	cp kraken $(HOME)/.local/bin/kraken.tmp
	mv -f $(HOME)/.local/bin/kraken.tmp $(HOME)/.local/bin/kraken
	ln -sf $(HOME)/.local/bin/kraken $(HOME)/.local/bin/k
	@$(HOME)/.local/bin/kraken logevent install install "make install ($(SHA))" || true
	@echo "installed kraken + k symlink → ~/.local/bin"
	# (re)start the launchd job on the fresh binary; launchd restarts it at login.
	# kickstart -k is ONE atomic launchd operation (kill + relaunch). The old
	# unload + load pair had a real failure mode: when the daemon itself is what
	# ran this recipe, the unload kills the process executing it and the load
	# never happens — the daemon stays down permanently. kickstart has no such
	# window.
	#
	# The not-loaded case (first install, or after a manual bootout) is detected
	# with `launchctl print`, NOT by treating any kickstart failure as "not
	# loaded": `load` on an ALREADY-loaded job also exits non-zero, so the old
	# `kickstart || load` chain failed the whole recipe — long after the new
	# binary was in place — whenever kickstart failed for any other reason. A
	# crash-looping serve under KeepAlive + ThrottleInterval is exactly that
	# shape, and reporting a completed install as a failed one sends the user
	# after the wrong problem. For the same reason the restart is a WARNING and
	# never a non-zero exit: by this point the binary is installed and `kraken
	# update` reads this recipe's exit code as "did the update work".
	@uid=$$(id -u); \
	if launchctl print gui/$$uid/$(KRAKEN_LABEL) >/dev/null 2>&1; then \
	  launchctl kickstart -k gui/$$uid/$(KRAKEN_LABEL) \
	    || echo "warning: kraken is installed but the launchd restart failed — 'launchctl kickstart -k gui/$$uid/$(KRAKEN_LABEL)' by hand"; \
	elif launchctl bootstrap gui/$$uid $(PLIST) 2>/dev/null || launchctl load $(PLIST); then \
	  echo "launchd job was not loaded — bootstrapped it"; \
	else \
	  echo "warning: kraken is installed but the launchd job could not be loaded — 'launchctl bootstrap gui/$$uid $(PLIST)' by hand"; \
	fi
	@echo "launchd job loaded — status: k v header, ~/.kraken/daemon.json, or ~/.kraken/kraken.log"

# the plist is generated, not checked in: it embeds $(HOME) paths. PATH must
# carry homebrew + ~/.local/bin: kraken serve shells out to tmux/git/gh/claude.
# /usr/local/go/bin is in there for a measured reason: go lives there, and
# without it a build spawned FROM the launchd daemon (kraken update → make
# install) dies with `go: command not found` while the identical command works
# in a terminal, because launchd jobs inherit none of the login shell's PATH.
# (That is belt and braces only — the updater now also prepends go's real
# directory to the PATH it hands make, internal/util's PrependToolDir,
# which is what actually makes a board-click build work.)
# ThrottleInterval keeps a losing single-instance guard from crash-looping
# under KeepAlive.
#
# Written to a temp file and compared, because a plist edit does NOT take
# effect on its own: `launchctl kickstart -k` (the install recipe's restart)
# relaunches the job from launchd's CACHED, in-memory definition and never
# re-reads the file, so a changed PATH would sit inert until the next logout.
# Only bootout + bootstrap genuinely re-reads it — and only a genuine change
# is worth that, since bootout leaves a window with the job unloaded while
# plain kickstart has none.
#
# bootout is safe here even when the daemon ITSELF is what triggered this
# install, which REVERSES the reasoning in the restart comment above — read it
# carefully before touching either. `kraken update` is spawned with Setsid
# (internal/api/update.go), so the updater and everything it forks (this make)
# have left the launchd job's process group and session entirely; booting the
# job out kills the job's processes, not ours. Before Setsid that was false,
# and this would have killed the recipe mid-flight.
install-serve:
	mkdir -p $(HOME)/Library/LaunchAgents $(HOME)/.kraken
	@printf '%s\n' \
	  '<?xml version="1.0" encoding="UTF-8"?>' \
	  '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">' \
	  '<plist version="1.0"><dict>' \
	  '  <key>Label</key><string>$(KRAKEN_LABEL)</string>' \
	  '  <key>ProgramArguments</key><array>' \
	  '    <string>$(HOME)/.local/bin/kraken</string>' \
	  '    <string>serve</string>' \
	  '  </array>' \
	  '  <key>RunAtLoad</key><true/>' \
	  '  <key>KeepAlive</key><true/>' \
	  '  <key>ThrottleInterval</key><integer>30</integer>' \
	  '  <key>StandardOutPath</key><string>$(HOME)/.kraken/kraken.log</string>' \
	  '  <key>StandardErrorPath</key><string>$(HOME)/.kraken/kraken.log</string>' \
	  '  <key>EnvironmentVariables</key><dict>' \
	  '    <key>PATH</key><string>$(HOME)/.local/bin:/usr/local/go/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>' \
	  '    <key>LANG</key><string>en_US.UTF-8</string>' \
	  '  </dict>' \
	  '</dict></plist>' > $(PLIST).new
	@uid=$$(id -u); \
	if cmp -s $(PLIST).new $(PLIST); then \
	  rm -f $(PLIST).new; \
	else \
	  mv -f $(PLIST).new $(PLIST); \
	  echo "launchd plist changed — reloading the job so the new definition takes effect"; \
	  launchctl bootout gui/$$uid/$(KRAKEN_LABEL) 2>/dev/null || true; \
	  launchctl bootstrap gui/$$uid $(PLIST) \
	    || echo "warning: launchctl bootstrap failed — the plist is updated but not loaded; retry 'make install' or log out"; \
	fi

# The completion goes into the first of these dirs that ALREADY EXISTS; only
# ~/.zsh/completions is ever created, as the fallback. Creating the oh-my-zsh
# path on a machine without oh-my-zsh would leave a stub ~/.oh-my-zsh that
# nothing loads (and that confuses a later oh-my-zsh install). ~/.zsh/completions
# is only read if it is on your fpath — the reload hint below says so.
# Completion is optional, so a machine where neither dir is usable (a FILE
# where a dir is expected, an unwritable dir) gets a note and the target still
# succeeds: failing here would fail the whole `make install`, and with it a
# board UPDATE, over a tab-completion file.
install-completion:
	@dest=""; \
	for d in "$(HOME)/.oh-my-zsh/custom/completions" "$(HOME)/.zsh/completions"; do \
		if [ -d "$$d" ]; then dest="$$d"; break; fi; \
	done; \
	if [ -z "$$dest" ]; then \
		dest="$(HOME)/.zsh/completions"; \
		if ! mkdir -p "$$dest" 2>/dev/null; then \
			echo "note: no usable zsh completion dir ($$dest could not be created) — skipping completion"; exit 0; \
		fi; \
		echo "created $$dest — add 'fpath=(~/.zsh/completions \$$fpath)' before compinit in ~/.zshrc if it is not there"; \
	fi; \
	if ! cp cmd/kraken/_k "$$dest/_k" 2>/dev/null; then \
		echo "note: could not write $$dest/_k — skipping completion"; exit 0; \
	fi; \
	echo "installed zsh completion → $$dest/_k"; \
	echo "reload with: rm -f ~/.zcompdump* && exec zsh"

# Build the iOS app and upload it to TestFlight. A pass-through, on purpose:
# ios/ship-testflight.sh is the ONE implementation, and `kraken ship-ios` calls
# that script directly rather than going through make. This target exists only
# so the pipeline is discoverable from the Makefile like everything else — do
# not grow logic here, or a board-triggered ship and a hand-run ship start
# doing different things. Pass flags through with ARGS, e.g.
#   make ios-testflight ARGS=--dry-run
# Setup and credentials: ios/TESTFLIGHT.md
ios-testflight:
	@./ios/ship-testflight.sh $(ARGS)

# Kraken.app — the macOS board (a frameless WKWebView over the SPA kraken
# serve embeds; macos/project.yml's header carries the provenance). `mac` is
# the Debug compile check; a regression here is otherwise SILENT, since
# nothing in this repo's CI builds any Swift (an iOS compile break once sat
# unnoticed across 24 commits).
#
# xcodegen regenerates macos/Kraken.xcodeproj from macos/project.yml on every
# run. The project and its generated Info.plist ARE committed (as under ios/)
# so Xcode can be opened on a fresh clone without a generate step — the one
# asymmetry with ios/ is that no shared scheme is tracked; xcodebuild
# auto-creates the `Kraken` scheme, so `-scheme Kraken` below still resolves.
# project.yml stays the source of truth and this target keeps the two in step.
# The regenerate MAY rewrite project.pbxproj's bytes (today's xcodegen emits
# them byte-identically; a version bump can change that), and then a `make
# mac` on main leaves the tree dirty until the churn is committed or reverted
# — `k update`'s preflight refuses on a dirty tree, which is where that state
# actually bites. install-mac below snapshots and restores around its own
# regenerate for exactly that reason; this hand-run compile check leaves the
# churn visible on purpose.
#
# No -derivedDataPath: the default DerivedData lives under ~/Library and so
# nothing build-shaped lands in the working tree. install-mac below does pass
# one, and points it at a mktemp dir outside the repo for the same reason
# ios/ship-testflight.sh keeps its archives there.
#
mac:
	@command -v xcodegen >/dev/null 2>&1 || { echo "xcodegen not found — brew install xcodegen"; exit 1; }
	xcodegen generate --spec macos/project.yml
	xcodebuild build -project macos/Kraken.xcodeproj -scheme Kraken -destination 'platform=macOS'

# Build Release and install into /Applications.
#
# Same never-write-over-a-live-bundle discipline as the `install` target above,
# for the same macOS reason: modifying a running app's files invalidates its
# code signature and gets the process SIGKILLed. A bundle is a DIRECTORY, so
# the single `mv -f` that works for the kraken binary does not apply — `mv` onto
# an existing directory moves the source INSIDE it. The dance is instead:
# stage a full copy under a dot-name in /Applications (same filesystem, so the
# renames below are cheap and atomic), rename any existing Kraken.app OUT of the
# way, then rename the staged copy IN. The running app's bundle is never
# rewritten in place — it is renamed aside and (once the swap has landed)
# unlinked, which a running process survives on its open inodes. Quitting the
# app first is still the right move if you want the new build — this recipe
# deliberately does not kill it.
#
# The generated tracked files (all three of them — pbxproj, the Info.plist,
# the workspace file) are snapshotted before xcodegen and restored after the
# build, ios/ship-testflight.sh's guard extended to the full generated set:
# today xcodegen regenerates them byte-identically, but that is luck, not a
# guarantee — a `brew upgrade xcodegen` that changes one line of formatting
# would otherwise leave a TRACKED file dirty, and a dirty tree here is two
# dead buttons (PreflightUpdate refuses every later update, the ship script's
# clean-tree gate refuses every later ship, neither with a board-reachable
# remedy). This target runs unattended on every board UPDATE click, so the
# guard is not optional. Mechanics borrowed from the script the hard way:
# the trap is armed BEFORE the snapshots, the snapshots are guarded (a
# missing generated file is xcodegen's to recreate, not a reason to die at
# cp), the restores are equally guarded and never abort the trap body, and
# the backups live in their own dir — NOT the -derivedDataPath dir, which
# xcodebuild owns and could prune.
install-mac:
	@command -v xcodegen >/dev/null 2>&1 || { echo "xcodegen not found — brew install xcodegen"; exit 1; }
	@set -e; \
	dd=$$(mktemp -d /tmp/kraken-mac-build.XXXXXX); \
	bk=$$(mktemp -d /tmp/kraken-mac-gen-backup.XXXXXX); \
	trap 'for f in project.pbxproj Generated-Info.plist contents.xcworkspacedata; do \
	        [ -f "$$bk/$$f" ] && cp "$$bk/$$f" "$$(cat "$$bk/$$f.dest")" || true; \
	      done; rm -rf "$$dd" "$$bk"' EXIT; \
	for pair in "macos/Kraken.xcodeproj/project.pbxproj project.pbxproj" \
	            "macos/Generated-Info.plist Generated-Info.plist" \
	            "macos/Kraken.xcodeproj/project.xcworkspace/contents.xcworkspacedata contents.xcworkspacedata"; do \
	  src=$${pair%% *}; name=$${pair##* }; \
	  if [ -f "$$src" ]; then cp "$$src" "$$bk/$$name"; printf '%s' "$$src" > "$$bk/$$name.dest"; fi; \
	done; \
	xcodegen generate --spec macos/project.yml; \
	xcodebuild build -project macos/Kraken.xcodeproj -scheme Kraken \
	  -configuration Release -destination 'platform=macOS' -derivedDataPath "$$dd"; \
	app="$$dd/Build/Products/Release/Kraken.app"; \
	[ -d "$$app" ] || { echo "build produced no Kraken.app at $$app"; exit 1; }; \
	rm -rf /Applications/.Kraken.app.new; \
	cp -R "$$app" /Applications/.Kraken.app.new; \
	if [ -d /Applications/Kraken.app ]; then rm -rf /Applications/.Kraken.app.old; mv /Applications/Kraken.app /Applications/.Kraken.app.old; fi; \
	mv /Applications/.Kraken.app.new /Applications/Kraken.app \
	  || { mv /Applications/.Kraken.app.old /Applications/Kraken.app 2>/dev/null; exit 1; }; \
	rm -rf /Applications/.Kraken.app.old; \
	echo "installed Kraken.app → /Applications (quit and relaunch to pick it up)"

# node_modules is deliberately NOT removed: it is expensive to rebuild and
# `npm ci` already replaces it wholesale. dist IS removed — it is the embed
# target, so a stale one is the thing most worth being able to clear. Note
# that after a clean, a bare `go build` fails until `make build` (or
# `make web-assets`) regenerates dist: go:embed cannot embed an empty dir.
clean:
	rm -f kraken
	rm -rf web/_ui/dist
