package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// questions.go — GET/POST /api/questions (54-feedback-contract.md): zeus'
// read/write surface for its own open doc questions, backed entirely by the
// relational `questions` table (store/questions.go).

// questionWire is one question as zeus posts/receives it — the zeus-owned
// columns only (answer/answered_at are kraken-owned and never accepted from
// this side; see store.SetQuestions/UpsertQuestion's "preserve" contract).
// Position only matters for the per-question add/update ops (see
// questionsPost): omitted/zero on "add" means "append to the end"; omitted/
// zero on "update" preserves the row's existing position. A bulk-set
// request (Questions non-nil) ignores this field entirely — B8: position is
// stamped from each entry's array index instead, since webDocQuestion
// deliberately omits position from the wire ("ordering is expressed purely
// by array order"), so GET never returns it and zeus never sends it —
// meaning every bulk-set would otherwise arrive with Position 0 across the
// board.
type questionWire struct {
	ID             string `json:"id"`
	Text           string `json:"text"`
	Context        string `json:"context,omitempty"`
	Importance     string `json:"importance"`
	Recommendation string `json:"recommendation,omitempty"`
	Position       int64  `json:"position,omitempty"`
}

// questionsReq covers every POST /api/questions form: a bulk set (non-nil
// Questions, possibly empty — see questionsPost's rejection of an empty
// array) or a per-question op (Op + Question, or Op "remove" + QID).
type questionsReq struct {
	Slug      string         `json:"slug"`
	Questions []questionWire `json:"questions,omitempty"`
	Op        string         `json:"op,omitempty"` // "add" | "update" | "remove"
	Question  *questionWire  `json:"question,omitempty"`
	QID       string         `json:"qid,omitempty"`
}

// questionsHandler serves GET (board/zeus read) and POST (zeus' bulk-set
// and per-question writes) /api/questions. POST is mirror-gated for the
// same reason feedback's is: a mirror board is read-only, full stop. The
// gate uses the same plain-text http.Error(...) shape every webAction gate
// uses, not a JSON body (mirror_test.go asserts the exact body across every
// gated route, this one included).
func questionsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		questionsGet(w, r)
	case http.MethodPost:
		if mirrorModeOn() {
			http.Error(w, "disabled in mirror mode", http.StatusForbidden)
			return
		}
		questionsPost(w, r)
	default:
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST only"})
	}
}

// questionsGet serves the same wire shape as the board's per-job
// doc_questions array — one uniform read path for zeus and the board alike.
// Always serves a JSON array, never null (B3): a job with no questions yet
// gets "[]".
func questionsGet(w http.ResponseWriter, r *http.Request) {
	slug := r.URL.Query().Get("slug")
	if slug == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing slug"})
		return
	}
	j, _, err := webJobFor(slug)
	if err != nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "no store wired"})
		return
	}
	qs := jobQuestions(st, j)
	if qs == nil {
		qs = []webDocQuestion{}
	}
	webJSON(w, http.StatusOK, map[string]any{"questions": qs})
}

