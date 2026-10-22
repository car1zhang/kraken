package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/util"
)

// fromCellHeader names the cell a request came from. Inside a cell every
// claude zeus (and every teammate it spawns, by inheritance) runs with
// KRAKEN_JOB_SLUG exported (internal/jobs/cell.go's jobSlugEnvPrefix); the
// verbs whose request MEANS something different when a cell makes it — the
// cross-node steer (cmd_nodes.go), and `kraken job draft` / `kraken cell
// submit`, where kraken serve records the sending cell as the new job's
// parent (internal/api/spawn.go) — send it as this header. Outside a cell the
// variable is absent and the request carries nothing.
const fromCellHeader = "X-Kraken-From-Cell"

// fromCellHeaders is the header set those verbs pass to qfPostWith: the one
// header when KRAKEN_JOB_SLUG is set (trimmed), nil otherwise — qfPostWith
// ranges over nil fine, so a caller never has to branch.
func fromCellHeaders() map[string]string {
	if cell := strings.TrimSpace(os.Getenv("KRAKEN_JOB_SLUG")); cell != "" {
		return map[string]string{fromCellHeader: cell}
	}
	return nil
}

// qfclient.go — shared HTTP plumbing for `kraken questions` and `kraken
// feedback` (cmd_questions.go, cmd_feedback.go): thin proxies to kraken
// serve's /api/questions and /api/feedback (internal/api/questions.go,
// internal/api/feedback.go), the same "cell subcommand POSTs to
// kraken serve over localhost" pattern `kraken pr-hook` already established
// (jobs.ServeBaseURL, KRAKEN_SERVE_ADDR — internal/jobs/prhook.go). These
// verbs exist so zeus (internal/jobs/crew.go's zeusBrief) never hand-crafts
// curl or fights shell-quoting on question text full of unicode/quotes. They
// NEVER open kraken.db directly: only kraken serve writes it (single-writer
// rule), so every op here is exactly one HTTP round trip.

// qfHTTPTimeout bounds every questions/feedback proxy call: these run
// synchronously inside a zeus tool turn, so a dead kraken serve must fail
// fast rather than hang the agent waiting on a dead connection — same
// reasoning as prhook.go's prAttachHTTPTimeout, just a little more generous
// since a bulk questions "set" body can be larger than a pr-attach ping.
const qfHTTPTimeout = 5 * time.Second

// qfMaxBodyBytes mirrors the server's http.MaxBytesReader cap for
// /api/questions and /api/feedback (internal/api/questions.go:89,
// internal/api/feedback.go:94, both 1<<20). Checking it here, before the
// request ever leaves this process, means an oversized body fails with a
// message naming the real cause and the remedy — the server can only say
// "bad json" once MaxBytesReader has already truncated the read, which reads
// identically to a malformed body and would send an LLM caller back to
// regenerate the same oversized JSON forever.
const qfMaxBodyBytes = 1 << 20

// qfServerError is a decoded {"error": "..."} body from a non-2xx kraken
// serve response. Kept as its own type (rather than a bare fmt.Errorf) so
// the CLI layer's error message is exactly the server's own sentence — never
// a raw HTTP status/body dump — matching the verbs' behavior contract.
type qfServerError struct {
	status int
	msg    string
}

func (e *qfServerError) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return fmt.Sprintf("kraken serve returned HTTP %d", e.status)
}

// qfDecodeError turns a non-2xx response body into a *qfServerError,
// preferring the server's own {"error": "..."} message and falling back to
// a bare status line when the body doesn't decode that way (e.g. serve is
// up but some other process answered the port).
func qfDecodeError(status int, body []byte) error {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return &qfServerError{status: status, msg: e.Error}
	}
	return &qfServerError{status: status}
}

// qfUnreachableError formats the "serve is down" case: names the exact
// address this process tried (ServeBaseURL, KRAKEN_SERVE_ADDR-aware) so the
// agent reading stderr knows precisely what to go check, per the verbs'
// "never silently succeed, name the address" contract. Reserved for genuine
// connection failures — a timed-out request gets qfTimeoutError instead,
// since the two are not the same failure and don't share a safe remedy.
func qfUnreachableError(err error) error {
	return fmt.Errorf("kraken serve unreachable at %s (is `kraken serve` running?): %w", jobs.ServeBaseURL(), err)
}

// qfTimeoutError formats the "request exceeded qfHTTPTimeout" case,
// distinct from qfUnreachableError: a POST that timed out behind kraken
// serve's single sqlite writer may already have committed by the time the
// client gives up, so treating a timeout as "unreachable" risks two
// failures at once — misreporting a possibly-successful write as a failure,
// and inviting the reader to start a second `kraken serve` (a second DB
// writer) to "fix" it, which is never safe.
func qfTimeoutError(err error) error {
	return fmt.Errorf("kraken serve did not respond within %s at %s — the request may have completed server-side; re-read with `get` before retrying rather than assuming it failed: %w", qfHTTPTimeout, jobs.ServeBaseURL(), err)
}

