package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// graph.go — the kg_* tables (schemaV15): the knowledge graph's episodes,
// nodes, edges, aliases, ingest cursors and cached layout.
//
// Split by reader, the same rule tokens.go states for its two tables: the
// viewer's hot read (GraphSnapshot) never touches kg_ingest_cursor, which
// only the daemon's graphPass sweeps. Writers are `kraken serve`'s handlers
// (k graph add arrives over HTTP) and the daemon's graphPass (cursors,
// embeddings, layout); none of the six tables above carries a carl-owned
// column, so none of watch.go's preserve-the-other-side splitting applies in
// this file. kg_watch (schemaV16) is the one kg_* table that DOES carry
// carl's trio, and it lives in watch.go precisely so that splitting stays
// where the columns are.
//
// Edges are never deleted. InvalidateEdge closes an edge's validity window
// (valid_until + expired_at); the fixed set of columns it may touch is the
// whole mutation surface of a stored fact — everything else is insert-only.

// ftsMaxTerms bounds one SearchEdgesFTS query's OR-of-terms after
// deduplication. 64 distinct terms is beyond any human query and enough of a
// job spec's distinctive vocabulary to rank on; the cap is what keeps a
// pasted-transcript spec from turning a spawn-path search into seconds of
// work on the sole connection.
const ftsMaxTerms = 64

// Episode is one ingested provenance unit: a span of a transcript, or one
// feedback/watchlist/artifact/job row, reduced to the text handed to the
// extraction agent. Tag-free like every type in this package (the API layer
// owns its own DTOs).
type Episode struct {
	ID        int64
	Kind      string // transcript | feedback | watchlist | artifact | job
	Ref       string // source pointer: "path#start-end" for files, row id otherwise
	Content   string // the reduced text
	Hash      string // dedupe key: a re-scan of the same span is a no-op
	TS        int64  // unix millis — when the source material was written
	State     string // pending | done
	CreatedAt int64  // unix millis
}

// GraphNode is one entity. Description is the short natural-language gloss of
// what the concept IS, independent of any edge — the field an agent expands
// an unfamiliar name with, without traversing.
type GraphNode struct {
	ID          int64
	Name        string
	Type        string
	Description string
	Embedding   []byte // little-endian float32; empty = not embedded yet
	CreatedAt   int64  // unix millis
	UpdatedAt   int64  // unix millis
}

// GraphEdge is one fact. Fact is the natural-language sentence — the edge's
// short description IS its name, per the spec's data model. Two time axes:
// ValidFrom/ValidUntil are world time (0 = unknown / still true),
// CreatedAt/ExpiredAt are system time (0 = live). A contradiction closes the
// old edge's window; nothing is deleted.
type GraphEdge struct {
	ID          int64
	SrcID       int64
	DstID       int64
	Predicate   string
	Fact        string
	EpisodeID   int64
	Strength    int64 // extractor's 1-10
	Occurrences int64 // distinct episodes independently stating this fact
	ValidFrom   int64 // unix millis; 0 = unknown
	ValidUntil  int64 // unix millis; 0 = still true
	Embedding   []byte
	CreatedAt   int64 // unix millis
	ExpiredAt   int64 // unix millis; 0 = live
}

// LayoutPos is one node's cached server-computed position, stamped with the
// graph revision (max kg_edges.id) it was computed at.
type LayoutPos struct {
	NodeID int64
	X, Y   float64
	Rev    int64
}

