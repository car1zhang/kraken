package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kraken/internal/util"
)

// typesafe.go is the client for TypeSafe's System One API, which the Jev
// dialog answerer asks which option of a pane dialog to pick.
//
// One request carries a free-form `state` (anything JSON) and a map of named
// questions. A `choice` question names its options in `criteria`, each with a
// description, and comes back with the chosen key, a confidence and a
// probability per key. A `noul` question is a yes/no judgement and comes back
// as one number. Errors are HTTP 400/422 with a JSON `detail`.
//
// Credentials follow linear.go's pattern: process env first, then
// ~/.kraken/typesafe.env (parsed, never sourced), because that file is the
// way a setting reaches the launchd-run `kraken serve`. The key's value must
// never reach an error string. It is only ever written into the
// Authorization header, and every piece of response body folded into an
// error is redacted first.

// TypeSafeEnvPath is the credential file, ~/.kraken/typesafe.env.
func TypeSafeEnvPath() string { return filepath.Join(util.Home(), ".kraken", "typesafe.env") }

const (
	typeSafeAPIURL       = "https://api.typesafe.ai"
	typeSafeDefaultModel = "jev-latest"
	// typeSafeTimeout is short because the call runs inside the daemon tick,
	// and a slow API must not hold the tick for long.
	typeSafeTimeout = 8 * time.Second
	// typeSafeMaxResponse caps a response body at 1 MiB; a larger one is an
	// error rather than a truncated decode.
	typeSafeMaxResponse = 1 << 20
	// typeSafeErrRunes bounds the response body quoted in an error.
	typeSafeErrRunes = 200
)

var typeSafeEnvKeys = []string{"TYPESAFE_API_KEY", "TYPESAFE_MODEL"}

// TypeSafeConfig is typesafe.env resolved. An empty APIKey means the feature
// is off.
type TypeSafeConfig struct {
	APIKey string
	Model  string
}

// loadTypeSafeEnv parses TypeSafeEnvPath. A missing, unreadable or malformed
// file is an empty map; only the two TypeSafe keys are kept.
func loadTypeSafeEnv() map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(TypeSafeEnvPath())
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(strings.Trim(strings.TrimSpace(v), `"'`))
		for _, allowed := range typeSafeEnvKeys {
			if k == allowed && v != "" {
				out[k] = v
			}
		}
	}
	return out
}

// LoadTypeSafeConfig resolves TYPESAFE_API_KEY and TYPESAFE_MODEL, process
// environment first, then TypeSafeEnvPath. Model defaults to jev-latest.
// Every file problem degrades silently to the env-only view.
func LoadTypeSafeConfig() TypeSafeConfig {
	file := loadTypeSafeEnv()
	get := func(key string) string {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
		return file[key]
	}
	cfg := TypeSafeConfig{APIKey: get("TYPESAFE_API_KEY"), Model: get("TYPESAFE_MODEL")}
	if cfg.Model == "" {
		cfg.Model = typeSafeDefaultModel
	}
	return cfg
}

// TypeSafeClient calls System One. BaseURL is exported so a test can point it
// at an httptest server; the key stays unexported.
type TypeSafeClient struct {
	BaseURL string

	key    string
	client *http.Client
}

// NewTypeSafeClient returns nil when cfg has no key (the feature is off),
// otherwise a client with an 8 s HTTP timeout (the call runs inside the
// daemon tick, so a slow API must not hold the tick for long).
func NewTypeSafeClient(cfg TypeSafeConfig) *TypeSafeClient {
	if cfg.APIKey == "" {
		return nil
	}
	return &TypeSafeClient{
		BaseURL: typeSafeAPIURL,
		key:     cfg.APIKey,
		client:  &http.Client{Timeout: typeSafeTimeout},
	}
}

// SystemOneQuestion is one named question. Type is "choice" or "noul";
// Criteria maps each choice key to its description.
type SystemOneQuestion struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions,omitempty"`
	Criteria     any    `json:"criteria,omitempty"`
}

// SystemOneRequest is the body POSTed to /v1/systemone.
type SystemOneRequest struct {
	Model     string                       `json:"model"`
	State     any                          `json:"state"`
	Questions map[string]SystemOneQuestion `json:"questions"`
}

// SystemOneAnswer is one question's answer: a noul answer fills Noul, a
// choice answer fills Choice, Confidence and Probabilities.
type SystemOneAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
	Noul          float64            `json:"noul"`
}

// SystemOneUsage is the token count TypeSafe reports for one call.
type SystemOneUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// SystemOneResponse is a 200 response, answers keyed by question name.
type SystemOneResponse struct {
	Model   string                     `json:"model"`
	Answers map[string]SystemOneAnswer `json:"answers"`
	Usage   SystemOneUsage             `json:"usage"`
}

// redact replaces every occurrence of the key with [redacted]. An empty key
// replaces nothing (strings.ReplaceAll with an empty old string would insert
// the replacement between every rune).
func (c *TypeSafeClient) redact(s string) string {
	if c.key == "" {
		return s
	}
	return strings.ReplaceAll(s, c.key, "[redacted]")
}

// SystemOne POSTs req to BaseURL/v1/systemone and decodes a 200 response.
// A non-200 is an error naming the status and the first 200 runes of the
// body, redacted before it is cut so a key straddling the cut cannot leak
// half of itself. Transport errors are wrapped as they come: net/http's
// errors carry the URL, and the key travels only in a header.
func (c *TypeSafeClient) SystemOne(req SystemOneRequest) (SystemOneResponse, error) {
	if c == nil {
		return SystemOneResponse{}, fmt.Errorf("typesafe: not configured (no TYPESAFE_API_KEY)")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return SystemOneResponse{}, fmt.Errorf("typesafe: encoding request: %s", c.redact(err.Error()))
	}
	base := c.BaseURL
	if base == "" {
		base = typeSafeAPIURL
	}
	hreq, err := http.NewRequest(http.MethodPost, strings.TrimRight(base, "/")+"/v1/systemone", bytes.NewReader(body))
	if err != nil {
		return SystemOneResponse{}, fmt.Errorf("typesafe: building request: %w", err)
	}
	hreq.Header.Set("Authorization", "Bearer "+c.key)
	hreq.Header.Set("Content-Type", "application/json")
	hc := c.client
	if hc == nil {
		hc = &http.Client{Timeout: typeSafeTimeout}
	}
	resp, err := hc.Do(hreq)
	if err != nil {
		return SystemOneResponse{}, fmt.Errorf("typesafe: request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, typeSafeMaxResponse+1))
	if err != nil {
		return SystemOneResponse{}, fmt.Errorf("typesafe: reading response: %w", err)
	}
	if len(raw) > typeSafeMaxResponse {
		return SystemOneResponse{}, fmt.Errorf("typesafe: HTTP %d: response body over %d bytes", resp.StatusCode, typeSafeMaxResponse)
	}
	if resp.StatusCode != http.StatusOK {
		text := []rune(c.redact(string(raw)))
		if len(text) > typeSafeErrRunes {
			text = text[:typeSafeErrRunes]
		}
		return SystemOneResponse{}, fmt.Errorf("typesafe: HTTP %d: %s", resp.StatusCode, string(text))
	}
	var out SystemOneResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return SystemOneResponse{}, fmt.Errorf("typesafe: decoding response: %s", c.redact(err.Error()))
	}
	return out, nil
}
