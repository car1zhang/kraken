package web

import "embed"

// k web — a localhost web UI over the same file-derived state as k j and the
// viewer: jobs as a kanban board, questions answered inline, artifacts rendered in
// the browser. Read-only in this first version; answering stays in k v.
// Vanilla html/css/js, embedded so the installed binary is self-contained.

//go:embed static
var webAssets embed.FS