// qfIsTimeout reports whether err is a client-side deadline expiry
// (http.Client.Timeout firing) rather than a connection failure. errors.As
// unwraps through *url.Error (what http.Client returns) to find the net.Error
// it wraps; net.Error.Timeout() is the correct check here — http.Client.Timeout
// expiry surfaces as *url.Error wrapping context.DeadlineExceeded, and
// *url.Error itself implements net.Error by delegating Timeout() to that
// inner error.
func qfIsTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// qfGet issues a GET against kraken serve's base URL plus path, returning
// the raw response body verbatim on a 2xx (the CLI layer prints it to
// stdout unmodified — no re-encoding, so it stays byte-identical to what
// the server sent; the one exception is `watchlist get`, which re-indents —
// see runWatchlistGet) and an error otherwise: qfServerError for a non-2xx
// response, qfTimeoutError for a client-side deadline expiry, or
// qfUnreachableError for any other transport-level failure.
func qfGet(path string) ([]byte, error) {
	client := http.Client{Timeout: qfHTTPTimeout}
	resp, err := client.Get(jobs.ServeBaseURL() + path)
	if err != nil {
		if qfIsTimeout(err) {
			return nil, qfTimeoutError(err)
		}
		return nil, qfUnreachableError(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, qfDecodeError(resp.StatusCode, body)
	}
	return body, nil
}

// qfPost issues a POST of reqBody (marshaled to JSON) against kraken
// serve's base URL plus path — same success/failure contract as qfGet, plus
// a local qfMaxBodyBytes check before the request is ever sent.
func qfPost(path string, reqBody any) ([]byte, error) {
	return qfPostWith(path, reqBody, nil, qfHTTPTimeout)
}

// qfPostWith is qfPost with extra request headers and its own client timeout
// — for the cross-node proxy, whose request waits on a SECOND hop (this
// serve to a sibling's) that has a timeout of its own, so the CLI must
// outlast it to print the sibling's answer rather than its own timeout.
func qfPostWith(path string, reqBody any, headers map[string]string, timeout time.Duration) ([]byte, error) {
	return qfPostLimit(path, reqBody, headers, timeout, qfMaxBodyBytes)
}

// qfPostLimit is qfPostWith with the route's own body cap, for the one route
// whose server cap is above qfMaxBodyBytes (`kraken mm propose`, 8 MB).
func qfPostLimit(path string, reqBody any, headers map[string]string, timeout time.Duration, limit int) ([]byte, error) {
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	if len(buf) > limit {
		if limit != qfMaxBodyBytes {
			// a route with its own cap (propose) is one call by design, so
			// there is no "split it up" advice to give — only the cap
			return nil, fmt.Errorf("request body is %d bytes, over %s's own %d-byte cap", len(buf), path, limit)
		}
		return nil, fmt.Errorf("request body is %d bytes, over kraken serve's %d-byte limit — split it into smaller ops (e.g. multiple `add` calls instead of one big `set`) rather than resending the same body", len(buf), limit)
	}
	req, err := http.NewRequest(http.MethodPost, jobs.ServeBaseURL()+path, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		if qfIsTimeout(err) {
			return nil, qfTimeoutError(err)
		}
		return nil, qfUnreachableError(err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, qfDecodeError(resp.StatusCode, respBody)
	}
	return respBody, nil
}

// qfStatter is the subset of *os.File's method set stdinIsInteractiveTTY
// needs — an interface rather than a concrete *os.File type check so tests
// can fake a TTY-shaped stdin cheaply, without opening a real terminal.
type qfStatter interface {
	Stat() (os.FileInfo, error)
}

// stdinIsInteractiveTTY reports whether r is a real terminal rather than a
// pipe, redirect, or in-process fake — used by the questions/feedback verbs
// that read a JSON body from stdin to refuse blocking forever when a human
// runs one of them by hand without piping anything in (zeus is unaffected:
// its non-TTY stdin EOFs immediately either way). r that isn't a qfStatter
// at all (a strings.Reader in tests, for instance) is never interactive.
//
// A char-device Mode() bit alone isn't enough: /dev/null (`k questions set
// slug < /dev/null`) is ITSELF a character device, so a Mode()-only check
// misreports a genuinely empty, non-blocking redirect as an interactive
// terminal and refuses it with the wrong message ("refusing to wait on an
// interactive terminal") instead of falling through to the real
// empty-stdin error the read would produce. os.SameFile compares the
// underlying device+inode — the portable stdlib way to tell "this fd IS
// /dev/null" apart from a real pty/tty character device — without a
// non-stdlib terminal-detection dependency.
func stdinIsInteractiveTTY(r io.Reader) bool {
	f, ok := r.(qfStatter)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	if fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if devNull, err := os.Stat(os.DevNull); err == nil && os.SameFile(fi, devNull) {
		return false
	}
	return true
}

// writeStdout prints a server response byte-for-byte. Kept as one helper so
// no proxy verb silently re-encodes the server's JSON on the way through — a
// re-marshal would reorder keys and drop the server's own formatting for no
// gain.
func writeStdout(out []byte) {
	if _, err := os.Stdout.Write(out); err != nil {
		util.Die("write stdout: %v", err)
	}
}
