.PHONY: build install install-completion install-daemon install-serve clean

PLIST := $(HOME)/Library/LaunchAgents/dev.carl.kraken.plist

build:
	CGO_ENABLED=1 go build -o kraken ./cmd/kraken

# E1: which launchd job gets (re)installed is a runtime decision, not a
# Makefile-parse-time one — it depends on whether this machine has already
# run `kraken import` (kraken.db exists), which can happen at any point in
# a developer's day independent of when they last edited this Makefile. So
# the choice is a shell `if` inside the install recipe itself (evaluated
# every `make install`), not a prerequisite list resolved once when make
# starts. Both targets below write the SAME $(PLIST) path/Label — daemon and
# serve are mutually exclusive deployments of one launchd job, never run
# side by side — so the single unload/load pair at the end of this recipe
# covers whichever one just got written.
install: build install-completion
	mkdir -p $(HOME)/.local/bin
	# mv, not cp: overwriting a running binary in place gets every live
	# kraken (k v!) SIGKILLed by macOS code signing; rename swaps the
	# directory entry and leaves running processes on the old inode
	cp kraken $(HOME)/.local/bin/kraken.tmp
	mv -f $(HOME)/.local/bin/kraken.tmp $(HOME)/.local/bin/kraken
	ln -sf $(HOME)/.local/bin/kraken $(HOME)/.local/bin/k
	@$(HOME)/.local/bin/kraken logevent install install "make install ($$(git rev-parse --short HEAD 2>/dev/null || echo unknown))" || true
	@echo "installed kraken + k symlink → ~/.local/bin"
	@if [ -f "$(HOME)/.kraken/kraken.db" ]; then \
		echo "kraken.db found — installing kraken serve (sqlite-backed)"; \
		$(MAKE) install-serve; \
	else \
		echo "no kraken.db yet — installing the legacy file-mode daemon"; \
		$(MAKE) install-daemon; \
	fi
	# (re)start the launchd job on the fresh binary; launchd restarts it at login
	launchctl unload $(PLIST) 2>/dev/null || true
	launchctl load $(PLIST)
	@echo "launchd job loaded — status: k v header, ~/.kraken/daemon.json, or ~/.kraken/kraken.log"

# the plist is generated, not checked in: it embeds $(HOME) paths. PATH must
# carry homebrew + ~/.local/bin: the daemon shells out to tmux/git/gh/claude.
# Plain > redirect (not temp+rename) is fine here: launchctl only reads the
# plist after the printf completes in this same recipe. ThrottleInterval keeps
# a losing single-instance guard from crash-looping under KeepAlive.
install-daemon:
	mkdir -p $(HOME)/Library/LaunchAgents $(HOME)/.kraken
	printf '%s\n' \
	  '<?xml version="1.0" encoding="UTF-8"?>' \
	  '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">' \
	  '<plist version="1.0"><dict>' \
	  '  <key>Label</key><string>dev.carl.kraken</string>' \
	  '  <key>ProgramArguments</key><array>' \
	  '    <string>$(HOME)/.local/bin/kraken</string>' \
	  '    <string>daemon</string>' \
	  '  </array>' \
	  '  <key>RunAtLoad</key><true/>' \
	  '  <key>KeepAlive</key><true/>' \
	  '  <key>ThrottleInterval</key><integer>30</integer>' \
	  '  <key>StandardOutPath</key><string>$(HOME)/.kraken/daemon.log</string>' \
	  '  <key>StandardErrorPath</key><string>$(HOME)/.kraken/daemon.log</string>' \
	  '  <key>EnvironmentVariables</key><dict>' \
	  '    <key>PATH</key><string>$(HOME)/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>' \
	  '    <key>LANG</key><string>en_US.UTF-8</string>' \
	  '  </dict>' \
	  '</dict></plist>' > $(PLIST)

# install-serve mirrors install-daemon's exact shape (same Label/PLIST path
# — see the note on `install` above — same RunAtLoad/KeepAlive/
# ThrottleInterval/PATH/LANG), swapping only ProgramArguments to `kraken
# serve` (the sqlite-backed replacement for daemon + web) and the log file
# name so the two deployments' logs never intermix if a machine switches
# between them across imports.
install-serve:
	mkdir -p $(HOME)/Library/LaunchAgents $(HOME)/.kraken
	printf '%s\n' \
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
	  '    <key>PATH</key><string>$(HOME)/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>' \
	  '    <key>LANG</key><string>en_US.UTF-8</string>' \
	  '  </dict>' \
	  '</dict></plist>' > $(PLIST)

install-completion:
	@dest=""; \
	for d in "$(HOME)/.oh-my-zsh/custom/completions" "$(HOME)/.zsh/completions"; do \
		if [ -d "$$d" ] || mkdir -p "$$d" 2>/dev/null; then dest="$$d"; break; fi; \
	done; \
	if [ -z "$$dest" ]; then echo "no writable completion dir"; exit 1; fi; \
	cp cmd/kraken/_k "$$dest/_k"; \
	echo "installed zsh completion → $$dest/_k"; \
	echo "reload with: rm -f ~/.zcompdump* && exec zsh"

clean:
	rm -f kraken
