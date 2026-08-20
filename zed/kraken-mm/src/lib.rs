//! kraken-mm — a Zed extension that is deliberately almost empty.
//!
//! Everything this feature does lives in kraken itself (`kraken mm lsp`,
//! cmd/kraken/mmlsp.go): the mark list, the locate, the diagnostics, the drop
//! command. This crate exists only because Zed will not talk to a language
//! server it was not told about by an extension, and an extension is WASM.
//!
//! So the whole shim is one question — where is the `kraken` binary — and one
//! answer: `worktree.which("kraken")`, which searches the worktree's own shell
//! environment rather than Zed's process environment. That distinction is the
//! only subtle thing in this file. Zed launched from Finder inherits a PATH
//! that does not include `~/.local/bin`, where `make install` puts kraken, so
//! resolving against the process environment would fail on exactly the normal
//! way carl starts his editor.
//!
//! A missing binary is reported as an error with the remedy in it, rather than
//! silently doing nothing: an extension that installs cleanly and then shows
//! no marks is indistinguishable from one where nothing has been marked yet.

use zed_extension_api::{self as zed, Command, LanguageServerId, Result, Worktree};

struct KrakenMmExtension;

impl zed::Extension for KrakenMmExtension {
    fn new() -> Self {
        Self
    }

    fn language_server_command(
        &mut self,
        _language_server_id: &LanguageServerId,
        worktree: &Worktree,
    ) -> Result<Command> {
        let command = worktree.which("kraken").ok_or_else(|| {
            "kraken is not on PATH — install it with `make install` in the kraken repo \
             (it lands in ~/.local/bin), then reload this extension"
                .to_string()
        })?;

        Ok(Command {
            command,
            args: vec!["mm".to_string(), "lsp".to_string()],
            // The server reaches kraken serve over localhost and honours
            // KRAKEN_SERVE_ADDR, so it is handed the worktree's shell
            // environment rather than a bare one — a non-default serve port
            // set in the shell profile has to reach it.
            env: worktree.shell_env(),
        })
    }
}

zed::register_extension!(KrakenMmExtension);
