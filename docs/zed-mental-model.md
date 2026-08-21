# Zed → mental model: mark lines read with one keystroke

Select lines in Zed, hit the keybinding, and the exact text lands in kraken's mental model store, anchored at `origin/main` — invalidated automatically when a future merge changes those lines. CAPTURE needs no Zed extension (an extension cannot read the selection or register a command); it is a plain task plus a keybinding, installed once, globally. Seeing the marks again inside the editor is the second half, and it does need one — for the same reason, upside down: a language server is the only channel an extension has for drawing anything onto a line. That half is [below](#see-and-drop-your-marks-in-the-editor).

Add to `~/.config/zed/tasks.json`:

```json
[
  {
    "label": "kraken: mark lines read",
    "command": "kraken mm mark --worktree \"$ZED_WORKTREE_ROOT\" --file \"$ZED_RELATIVE_FILE\" --start-row \"$ZED_ROW\"",
    "hide": "on_success",
    "reveal": "no_focus",
    "allow_concurrent_runs": true,
    "use_new_terminal": false
  }
]
```

Add to `~/.config/zed/keymap.json` (pick any free binding):

```json
[
  { "context": "Editor", "bindings": { "alt-r": ["task::Spawn", { "task_name": "kraken: mark lines read" }] } }
]
```

Mechanics worth knowing, so the behavior does not surprise:

- `$ZED_ROW` is the 1-based row of the selection's START (regardless of drag direction); the end row is derived server-side from the selection's newline count, with the whole-line trailing-newline shape handled. With NO selection, the single line at the cursor is marked (read from disk — save first).
- The selection reaches kraken through Zed's own task environment (`ZED_SELECTED_TEXT`, which Zed exports to every task it spawns), never through the command line — the command must NOT mention it. Zed substitutes `${VAR}` references textually into the command before any shell parses it, so a command carrying the selection breaks on the first selection containing a double quote (the selected text runs as shell) and mangles newlines; the env value carries both intact.
- Bind `task::Spawn` with the explicit `task_name`, never `task::Rerun` — rerun reuses stale variable values (open Zed bug).
- `hide: on_success` + `reveal: no_focus` keeps failures visible (kraken serve down, bad path) without stealing focus on success.
- The mark's identity keys on repo + relative path, so marking from a kraken cell worktree and from `~/Developer/<repo>` are the same mark. The anchor ladder prefers `origin/main`, then the merge base, then `HEAD`; unsaved or uncommitted content falls back to a text-only "dirty" anchor, flagged as such.
- Marks are carl's: the verbs are not documented for agent use anywhere, and the daemon's verify pass (every ~10 minutes) is what invalidates a mark when the anchored text stops existing at `origin/main` — shifts, renames, moves and whitespace survive; one changed character invalidates.

## See and drop your marks in the editor

The task above is write-only: it records a mark and shows nothing. The other half is a small Zed extension that draws the marks you have already made back onto the lines they cover, and gives you a code action to drop one.

The mechanism is worth knowing because it explains everything odd about it. A Zed extension cannot draw a decoration, read a selection, or register a command — only a language server can put anything on a line, and of its channels only **LSP document colors** can paint a background (diagnostics, the other one, draw wavy underlines only). So kraken itself is the language server: `kraken mm lsp`, a stdio JSON-RPC server, and the extension in `zed/kraken-mm` is a shim whose entire body is "run `kraken mm lsp`".

### Install

