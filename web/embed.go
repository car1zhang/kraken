package web

import "embed"

// k web — a localhost web UI over the same job/board state as the bubbletea
// viewer: jobs as a kanban board, questions answered one click into a job
// (the card carries only a count chip), artifacts rendered in the browser.
// A React + TypeScript app under web/_ui, built by vite and embedded so the
// installed binary is self-contained.
//
// Two details of this one directive are load bearing, and both are about the
// go tool rather than about the UI:
//
//   the leading underscore on _ui — `go build ./...` walks into node_modules
//     and one stray .go file inside any npm package fails the whole build
//     (verified: it is a parse error pointing inside a dependency). The go
//     tool skips directories named "testdata" and any beginning with "." or
//     "_", but NOT node_modules, so the underscore is what keeps ~150
//     packages of JavaScript out of the build graph.
//
//   the all: prefix — embed patterns skip "_"-prefixed paths for the same
//     reason the go tool does, so without it this pattern matches nothing.
//
// The directory itself is generated: `npm run build` writes _ui/dist and it
// is gitignored, which is why the Makefile's build target depends on
// web-assets. go:embed reads the directory from disk at compile time and
// CANNOT embed an empty one, so a clean clone fails to build until the
// bundle exists — loudly, which is the intended failure. `make build` is the
// supported entrypoint; a bare `go build` works only once assets are there.

//go:embed all:_ui/dist
var webAssets embed.FS
