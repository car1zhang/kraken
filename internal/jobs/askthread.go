package jobs

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"kraken/internal/core"
	"kraken/internal/store"
	"kraken/internal/util"
)

// askthread.go — ask threads: a side thread with no pane. Each question runs
// as ONE headless `claude -p` process started by kraken serve, which forks
// zeus' session on the thread's first question and resumes the thread's own
// session on every later one. The question goes in on stdin; the answer lands
// in the thread's transcript, which GET /api/thread/read already serves.
//
// The tool set is keyed on who opened the thread AND who sent the question.
// A question this node sends into a thread this node opened gets the
// read-only list in AskThreadTools; every other question gets no tools at
// all. The cwd follows the opener alone: the cell's worktree for a thread
// opened here, the cell dir for one opened from another node. No run
// bypasses permissions: every one runs `--permission-mode default
// --permission-prompts none`.
//
// The row (store.Thread, Kind "headless") carries an answering flag that
// store.ClaimThreadAnswering compare-and-sets before any process starts, so
// one thread never has two runs on its session id at once. A run that fails
// leaves its reason in the row's note.
//
// The pane thread (BranchThread, thread.go) is untouched by this file: the
// owner can still open one, and every pane-keyed exclusion keeps working for
// it.
//
// Two columns describe a thread row and they answer different questions.
// Role ("ask" or "mind", older than this file) is what the thread is FOR:
// carl asking about the work, or the job's mind thread converging on a
// mental-model item. Kind ("pane" or "headless", schema v36) is how it RUNS:
// an interactive claude in a tmux window, or one `claude -p` per question.
// The feature is called "ask threads", but its stored kind is "headless"
// because the value "ask" was already taken by Role on the wire. Every row
// this file creates is Kind "headless" with Role "ask"; a headless mind
// thread is refused by the store (ErrThreadKind).

// ThreadOrigin is who opened, or is sending into, a thread: the node's
// tailscale stable id and label, and the login of the person on it.
type ThreadOrigin struct {
	Node, Label, Login string
}

// selfNodeTTL bounds how long SelfNodeID trusts one tailscale read. Every
// ask, send and revive asks it, countRemoteAskThreads once per thread row,
// and each read is a `tailscale status` exec. (The board's own node_id comes
// from api's cachedNodeSelf, a separate 30 s cache over the same status.)
const selfNodeTTL = 30 * time.Second

var selfNodeCache struct {
	sync.Mutex
	id string
	at time.Time
}

// SelfNodeID is this node's tailscale stable id, "" when tailscale cannot be
// read or the fleet is off (KRAKEN_NODES=off). It is the ID NodeSelf reports
// (both read tailscaleStatus' Self.ID); it reads only that status, not the
// label and operator NodeSelf also gathers. The fleet-off "" matches the
// board, which states no node_id then either, so a fleet-off node's own rows
// never read as another node's.
func SelfNodeID() string {
	if !FleetEnabled() {
		return ""
	}
	selfNodeCache.Lock()
	defer selfNodeCache.Unlock()
	if !selfNodeCache.at.IsZero() && time.Since(selfNodeCache.at) < selfNodeTTL {
		return selfNodeCache.id
	}
	id := ""
	if st, err := tailscaleStatus(); err == nil {
		id = st.Self.ID
	}
	selfNodeCache.id, selfNodeCache.at = id, time.Now()
	return id
}

// SelfThreadOrigin is the origin this node's own operator asks under.
func SelfThreadOrigin() ThreadOrigin {
	return ThreadOrigin{Node: SelfNodeID(), Label: DeviceLabel(), Login: Operator()}
}

// IsSelf reports whether the origin is this node. A row opened on a node
// with no tailscale or the fleet off stores an empty node id, which matches
// this node's empty id for as long as it still has no tailscale or the fleet
// off.
func (o ThreadOrigin) IsSelf() bool { return o.Node == SelfNodeID() }

func rowOrigin(t store.Thread) ThreadOrigin {
	return ThreadOrigin{Node: t.OriginNode, Label: t.OriginLabel, Login: t.OriginLogin}
}

