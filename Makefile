.PHONY: build install install-completion clean

build:
	go build -o kraken .

install: build install-completion
	mkdir -p $(HOME)/.local/bin
	# mv, not cp: overwriting a running binary in place gets every live
	# kraken (k v!) SIGKILLed by macOS code signing; rename swaps the
	# directory entry and leaves running processes on the old inode
	cp kraken $(HOME)/.local/bin/kraken.tmp
	mv -f $(HOME)/.local/bin/kraken.tmp $(HOME)/.local/bin/kraken
	ln -sf $(HOME)/.local/bin/kraken $(HOME)/.local/bin/k
	@echo "installed kraken + k symlink → ~/.local/bin"

install-completion:
	@dest=""; \
	for d in "$(HOME)/.oh-my-zsh/custom/completions" "$(HOME)/.zsh/completions"; do \
		if [ -d "$$d" ] || mkdir -p "$$d" 2>/dev/null; then dest="$$d"; break; fi; \
	done; \
	if [ -z "$$dest" ]; then echo "no writable completion dir"; exit 1; fi; \
	cp _k "$$dest/_k"; \
	echo "installed zsh completion → $$dest/_k"; \
	echo "reload with: rm -f ~/.zcompdump* && exec zsh"

clean:
	rm -f kraken
