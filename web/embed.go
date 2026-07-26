package web

import "embed"

// k web — a localhost web UI over the same job/board state as the bubbletea
// viewer: jobs as a kanban board, questions answered one click into a job
// (the card carries only a count chip), artifacts rendered in the browser.
// Vanilla html/css/js, embedded so the installed binary is self-contained.

//go:embed static
var webAssets embed.FS