// AskThreadTools is the tool list a question runs with when both its thread
// and its sender are this node (askToolsSelf): reads and web lookups only —
// no shell, no edits, no agents.
const AskThreadTools = "Read,Grep,Glob,WebFetch,WebSearch"

// AskThreadTimeout bounds one question's run; the process is killed at the
// deadline. A var so a test can shorten it.
var AskThreadTimeout = 5 * time.Minute

// MaxAskThreads caps a job's live ask rows opened by OTHER nodes. Every
// question bills the cell's own account, so the cap bounds what the fleet
// can spend; the owner's own threads are not counted, so another node can
// never block the owner's branches. A var so a test can lower it.
var MaxAskThreads = 5

// ErrAskThreadCap refuses an ask past MaxAskThreads.
var ErrAskThreadCap = errors.New("ask thread cap reached — close one first")

// askTitleMax and askQuestionMax bound the asker-controlled input. The title
// rides argv inside the brief (visible in `ps`, and re-sent as system prompt
// on every question); the question rides stdin.
const (
	askTitleMax    = 120      // runes, after trimming
	askQuestionMax = 16 << 10 // bytes
)

// askCheckTitle and askCheckQuestion are the two bound refusals; the routes
// map them to 400 (IsAskInputError).
func askCheckTitle(j *Job, title string) error {
	if utf8.RuneCountInString(title) > askTitleMax {
		return &askInputError{fmt.Sprintf("%s: ask title is too long (max 120 characters)", j.Slug)}
	}
	return nil
}

func askCheckQuestion(j *Job, q string) error {
	if len(q) > askQuestionMax {
		return &askInputError{fmt.Sprintf("%s: ask question is too long (max 16 KB)", j.Slug)}
	}
	return nil
}

// askInputError marks a refusal about the request's own content, which the
// api answers 400 rather than the 409 its live-state refusals get.
type askInputError struct{ msg string }

func (e *askInputError) Error() string { return e.msg }

// IsAskInputError reports whether err is an ask title or question bound
// refusal.
func IsAskInputError(err error) bool {
	var ie *askInputError
	return errors.As(err, &ie)
}

