.PHONY: build web-assets install install-completion install-serve ios-testflight mac install-mac clean

PLIST := $(HOME)/Library/LaunchAgents/dev.carl.kraken.plist

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
# knowledge graph's kg_edges_fts virtual table needs at migration time. A
# binary built without the tag fails LOUDLY on open ("no such module: fts5")
# rather than serving a database with broken search — same deliberate-failure
# stance as web-assets above. `go test ./...` needs the tag for the same
# reason; the commands in CLAUDE.md carry it.
build: web-assets
	CGO_ENABLED=1 go build -tags sqlite_fts5 -ldflags "-X kraken/internal/util.BuildSHA=$(SHA)" -o kraken ./cmd/kraken

# kraken runs on exactly one personal machine, so install always deploys
# `kraken serve` — the only runtime. `kraken import` (a separate, human-run
# one-shot) moves any legacy file-store data into kraken.db before or after
# this; install itself no longer branches on whether that's happened yet.
install: build install-completion install-serve
	mkdir -p $(HOME)/.local/bin
	# Sign the fresh binary HERE — in the build dir, before it is copied into
	# ~/.local/bin. codesign rewrites the file it signs, so signing the
	# installed path in place would modify a live binary and SIGKILL every
	# running kraken: the exact hazard the cp-to-.tmp + mv dance below exists
	# to avoid.
	#
	# Why sign at all: a Go binary is ad-hoc signed by default, so its
	# designated requirement is `cdhash H"..."` — a hash of its own bytes,
	# invalidated by every rebuild. macOS TCC keys the Documents-folder grant
	# (kraken reads ~/Documents/pluto) on that requirement, so each `make
	# install` looked like a brand new app and re-prompted. Signing with a real
	# identity plus a stable --identifier makes the requirement name WHO signed
	# it instead of WHAT the bytes are, so the grant survives rebuilds.
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
	if launchctl print gui/$$uid/dev.carl.kraken >/dev/null 2>&1; then \
	  launchctl kickstart -k gui/$$uid/dev.carl.kraken \
	    || echo "warning: kraken is installed but the launchd restart failed — 'launchctl kickstart -k gui/$$uid/dev.carl.kraken' by hand"; \
	elif launchctl bootstrap gui/$$uid $(PLIST) 2>/dev/null || launchctl load $(PLIST); then \
	  echo "launchd job was not loaded — bootstrapped it"; \
	else \
	  echo "warning: kraken is installed but the launchd job could not be loaded — 'launchctl bootstrap gui/$$uid $(PLIST)' by hand"; \
	fi
	@echo "launchd job loaded — status: k v header, ~/.kraken/daemon.json, or ~/.kraken/kraken.log"
	# one-time cutover nudge: legacy file-mode state (order.json, JSONL logs,
	# job files) only reaches kraken.db via a manual `kraken import` run
	@if [ -f $(HOME)/.kraken/order.json ] && [ -f $(HOME)/.kraken/kraken.db ]; then \
	  echo "note: legacy ~/.kraken/order.json detected — run 'kraken import' once to migrate file-mode state"; \
	fi

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
	  '  <key>Label</key><string>dev.carl.kraken</string>' \
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
	  launchctl bootout gui/$$uid/dev.carl.kraken 2>/dev/null || true; \
	  launchctl bootstrap gui/$$uid $(PLIST) \
	    || echo "warning: launchctl bootstrap failed — the plist is updated but not loaded; retry 'make install' or log out"; \
	fi

install-completion:
	@dest=""; \
	for d in "$(HOME)/.oh-my-zsh/custom/completions" "$(HOME)/.zsh/completions"; do \
		if [ -d "$$d" ] || mkdir -p "$$d" 2>/dev/null; then dest="$$d"; break; fi; \
	done; \
	if [ -z "$$dest" ]; then echo "no writable completion dir"; exit 1; fi; \
	cp cmd/kraken/_k "$$dest/_k"; \
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

# Kraken.app — the native macOS board. `mac` is the Debug compile check; a
# regression here is otherwise SILENT, since nothing in this repo's CI builds
# any Swift (an iOS compile break once sat unnoticed across 24 commits).
#
# xcodegen regenerates macos/Kraken.xcodeproj from macos/project.yml on every
# run. The project IS committed (same as ios/Kraken.xcodeproj) so Xcode can be
# opened on a fresh clone without a generate step; project.yml stays the source
# of truth and this target is what keeps the two in agreement.
#
# No -derivedDataPath: the default DerivedData lives under ~/Library and so
# nothing build-shaped lands in the working tree. install-mac below does pass
# one, and points it at a mktemp dir outside the repo for the same reason
# ios/ship-testflight.sh keeps its archives there.
#
# The product is Kraken.app while the target and Swift module are KrakenMac
# (macos/project.yml pins PRODUCT_NAME/PRODUCT_MODULE_NAME apart, because the
# test contract freezes `@testable import KrakenMac`) — which is why the
# scheme below is KrakenMac and the bundle copied further down is Kraken.app.
mac:
	@command -v xcodegen >/dev/null 2>&1 || { echo "xcodegen not found — brew install xcodegen"; exit 1; }
	xcodegen generate --spec macos/project.yml
	xcodebuild build -project macos/Kraken.xcodeproj -scheme KrakenMac -destination 'platform=macOS'

# Build Release and install into /Applications.
#
# Same never-write-over-a-live-bundle discipline as the `install` target above,
# for the same macOS reason: modifying a running app's files invalidates its
# code signature and gets the process SIGKILLed. A bundle is a DIRECTORY, so
# the single `mv -f` that works for the kraken binary does not apply — `mv` onto
# an existing directory moves the source INSIDE it. The dance is instead:
# stage a full copy under a dot-name in /Applications (same filesystem, so the
# renames below are cheap and atomic), rename any existing Kraken.app OUT of the
# way, then rename the staged copy IN. A running Kraken.app keeps its files via
# the moved-aside directory and is never touched; the old copy is removed only
# after the new one is in place. Quitting the app first is still the right move
# if you want the new build — this recipe deliberately does not kill it.
install-mac:
	@command -v xcodegen >/dev/null 2>&1 || { echo "xcodegen not found — brew install xcodegen"; exit 1; }
	xcodegen generate --spec macos/project.yml
	@set -e; \
	dd=$$(mktemp -d /tmp/kraken-mac-build.XXXXXX); \
	trap 'rm -rf "$$dd"' EXIT; \
	xcodebuild build -project macos/Kraken.xcodeproj -scheme KrakenMac \
	  -configuration Release -destination 'platform=macOS' -derivedDataPath "$$dd"; \
	app="$$dd/Build/Products/Release/Kraken.app"; \
	[ -d "$$app" ] || { echo "build produced no Kraken.app at $$app"; exit 1; }; \
	rm -rf /Applications/.Kraken.app.new /Applications/.Kraken.app.old; \
	cp -R "$$app" /Applications/.Kraken.app.new; \
	if [ -d /Applications/Kraken.app ]; then mv /Applications/Kraken.app /Applications/.Kraken.app.old; fi; \
	mv /Applications/.Kraken.app.new /Applications/Kraken.app; \
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