1. `make install` in the kraken repo, so `kraken` is on `PATH` (the extension resolves it through the worktree's shell environment, so `~/.local/bin` is found even when Zed was launched from Finder).
2. In Zed: **cmd-shift-P → `zed: install dev extension`**, and pick `<kraken repo>/zed/kraken-mm`.
3. Open a file in any repo you have marked lines in. Marks show up within a second or two.

Zed builds the extension to WebAssembly on install, so the first run needs a Rust toolchain with the `wasm32-wasip1` target — `rustup target add wasm32-wasip1` if the install reports a missing target. Re-run "install dev extension" on the same directory after any change to the extension itself — that is the rare case, since the extension is a static shim; a change to `kraken` alone needs only the binary reinstalled. **`k update` (and the board's UPDATE button) handles the rest automatically**: after installing, it sweeps any running `kraken mm lsp` process, and Zed demand-starts a fresh one from the new binary the next time you open a file of a registered language. Only a hand-run `make install` leaves the old server running — there, use **`editor: restart language server`** (or reopen the project window). Reopening the FILE is not enough, and the reason is the same one the Makefile exists for: Zed keeps one `kraken mm lsp` process per project window, and `make install` deliberately copies to a temp name and renames over the old binary — so the running server keeps the old inode and goes on executing the old code. Nothing about it looks stale; it just behaves like the change was never made.

### What it looks like

A marked block renders as **tinted line bars**: the server reports every covered line as an LSP *document color* (a soft blue at low alpha), and `"lsp_document_colors": "background"` in `settings.json` makes Zed paint each line's background with it — an actual highlight, and the only way to get one, since the alternative channel (diagnostics) only ever draws wavy underlines. The setting is global: real color values (CSS literals, say) also render as backgrounds instead of swatch chips. No underlines are published at all.

Bars are **pulled** by Zed — on open, on edit, and on scroll — with no push channel, so a brand-new mark's bar appears on the next of those rather than instantly; the server's 2-second poll guarantees whatever pulls next sees a mark list at most a tick old. The item id a drop needs is in the code action itself (`drop read mark (mm 2)`), and the full anchor detail lives on the board's memory pane.

Put the cursor on a marked line and open code actions (**cmd-.**): `drop read mark (mm 2)`. Taking it posts the unmark to `kraken serve` and force-refreshes the server's list; the bar disappears on the next color pull (an edit, a scroll, a reopen). `k mm drop <id>` does the same thing from a shell.

Mechanics that will otherwise surprise:

- **The buffer is the truth.** Marks are located in what you are looking at, unsaved edits included — that is exactly when you want to know you are about to touch something you read.
- **Editing a marked block hides its mark; it does not kill it.** The server only draws what it can find, and it searches with the same two primitives the verify pass does — exact text, then whitespace-normalized — so a reindent still shows. The editor's search is the more PERMISSIVE of the two, though, not the same one: it will also find a block a formatter has reflowed across a different number of lines, which the nightly pass would not. So "it still draws" is not a promise that it will still be there tomorrow. Whether a mark is still *true* is the daemon's question, asked against `origin/main` every ~10 minutes — never the editor's.
- **`kraken serve` down means no marks and one log line** (`window/logMessage`, visible under `dev: open language server logs`), never an error dialog and never a crash.
- The server's mark list refreshes **on its own, every ~2 seconds** (a poll against `kraken serve`), but bars are *pulled* by Zed with no push channel — so what the poll buys is that any pull (open, edit, scroll) answers from a list at most a tick old, not that an untouched, unscrolled buffer repaints by itself. `kraken serve` going down costs the bars (plus one log line in `dev: open language server logs`) and its return restores them, both on the next pull.
- Identity is repo + path. The REPO half is computed exactly as the capture task computes it (origin's basename, `.git` stripped, falling back to the worktree directory name), which is why a mark made in `~/Developer/kraken` draws in every `wt-kraken` cell worktree of the same repo, and vice versa. The PATH half is relative to the **git root** — and `k mm mark` walks up to that same root before it posts, so opening a sub-directory as your Zed project (`~/Developer/monolith/agent-service`, say) still stores and finds the mark under `agent-service/…`. The walk starts at the marked file itself, so even a project folder *above* the repo (`~/Developer` holding several checkouts) resolves each mark to the checkout its file actually lives in. Before that walk existed, a mark made from a sub-directory project was invisible in the file it named.

### If nothing shows up

Check, in this order: `kraken mm get` prints the mark you expect and its `anchor.repo`/`anchor.path` match the file you have open; `dev: open language server logs` shows `kraken mental model` running; the marked text still exists verbatim in the buffer.

If the server does not appear in that log list AT ALL — no `kraken mental model` entry, for any file — suspect the language list before you suspect anything else. `Swift` is the one name in it that is not a Zed built-in on a stock install (it comes from a community extension), and an unknown language name is the kind of thing an extension host may reject the whole manifest over. Remove `"Swift"` from `zed/kraken-mm/extension.toml`'s `languages` list and reinstall, or install Zed's Swift extension first.

One more possibility, and it depends on your settings rather than on this extension. Zed runs every language server registered for a language *unless* you have pinned an explicit `language_servers` list for it — in which case a server you did not name is excluded. There is no such override in `~/.config/zed/settings.json` today, so no settings block is needed; if you add one later, re-admit this server with `"..."` (Zed's "and all the rest" marker) or by name:

```json
{
  "languages": {
    "Go": { "language_servers": ["gopls", "kraken-mm", "..."] }
  }
}
```

The extension declares an explicit language list (Go, Python, TypeScript, TSX, JavaScript, Swift, Markdown, JSON, YAML, Shell Script, Rust, C, CSS, HTML, Plain Text) because Zed has no wildcard for this — a file type not on that list gets no marks drawn until it is added to `zed/kraken-mm/extension.toml`.