// countRemoteAskThreads counts j's live ask rows whose origin is not this
// node — what MaxAskThreads caps.
func countRemoteAskThreads(st *store.Store, j *Job) (int, error) {
	rows, err := st.ListThreads(j.ID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range rows {
		if t.Kind == "headless" && t.State == "live" && !rowOrigin(t).IsSelf() {
			n++
		}
	}
	return n, nil
}

// askOneLine makes an asker-controlled identity value safe to set inside the
// brief: control and Unicode format (Cf) characters dropped, newlines (and
// any run of whitespace) collapsed to single spaces.
func askOneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			// format characters too (bidi overrides, zero-width, BOM): they
			// can make a label read as something other than its bytes
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// askStderrMax caps how much of a run's stderr is kept for its note.
const askStderrMax = 4 << 10

// askNoteMax caps the stderr line a failed run's note quotes, in runes.
const askNoteMax = 200

// askCwd resolves the directory an ask run starts in: the worktree for a
// self origin (the read-only tools read it; `--restricted` is passed to keep
// them inside it), the cell dir for a remote one (no tools, and no repo files or
// project settings in reach).
func askCwd(j *Job, self bool) (string, error) {
	if self {
		wt := threadWorktree(j)
		if wt == "" {
			return "", fmt.Errorf("%s has no worktree to ask into — reactivate the job first", j.Slug)
		}
		return wt, nil
	}
	// existingCellDir always yields a path (the current name when no
	// candidate exists), so a remote origin has no refusal here
	return existingCellDir(j), nil
}

// AskArgv builds one ask run's command, working directory and environment.
// resume false forks zeus' session (t.ParentSession) under the thread's own
// minted id; resume true continues the thread's session.
//
// The read-only tools need BOTH the row and the sender (from) to be this
// node: a question another node sends into a thread the owner opened runs
// with no tools, or it could read the worktree and quote any file there. The
// cwd stays keyed on the row alone, so a resume starts where the session was
// written; with no tools, the worktree as cwd gives the sender nothing.
//
// --system-prompt-snapshot off rides beside --append-system-prompt on every
// run, fork and resume alike; it is carried over unchanged from kraken-core's
// ask threads, which pass it so askThreadBrief is the brief a forked or
// resumed run holds.
//
// The account binding is an exec env entry here, not the quoted shell prefix
// accountEnvPrefix renders, because nothing here goes through a shell.
func AskArgv(j *Job, t store.Thread, from ThreadOrigin, resume bool) (argv []string, cwd string, env []string, err error) {
	resolved, err := threadModel(j, t.Model)
	if err != nil {
		return nil, "", nil, err
	}
	cwd, err = askCwd(j, rowOrigin(t).IsSelf())
	if err != nil {
		return nil, "", nil, err
	}
	tools := ""
	if askToolsSelf(t, from) {
		tools = AskThreadTools
	}
	argv = []string{core.ClaudeLauncher(), "-p",
		"--strict-mcp-config", "--restricted", "--disable-slash-commands",
		"--permission-mode", "default", "--permission-prompts", "none",
		"--system-prompt-snapshot", "off",
		"--tools", tools}
	if tools != "" {
		// `--tools` only ROSTERS a tool; under `--permission-mode default`
		// with `--permission-prompts none` any tool that needs approval is
		// then denied automatically, and the web pair does — so the owner's
		// thread advertised WebFetch and WebSearch and had every call refused
		// ("this session has no approval surface"). `--allowedTools` is the
		// pre-grant, over exactly the same list, so the read-only set stays
		// the one constant and a thread with no tools gets no grant either.
		argv = append(argv, "--allowedTools", tools)
	}
	argv = append(argv, core.ModelArgs(resolved)...)
	argv = append(argv, "--name", ThreadAgentName(j, t.TID), "--append-system-prompt", askThreadBrief(j, t))
	if resume {
		argv = append(argv, "-r", t.SessionID)
	} else {
		argv = append(argv, "--fork-session", "-r", t.ParentSession, "--session-id", t.SessionID)
	}
	env = cellSpawnEnv()
	if env == nil {
		env = os.Environ()
	}
	if j.AccountSlug != "" && j.Agent != "codex" {
		env = append(env, "CLAUDE_SECURESTORAGE_CONFIG_DIR="+slotDir(j.AccountSlug))
	}
	return argv, cwd, env, nil
}

// askToolsSelf is the one test for the read-only tool set and the self
// guard: the row's origin AND the question's sender are both this node.
func askToolsSelf(t store.Thread, from ThreadOrigin) bool {
	return rowOrigin(t).IsSelf() && from.IsSelf()
}

// AskStdin is what an ask run reads on stdin: the sender's stamp, the
// question, and the guard. The question never goes on the command line,
// where `ps` would show it to every user of the machine.
//
// Nothing the sender controls can pass for a stamp. The label (a sibling
// announces its own) and the login go through askOneLine and lose `[` and
// `]`, so neither can end the stamp early or start a new line. The
// question's line breaks — `\r\n`, `\r` and `\n` alike — are normalised to
// `\n`, and every line that begins with `[from`, in any case and after any
// leading whitespace, gets a leading `· `. For a non-self question every
// ASCII `@` then becomes `＠` (askFenceMentions), and the stamp's label and
// login get the same rewrite on every run (askStampValue), so neither can
// `@`-mention a file.
func AskStdin(text string, from ThreadOrigin, self bool) string {
	// every line separator a reader might render as a break, not only \r\n
	// and \r, so none can start a line that skips the `[from` fence below
	text = strings.NewReplacer("\r\n", "\n", "\r", "\n",
		string(rune(0x2028)), "\n", string(rune(0x2029)), "\n", string(rune(0x85)), "\n", "\v", "\n", "\f", "\n").Replace(text)
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.ToLower(strings.TrimLeftFunc(l, unicode.IsSpace)), "[from") {
			lines[i] = "· " + l
		}
	}
	question := strings.Join(lines, "\n")
	if !self {
		// after the normalisation and the fence, on the question alone
		question = askFenceMentions(question)
	}
	return RemotePrefix(askStampValue(from.Label), askStampValue(from.Login), "") + " " + question + askGuard(self)
}

