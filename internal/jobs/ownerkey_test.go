package jobs

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// ownerkey_test.go — the owner key file (EnsureOwnerKey/OwnerKey) and the
// client every local caller of the serve API uses (ServeClient).

// ownerKeyHome points HOME at a fresh temp dir, so no test reads or writes
// the real ~/.kraken/owner-key.
func ownerKeyHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestEnsureOwnerKeyCreatesAPrivateRandomKey(t *testing.T) {
	home := ownerKeyHome(t)
	key, err := EnsureOwnerKey()
	if err != nil {
		t.Fatalf("EnsureOwnerKey: %v", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || len(raw) != 32 {
		t.Fatalf("key %q: want 32 bytes, base64url without padding (decoded %d bytes, err %v)", key, len(raw), err)
	}
	path := filepath.Join(home, ".kraken", "owner-key")
	if OwnerKeyPath() != path {
		t.Errorf("OwnerKeyPath = %q, want %q", OwnerKeyPath(), path)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %o, want 600", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(path)
	if strings.TrimSpace(string(b)) != key {
		t.Errorf("file holds %q, EnsureOwnerKey returned %q", b, key)
	}
	// no temp file left beside it
	entries, _ := os.ReadDir(filepath.Join(home, ".kraken"))
	if len(entries) != 1 {
		t.Errorf(".kraken holds %d entries, want only owner-key", len(entries))
	}
}

func TestEnsureOwnerKeyKeepsAnExistingKey(t *testing.T) {
	home := ownerKeyHome(t)
	dir := filepath.Join(home, ".kraken")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// a hand-written key with loose permissions: kept, and tightened
	if err := os.WriteFile(filepath.Join(dir, "owner-key"), []byte("existing-key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	key, err := EnsureOwnerKey()
	if err != nil || key != "existing-key" {
		t.Fatalf("EnsureOwnerKey = %q, %v; want the existing key", key, err)
	}
	fi, _ := os.Stat(filepath.Join(dir, "owner-key"))
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %o, want tightened to 600", fi.Mode().Perm())
	}
	if again, _ := EnsureOwnerKey(); again != "existing-key" {
		t.Errorf("second EnsureOwnerKey = %q, want the same key", again)
	}
}

func TestOwnerKeyIsEmptyWithoutAFile(t *testing.T) {
	ownerKeyHome(t)
	if k := OwnerKey(); k != "" {
		t.Errorf("OwnerKey with no file = %q, want \"\"", k)
	}
	key, err := EnsureOwnerKey()
	if err != nil {
		t.Fatal(err)
	}
	// a missing file is not cached: the key written later is found
	if k := OwnerKey(); k != key {
		t.Errorf("OwnerKey after EnsureOwnerKey = %q, want %q", k, key)
	}
}

// ServeClient carries the key to kraken serve's own address and to no other
// host, including one a redirect points at.
func TestServeClientSendsTheKeyOnlyToServe(t *testing.T) {
	ownerKeyHome(t)
	key, err := EnsureOwnerKey()
	if err != nil {
		t.Fatal(err)
	}
	var otherGot string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherGot = r.Header.Get(OwnerKeyHeader)
	}))
	defer other.Close()
	var serveGot string
	serve := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveGot = r.Header.Get(OwnerKeyHeader)
		if r.URL.Path == "/bounce" {
			http.Redirect(w, r, other.URL+"/landed", http.StatusFound)
		}
	}))
	defer serve.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", strings.TrimPrefix(serve.URL, "http://"))

	c := ServeClient(0)
	resp, err := c.Get(ServeBaseURL() + "/api/board")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if serveGot != key {
		t.Errorf("serve saw key %q, want %q", serveGot, key)
	}
	resp, err = c.Post(ServeBaseURL()+"/bounce", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if otherGot != "" {
		t.Errorf("another host (reached by redirect) saw the key %q", otherGot)
	}
	resp, err = c.Get(other.URL + "/direct")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if otherGot != "" {
		t.Errorf("another host saw the key %q", otherGot)
	}
}

// OwnerLink is the URL `k board open` hands the browser: serve's /api/owner
// with the key as its query.
func TestOwnerLink(t *testing.T) {
	ownerKeyHome(t)
	t.Setenv("KRAKEN_SERVE_ADDR", "127.0.0.1:7878")
	if _, err := OwnerLink(); err == nil {
		t.Error("OwnerLink with no key file: want an error naming the file")
	}
	key, err := EnsureOwnerKey()
	if err != nil {
		t.Fatal(err)
	}
	link, err := OwnerLink()
	if err != nil || link != "http://127.0.0.1:7878/api/owner?key="+key {
		t.Errorf("OwnerLink = %q, %v", link, err)
	}
}

func TestOwnerKeyPathHonoursTheFileOverride(t *testing.T) {
	t.Setenv(ownerKeyFileEnvVar, "/elsewhere/owner-key")
	if got := OwnerKeyPath(); got != "/elsewhere/owner-key" {
		t.Errorf("OwnerKeyPath = %q, want the %s override", got, ownerKeyFileEnvVar)
	}
}

// A parallel serve on an isolated HOME (the manual-test recipe) writes its key
// under that HOME, but the cells it spawns run under the account's real HOME
// (tmux). Its hooks and agent verbs are told where the key is — the path,
// never the key — the way they are told the serve's address. A serve under
// the account's own home adds nothing, so a normal cell's hook commands and
// settings stay byte-identical.
func TestOwnerKeyFileReachesCellsOfAParallelServe(t *testing.T) {
	SetHookServeAddr("")
	t.Cleanup(func() { SetHookServeAddr("") })
	t.Setenv("KRAKEN_DB_PATH", "")
	t.Setenv(ownerKeyFileEnvVar, "")
	home := ownerKeyHome(t)
	want := filepath.Join(home, ".kraken", "owner-key")

	if got := hookCommandFlags(); got != " --owner-key-file "+want {
		t.Errorf("isolated HOME: hookCommandFlags = %q, want the key file flag", got)
	}
	parsed := map[string]any{}
	mergeServeAddrSettings(parsed)
	if got := parsed["env"].(map[string]any)[ownerKeyFileEnvVar]; got != want {
		t.Errorf("isolated HOME: settings env %s = %v, want %s", ownerKeyFileEnvVar, got, want)
	}

	u, err := user.Current()
	if err != nil {
		t.Skip("no user database entry:", err)
	}
	t.Setenv("HOME", u.HomeDir)
	if got := hookCommandFlags(); got != "" {
		t.Errorf("account's own home: hookCommandFlags = %q, want empty", got)
	}
	parsed = map[string]any{"env": map[string]any{ownerKeyFileEnvVar: "/stale/owner-key"}}
	mergeServeAddrSettings(parsed)
	if _, set := parsed["env"].(map[string]any)[ownerKeyFileEnvVar]; set {
		t.Errorf("account's own home: a stale %s was kept", ownerKeyFileEnvVar)
	}
}