// questionsPost dispatches a bulk-set or a per-question add/update/remove.
// Store validation errors (bad qid shape, bad importance, a duplicate qid
// in one bulk set) surface as 400 with the store's own message text — the
// cell verbs surface it on stderr with a non-zero exit. An oversize body is
// refused with 413, distinct from an ordinary JSON-decode error (400).
func questionsPost(w http.ResponseWriter, r *http.Request) {
	var req questionsReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	j, _, err := webJobFor(req.Slug)
	if err != nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "no store wired"})
		return
	}
	action := "questions-" + req.Op
	switch {
	case req.Op == "remove":
		qid := strings.TrimSpace(req.QID)
		if qid == "" {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing qid"})
			return
		}
		if err := st.DeleteQuestion(j.ID, qid); err != nil {
			util.LogEvent("web", "error", action, action+" "+req.Slug+": "+err.Error())
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	case req.Op == "add" || req.Op == "update":
		if req.Question == nil {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing question"})
			return
		}
		q := *req.Question
		existing, err := st.ListQuestions(j.ID)
		if err != nil {
			util.LogEvent("web", "error", action, action+" "+req.Slug+": "+err.Error())
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		var found bool
		var existingPosition, maxPos int64
		for _, e := range existing {
			if e.Position > maxPos {
				maxPos = e.Position
			}
			if e.QID == q.ID {
				found, existingPosition = true, e.Position
			}
		}
		if req.Op == "add" {
			if found {
				// non-blocking#5: "add" is an upsert underneath (UpsertQuestion),
				// which would otherwise silently overwrite an existing question —
				// loud 409 instead, matching "update" as the explicit edit verb.
				err := fmt.Sprintf("question %q already exists — use op \"update\" to edit it", q.ID)
				util.LogEvent("web", "error", action, action+" "+req.Slug+": "+err)
				webJSON(w, http.StatusConflict, map[string]string{"error": err})
				return
			}
			if q.Position == 0 {
				q.Position = maxPos + 1
			}
		} else if !found {
			// non-blocking#2: "update" is an upsert underneath too
			// (UpsertQuestion) — without this check an unknown qid silently
			// INSERTs a new row at Position 0 (found is false, so the B4
			// preserve-position branch below never fires), jumping a phantom
			// question to the top of the list instead of refusing the typo'd
			// edit. 404, matching "add" as the explicit create verb and
			// mirroring "add"'s own 409 on the opposite mismatch.
			err := fmt.Sprintf("question %q not found — use op \"add\" to create it", q.ID)
			util.LogEvent("web", "error", action, action+" "+req.Slug+": "+err)
			webJSON(w, http.StatusNotFound, map[string]string{"error": err})
			return
		} else if q.Position == 0 {
			// B4: "update" must never reassign position — store.SetQuestions/
			// UpsertQuestion write position unconditionally, so an update that
			// left Position at its zero value (the caller only meant to change
			// text/importance/etc) would otherwise jump the question to the top.
			q.Position = existingPosition
		}
		if err := st.UpsertQuestion(j.ID, store.DocQuestion{
			QID: q.ID, Text: q.Text, Context: q.Context, Importance: q.Importance,
			Recommendation: q.Recommendation, Position: q.Position,
		}); err != nil {
			util.LogEvent("web", "error", action, action+" "+req.Slug+": "+err.Error())
			webJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	case req.Questions != nil:
		action = "questions-set"
		if len(req.Questions) == 0 {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "bulk-set requires a non-empty questions array — use op \"remove\" to clear questions explicitly, one at a time"})
			return
		}
		rows := make([]store.DocQuestion, len(req.Questions))
		for i, q := range req.Questions {
			rows[i] = store.DocQuestion{
				// B8: position is assigned from the array index, NEVER copied
				// from q.Position. webDocQuestion (board.go) deliberately omits
				// position from the wire — "ordering is expressed purely by
				// array order" — so GET never returns it and zeus never sends
				// it; every bulk-set would otherwise arrive with Position 0 on
				// every row, and ListQuestions' `ORDER BY position, qid` would
				// silently fall back to a text sort (Q1 Q10 Q11 Q2 Q3 ... Q9)
				// the instant a job crosses 9 questions. Array order IS the
				// order, so it's what gets stamped, 1-based to match "add"'s
				// own maxPos+1 convention.
				QID: q.ID, Text: q.Text, Context: q.Context, Importance: q.Importance,
				Recommendation: q.Recommendation, Position: int64(i + 1),
			}
		}
		if err := st.SetQuestions(j.ID, rows); err != nil {
			util.LogEvent("web", "error", action, action+" "+req.Slug+": "+err.Error())
			webJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	default:
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "unrecognized questions request — need a non-empty questions array, or op add/update/remove"})
		return
	}
	util.LogEvent("web", "info", action, action+" "+req.Slug)
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