// askMentionAt is U+FF20 FULLWIDTH COMMERCIAL AT, what every ASCII `@`
// becomes in a non-self question and in the stamp (askFenceMentions).
const askMentionAt = rune(0xFF20)

// askFenceMentions replaces every ASCII `@` in s with askMentionAt — no
// token-boundary carve-out, so the fence does not depend on how Claude Code
// decides where a mention starts. AskStdin applies it to a non-self
// question (the same condition that drops the tools) and askStampValue to
// every stamp value; the guard is never touched.
//
// Why: measured in this session on Claude Code 2.1.306, a `-p` fork
// launched with AskArgv's argv for a remote-origin row (`--tools ""`,
// `--strict-mcp-config`, `--restricted`, cwd the cell dir) reported
// `tools: []` in its init event and still quoted the first lines of
// `<cell dir>/zeus-brief.md` when the stdin question carried an `@` mention
// of that file's absolute path; an `@/etc/hosts` mention was refused. So a
// mention reaches file contents even with no tools. Unmeasured but the
// reason for fencing every non-self question and every stamp: a stranger's
// question into a thread the owner opened runs with the worktree as its
// cwd, where the same mention would presumably reach worktree files, and a
// sibling's announced label is the stamp's text.
func askFenceMentions(s string) string {
	return strings.ReplaceAll(s, "@", string(askMentionAt))
}

// askStampValue is a sender's label or login made safe to set inside the
// `[from …]` stamp: one line (askOneLine), with no brackets, and with every
// ASCII `@` fenced (askFenceMentions) on every run, self included — a
// sibling announces its own label, and a stamp has no reason to carry an `@`.
func askStampValue(s string) string {
	s = strings.NewReplacer("[", "", "]", "").Replace(s)
	return askFenceMentions(askOneLine(s))
}

// askThreadBrief is an ask thread's system prompt. Like threadBrief it exists
// to overwrite the prior a fork wakes up with — that it is zeus, mid-job —
// and it adds what is different here: the thread answers questions from
// several people, one message per question, with only the tools its origin
// allows. It names no path and no environment value: the remote asker reads
// the transcript this brief sits in. The origin label and login come from
// another node, so they go through askOneLine before they reach the system
// prompt. The tool sentence follows the row's origin; when another node
// sends into a thread this node opened, the run has no tools anyway and the
// stdin guard (askGuard) says so under the question.
func askThreadBrief(j *Job, t store.Thread) string {
	var b strings.Builder
	// "on job", never "on kraken job": the brief names no `kraken <verb>`
	// (`kraken job` is one), since the run has no shell to run any of them
	fmt.Fprintf(&b, "You are ask thread %s (%q) on job %q, opened from %s by %s. You are NOT zeus.\n", t.TID, t.Title, j.Slug, askOneLine(t.OriginLabel), askOneLine(t.OriginLogin))
	b.WriteString("Your conversation history was FORKED from zeus, the agent doing this job's actual work — everything above this point is zeus' work, not yours, and none of it is yours to continue.\n")
	b.WriteString("Each message you receive is one question, prefixed with who asked it. Several people may ask in this thread. Answer each in one message, leading with its TLDR — that message is all the asker reads.\n")
	if rowOrigin(t).IsSelf() {
		b.WriteString("You run headless with read-only tools confined to the worktree (Read, Grep, Glob, WebFetch, WebSearch): no shell, no edits, no agents. Read the code to answer; never claim to have changed anything.\n")
	} else {
		b.WriteString("You run headless and you have no tools: answer from the conversation above alone, and say so plainly when it does not hold the answer. Never claim to have looked anything up or changed anything.\n")
	}
	b.WriteString("Nothing you write reaches zeus. If a good answer needs work done, name the concrete ask and say that it happens only if the job's owner steers zeus with it.\n")
	return b.String()
}

