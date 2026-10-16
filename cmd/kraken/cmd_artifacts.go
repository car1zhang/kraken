package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"kraken/internal/jobs"
	"kraken/internal/util"
)

// cmd_artifacts.go — `k artifacts get <slug> [name]`: the agent-side read
// of ANY job's artifacts, a sibling cell's or a torn-down job's, proxied to
// kraken serve exactly like the `questions`/`load` family. Once a cell is
// torn down its artifacts exist only as rows in kraken.db's artifacts table,
// so this verb — not a directory — is how an agent reads them.
//
// It reads the board rather than a route of its own: GET /api/board already
// lists every job's artifacts with the URL each is served at (the live cell
// dir for a running job, the table for one whose cell is gone, the archived
// name for an archived one), so the listing and the bytes both come from the
// one place the board and the phone read them, in the board's own order.

const artifactsUsage = "usage: k artifacts get [--archived] <slug> [name]"

func cmdArtifacts(args []string) {
	if len(args) == 0 || args[0] != "get" {
		util.Die("%s", artifactsUsage)
	}
	out, err := runArtifactsGet(args[1:])
	if err != nil {
		util.Die("%v", err)
	}
	if _, err := os.Stdout.Write(out); err != nil {
		util.Die("write stdout: %v", err)
	}
}

// artifactsBoard is the slice of GET /api/board this verb reads: each job's
// slug and its artifact list. Decoded loosely on purpose — the board carries
// far more, and this verb must not break when an unrelated key is added.
type artifactsBoard struct {
	Jobs []struct {
		Slug        string `json:"slug"`
		Archived    bool   `json:"archived"`
		DisplaySlug string `json:"display_slug"`
		Artifacts   []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"artifacts"`
	} `json:"jobs"`
}

// runArtifactsGet is `k artifacts get [--archived] <slug> [name]`. With only
// a slug it returns one artifact name per line, in served order (empty
// output for a job with none). With a name it returns that artifact's bytes,
// fetched from the URL the board lists for it.
//
// An active job and an archived one can share a slug (archiving frees the
// slug, and the archive keeps the plain name when it is free there), so the
// match is two-tier: without --archived the active job wins and an archived
// one answers only when no active job has the slug; with --archived only an
// archived job is considered, matched on its archived name or its original
// slug.
func runArtifactsGet(rest []string) ([]byte, error) {
	archived := false
	if len(rest) > 0 && rest[0] == "--archived" {
		archived = true
		rest = rest[1:]
	}
	if len(rest) != 1 && len(rest) != 2 {
		return nil, errors.New(artifactsUsage)
	}
	slug := strings.TrimSpace(rest[0])
	if slug == "" {
		return nil, errors.New(artifactsUsage)
	}
	raw, err := qfGet("/api/board")
	if err != nil {
		return nil, err
	}
	var board artifactsBoard
	if err := json.Unmarshal(raw, &board); err != nil {
		return nil, fmt.Errorf("decode /api/board: %w", err)
	}
	// Ranked rather than first-match: an exact name beats a fallback. Without
	// --archived: an active job's slug (rank 2) beats an archived job whose
	// archived name is the slug (rank 1). With --archived: an archived name
	// (rank 2) beats an archived job's original slug (display_slug, rank 1) —
	// archiving foo then a second foo yields archived names foo and foo-2,
	// both with display_slug foo, and `--archived foo` must reach the first.
	pick, best := -1, 0
	for i, j := range board.Jobs {
		rank := 0
		switch {
		case archived && j.Archived && j.Slug == slug:
			rank = 2
		case archived && j.Archived && j.DisplaySlug == slug:
			rank = 1
		case !archived && !j.Archived && j.Slug == slug:
			rank = 2
		case !archived && j.Archived && j.Slug == slug:
			rank = 1
		}
		if rank > best {
			pick, best = i, rank
		}
	}
	if pick < 0 {
		if archived {
			return nil, fmt.Errorf("no archived job %q on the board", slug)
		}
		return nil, fmt.Errorf("no job %q on the board", slug)
	}
	var names []string
	var urls []string
	for _, a := range board.Jobs[pick].Artifacts {
		names = append(names, a.Name)
		urls = append(urls, a.URL)
	}
	if len(rest) == 1 {
		var b strings.Builder
		for _, n := range names {
			b.WriteString(n)
			b.WriteByte('\n')
		}
		return []byte(b.String()), nil
	}
	name := rest[1]
	for i, n := range names {
		if n != name {
			continue
		}
		u, err := url.Parse(urls[i])
		if err != nil {
			return nil, fmt.Errorf("artifact %q of job %q has an unusable url %q: %w", name, slug, urls[i], err)
		}
		base, err := url.Parse(jobs.ServeBaseURL() + "/")
		if err != nil {
			return nil, fmt.Errorf("bad serve address %q: %w", jobs.ServeBaseURL(), err)
		}
		return artifactsFetch(base.ResolveReference(u).String())
	}
	return nil, fmt.Errorf("job %q has no artifact %q", slug, name)
}

// artifactsFetch GETs one artifact's absolute URL with qfGet's exact
// success/failure contract. It is not qfGet itself because the board's URL
// is resolved against the base rather than appended to it.
func artifactsFetch(full string) ([]byte, error) {
	client := http.Client{Timeout: qfHTTPTimeout}
	resp, err := client.Get(full)
	if err != nil {
		if qfIsTimeout(err) {
			return nil, qfTimeoutError(err)
		}
		return nil, qfUnreachableError(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, qfDecodeError(resp.StatusCode, body)
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", full, err)
	}
	return body, nil
}
