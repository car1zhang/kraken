package main

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// kronosremoval_contract_test.go — athena-logic's BLIND contract tests for
// the kronos removal's CLI half (artifacts/50-contract.md slice G, rows
// `cmd/kraken` dispatch, `logCellAction`, `usage()`, and "repo text").
// Written from the contract text alone; main.go, _k and every other non-test
// file are only ever read BY THE TESTS at run time, never by the author.
// Dimension: LOGIC — the general agent verbs survive, the kronos verbs are
// gone, cell actions log under src "cell", and no kronos text is left.

func contractKronosRemovalReadFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// Contract: main.go contains `case "board":`, `case "cell":`, `case "job":`,
// `case "slack":`, `case "limit-hook":`, `case "ctx-hook":` (Q2 keeps board
// and cell; ctx-hook survives as every cell's statusline hook, per the
// contract amendment at the merge with main) and does NOT contain
// `case "orch":`, `case "rearm-hook":`. The ctx-hook half was rewritten by
// the lead after the authors were stopped — a test-bug verdict against the
// amended contract, recorded in the build report.
func TestKronosRemovalContractDispatchCases(t *testing.T) {
	src := contractKronosRemovalReadFile(t, "main.go")
	for _, want := range []string{`case "board":`, `case "cell":`, `case "job":`, `case "slack":`, `case "limit-hook":`, `case "ctx-hook":`} {
		if !strings.Contains(src, want) {
			t.Errorf("main.go's dispatch lost %s — that verb must survive the removal", want)
		}
	}
	for _, gone := range []string{`case "orch":`, `case "rearm-hook":`} {
		if strings.Contains(src, gone) {
			t.Errorf("main.go still dispatches %s", gone)
		}
	}
}

// Contract: usage()/help text in main.go contains no `orch`, `rearm-hook`,
// `kronos` (ctx-hook survives per the amendment above). Asserted over the
// whole of main.go (the usage text lives there; nothing else in main.go has a
// reason to say these words). `orch` is matched as a whole word so an
// unrelated identifier containing the letters cannot trip it; `kronos`
// case-insensitively.
func TestKronosRemovalContractUsageTextClean(t *testing.T) {
	src := contractKronosRemovalReadFile(t, "main.go")
	if regexp.MustCompile(`\borch\b`).MatchString(src) {
		t.Error("main.go still names `orch`")
	}
	for _, gone := range []string{"rearm-hook"} {
		if strings.Contains(src, gone) {
			t.Errorf("main.go still names %q", gone)
		}
	}
	if strings.Contains(strings.ToLower(src), "kronos") {
		t.Error("main.go still mentions kronos")
	}
}

// Contract: `_k` names board, cell, slack, job and does not contain the
// strings orch, kronos, ctx-hook, rearm-hook.
func TestKronosRemovalContractCompletionFile(t *testing.T) {
	comp := contractKronosRemovalReadFile(t, "_k")
	for _, want := range []string{"board", "cell", "slack", "job"} {
		if !strings.Contains(comp, want) {
			t.Errorf("_k no longer names %q", want)
		}
	}
	for _, gone := range []string{"orch", "kronos", "ctx-hook", "rearm-hook"} {
		if strings.Contains(comp, gone) {
			t.Errorf("_k still contains %q", gone)
		}
	}
}

// Contract: the body logCellAction POSTs to /api/logevent carries
// "src":"cell" (was kronos), "event":<event>, "msg":<slug>.
func TestKronosRemovalContractLogCellActionSrcIsCell(t *testing.T) {
	type contractKronosRemovalReq struct {
		path string
		body map[string]any
		err  error
	}
	got := make(chan contractKronosRemovalReq, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		rec := contractKronosRemovalReq{path: r.URL.Path, err: err}
		if err == nil {
			var m map[string]any
			rec.err = json.Unmarshal(raw, &m)
			rec.body = m
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
		select {
		case got <- rec:
		default:
		}
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	logCellAction("cell-steer", "demo-job")

	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("logevent body is not a JSON object: %v", r.err)
		}
		if r.path != "/api/logevent" {
			t.Errorf("POST path = %q, want /api/logevent", r.path)
		}
		if r.body["src"] != "cell" {
			t.Errorf(`src = %#v, want "cell"`, r.body["src"])
		}
		if r.body["event"] != "cell-steer" {
			t.Errorf(`event = %#v, want "cell-steer"`, r.body["event"])
		}
		if r.body["msg"] != "demo-job" {
			t.Errorf(`msg = %#v, want "demo-job"`, r.body["msg"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("logCellAction posted nothing to KRAKEN_SERVE_ADDR within 5s")
	}
}

// Contract ("repo text", amended): `rg -i kronos` over the NON-TEST .go files
// in internal/, cmd/ and skills_embed.go, plus cmd/kraken/_k, returns ONLY
// lines inside internal/store/schema.go's v31 migration step — the five kv
// key literals and the comment naming them as retired — and nothing else.
// So a kronos line in internal/store/schema.go is tolerated only when it
// carries one of the five key literals or the word "retired"; every other
// kronos line anywhere fails.
func TestKronosRemovalContractNoKronosTextInSource(t *testing.T) {
	root := filepath.Join("..", "..")
	schemaGo := filepath.Join(root, "internal", "store", "schema.go")
	allowed := []string{"kronos_agent", "kronos_ctx", "kronos_ctx_warned", "limit_hit:kronos", "fable_fallback:kronos", "retired"}

	var hits []string
	scan := func(p string) {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			lower := strings.ToLower(line)
			if !strings.Contains(lower, "kronos") {
				continue
			}
			if filepath.Clean(p) == filepath.Clean(schemaGo) {
				ok := false
				for _, a := range allowed {
					if strings.Contains(lower, a) {
						ok = true
						break
					}
				}
				if ok {
					continue
				}
			}
			hits = append(hits, p+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
		}
	}

	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			scan(p)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	scan(filepath.Join(root, "skills_embed.go"))
	scan(filepath.Join(root, "cmd", "kraken", "_k"))

	if len(hits) > 0 {
		t.Fatalf("kronos still mentioned outside the v31 migration's retired keys (%d line(s)):\n%s", len(hits), strings.Join(hits, "\n"))
	}
}