// askGuard follows every question on stdin — the last text before the
// answer, where threadPromptGuard's measurement found a restatement is
// actually obeyed.
func askGuard(self bool) string {
	reach := "you have no tools, so answer from the conversation alone"
	if self {
		reach = "you may read the worktree, but you cannot edit, run commands or spawn agents"
	}
	return "\n\n(Here, answer in one message. This is a question ABOUT the job's work, never a request to do it: " + reach + ". If a good answer needs work done, name the ask and say only the job's owner can carry it to zeus.)"
}

// AskLauncherFunc starts one ask run and returns its wait: askLauncher's
// type, exported so SetAskSeamsForTest can take one from another package.
type AskLauncherFunc func(ctx context.Context, argv []string, dir string, env []string, stdin string) (wait func() (stderr string, err error), err error)

// askLauncher starts one ask run and returns its wait. The seam a test
// swaps to see the argv, dir, env and stdin, and to script the exit, the
// stderr and a hang.
var askLauncher AskLauncherFunc = func(ctx context.Context, argv []string, dir string, env []string, stdin string) (wait func() (stderr string, err error), err error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	// nil Stdout is /dev/null: the answer is read from the transcript
	errBuf := &capBuffer{max: askStderrMax}
	cmd.Stderr = errBuf
	// a child that inherited the stderr pipe would otherwise hold Wait open
	// after claude itself was killed at the deadline
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return func() (string, error) {
		werr := cmd.Wait()
		return errBuf.String(), werr
	}, nil
}

// capBuffer keeps the first max bytes written to it and drops the rest
// without failing the write, so a noisy process is never blocked or killed
// by its own stderr.
type capBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (c *capBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := c.max - len(c.buf); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		c.buf = append(c.buf, p[:room]...)
	}
	return len(p), nil
}

func (c *capBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.buf)
}

// AskDoneHook runs once per finished run; the api package points it at the
// board cache so the answering flag clears on the next poll.
var AskDoneHook func()

// askRun is one registered run. token tells this run's entry apart from a
// later run of the same thread: KillAskRun unregisters a run at once, so a
// revive and a new send can register a newer run before the killed process
// exits, and that process' own finish must then find itself superseded and
// leave the row alone.
type askRun struct {
	token     uint64
	cancel    context.CancelFunc
	cancelled bool // set under askRuns' lock by KillAskRun
}

var askRuns = struct {
	sync.Mutex
	m    map[string]*askRun
	next uint64
}{m: map[string]*askRun{}}

// askStartMu orders run starts against ReconcileThreads' stale-flag sweep:
// a start holds it shared from the moment its row says answering until the
// run is registered, and the sweep holds it exclusively, so the sweep never
// reads a just-claimed row as one whose run died with a serve restart.
var askStartMu sync.RWMutex

// askCapMu serialises the cap's recount with the insert it guards, so two
// concurrent asks cannot both pass at MaxAskThreads-1.
var askCapMu sync.Mutex

// KillAskRun kills the registered run of (jobID, tid), if any, and reports
// whether one was registered. The row's outcome is written here, before this
// returns — answering cleared, note "ask cancelled" — and the run is
// unregistered at once, so the dying process' own finish (which may land
// after a revive and a new send have claimed the row) finds itself no longer
// the registered run and writes nothing.
func KillAskRun(jobID int64, tid string) bool {
	askRuns.Lock()
	defer askRuns.Unlock()
	key := threadSpawnKey(jobID, tid)
	r, ok := askRuns.m[key]
	if !ok {
		return false
	}
	r.cancelled = true
	r.cancel()
	if st := ActiveStore(); st != nil {
		if err := st.FinishThreadAnswering(jobID, tid, "ask cancelled"); err != nil {
			util.LogEvent("jobs", "error", "thread-ask-done", fmt.Sprintf("thread %s of job %d: recording the cancel failed: %v", tid, jobID, err))
		}
	}
	delete(askRuns.m, key)
	return true
}

