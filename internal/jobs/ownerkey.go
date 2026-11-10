package jobs

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"kraken/internal/util"
)

// ownerkey.go — the owner key: a random secret in ~/.kraken/owner-key that
// proves a request comes from this Mac's own user. The request gate
// (api/gate.go) gives the owner tier only to a local address that carries
// it, because a local address alone also covers every Docker container on
// this Mac (they reach the host's loopback ports) and any web page rebound
// by DNS to 127.0.0.1. kraken serve creates the file; every local caller —
// the CLI, the cell hooks, the mac app, a browser through /api/owner — reads
// it and sends it. It is never logged.

// OwnerKeyHeader carries the key on a request that can set headers.
const OwnerKeyHeader = "X-Kraken-Owner-Key"

// OwnerCookieName is the cookie a browser or the mac app's web view carries
// the key in. /api/owner sets it.
const OwnerCookieName = "kraken_owner"

// ownerKeyFileEnvVar overrides OwnerKeyPath. A parallel serve sets it for
// the cells it spawns (ownerKeyFileForCells); tests may set it too.
const ownerKeyFileEnvVar = "KRAKEN_OWNER_KEY_FILE"

// OwnerKeyPath is ~/.kraken/owner-key, or KRAKEN_OWNER_KEY_FILE when set.
func OwnerKeyPath() string {
	if p := os.Getenv(ownerKeyFileEnvVar); p != "" {
		return p
	}
	return filepath.Join(util.Home(), ".kraken", "owner-key")
}

// ownerKeyFileForCells is the key file a cell must be told about, "" when
// there is nothing to tell. A cell runs under the account's real HOME (tmux
// gives its pane the tmux server's), so it finds the key on its own unless
// this serve keeps it elsewhere — a parallel serve on an isolated HOME, the
// manual-test recipe. Then hookCommandFlags and mergeServeAddrSettings pass
// the path, never the key, the way they pass the serve's address.
func ownerKeyFileForCells() string {
	p := OwnerKeyPath()
	if u, err := user.Current(); err == nil && p == filepath.Join(u.HomeDir, ".kraken", "owner-key") {
		return ""
	}
	return p
}

var (
	ownerKeyMu    sync.Mutex
	ownerKeyFrom  string // the path ownerKeyValue was read from
	ownerKeyValue string
)

// OwnerKey is the key in the file, read once per process and cached; "" when
// the file is missing or empty. A miss is not cached, so a key kraken serve
// writes later is still found, and a different HOME reads its own file.
func OwnerKey() string {
	path := OwnerKeyPath()
	ownerKeyMu.Lock()
	defer ownerKeyMu.Unlock()
	if ownerKeyFrom != path || ownerKeyValue == "" {
		ownerKeyFrom, ownerKeyValue = path, ""
		if b, err := os.ReadFile(path); err == nil {
			ownerKeyValue = strings.TrimSpace(string(b))
		}
	}
	return ownerKeyValue
}

// EnsureOwnerKey returns the key, first creating the file when it is missing:
// 32 random bytes, base64url, mode 0600. kraken serve calls it at start. The
// new file is linked into place, which never replaces a file that appeared
// meanwhile, so two callers racing both end up with the one key on disk. An
// existing file with looser permissions is tightened to 0600.
func EnsureOwnerKey() (string, error) {
	path := OwnerKeyPath()
	if fi, err := os.Stat(path); err == nil && fi.Mode().Perm() != 0o600 {
		if err := os.Chmod(path, 0o600); err != nil {
			return "", err
		}
	}
	if k := OwnerKey(); k != "" {
		return k, nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".owner-key-*") // created 0600
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	_, werr := tmp.WriteString(base64.RawURLEncoding.EncodeToString(raw) + "\n")
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", werr
	}
	if err := os.Link(tmp.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	if k := OwnerKey(); k != "" {
		return k, nil
	}
	return "", fmt.Errorf("%s is empty", path)
}

// OwnerLink is the URL that hands a browser the key: serve's /api/owner,
// which sets the kraken_owner cookie and redirects to the board.
func OwnerLink() (string, error) {
	k := OwnerKey()
	if k == "" {
		return "", fmt.Errorf("no owner key at %s (kraken serve writes it when it starts)", OwnerKeyPath())
	}
	return ServeBaseURL() + "/api/owner?key=" + url.QueryEscape(k), nil
}

// ServeClient is the http.Client every local caller of kraken serve's API
// uses: it adds the owner key header to each request addressed to
// ServeBaseURL's host, and to no other host — a redirect elsewhere goes out
// without it. A timeout of 0 means none.
func ServeClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: ownerKeyTransport{}}
}

type ownerKeyTransport struct{}

func (ownerKeyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if base, err := url.Parse(ServeBaseURL()); err == nil && r.URL.Host == base.Host {
		if k := OwnerKey(); k != "" {
			r = r.Clone(r.Context())
			r.Header.Set(OwnerKeyHeader, k)
		}
	}
	return http.DefaultTransport.RoundTrip(r)
}