// AddEpisode inserts one pending episode, deduped by hash: re-scanning a span
// that is already stored returns (existing id, false, nil) and writes
// nothing, which is what makes graphPass safe to re-run over the same bytes
// after a cursor was deliberately carried backward.
func (s *Store) AddEpisode(e Episode) (id int64, added bool, err error) {
	now := time.Now().UnixMilli()
	res, err := s.db.Exec(`INSERT INTO kg_episodes (kind, ref, content, hash, ts, state, created_at)
		VALUES (?, ?, ?, ?, ?, 'pending', ?)
		ON CONFLICT(hash) DO NOTHING`,
		e.Kind, e.Ref, e.Content, e.Hash, e.TS, now)
	if err != nil {
		return 0, false, fmt.Errorf("add episode: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		id, _ := res.LastInsertId()
		return id, true, nil
	}
	var existing int64
	if err := s.db.QueryRow(`SELECT id FROM kg_episodes WHERE hash = ?`, e.Hash).Scan(&existing); err != nil {
		return 0, false, fmt.Errorf("add episode (existing): %w", err)
	}
	return existing, false, nil
}

// PendingEpisodes returns up to limit episodes still awaiting extraction,
// oldest first, so the nightly agent drains the backlog in ingest order.
func (s *Store) PendingEpisodes(limit int) ([]Episode, error) {
	rows, err := s.db.Query(`SELECT id, kind, ref, content, hash, ts, state, created_at
		FROM kg_episodes WHERE state = 'pending' ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("pending episodes: %w", err)
	}
	defer rows.Close()
	var out []Episode
	for rows.Next() {
		var e Episode
		if err := rows.Scan(&e.ID, &e.Kind, &e.Ref, &e.Content, &e.Hash, &e.TS, &e.State, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("pending episodes scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkEpisodeDone flips one episode out of the pending queue.
func (s *Store) MarkEpisodeDone(id int64) error {
	_, err := s.db.Exec(`UPDATE kg_episodes SET state = 'done' WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("mark episode done: %w", err)
	}
	return nil
}

// GetEpisode returns one episode, nil when absent.
func (s *Store) GetEpisode(id int64) (*Episode, error) {
	var e Episode
	err := s.db.QueryRow(`SELECT id, kind, ref, content, hash, ts, state, created_at
		FROM kg_episodes WHERE id = ?`, id).
		Scan(&e.ID, &e.Kind, &e.Ref, &e.Content, &e.Hash, &e.TS, &e.State, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get episode: %w", err)
	}
	return &e, nil
}

// ResolveNode finds a node by canonical name or alias, case-insensitively.
// Returns nil when nothing matches — the extraction agent's cue to create
// rather than merge.
func (s *Store) ResolveNode(name string) (*GraphNode, error) {
	n := strings.TrimSpace(name)
	var g GraphNode
	err := s.db.QueryRow(`SELECT id, name, type, description, embedding, created_at, updated_at
		FROM kg_nodes WHERE LOWER(name) = LOWER(?)`, n).
		Scan(&g.ID, &g.Name, &g.Type, &g.Description, &g.Embedding, &g.CreatedAt, &g.UpdatedAt)
	if err == nil {
		return &g, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("resolve node: %w", err)
	}
	// ORDER BY n.id: kg_node_aliases' primary key is (node_id, alias), so two
	// nodes CAN carry the same alias; without an order the winner would be
	// whichever row SQLite hands back first, and the same fact could attach
	// to different nodes on different runs. Oldest node wins, deterministically.
	err = s.db.QueryRow(`SELECT n.id, n.name, n.type, n.description, n.embedding, n.created_at, n.updated_at
		FROM kg_node_aliases a JOIN kg_nodes n ON n.id = a.node_id
		WHERE LOWER(a.alias) = LOWER(?) ORDER BY n.id LIMIT 1`, n).
		Scan(&g.ID, &g.Name, &g.Type, &g.Description, &g.Embedding, &g.CreatedAt, &g.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve node alias: %w", err)
	}
	return &g, nil
}

// UpsertNode creates the node if its name (or an alias of an existing node)
// is unknown, else updates type/description when the incoming values are
// non-empty (an extractor that saw less detail must not blank a richer gloss)
// and clears the embedding when the description changed, since the stored
// vector embedded the old text. Returns the node's id.
func (s *Store) UpsertNode(n GraphNode) (int64, error) {
	now := time.Now().UnixMilli()
	existing, err := s.ResolveNode(n.Name)
	if err != nil {
		return 0, err
	}
	if existing == nil {
		res, err := s.db.Exec(`INSERT INTO kg_nodes (name, type, description, embedding, created_at, updated_at)
			VALUES (?, ?, ?, x'', ?, ?)`,
			strings.TrimSpace(n.Name), n.Type, n.Description, now, now)
		if err != nil {
			return 0, fmt.Errorf("insert node: %w", err)
		}
		id, _ := res.LastInsertId()
		return id, nil
	}
	typ := existing.Type
	if n.Type != "" {
		typ = n.Type
	}
	desc := existing.Description
	if n.Description != "" {
		desc = n.Description
	}
	if typ == existing.Type && desc == existing.Description {
		return existing.ID, nil
	}
	// A changed description invalidates the stored vector (it embedded the
	// old text); clearing it is what re-queues the node for graphPass's
	// embed sweep.
	emb := "embedding"
	if desc != existing.Description {
		emb = "x''"
	}
	if _, err := s.db.Exec(`UPDATE kg_nodes SET type = ?, description = ?, embedding = `+emb+`, updated_at = ? WHERE id = ?`,
		typ, desc, now, existing.ID); err != nil {
		return 0, fmt.Errorf("update node: %w", err)
	}
	return existing.ID, nil
}

// AddAlias records that alias resolves to nodeID. Idempotent.
func (s *Store) AddAlias(nodeID int64, alias string) error {
	_, err := s.db.Exec(`INSERT INTO kg_node_aliases (node_id, alias) VALUES (?, ?)
		ON CONFLICT(node_id, alias) DO NOTHING`, nodeID, strings.TrimSpace(alias))
	if err != nil {
		return fmt.Errorf("add alias: %w", err)
	}
	return nil
}

// InsertEdge stores one fact. If a LIVE edge with the same (src, dst,
// predicate) AND the same fact sentence already exists, the incoming edge is
// a repeat sighting: occurrences increments, strength takes the max, and the
// existing id returns with added=false. The sentence is part of the repeat
// key on purpose — it is the row's entire payload (what FTS indexes, what
// gets embedded, what graphContext injects), so two distinct facts that
// happen to share endpoints and predicate must both be STORED, not collapsed
// into a bumped counter that presents one of them as twice-corroborated
// while silently discarding the other (review finding). Near-duplicate
// wordings therefore create near-duplicate edges; merging those is the
// revision job's judgment call, not this function's. The caller decides
// contradiction (InvalidateEdge) before calling this; the store only
// recognises repeats.
func (s *Store) InsertEdge(e GraphEdge) (id int64, added bool, err error) {
	now := time.Now().UnixMilli()
	var exID, exStrength int64
	err = s.db.QueryRow(`SELECT id, strength FROM kg_edges
		WHERE src_id = ? AND dst_id = ? AND predicate = ? AND fact = ? AND expired_at = 0`,
		e.SrcID, e.DstID, e.Predicate, e.Fact).Scan(&exID, &exStrength)
	if err == nil {
		st := exStrength
		if e.Strength > st {
			st = e.Strength
		}
		if _, err := s.db.Exec(`UPDATE kg_edges SET occurrences = occurrences + 1, strength = ? WHERE id = ?`, st, exID); err != nil {
			return 0, false, fmt.Errorf("bump edge: %w", err)
		}
		return exID, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, fmt.Errorf("find edge: %w", err)
	}
	occ := e.Occurrences
	if occ <= 0 {
		occ = 1
	}
	res, err := s.db.Exec(`INSERT INTO kg_edges
		(src_id, dst_id, predicate, fact, episode_id, strength, occurrences, valid_from, valid_until, embedding, created_at, expired_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, x'', ?, 0)`,
		e.SrcID, e.DstID, e.Predicate, e.Fact, e.EpisodeID, e.Strength, occ, e.ValidFrom, e.ValidUntil, now)
	if err != nil {
		return 0, false, fmt.Errorf("insert edge: %w", err)
	}
	id, _ = res.LastInsertId()
	return id, true, nil
}

// InvalidateEdge closes an edge's validity window: world time ends at
// validUntil (unix millis) and system time records the moment kraken
// unlearned it. The row survives — point-in-time queries are the reason the
// graph is bi-temporal at all.
func (s *Store) InvalidateEdge(id int64, validUntil int64) error {
	now := time.Now().UnixMilli()
	res, err := s.db.Exec(`UPDATE kg_edges SET valid_until = ?, expired_at = ? WHERE id = ? AND expired_at = 0`,
		validUntil, now, id)
	if err != nil {
		return fmt.Errorf("invalidate edge: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Distinguish "already invalidated" from "no such edge": the first is
		// a success, or a retry of a partially-applied add would 500 on work
		// that already landed (the api handler documents its adds as
		// idempotent under retry, and this is what makes that claim true).
		var expired int64
		err := s.db.QueryRow(`SELECT expired_at FROM kg_edges WHERE id = ?`, id).Scan(&expired)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("invalidate edge %d: no edge with that id", id)
		}
		if err != nil {
			return fmt.Errorf("invalidate edge %d: %w", id, err)
		}
		if expired == 0 {
			return fmt.Errorf("invalidate edge %d: update matched no row", id)
		}
		return nil
	}
	return nil
}

// SearchEdgesFTS runs a BM25 query over live edges' fact sentences and
// returns up to limit edge ids, best first. The query string is passed to
// FTS5 as a plain OR-of-terms (each token quoted), never raw: FTS5's own
// query syntax would turn a hyphen or quote in user text into a syntax
// error, and a search verb must not error on the strings this corpus is
// full of (`carl/<slug>-<id>`, `%3`).
func (s *Store) SearchEdgesFTS(query string, limit int) ([]int64, error) {
	terms := strings.Fields(query)
	if len(terms) == 0 {
		return nil, nil
	}
	// Dedupe, then cap at ftsMaxTerms. Query cost is roughly O(terms × index
	// size) — measured during review at ~0.8s for a 1,476-word job spec over
	// 20k edges, on the process's ONLY database connection — and callers pass
	// arbitrary text (graphContext hands over a whole job spec). Duplicates
	// add cost and nothing else (an OR of two identical quoted terms matches
	// the same rows); past the cap, later terms add recall no ranked list of
	// `limit` rows can surface anyway. First-seen order keeps the cap
	// deterministic.
	seen := make(map[string]bool, len(terms))
	quoted := make([]string, 0, len(terms))
	for _, t := range terms {
		if seen[t] {
			continue
		}
		seen[t] = true
		quoted = append(quoted, `"`+strings.ReplaceAll(t, `"`, `""`)+`"`)
		if len(quoted) == ftsMaxTerms {
			break
		}
	}
	match := strings.Join(quoted, " OR ")
	rows, err := s.db.Query(`SELECT f.rowid FROM kg_edges_fts f
		JOIN kg_edges e ON e.id = f.rowid
		WHERE kg_edges_fts MATCH ? AND e.expired_at = 0
		ORDER BY bm25(kg_edges_fts) LIMIT ?`, match, limit)
	if err != nil {
		return nil, fmt.Errorf("fts search: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("fts scan: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// GetEdges returns the edges for the given ids, in the given order; ids with
// no row are skipped rather than erroring (a stale id from a fused rank list
// is not the caller's failure).
func (s *Store) GetEdges(ids []int64) ([]GraphEdge, error) {
	byID := make(map[int64]GraphEdge, len(ids))
	for _, chunk := range chunkIDs(ids, 500) {
		ph := strings.TrimRight(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := s.db.Query(`SELECT id, src_id, dst_id, predicate, fact, episode_id, strength, occurrences,
			valid_from, valid_until, embedding, created_at, expired_at
			FROM kg_edges WHERE id IN (`+ph+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("get edges: %w", err)
		}
		for rows.Next() {
			var e GraphEdge
			if err := rows.Scan(&e.ID, &e.SrcID, &e.DstID, &e.Predicate, &e.Fact, &e.EpisodeID, &e.Strength,
				&e.Occurrences, &e.ValidFrom, &e.ValidUntil, &e.Embedding, &e.CreatedAt, &e.ExpiredAt); err != nil {
				rows.Close()
				return nil, fmt.Errorf("get edges scan: %w", err)
			}
			byID[e.ID] = e
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	out := make([]GraphEdge, 0, len(ids))
	for _, id := range ids {
		if e, ok := byID[id]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}

// chunkIDs splits ids into slices of at most n, keeping order. SQLite's
// default variable cap is 999; 500 stays comfortably under it.
func chunkIDs(ids []int64, n int) [][]int64 {
	var out [][]int64
	for len(ids) > n {
		out = append(out, ids[:n])
		ids = ids[n:]
	}
	if len(ids) > 0 {
		out = append(out, ids)
	}
	return out
}

// NeighborEdges returns every LIVE edge touching any of the given node ids —
// the 1-hop expansion the read path caps and packs.
func (s *Store) NeighborEdges(nodeIDs []int64) ([]GraphEdge, error) {
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	ph := strings.TrimRight(strings.Repeat("?,", len(nodeIDs)), ",")
	args := make([]any, 0, len(nodeIDs)*2)
	for _, id := range nodeIDs {
		args = append(args, id)
	}
	for _, id := range nodeIDs {
		args = append(args, id)
	}
	// embedding is deliberately NOT selected: every consumer of a neighbor
	// edge reads names/fact/counts (expansion, the inspector, factLine) and a
	// hub node with 10k edges would otherwise hydrate ~40 MB of blobs at 1024
	// dims so the caller could keep 12 of them (review finding). The Embedding
	// field stays zero — a caller needing vectors goes through AllEdges, or
	// reads the specific rows back with GetEdges as the search route's
	// weak-marking does.
	rows, err := s.db.Query(`SELECT id, src_id, dst_id, predicate, fact, episode_id, strength, occurrences,
		valid_from, valid_until, x'' , created_at, expired_at
		FROM kg_edges WHERE expired_at = 0 AND (src_id IN (`+ph+`) OR dst_id IN (`+ph+`))
		ORDER BY occurrences DESC, id`, args...)
	if err != nil {
		return nil, fmt.Errorf("neighbor edges: %w", err)
	}
	defer rows.Close()
	var out []GraphEdge
	for rows.Next() {
		var e GraphEdge
		if err := rows.Scan(&e.ID, &e.SrcID, &e.DstID, &e.Predicate, &e.Fact, &e.EpisodeID, &e.Strength,
			&e.Occurrences, &e.ValidFrom, &e.ValidUntil, &e.Embedding, &e.CreatedAt, &e.ExpiredAt); err != nil {
			return nil, fmt.Errorf("neighbor edges scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetNodes returns nodes by id; absent ids are skipped, same contract as
// GetEdges.
func (s *Store) GetNodes(ids []int64) ([]GraphNode, error) {
	byID := make(map[int64]GraphNode, len(ids))
	for _, chunk := range chunkIDs(ids, 500) {
		ph := strings.TrimRight(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := s.db.Query(`SELECT id, name, type, description, embedding, created_at, updated_at
			FROM kg_nodes WHERE id IN (`+ph+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("get nodes: %w", err)
		}
		for rows.Next() {
			var n GraphNode
			if err := rows.Scan(&n.ID, &n.Name, &n.Type, &n.Description, &n.Embedding, &n.CreatedAt, &n.UpdatedAt); err != nil {
				rows.Close()
				return nil, fmt.Errorf("get nodes scan: %w", err)
			}
			byID[n.ID] = n
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	out := make([]GraphNode, 0, len(ids))
	for _, id := range ids {
		if n, ok := byID[id]; ok {
			out = append(out, n)
		}
	}
	return out, nil
}

// NodesLite and EdgesLite are the viewer's hot read: every row, embedding
// column NOT selected (the field stays zero-length). At 1024 dims the blobs
// are 4 KB per row that no client can render — the split-by-reader rule the
// file header states, applied to the one payload that is polled. Vector
// consumers use AllNodes/AllEdges.
func (s *Store) NodesLite() ([]GraphNode, error) {
	rows, err := s.db.Query(`SELECT id, name, type, description, x'', created_at, updated_at
		FROM kg_nodes ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("nodes lite: %w", err)
	}
	defer rows.Close()
	var out []GraphNode
	for rows.Next() {
		var n GraphNode
		if err := rows.Scan(&n.ID, &n.Name, &n.Type, &n.Description, &n.Embedding, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, fmt.Errorf("nodes lite scan: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) EdgesLite() ([]GraphEdge, error) {
	rows, err := s.db.Query(`SELECT id, src_id, dst_id, predicate, fact, episode_id, strength, occurrences,
		valid_from, valid_until, x'', created_at, expired_at
		FROM kg_edges ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("edges lite: %w", err)
	}
	defer rows.Close()
	var out []GraphEdge
	for rows.Next() {
		var e GraphEdge
		if err := rows.Scan(&e.ID, &e.SrcID, &e.DstID, &e.Predicate, &e.Fact, &e.EpisodeID, &e.Strength,
			&e.Occurrences, &e.ValidFrom, &e.ValidUntil, &e.Embedding, &e.CreatedAt, &e.ExpiredAt); err != nil {
			return nil, fmt.Errorf("edges lite scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AllNodes returns every node. The viewer's snapshot and the layout pass both
// want the whole set; at this table's scale (see the spec's measurements) a
// full sweep is milliseconds.
func (s *Store) AllNodes() ([]GraphNode, error) {
	rows, err := s.db.Query(`SELECT id, name, type, description, embedding, created_at, updated_at
		FROM kg_nodes ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("all nodes: %w", err)
	}
	defer rows.Close()
	var out []GraphNode
	for rows.Next() {
		var n GraphNode
		if err := rows.Scan(&n.ID, &n.Name, &n.Type, &n.Description, &n.Embedding, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, fmt.Errorf("all nodes scan: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// AllEdges returns every edge, live and invalidated both — the viewer renders
// invalidated facts struck-through rather than hiding history, and the layout
// pass wants the full topology.
func (s *Store) AllEdges() ([]GraphEdge, error) {
	rows, err := s.db.Query(`SELECT id, src_id, dst_id, predicate, fact, episode_id, strength, occurrences,
		valid_from, valid_until, embedding, created_at, expired_at
		FROM kg_edges ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("all edges: %w", err)
	}
	defer rows.Close()
	var out []GraphEdge
	for rows.Next() {
		var e GraphEdge
		if err := rows.Scan(&e.ID, &e.SrcID, &e.DstID, &e.Predicate, &e.Fact, &e.EpisodeID, &e.Strength,
			&e.Occurrences, &e.ValidFrom, &e.ValidUntil, &e.Embedding, &e.CreatedAt, &e.ExpiredAt); err != nil {
			return nil, fmt.Errorf("all edges scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// NodeAliases returns the alias list for one node.
func (s *Store) NodeAliases(nodeID int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT alias FROM kg_node_aliases WHERE node_id = ? ORDER BY alias`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("node aliases: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, fmt.Errorf("node aliases scan: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// MissingEmbeddings returns up to limit (kind, id, text) triples still
// awaiting a vector — edges embed their fact sentence, nodes their
// name-prefixed description. graphPass drains this queue asynchronously; a
// row sits here harmlessly forever if no API key is ever configured.
//
// EDGES COME FIRST, and the order is load-bearing: every vector reader in
// the binary (core.GraphSearch's vectorLeg, jobs' graphContext) reads EDGE
// embeddings only — node vectors are stored for a later node-level search
// and nothing consumes them yet. UNION ALL + LIMIT drains arms in order, so
// with nodes first a 10k-node ingest would starve the only vectors search
// uses behind ~2.6 hours of paid embedding calls nothing reads (review
// finding, measured: a 200/200 fixture returned map[node:64] for the first
// batch). Nodes still embed — after the edges are done.
type MissingEmbedding struct {
	Kind string // "node" | "edge"
	ID   int64
	Text string
}

func (s *Store) MissingEmbeddings(limit int) ([]MissingEmbedding, error) {
	rows, err := s.db.Query(`SELECT 'edge', id, fact FROM kg_edges WHERE length(embedding) = 0 AND expired_at = 0
		UNION ALL
		SELECT 'node', id, name || ': ' || description FROM kg_nodes WHERE length(embedding) = 0
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("missing embeddings: %w", err)
	}
	defer rows.Close()
	var out []MissingEmbedding
	for rows.Next() {
		var m MissingEmbedding
		if err := rows.Scan(&m.Kind, &m.ID, &m.Text); err != nil {
			return nil, fmt.Errorf("missing embeddings scan: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetEmbedding stores one vector on a node or edge.
func (s *Store) SetEmbedding(kind string, id int64, emb []byte) error {
	table := "kg_nodes"
	if kind == "edge" {
		table = "kg_edges"
	} else if kind != "node" {
		return fmt.Errorf("set embedding: unknown kind %q", kind)
	}
	if _, err := s.db.Exec(`UPDATE `+table+` SET embedding = ? WHERE id = ?`, emb, id); err != nil {
		return fmt.Errorf("set embedding: %w", err)
	}
	return nil
}

// IngestCursor returns one source's cursor, 0 when never seen.
func (s *Store) IngestCursor(source string) (int64, error) {
	var c int64
	err := s.db.QueryRow(`SELECT cursor FROM kg_ingest_cursor WHERE source = ?`, source).Scan(&c)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("ingest cursor: %w", err)
	}
	return c, nil
}

// AllIngestCursors returns every source's cursor in one query — graphPass
// reads the whole map per tick rather than one round trip per file, the same
// shape AllJobTokenOffsets serves tokensPass with.
func (s *Store) AllIngestCursors() (map[string]int64, error) {
	rows, err := s.db.Query(`SELECT source, cursor FROM kg_ingest_cursor`)
	if err != nil {
		return nil, fmt.Errorf("all ingest cursors: %w", err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var src string
		var c int64
		if err := rows.Scan(&src, &c); err != nil {
			return nil, fmt.Errorf("all ingest cursors scan: %w", err)
		}
		out[src] = c
	}
	return out, rows.Err()
}

// SetIngestCursor advances (or rewinds — the caller owns the direction) one
// source's cursor.
func (s *Store) SetIngestCursor(source string, cursor int64) error {
	now := time.Now().UnixMilli()
	_, err := s.db.Exec(`INSERT INTO kg_ingest_cursor (source, cursor, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(source) DO UPDATE SET cursor = excluded.cursor, updated_at = excluded.updated_at`,
		source, cursor, now)
	if err != nil {
		return fmt.Errorf("set ingest cursor: %w", err)
	}
	return nil
}

// Layout returns every cached node position.
func (s *Store) Layout() ([]LayoutPos, error) {
	rows, err := s.db.Query(`SELECT node_id, x, y, rev FROM kg_layout`)
	if err != nil {
		return nil, fmt.Errorf("layout: %w", err)
	}
	defer rows.Close()
	var out []LayoutPos
	for rows.Next() {
		var p LayoutPos
		if err := rows.Scan(&p.NodeID, &p.X, &p.Y, &p.Rev); err != nil {
			return nil, fmt.Errorf("layout scan: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetLayout replaces the cached layout wholesale, in one transaction — the
// layout pass computes all positions together, and a half-written layout
// would draw half the graph at the origin.
func (s *Store) SetLayout(pos []LayoutPos) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("set layout: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM kg_layout`); err != nil {
		return fmt.Errorf("set layout clear: %w", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO kg_layout (node_id, x, y, rev) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("set layout prepare: %w", err)
	}
	defer stmt.Close()
	for _, p := range pos {
		if _, err := stmt.Exec(p.NodeID, p.X, p.Y, p.Rev); err != nil {
			return fmt.Errorf("set layout insert: %w", err)
		}
	}
	return tx.Commit()
}

// GraphStats is the k graph stats payload: the growth curve Q10 asked to
// keep visible.
type GraphStats struct {
	Nodes, Edges, LiveEdges, Episodes, PendingEpisodes int64
	MissingEmbeddings                                  int64
	MaxEdgeID                                          int64 // the layout revision marker
}

func (s *Store) GetGraphStats() (GraphStats, error) {
	var st GraphStats
	row := s.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM kg_nodes),
		(SELECT COUNT(*) FROM kg_edges),
		(SELECT COUNT(*) FROM kg_edges WHERE expired_at = 0),
		(SELECT COUNT(*) FROM kg_episodes),
		(SELECT COUNT(*) FROM kg_episodes WHERE state = 'pending'),
		(SELECT COUNT(*) FROM kg_nodes WHERE length(embedding) = 0) +
			(SELECT COUNT(*) FROM kg_edges WHERE length(embedding) = 0 AND expired_at = 0),
		COALESCE((SELECT MAX(id) FROM kg_edges), 0)`)
	if err := row.Scan(&st.Nodes, &st.Edges, &st.LiveEdges, &st.Episodes, &st.PendingEpisodes,
		&st.MissingEmbeddings, &st.MaxEdgeID); err != nil {
		return GraphStats{}, fmt.Errorf("graph stats: %w", err)
	}
	return st, nil
}