// AskRunning reports whether a run of (jobID, tid) is registered.
func AskRunning(jobID int64, tid string) bool {
	askRuns.Lock()
	defer askRuns.Unlock()
	_, ok := askRuns.m[threadSpawnKey(jobID, tid)]
	return ok
}

// startAskRun starts one run of t and returns once the process has started
// and the run is registered (AskRunning is true by then, and stays true
// until the run's outcome is on the row). The answer is the transcript's
// business; the outcome is written to the row by FinishThreadAnswering when
// the process exits, and only while this run is still the registered one
// (KillAskRun writes a killed run's outcome itself). The caller has already
// set the row's answering flag (AddThreadNext with Answering true, or
// ClaimThreadAnswering); on an error here no process was started and the
// caller still owns clearing it.
func startAskRun(j *Job, t store.Thread, text string, from ThreadOrigin, resume bool) error {
	argv, cwd, env, err := AskArgv(j, t, from, resume)
	if err != nil {
		return err
	}
	stdin := AskStdin(text, from, askToolsSelf(t, from))
	ctx, cancel := context.WithTimeout(context.Background(), AskThreadTimeout)
	key := threadSpawnKey(j.ID, t.TID)
	run := &askRun{cancel: cancel}
	askRuns.Lock()
	askRuns.next++
	run.token = askRuns.next
	askRuns.m[key] = run
	askRuns.Unlock()
	unregister := func() {
		askRuns.Lock()
		if cur, ok := askRuns.m[key]; ok && cur.token == run.token {
			delete(askRuns.m, key)
		}
		askRuns.Unlock()
	}
	wait, err := askLauncher(ctx, argv, cwd, env, stdin)
	if err != nil {
		cancel()
		unregister()
		return fmt.Errorf("%s: start ask thread %s: %v", j.Slug, t.TID, err)
	}
	slug, tid, jobID := j.Slug, t.TID, j.ID
	go func() {
		stderr, werr := wait()
		// the check, the row write and the unregister under one hold of the
		// lock a new run registers under: a run that is no longer the
		// registered one (killed, then superseded) must not touch a row a
		// newer run has claimed
		askRuns.Lock()
		cancelled := run.cancelled
		note := askNote(ctx, cancelled, stderr, werr)
		cur, mine := askRuns.m[key]
		mine = mine && cur.token == run.token
		if mine {
			if st := ActiveStore(); st != nil {
				if err := st.FinishThreadAnswering(jobID, tid, note); err != nil {
					util.LogEvent("jobs", "error", "thread-ask-done", fmt.Sprintf("%s: thread %s: recording the run's end failed: %v", slug, tid, err))
				}
			}
			delete(askRuns.m, key)
		}
		askRuns.Unlock()
		cancel()
		msg := fmt.Sprintf("%s: thread %s answered", slug, tid)
		if note != "" {
			msg = fmt.Sprintf("%s: thread %s: %s", slug, tid, note)
		}
		util.LogEvent("jobs", "info", "thread-ask-done", msg)
		if hook := AskDoneHook; hook != nil {
			hook()
		}
	}()
	return nil
}

// askNote turns a finished run into the row's note: "" whenever the process
// exited 0, whatever the context's state by then (a deadline or a kill that
// lands after a clean exit changes nothing), else why it ended. A kill by
// KillAskRun wins over the deadline, and both win over the exit status they
// caused.
func askNote(ctx context.Context, cancelled bool, stderr string, werr error) string {
	switch {
	case werr == nil:
		return ""
	case cancelled:
		return "ask cancelled"
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "ask timed out after " + AskThreadTimeout.String()
	}
	sc := bufio.NewScanner(strings.NewReader(stderr))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > askNoteMax {
			line = string(r[:askNoteMax])
		}
		return "ask failed: " + line
	}
	return "ask failed: " + werr.Error()
}

