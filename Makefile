.PHONY: build install install-completion install-daemon clean

PLIST := $(HOME)/Library/LaunchAgents/dev.carl.kraken.plist

build:
	go build -o kraken ./cmd/kraken

install: build install-completion install-daemon
	mkdir -p $(HOME)/.local/bin
	# mv, not cp: overwriting a running binary in place gets every live
	# kraken (k v!) SIGKILLed by macOS code signing; rename swaps the
	# directory entry and leaves running processes on the old inode
	cp kraken $(HOME)/.local/bin/kraken.tmp
	mv -f $(HOME)/.local/bin/kraken.tmp $(HOME)/.local/bin/kraken
	ln -sf $(HOME)/.local/bin/kraken $(HOME)/.local/bin/k
	@$(HOME)/.local/bin/kraken logevent install install "make install ($$(git rev-parse --short HEAD 2>/dev/null || echo unknown))" || true
	@echo "installed kraken + k symlink → ~/.local/bin"
	# (re)start the daemon on the fresh binary; launchd restarts it at login
	launchctl unload $(PLIST) 2>/dev/null || true
	launchctl load $(PLIST)
	@echo "daemon loaded (launchd) — status: k v header or ~/.kraken/daemon.json"

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
