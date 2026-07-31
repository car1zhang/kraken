package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"kraken/internal/util"
)

// embed.go — text -> vector via the OpenAI embeddings API. This is kraken's
// first (and only) outbound API client, and the seam is deliberately one
// small interface so the Q2 decision stays reversible: everything above it
// asks an Embedder for vectors and never knows which model, which dims, or
// whether a network was involved.
//
// The decided default (Q2, answered 2026-07-31) is text-embedding-3-large at
// dimensions=1024: native 3072 dims would put a gigabyte-class slab in the
// daemon for a quality gain the research measured as marginal, and OpenAI's
// dimensions parameter is native Matryoshka truncation. Both are config, not
// code — OPENAI_EMBED_MODEL / OPENAI_EMBED_DIMS in the env file below.
//
// Credentials follow shipios.go's asc.env pattern exactly, for the same
// launchd reason: `launchctl setenv` does not reach a job whose plist
// declares its own EnvironmentVariables, so a file is the only way these
// reach `kraken serve`. Process env wins over the file (an explicit export
// in a terminal must be able to override for a test), the file is parsed
// never sourced, and the key's VALUE must never reach a log, an error
// message, or an event — only its name, and only to say it is missing.
//
// Failure posture: embedding is ASYNC everywhere. Rows are written without
// vectors and graphPass fills them in later; a missing key or a down API
// degrades retrieval to BM25-only and blocks nothing. Callers treat
// ErrNoEmbedder as "feature not configured", never as a failure to report.

// EmbedEnvPath is the credential file: ~/.kraken/openai.env, 0600, holding
// OPENAI_API_KEY and optionally OPENAI_EMBED_MODEL / OPENAI_EMBED_DIMS.
func EmbedEnvPath() string { return filepath.Join(util.Home(), ".kraken", "openai.env") }

var embedEnvKeys = []string{"OPENAI_API_KEY", "OPENAI_EMBED_MODEL", "OPENAI_EMBED_DIMS"}

const (
	defaultEmbedModel = "text-embedding-3-large"
	defaultEmbedDims  = 1024
	embedURL          = "https://api.openai.com/v1/embeddings"
	// embedBatch bounds one request's input list. OpenAI accepts far larger
	// arrays; 128 keeps a single request's failure blast radius (and its
	// response body) small while amortizing the round trip fine.
	embedBatch = 128
)

// ErrNoEmbedder marks "no API key configured" — the normal state of a fresh
// install, not an error condition. Callers switch on it with errors.Is. The
// message names the file generically rather than interpolating EmbedEnvPath:
// this is a package-level var initialised before anything can override
// util.Home in tests, and the tilde form is what a human fixes it with.
var ErrNoEmbedder = errors.New("no embedder configured (no OPENAI_API_KEY in env or ~/.kraken/openai.env)")

// Embedder turns short texts into unit-norm vectors.
type Embedder interface {
	Embed(texts []string) ([][]float32, error)
	Dims() int
}

// loadEmbedEnv parses EmbedEnvPath, same defensive shape as loadASCEnv: a
// missing or malformed file is an empty map, never an error.
func loadEmbedEnv() map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(EmbedEnvPath())
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(strings.Trim(strings.TrimSpace(v), `"'`))
		for _, allowed := range embedEnvKeys {
			if k == allowed && v != "" {
				out[k] = v
			}
		}
	}
	return out
}

// embedSetting reads one config key from the pre-loaded file map, process
// env winning over the file.
func embedSetting(file map[string]string, key string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return file[key]
}

// NewEmbedder returns the configured OpenAI embedder, or ErrNoEmbedder when
// no key is present anywhere. Model and dims fall back to the Q2 defaults.
// The credential file is read ONCE per call — the api search handler calls
// this per request, and three settings each re-reading a 0600 file from disk
// was pure waste (review finding).
func NewEmbedder() (Embedder, error) {
	file := loadEmbedEnv()
	key := embedSetting(file, "OPENAI_API_KEY")
	if key == "" {
		return nil, ErrNoEmbedder
	}
	model := embedSetting(file, "OPENAI_EMBED_MODEL")
	if model == "" {
		model = defaultEmbedModel
	}
	dims := defaultEmbedDims
	if d := embedSetting(file, "OPENAI_EMBED_DIMS"); d != "" {
		n, err := strconv.Atoi(d)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("OPENAI_EMBED_DIMS %q is not a positive integer", d)
		}
		dims = n
	}
	return &openAIEmbedder{key: key, model: model, dims: dims,
		client: &http.Client{Timeout: 60 * time.Second}}, nil
}

type openAIEmbedder struct {
	key    string
	model  string
	dims   int
	client *http.Client
}

func (o *openAIEmbedder) Dims() int { return o.dims }

// Embed requests vectors for texts, batching internally, and returns them in
// input order, each normalized (the API documents unit-norm output; Normalize
// runs anyway so the on-write invariant every scan relies on is enforced
// here, not assumed of a remote service).
func (o *openAIEmbedder) Embed(texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += embedBatch {
		end := start + embedBatch
		if end > len(texts) {
			end = len(texts)
		}
		vecs, err := o.embedBatch(texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (o *openAIEmbedder) embedBatch(texts []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{
		"model":      o.model,
		"input":      texts,
		"dimensions": o.dims,
	})
	if err != nil {
		return nil, fmt.Errorf("embed marshal: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, embedURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embed request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+o.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		// err carries the URL, never the Authorization header — safe to wrap.
		return nil, fmt.Errorf("embed call: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<24))
	if err != nil {
		return nil, fmt.Errorf("embed read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// The response body of an OpenAI error is a JSON envelope with a
		// message; it does not echo credentials. Truncate hard anyway.
		msg := string(raw)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, fmt.Errorf("embed: %s: %s", resp.Status, msg)
	}
	var parsed struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("embed parse: %w", err)
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("embed: asked for %d vectors, got %d", len(texts), len(parsed.Data))
	}
	out := make([][]float32, len(texts))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("embed: response index %d out of range", d.Index)
		}
		v := make([]float32, len(d.Embedding))
		for i, f := range d.Embedding {
			v[i] = float32(f)
		}
		if len(v) != o.dims {
			return nil, fmt.Errorf("embed: got %d dims, configured %d", len(v), o.dims)
		}
		n, err := Normalize(v)
		if err != nil {
			return nil, fmt.Errorf("embed: vector %d: %w", d.Index, err)
		}
		out[d.Index] = n
	}
	for i, v := range out {
		if v == nil {
			return nil, fmt.Errorf("embed: response missing index %d", i)
		}
	}
	return out, nil
}