// askCellLive and askZeusTarget are the only tmux and discovery contact
// AskThread and AskThreadSend make, as vars so a test can swap both (with
// askLauncher) and drive every path with no tmux.
var (
	askCellLive = func(j *Job) (sess string, ok bool) {
		return FindCellSession(j, LiveSessions())
	}
	askZeusTarget = func(j *Job) (*core.Peer, error) {
		all, _ := core.OrderAll(core.DiscoverAll())
		return zeusForkTarget(j, all)
	}
)

// SetAskSeamsForTest swaps askCellLive, askZeusTarget and askLauncher for a
// test in another package (the api tests), which cannot reach the
// unexported vars. A nil argument leaves that seam as it is; restore puts
// all three back as they were before the call.
func SetAskSeamsForTest(cellLive func(*Job) (string, bool), zeusTarget func(*Job) (*core.Peer, error), launcher AskLauncherFunc) (restore func()) {
	prevLive, prevZeus, prevLaunch := askCellLive, askZeusTarget, askLauncher
	if cellLive != nil {
		askCellLive = cellLive
	}
	if zeusTarget != nil {
		askZeusTarget = zeusTarget
	}
	if launcher != nil {
		askLauncher = launcher
	}
	return func() {
		askCellLive, askZeusTarget, askLauncher = prevLive, prevZeus, prevLaunch
	}
}

// askOriginMax bounds an origin's label and login, in runes.
const askOriginMax = 120

// boundOrigin truncates (never refuses) the origin's Label and Login to
// askOriginMax runes before AskThread stores them on the row or either entry
// point stamps stdin and builds the brief with them. A sibling announces its
// own label, so an unbounded one would ride every board payload and argv
// (the brief carries it) at whatever size it chose. Node is left alone: it
// is compared, never displayed, and comes from the tailnet identity.
func boundOrigin(o ThreadOrigin) ThreadOrigin {
	o.Label = core.TruncateOneLine(o.Label, askOriginMax)
	o.Login = core.TruncateOneLine(o.Login, askOriginMax)
	return o
}

// askLiveCell refuses a job whose cell is not running: an ask thread reads
// the cell's worktree or sits in its cell dir, and bills its account.
func askLiveCell(j *Job) error {
	if _, live := askCellLive(j); !live {
		return fmt.Errorf("%s has no live cell to ask — start or reactivate it first", j.Slug)
	}
	return nil
}

// AskThread opens an ask thread on j for origin and starts its first run,
// returning as soon as the process has started, with the row reading
// Answering true. Every refusal comes before the row is reserved, in the
// order below, and leaves no row and no process. From the reservation on, a
// failure closes the row again, as BranchThread does.
func AskThread(j *Job, origin ThreadOrigin, title, question, model string) (store.Thread, error) {
	origin = boundOrigin(origin)
	if err := refuseCodexThread(j, "ask"); err != nil {
		return store.Thread{}, err
	}
	st, err := threadStore(j)
	if err != nil {
		return store.Thread{}, err
	}
	if _, err := threadModel(j, model); err != nil {
		return store.Thread{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return store.Thread{}, fmt.Errorf("%s: ask title cannot be empty", j.Slug)
	}
	if err := askCheckTitle(j, title); err != nil {
		return store.Thread{}, err
	}
	if strings.TrimSpace(question) == "" {
		return store.Thread{}, fmt.Errorf("%s: ask question cannot be empty", j.Slug)
	}
	if err := askCheckQuestion(j, question); err != nil {
		return store.Thread{}, err
	}
	if err := askLiveCell(j); err != nil {
		return store.Thread{}, err
	}
	self := origin.IsSelf()
	cwd, err := askCwd(j, self)
	if err != nil {
		return store.Thread{}, err
	}
	// the cap binds other nodes only (MaxAskThreads)
	if !self {
		if n, err := countRemoteAskThreads(st, j); err != nil {
			return store.Thread{}, err
		} else if n >= MaxAskThreads {
			return store.Thread{}, ErrAskThreadCap
		}
	}
	zeus, err := askZeusTarget(j)
	if err != nil {
		return store.Thread{}, err
	}
	sessionID, err := newSessionUUID()
	if err != nil {
		return store.Thread{}, err
	}
	// provenance only, best effort, as in BranchThread
	forkUUID := ""
	if path, perr := core.ReadTranscriptPath(zeus); perr == nil {
		forkUUID = core.LastMessageUUID(path)
	}
	askStartMu.RLock()
	defer askStartMu.RUnlock()
	askCapMu.Lock()
	if !self {
		var n int
		n, err = countRemoteAskThreads(st, j)
		if err == nil && n >= MaxAskThreads {
			err = ErrAskThreadCap
		}
	}
	var t store.Thread
	if err == nil {
		t, err = st.AddThreadNext(store.Thread{
			JobID:         j.ID,
			Kind:          "headless",
			Role:          "ask",
			Title:         title,
			SessionID:     sessionID,
			ParentSession: zeus.SessionID,
			ForkUUID:      forkUUID,
			CWD:           cwd,
			State:         "live",
			Seed:          question,
			Model:         model,
			OriginNode:    origin.Node,
			OriginLabel:   origin.Label,
			OriginLogin:   origin.Login,
			Answering:     true,
		})
	}
	askCapMu.Unlock()
	if err != nil {
		return store.Thread{}, err
	}
	if err := startAskRun(j, t, question, origin, false); err != nil {
		releaseReservedThread(st, j, t.TID)
		return store.Thread{}, err
	}
	util.LogEvent("jobs", "info", "thread-ask", fmt.Sprintf("%s: opened ask thread %s (%s) from %s", j.Slug, t.TID, core.TruncateOneLine(title, 80), core.TruncateOneLine(origin.Label, 80)))
	return t, nil
}

// AskThreadSend asks one more question in an ask thread, resuming its own
// session, and returns as soon as the run has started. Anyone who can see
// the job may send; the stamp on stdin says who did.
func AskThreadSend(j *Job, tid, text string, from ThreadOrigin) error {
	from = boundOrigin(from)
	st, err := threadStore(j)
	if err != nil {
		return err
	}
	t, err := st.GetThread(j.ID, tid)
	if err != nil {
		return err
	}
	if t.Kind != "headless" {
		return fmt.Errorf("thread %s of %s is a pane thread — send to its pane", tid, j.Slug)
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("%s: ask text cannot be empty", j.Slug)
	}
	if err := askCheckQuestion(j, text); err != nil {
		return err
	}
	if err := askLiveCell(j); err != nil {
		return err
	}
	askStartMu.RLock()
	defer askStartMu.RUnlock()
	if err := st.ClaimThreadAnswering(j.ID, tid); err != nil {
		return err
	}
	if err := startAskRun(j, *t, text, from, true); err != nil {
		if ferr := st.FinishThreadAnswering(j.ID, tid, core.TruncateOneLine("ask failed: "+err.Error(), askNoteMax)); ferr != nil {
			util.LogEvent("jobs", "error", "thread-ask", fmt.Sprintf("%s: thread %s: clearing answering failed: %v", j.Slug, tid, ferr))
		}
		return err
	}
	return nil
}

// sweepInterruptedAsks clears the answering flag of every live ask row of j
// with no registered run: its run was lost with a kraken serve restart, and
// nothing else would ever clear it.
func sweepInterruptedAsks(st *store.Store, j *Job, threads []store.Thread) error {
	for _, t := range threads {
		if t.Kind != "headless" || t.State != "live" || !t.Answering || AskRunning(j.ID, t.TID) {
			continue
		}
		// conditional on answering = 1 at write time: a run that finished
		// after ListThreads read the row has already written its own note
		wrote, err := st.ClearStaleThreadAnswering(j.ID, t.TID, "answer interrupted — kraken serve restarted")
		if err != nil {
			return err
		}
		if !wrote {
			continue
		}
		util.LogEvent("jobs", "info", "thread-ask-done", fmt.Sprintf("%s: thread %s: answer interrupted — kraken serve restarted", j.Slug, t.TID))
	}
	return nil
}
