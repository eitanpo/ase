// Package parse turns a session JSONL file (and its subagent sidecar files)
// into the canonical model.Session. The extraction logic is ported from the
// claude-logs-search Python reference, verified against live logs under
// ~/.claude/projects/. The LLM-safety envelope from the reference is
// deliberately omitted — agentry renders for humans, not for re-ingestion.
package parse

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/eitanpo/agentry/internal/model"
)

// Only user and assistant entries carry content we render; every other type
// (ai-title, attachment, system, permission-mode, file-history-snapshot,
// progress, queue-operation, last-prompt, …) is ignored.

var agentIDRe = regexp.MustCompile(`agentId:\s*(\S+)`)

// injectedMarkers identify user entries that are system-injected, not typed.
var injectedMarkers = []string{
	"<local-command-caveat>", "<bash-stdout>", "<bash-stderr>",
	"<bash-input>", "Base directory for this skill:", "<local-command-stdout>",
	"<task-notification>", // harness-injected background-task event/completion, not a typed prompt
}

// Load parses the session at jsonlPath into a Session.
func Load(jsonlPath string) (*model.Session, error) {
	entries, err := loadEntries(jsonlPath)
	if err != nil {
		return nil, err
	}

	stem := strings.TrimSuffix(filepath.Base(jsonlPath), filepath.Ext(jsonlPath))
	subs := loadSubagents(subagentDir(jsonlPath))
	eps := entrypoints(entries)
	effs := efforts(entries)
	ms := models(entries)

	sess := &model.Session{
		Meta: model.Meta{
			ID:           stem,
			Model:        lastOf(ms),
			Models:       manyOrNone(ms),
			NumSubagents: len(subs),
			Entrypoint:   lastOf(eps),
			Entrypoints:  manyOrNone(eps),
			Effort:       lastOf(effs),
			Efforts:      manyOrNone(effs),
			PRs:          sessionPRs(entries),
			Artifacts:    sessionArtifacts(entries),
		},
	}
	sess.Meta.Start, sess.Meta.End = timeRange(entries)

	sess.Meta.Usage = sumUsage(entries)
	for _, s := range subs {
		sess.Meta.Usage.Add(sumUsage(s.entries))
	}

	for _, t := range splitTurns(entries) {
		turn := model.Turn{
			Prompt: t.prompt,
			Start:  t.start,
			End:    t.end,
			Events: buildEvents(t.entries, subs, map[string]bool{}),
		}
		turn.Usage, turn.ToolCount, turn.ErrorCount = turnMetrics(t.entries, subs)
		sess.Turns = append(sess.Turns, turn)
	}
	return sess, nil
}

// Summarize scans a session JSONL into a lightweight Summary without building
// the full event tree or loading subagents — cheap enough to run over every
// session in a project for `agentry list`. Title reuses the same turn-splitting as
// Load, so it matches the prompt the renderer would show.
func Summarize(jsonlPath string) (model.Summary, error) {
	entries, err := loadEntries(jsonlPath)
	if err != nil {
		return model.Summary{}, err
	}
	stem := strings.TrimSuffix(filepath.Base(jsonlPath), filepath.Ext(jsonlPath))
	start, end := timeRange(entries)
	turns := splitTurns(entries)
	eps := entrypoints(entries)
	effs := efforts(entries)
	ms := models(entries)
	cwd := sessionCwd(entries)
	var prompts []string
	for _, tn := range turns {
		if !isClearCmd(tn.prompt) {
			prompts = append(prompts, tn.prompt)
		}
	}
	return model.Summary{
		ID:       stem,
		Start:    start,
		End:      end,
		Title:    sessionTitle(lastTitleOf(entries, manualTitleTypes...), lastTitleOf(entries, "ai-title"), turns),
		Prompts:  prompts,
		NumTurns: len(turns),
		Tools:    toolStats(entries),
		Commands: bashCommands(entries),
		Replies:  replyTexts(entries),
		RootUUID: rootUUID(entries),
		Cwd:      cwd,
		Files:    sessionFiles(entries, cwd),
		Denials:  denialStats(entries),
		Born:     fileBorn(jsonlPath),
		// The last value is the session's, matching the last-activity time the
		// listing orders by. The full list is kept only when it diverges, so a
		// single-entrypoint session serializes one field rather than two.
		Entrypoint:  lastOf(eps),
		Entrypoints: manyOrNone(eps),
		Model:       lastOf(ms),
		Models:      manyOrNone(ms),
		Effort:      lastOf(effs),
		Efforts:     manyOrNone(effs),
		Usage:       sessionUsage(jsonlPath, entries),
		PRs:         sessionPRs(entries),
		Artifacts:   sessionArtifacts(entries),
	}, nil
}

// subagentDir is where a session's subagent sidecars live, next to its own log.
// Named once because Load and Summarize must look in the same place: a listing
// that read a different directory would report a different cost for the session
// the render path is showing.
func subagentDir(jsonlPath string) string {
	stem := strings.TrimSuffix(filepath.Base(jsonlPath), filepath.Ext(jsonlPath))
	return filepath.Join(filepath.Dir(jsonlPath), stem, "subagents")
}

// sessionUsage totals a session's tokens the way Load builds Meta.Usage: the
// main thread plus every subagent sidecar. Summarize is otherwise deliberately
// cheap, and this is the one place it opens files the main log does not name —
// sidecars run to roughly 60% of a project tree's bytes. It is paid anyway,
// because a tally that silently dropped delegated work would answer the cost
// question wrong for exactly the sessions that cost the most.
func sessionUsage(jsonlPath string, entries []entry) model.Usage {
	u := sumUsage(entries)
	paths, _ := filepath.Glob(filepath.Join(subagentDir(jsonlPath), "agent-*.jsonl"))
	for _, p := range paths {
		u.Add(sidecarUsage(p))
	}
	return u
}

// usageOnly is the slice of an entry a token tally needs. Sidecars are read
// through it rather than through loadEntries, which would also decode every
// content block on the way: over 250 local sessions a cross-project listing
// measured 2.40s reading no sidecars, 2.96s through this, and 3.96s through
// loadEntries. An unreadable file or a malformed line is skipped rather than
// raised — it undercounts the tally, where an error would drop the whole
// session from the listing over a subagent's log.
type usageOnly struct {
	Type string `json:"type"`
	// RequestID and UUID are what usageKey groups by, so a sidecar's tokens are
	// deduplicated on the same rule as the main log's rather than counting a
	// delegated reply once per content block.
	RequestID string `json:"requestId"`
	UUID      string `json:"uuid"`
	Message   struct {
		Usage rawUsage `json:"usage"`
	} `json:"message"`
}

func sidecarUsage(path string) model.Usage {
	var t usageTally
	f, err := os.Open(path)
	if err != nil {
		return t.total
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // sidecars hold large tool results too
	for sc.Scan() {
		var re usageOnly
		if json.Unmarshal(sc.Bytes(), &re) != nil || re.Type != "assistant" {
			continue
		}
		t.add(usageKey(re.RequestID, re.UUID), model.Usage{
			Input: re.Message.Usage.Input, Output: re.Message.Usage.Output,
			CacheRead: re.Message.Usage.CacheRead, CacheCreate: re.Message.Usage.CacheCreate,
		})
	}
	return t.total
}

// entrypoints returns every distinct entrypoint the session carries, in
// first-seen order. Meta entries omit the field, so an absent value is skipped
// rather than recorded as a change. A session resumed in another client carries
// two — observed as contiguous blocks, never interleaved.
func entrypoints(entries []entry) []string {
	return distinct(entries, func(e entry) string { return e.entrypoint })
}

// efforts returns every distinct reasoning effort the session carries, in
// first-seen order. Only assistant entries have the field, so an absent value is
// skipped rather than recorded as a change — otherwise every user turn would
// read as effort being switched off and back on.
func efforts(entries []entry) []string {
	return distinct(entries, func(e entry) string { return e.effort })
}

// distinct collects the non-empty values of one per-entry setting in first-seen
// order. Entrypoint and effort are the same shape of fact — absent on some entry
// types, occasionally changed mid-session — and both resolve as "last wins" with
// the full list kept only when it diverges, so they share the reading.
func distinct(entries []entry, field func(entry) string) []string {
	var out []string
	seen := map[string]bool{}
	for _, e := range entries {
		v := field(e)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func lastOf(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[len(s)-1]
}

// manyOrNone returns s only when it holds more than one value, so the common
// single-entrypoint case does not repeat what Entrypoint already says.
func manyOrNone(s []string) []string {
	if len(s) < 2 {
		return nil
	}
	return s
}

// sessionCwd is the working directory the session ran in — the first non-empty
// cwd any entry carries. Meta entries (ai-title, agent-name, …) omit the field,
// so taking the first entry's value unconditionally would report none.
func sessionCwd(entries []entry) string {
	for _, e := range entries {
		if e.cwd != "" {
			return e.cwd
		}
	}
	return ""
}

// rootUUID is the uuid of the first entry that carries one — the conversation
// root. A fork copies its parent's chain verbatim, root entry included, so
// sessions sharing a root uuid are one fork family. /clear starts an empty
// session, so its root uuid is fresh and it does not join the parent's family.
func rootUUID(entries []entry) string {
	for _, e := range entries {
		if e.uuid != "" {
			return e.uuid
		}
	}
	return ""
}

// fileBorn is the session file's creation time, used to order a fork family
// (earliest = original). A fork is a new file written at fork time, so its
// birthtime exceeds the original's — a signal the fork cannot forge, unlike the
// in-content timestamps it copies. Off macOS, where creation time is not
// portably readable, it falls back to the modification time.
func fileBorn(path string) time.Time {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	if bt, ok := fileBirthtime(fi); ok {
		return bt
	}
	return fi.ModTime()
}

// bashCommands returns the session's distinct top-level Bash commands in
// first-seen order, the corpus --used-command and --used substring-match.
func bashCommands(entries []entry) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range entries {
		if e.typ != "assistant" {
			continue
		}
		for _, b := range e.blocks {
			if b.typ != "tool_use" || b.name != "Bash" {
				continue
			}
			cmd, _ := b.input["command"].(string)
			if cmd == "" || seen[cmd] {
				continue
			}
			seen[cmd] = true
			out = append(out, cmd)
		}
	}
	return out
}

// replyTexts returns the main thread's assistant text blocks in order — the
// corpus --reply-matches tests. One entry per block rather than one joined
// string, so a pattern's ^ and $ anchor to a single reply.
//
// Thinking blocks are excluded because reasoning is not a reply: a rule about
// what a reply said must not be satisfied by a thought the user never saw.
// Blank blocks are dropped on the same rule buildEvents applies, so the render
// path and the filter agree on which blocks are replies at all.
func replyTexts(entries []entry) []string {
	var out []string
	for _, e := range entries {
		if e.typ != "assistant" {
			continue
		}
		for _, b := range e.blocks {
			if b.typ == "text" && strings.TrimSpace(b.text) != "" {
				out = append(out, b.text)
			}
		}
	}
	return out
}

// toolStats aggregates the session's top-level tool calls by (tool, identity),
// preserving first-seen order so output is stable before the renderer sorts it.
// It counts only the main thread's calls — subagent sidecars are not loaded —
// matching the top-level population of turnMetrics.
func toolStats(entries []entry) []model.ToolStat {
	type key struct{ tool, identity string }
	counts := map[key]int{}
	var order []key
	for _, e := range entries {
		if e.typ != "assistant" {
			continue
		}
		for _, b := range e.blocks {
			if b.typ != "tool_use" {
				continue
			}
			k := key{b.name, toolIdentity(b.name, b.input)}
			if counts[k] == 0 {
				order = append(order, k)
			}
			counts[k]++
		}
	}
	out := make([]model.ToolStat, 0, len(order))
	for _, k := range order {
		out = append(out, model.ToolStat{Tool: k.tool, Identity: k.identity, Count: counts[k]})
	}
	return out
}

// toolIdentity is the grouping label for a tool call: the invoked program for
// Bash, the skill for Skill, the subagent type for Agent, the target file for
// Edit and Write. Empty for every other tool, whose own name is its identity.
// Field names verified against live logs.
//
// Read also carries file_path but is deliberately left without an identity: the
// question these labels answer is what a session changed, and a Read tally by
// path would be the largest group in most sessions while changing nothing.
func toolIdentity(name string, input map[string]any) string {
	str := func(k string) string { s, _ := input[k].(string); return s }
	switch name {
	case "Bash":
		return bashProgram(str("command"))
	case "Skill":
		return str("skill")
	case "Agent":
		return str("subagent_type")
	case "Edit", "Write":
		return str("file_path")
	default:
		return ""
	}
}

// toolModel returns the model a call delegated to, or "" when it named none.
// Read from any tool's input rather than gated on the name: `Agent` is the only
// tool that carries the field today (332 of 543 local Agent calls, and no other
// tool at all), and a later tool carrying it would mean the same thing.
func toolModel(input map[string]any) string {
	s, _ := input["model"].(string)
	return s
}

// bashProgram reduces a shell command to the program a histogram groups by: the
// first token after any leading VAR=value assignments, reduced to its basename
// ("/a/b/exa --x" → "exa"). A heuristic — a pipeline or "cd x && y" reports only
// its first program, which is enough for a usage tally.
func bashProgram(cmd string) string {
	fields := strings.Fields(cmd)
	i := 0
	for i < len(fields) && isAssignment(fields[i]) {
		i++
	}
	if i >= len(fields) {
		return ""
	}
	return filepath.Base(fields[i])
}

// isAssignment reports whether tok is a leading shell VAR=value assignment (the
// name left of '=' is a non-empty run of identifier characters).
func isAssignment(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	for _, r := range tok[:eq] {
		if r != '_' && !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// lastTitleOf returns the most recent non-empty title carried on entries of the
// given type. ai-title and custom-title both regenerate/rewrite as the session
// evolves, so the last one wins.
func lastTitleOf(entries []entry, typ ...string) string {
	title := ""
	for _, e := range entries {
		if !slices.Contains(typ, e.typ) || strings.TrimSpace(e.title) == "" {
			continue
		}
		title = e.title
	}
	return title
}

// manualTitleTypes are the two entries that record a name the user chose: a
// custom-title from renaming the session, an agent-name from --name or /rename.
// Neither outranks the other by kind, so lastTitleOf takes whichever the log
// records last — these entries carry no timestamp, so file order is the only
// ordering there is.
var manualTitleTypes = []string{"custom-title", "agent-name"}

// sessionTitle picks a listing title by a fallback ladder: a manual title the
// user chose (see manualTitleTypes) if present, else Claude Code's ai-title,
// else the first turn's prompt skipping a leading /clear (which resets context
// and describes nothing), else the first prompt.
func sessionTitle(manualTitle, aiTitle string, turns []rawTurn) string {
	if t := strings.TrimSpace(manualTitle); t != "" {
		return t
	}
	if t := strings.TrimSpace(aiTitle); t != "" {
		return t
	}
	for _, t := range turns {
		if !isClearCmd(t.prompt) {
			return t.prompt
		}
	}
	if len(turns) > 0 {
		return turns[0].prompt
	}
	return ""
}

// isClearCmd reports whether a turn prompt is the /clear command. userPrompt
// renders it with one leading slash ("/clear"), but trimming all leading slashes
// also matches an older "//clear" rendering and a bare "clear".
//
// "/clear <text>" — Claude Code records same-line text after /clear as the
// command's arguments — still counts: the clear resets context and the trailing
// text describes the reset, not the session. The leading-slash check (s != rest)
// keeps prose like "clear the table" from being misread as a reset.
func isClearCmd(prompt string) bool {
	s := strings.TrimSpace(prompt)
	rest := strings.TrimLeft(s, "/")
	return rest == "clear" || (s != rest && strings.HasPrefix(rest, "clear "))
}

// ── Raw JSONL decoding ───────────────────────────────────────────────────

type entry struct {
	typ        string
	t          time.Time
	uuid       string
	model      string
	usage      model.Usage
	title      string  // set on ai-title (aiTitle), custom-title (customTitle) and agent-name (agentName) entries
	contentStr string  // set when message.content is a JSON string
	hasStr     bool    // distinguishes "" content from absent/array content
	blocks     []block // set when message.content is a JSON array
	// toolUseResultAgentID is the structured spawn-child id from the top-level
	// toolUseResult.agentId, set on the user entry carrying an Agent/forked-Skill
	// tool_result. Empty when absent (older logs) or for non-spawning tools.
	toolUseResultAgentID string
	// isCompactSummary marks the user entry Claude Code writes at a compaction
	// boundary, whose content is the summary rather than anything typed.
	isCompactSummary bool
	// cwd is the working directory the session ran in. Meta entries omit it, so
	// the session's value is the first non-empty one.
	cwd string
	// entrypoint is where the session was run. Meta entries omit it, and a
	// session resumed elsewhere carries two values in contiguous blocks.
	entrypoint string
	// effort is the reasoning effort, carried on assistant entries only. Absent
	// on sessions predating Claude Code 2.1.212, and it can change mid-session.
	effort string
	// denialKind is why a call was refused, on the user entry carrying its
	// tool_result. Empty on every other entry and on results that ran.
	denialKind string
	// trackingPath (file-history-delta) and trackedPaths (file-history-snapshot)
	// are Claude Code's own record of which files changed, in the log's own mix of
	// repo-relative and absolute forms — sessionFiles resolves them.
	trackingPath string
	trackedPaths []string
	// pr and frame carry the payloads of the two entries recording what a session
	// produced. Each is filled only on its own entry type, so the frame's title
	// cannot be mistaken for the session title the three title entries carry.
	pr    model.PR
	frame model.Artifact
	// requestID names the API response this assistant entry came from, the key a
	// token tally groups by. Empty on non-assistant entries and on older logs.
	requestID string
	// turnCompanion marks harness-attached material filed as a user entry.
	turnCompanion bool
}

type block struct {
	typ        string
	text       string         // text blocks
	thinking   string         // thinking blocks
	id         string         // tool_use id
	name       string         // tool_use name
	input      map[string]any // tool_use input
	toolUseID  string         // tool_result target
	isError    bool           // tool_result error flag
	resultText string         // tool_result flattened text
}

type rawEntry struct {
	Type          string          `json:"type"`
	Timestamp     string          `json:"timestamp"`
	UUID          string          `json:"uuid"` // entry id; the first one is the conversation root (fork-family key)
	Message       json.RawMessage `json:"message"`
	AiTitle       string          `json:"aiTitle"`       // ai-title entries: Claude Code's own session summary
	CustomTitle   string          `json:"customTitle"`   // custom-title entries: the name set by renaming the session
	AgentName     string          `json:"agentName"`     // agent-name entries: the name set by --name or /rename
	Cwd           string          `json:"cwd"`           // working directory the session ran in
	Entrypoint    string          `json:"entrypoint"`    // where the session was run: cli, claude-desktop, sdk-cli
	Effort        string          `json:"effort"`        // reasoning effort, on assistant entries: low, high, xhigh, …
	ToolUseResult json.RawMessage `json:"toolUseResult"` // structured tool-result mirror; carries agentId for spawn children
	// ToolDenialKind is why a call was refused, on the user entry carrying its
	// tool_result. Not to be confused with permission-mode entries, which record
	// the mode in effect and never a per-call decision.
	ToolDenialKind string `json:"toolDenialKind"`
	// TrackingPath is the file a file-history-delta entry records a change to,
	// and Snapshot the cumulative tracked set on a file-history-snapshot entry.
	// Both are Claude Code's own record of what changed, independent of tools.
	TrackingPath string       `json:"trackingPath"`
	Snapshot     *rawSnapshot `json:"snapshot"`
	// pr-link and frame-link payloads: what the session produced. Title is
	// frame-link's own field and unrelated to aiTitle/customTitle/agentName, which
	// is why it is not folded into the title trio above.
	PRNumber     int    `json:"prNumber"`
	PRURL        string `json:"prUrl"`
	PRRepository string `json:"prRepository"`
	FrameURL     string `json:"frameUrl"`
	FramePath    string `json:"path"`
	FrameTitle   string `json:"title"`
	// IsCompactSummary flags the compaction-boundary user entry. Absent in logs
	// written before Claude Code added it, hence the text fallback in userPrompt.
	IsCompactSummary bool `json:"isCompactSummary"`
	// RequestID names the API response an assistant entry came from. Claude Code
	// splits one response across an entry per content block and repeats the whole
	// response's usage on each, so this is what a token tally groups by. Absent on
	// entries Claude Code composed itself and on logs predating the field.
	RequestID string `json:"requestId"`
	// TurnCompanion marks a user entry as material the harness attached to the
	// turn rather than anything a person typed. Claude Code's own prompt test
	// refuses to count an entry carrying it; written since 2.1.236.
	TurnCompanion bool `json:"turnCompanion"`
}

// rawSnapshot is the file-history-snapshot payload. Only the keys of
// trackedFileBackups matter — they are the paths — so the values are left
// unparsed rather than modelling a backup record agentry never reads.
type rawSnapshot struct {
	TrackedFileBackups map[string]json.RawMessage `json:"trackedFileBackups"`
}

type rawMessage struct {
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Usage   rawUsage        `json:"usage"`
}

type rawUsage struct {
	Input       int `json:"input_tokens"`
	Output      int `json:"output_tokens"`
	CacheRead   int `json:"cache_read_input_tokens"`
	CacheCreate int `json:"cache_creation_input_tokens"`
}

type rawBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     map[string]any  `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

func loadEntries(path string) ([]entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // logs hold large tool results
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var re rawEntry
		if json.Unmarshal([]byte(line), &re) != nil {
			continue // skip malformed lines, as the reference does
		}
		e := entry{
			// Only one of the three title fields is ever set on a given entry —
			// each belongs to a different entry type — so concatenating picks it.
			typ: re.Type, uuid: re.UUID, title: re.AiTitle + re.CustomTitle + re.AgentName,
			isCompactSummary: re.IsCompactSummary, cwd: re.Cwd, entrypoint: re.Entrypoint,
			effort:     re.Effort,
			denialKind: re.ToolDenialKind, trackingPath: re.TrackingPath,
			requestID: re.RequestID, turnCompanion: re.TurnCompanion,
		}
		switch re.Type {
		case "pr-link":
			e.pr = model.PR{Repository: re.PRRepository, Number: re.PRNumber, URL: re.PRURL}
		case "frame-link":
			e.frame = model.Artifact{Title: re.FrameTitle, URL: re.FrameURL, Path: re.FramePath}
		}
		if re.Snapshot != nil {
			for p := range re.Snapshot.TrackedFileBackups {
				e.trackedPaths = append(e.trackedPaths, p)
			}
			// Map iteration is unordered, and the touched-file list is documented as
			// first-seen order, so a snapshot's own paths are sorted to make one
			// session's output identical on every run.
			sort.Strings(e.trackedPaths)
		}
		if ts, err := time.Parse(time.RFC3339, re.Timestamp); err == nil {
			e.t = ts
		}
		// toolUseResult is sometimes a structured object (spawn children carry
		// agentId), sometimes a plain string — only the object form has an id.
		if len(re.ToolUseResult) > 0 && re.ToolUseResult[0] == '{' {
			var tur struct {
				AgentID string `json:"agentId"`
			}
			if json.Unmarshal(re.ToolUseResult, &tur) == nil {
				e.toolUseResultAgentID = tur.AgentID
			}
		}
		if len(re.Message) > 0 {
			var msg rawMessage
			if json.Unmarshal(re.Message, &msg) == nil {
				e.model = msg.Model
				e.usage = model.Usage{
					Input: msg.Usage.Input, Output: msg.Usage.Output,
					CacheRead: msg.Usage.CacheRead, CacheCreate: msg.Usage.CacheCreate,
				}
				e.contentStr, e.hasStr, e.blocks = decodeContent(msg.Content)
			}
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// decodeContent handles message.content being either a JSON string or an array
// of typed blocks.
func decodeContent(raw json.RawMessage) (str string, hasStr bool, blocks []block) {
	if len(raw) == 0 {
		return "", false, nil
	}
	if raw[0] == '"' {
		_ = json.Unmarshal(raw, &str)
		return str, true, nil
	}
	var rbs []rawBlock
	if json.Unmarshal(raw, &rbs) != nil {
		return "", false, nil
	}
	for _, rb := range rbs {
		blocks = append(blocks, block{
			typ: rb.Type, text: rb.Text, thinking: rb.Thinking,
			id: rb.ID, name: rb.Name, input: rb.Input,
			toolUseID: rb.ToolUseID, isError: rb.IsError,
			resultText: flattenResult(rb.Content),
		})
	}
	return "", false, blocks
}

// flattenResult extracts text from a tool_result's content, which is either a
// string or an array of {type:"text", text:...} blocks.
func flattenResult(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var sb strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}

// ── Session-level extraction ───────────────────────────────────────────────

func timeRange(entries []entry) (start, end time.Time) {
	for _, e := range entries {
		if e.t.IsZero() {
			continue
		}
		if start.IsZero() {
			start = e.t
		}
		end = e.t
	}
	return start, end
}

// syntheticModel is the model Claude Code writes on assistant messages it
// composed itself rather than received from a model — an API-error notice, a
// session-limit warning, "No response requested.". They carry zero tokens and
// name no model the session ran on, so counting one would end a session on a
// model that never ran: 17 of 250 local sessions carry such an entry, and in
// none of them was it the last real message.
const syntheticModel = "<synthetic>"

// models returns every distinct model the session ran on, in first-seen order.
// Only assistant entries name one, so an absent value is skipped rather than
// recorded as a change — the same reading efforts uses, and for the same reason.
// Switching mid-session is not rare (13 of 250 local sessions did), and the
// values come in contiguous blocks, so the last one is the session's.
func models(entries []entry) []string {
	return distinct(entries, func(e entry) string {
		if e.typ != "assistant" || e.model == syntheticModel {
			return ""
		}
		return e.model
	})
}

// usageTally totals assistant tokens while counting each API response once.
// Claude Code splits one response across an entry per content block and repeats
// that response's whole usage object on every one of them, so adding the entries
// up multiplies a reply's tokens by how many blocks it held — 1.75x to 3.11x
// across local sessions, and enough of a per-turn variable to reorder the
// summary as well as inflate the totals.
type usageTally struct {
	total model.Usage
	seen  map[string]bool
}

// add counts one assistant entry unless its response is already counted. An
// entry naming neither a response nor itself is counted every time: with no
// identity there is nothing to compare against, so such an entry keeps the
// undeduplicated behavior rather than collapsing into whichever came first.
func (t *usageTally) add(key string, u model.Usage) {
	if key != "" {
		if t.seen[key] {
			return
		}
		if t.seen == nil {
			t.seen = map[string]bool{}
		}
		t.seen[key] = true
	}
	t.total.Add(u)
}

// usageKey identifies the response an assistant entry belongs to. Claude Code
// composes some assistant entries itself and writes no requestId on them, so the
// entry's own id stands in — which counts that entry once, exactly as it was
// counted before. Every assistant entry of the oldest local log (2.1.205) carries
// the field, so that fallback is for the synthetic entries and for any log older
// than anything measured.
func usageKey(requestID, uuid string) string {
	if requestID != "" {
		return requestID
	}
	return uuid
}

func sumUsage(entries []entry) model.Usage {
	var t usageTally
	for _, e := range entries {
		if e.typ == "assistant" {
			t.add(usageKey(e.requestID, e.uuid), e.usage)
		}
	}
	return t.total
}

// ── Tool results and agent stitching ─────────────────────────────────────

type toolResult struct {
	end     time.Time
	isError bool
	text    string
	denial  string
}

// toolResultMap indexes each call's outcome by tool_use id. The denial kind is
// read from the entry rather than the block: the log puts toolDenialKind at the
// top level of the user entry carrying the tool_result, not inside the result.
func toolResultMap(entries []entry) map[string]toolResult {
	m := map[string]toolResult{}
	for _, e := range entries {
		if e.typ != "user" {
			continue
		}
		for _, b := range e.blocks {
			if b.typ == "tool_result" {
				m[b.toolUseID] = toolResult{end: e.t, isError: b.isError, text: b.resultText, denial: e.denialKind}
			}
		}
	}
	return m
}

// denialStats groups the session's refused top-level calls by what refused them
// and which call it was — the shape an auto-allow decision is made in. A denial
// is matched back to its tool_use by id, so a result with no matching call (a
// truncated log) contributes nothing rather than an entry named "".
func denialStats(entries []entry) []model.DenialStat {
	type call struct{ tool, identity string }
	calls := map[string]call{}
	for _, e := range entries {
		if e.typ != "assistant" {
			continue
		}
		for _, b := range e.blocks {
			if b.typ == "tool_use" {
				calls[b.id] = call{b.name, toolIdentity(b.name, b.input)}
			}
		}
	}
	type key struct{ kind, tool, identity string }
	counts := map[key]int{}
	var order []key
	for _, e := range entries {
		if e.typ != "user" || e.denialKind == "" {
			continue
		}
		for _, b := range e.blocks {
			if b.typ != "tool_result" {
				continue
			}
			c, ok := calls[b.toolUseID]
			if !ok {
				continue
			}
			k := key{e.denialKind, c.tool, c.identity}
			if counts[k] == 0 {
				order = append(order, k)
			}
			counts[k]++
		}
	}
	out := make([]model.DenialStat, 0, len(order))
	for _, k := range order {
		out = append(out, model.DenialStat{Kind: k.kind, Tool: k.tool, Identity: k.identity, Count: counts[k]})
	}
	return out
}

// sessionFiles is every file the session modified, absolute and deduplicated in
// first-seen order. The log mixes forms — a path inside the session's working
// directory is recorded relative to it, one outside is absolute — so a relative
// path is resolved against cwd. With no cwd (a log predating the field) the
// relative paths are kept as they are: reporting them beats dropping a real
// change, and an unrooted path still names the file.
func sessionFiles(entries []entry, cwd string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p == "" {
			return
		}
		if !filepath.IsAbs(p) && cwd != "" {
			p = filepath.Join(cwd, p)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, e := range entries {
		add(e.trackingPath)
		for _, p := range e.trackedPaths {
			add(p)
		}
	}
	return out
}

// sessionPRs and sessionArtifacts are what the session produced beyond its own
// transcript, each read from the entry type Claude Code writes for it.
func sessionPRs(entries []entry) []model.PR {
	return dedupeOutputs(entries, "pr-link",
		func(e entry) model.PR { return e.pr },
		model.PR.Key, latestPR)
}

func sessionArtifacts(entries []entry) []model.Artifact {
	return dedupeOutputs(entries, "frame-link",
		func(e entry) model.Artifact { return e.frame },
		model.Artifact.Key, latestArtifact)
}

// dedupeOutputs collapses one kind of re-recorded session event into the distinct
// things it names, in first-seen order. Claude Code re-emits both pr-link and
// frame-link on later turns of the same session, and heavily — 963 pr-link
// entries across 250 local sessions name 67 pull requests — so the entries are a
// stream of restatements, not a list. A record naming nothing (no key) is
// dropped: it identifies no thing a reader could act on.
//
// One function rather than two loops because the rule is one rule. A copy per
// entry type is where a later kind of output quietly stops deduplicating.
func dedupeOutputs[T any](entries []entry, typ string, get func(entry) T, key func(T) string, merge func(old, cur T) T) []T {
	var out []T
	at := map[string]int{}
	for _, e := range entries {
		if e.typ != typ {
			continue
		}
		v := get(e)
		k := key(v)
		if k == "" {
			continue
		}
		if i, ok := at[k]; ok {
			out[i] = merge(out[i], v)
			continue
		}
		at[k] = len(out)
		out = append(out, v)
	}
	return out
}

// latestPR and latestArtifact fold a re-record over the entry already held: the
// newer values win, except where the newer entry says nothing. An omitted field
// is an omission and not a deletion — the log gives no way to tell those apart,
// and discarding a value agentry already read is the worse guess. It is not
// hypothetical for an artifact: a third of local frame-link entries carry no
// title, so a republish from a moved file would otherwise lose the name the
// artifact had.
func latestPR(old, cur model.PR) model.PR {
	if cur.Repository == "" {
		cur.Repository = old.Repository
	}
	if cur.Number == 0 {
		cur.Number = old.Number
	}
	if cur.URL == "" {
		cur.URL = old.URL
	}
	return cur
}

func latestArtifact(old, cur model.Artifact) model.Artifact {
	if cur.Title == "" {
		cur.Title = old.Title
	}
	if cur.URL == "" {
		cur.URL = old.URL
	}
	if cur.Path == "" {
		cur.Path = old.Path
	}
	return cur
}

// sidecarIDs maps the tool_use ids of spawning calls (the named tool) to their
// subagent log key ("agent-xxx"). It prefers the structured toolUseResult.agentId
// carried on the result entry and falls back to the "agentId: …" line in the
// result text — the only mechanism in pre-structured logs, present on Agent
// results. Restricting to a single tool name keeps an "agentId:" string in some
// unrelated result from being misread as a spawn link.
func sidecarIDs(entries []entry, toolName string) map[string]string {
	tools := map[string]bool{}
	for _, e := range entries {
		if e.typ != "assistant" {
			continue
		}
		for _, b := range e.blocks {
			if b.typ == "tool_use" && b.name == toolName {
				tools[b.id] = true
			}
		}
	}
	m := map[string]string{}
	for _, e := range entries {
		if e.typ != "user" {
			continue
		}
		for _, b := range e.blocks {
			if b.typ != "tool_result" || !tools[b.toolUseID] {
				continue
			}
			id := e.toolUseResultAgentID
			if id == "" {
				if mt := agentIDRe.FindStringSubmatch(b.resultText); mt != nil {
					id = mt[1]
				}
			}
			if id != "" {
				m[b.toolUseID] = "agent-" + id
			}
		}
	}
	return m
}

// agentIDMap maps an Agent tool_use id to its subagent log key.
func agentIDMap(entries []entry) map[string]string { return sidecarIDs(entries, "Agent") }

// skillSidecarMap maps a forked-Skill tool_use id to its subagent log key.
// Inline skills run in the main chain and write no sidecar, so they never appear
// here — leaving attachSubagent to fall back to legacy name matching, then to no
// expansion.
func skillSidecarMap(entries []entry) map[string]string { return sidecarIDs(entries, "Skill") }

// ── Subagents ────────────────────────────────────────────────────────────

type subagent struct {
	entries   []entry
	skillName string
}

func loadSubagents(dir string) map[string]*subagent {
	subs := map[string]*subagent{}
	matches, _ := filepath.Glob(filepath.Join(dir, "agent-*.jsonl"))
	for _, path := range matches {
		entries, err := loadEntries(path)
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		subs[id] = &subagent{entries: entries, skillName: subagentSkill(entries)}
	}
	return subs
}

func subagentSkill(entries []entry) string {
	const marker = "Base directory for this skill:"
	for _, e := range entries {
		text := e.contentStr
		if !e.hasStr {
			for _, b := range e.blocks {
				if b.typ == "text" {
					text = b.text
					break
				}
			}
		}
		if !strings.Contains(text, marker) {
			continue
		}
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(line, marker) {
				p := strings.TrimSpace(line[strings.Index(line, marker)+len(marker):])
				return filepath.Base(p)
			}
		}
	}
	return ""
}

// totalAgentUsage sums an agent's tokens plus those of every agent it spawned.
func totalAgentUsage(id string, subs map[string]*subagent, seen map[string]bool) model.Usage {
	var u model.Usage
	if seen[id] {
		return u
	}
	seen[id] = true
	s := subs[id]
	if s == nil {
		return u
	}
	u = sumUsage(s.entries)
	for _, nestedID := range agentIDMap(s.entries) {
		u.Add(totalAgentUsage(nestedID, subs, seen))
	}
	return u
}

// ── Turn splitting ─────────────────────────────────────────────────────────

type rawTurn struct {
	prompt  string
	start   time.Time
	end     time.Time
	entries []entry
}

func splitTurns(entries []entry) []rawTurn {
	var turns []rawTurn
	var cur *rawTurn
	for _, e := range entries {
		if e.typ == "user" {
			if prompt, ok := userPrompt(e); ok {
				if cur != nil {
					turns = append(turns, *cur)
				}
				cur = &rawTurn{prompt: prompt, start: e.t, end: e.t}
				continue
			}
		}
		if cur != nil {
			cur.entries = append(cur.entries, e)
			if !e.t.IsZero() {
				cur.end = e.t
			}
		}
	}
	if cur != nil {
		turns = append(turns, *cur)
	}
	return turns
}

var (
	cmdNameRe = regexp.MustCompile(`<command-name>(.*?)</command-name>`)
	cmdArgsRe = regexp.MustCompile(`<command-args>(.*?)</command-args>`)
)

// compactSummaryPlaceholder stands in for a compaction boundary's summary, which
// is a user entry Claude Code wrote rather than a prompt anyone typed.
const compactSummaryPlaceholder = "[context compacted — see session log for full summary]"

// userPrompt returns the human-typed prompt for a user entry, or ok=false for
// system-injected content (tool results, skill bodies, bash output).
func userPrompt(e entry) (string, bool) {
	if !e.hasStr {
		return "", false
	}
	content := e.contentStr
	// The compaction check runs before the injected markers because the flag says
	// what the entry *is*, while a marker only says what its text contains — and a
	// summary of a conversation can quote one, which would drop the boundary
	// entirely instead of standing in for it.
	if e.isCompactSummary {
		return compactSummaryPlaceholder, true
	}
	// Claude Code's own prompt test refuses an entry carrying this, so agentry
	// refuses it too rather than guessing from the text — a skill body, a
	// re-invocation notice, an image-rescaling note and a malformed-tool-call
	// nudge are all filed as user entries and none was typed. It sits below the
	// compaction check for the reason that check leads: a boundary stands in for
	// the conversation it replaced and has to keep its place in the sequence.
	// Older logs carry no marker, which is what the text markers below still
	// cover.
	if e.turnCompanion {
		return "", false
	}
	for _, m := range injectedMarkers {
		if strings.Contains(content, m) {
			return "", false
		}
	}
	// Fallback for logs written before Claude Code added isCompactSummary. It
	// matches Claude Code's own wording, so an upstream rewording silently stops
	// it firing — which on a flagged entry no longer matters, and on an unflagged
	// one would spill the whole summary into the prompt list.
	if strings.Contains(content, "This session is being continued from a previous conversation") {
		return compactSummaryPlaceholder, true
	}
	if strings.Contains(content, "<command-name>") {
		cmd, args := "?", ""
		if m := cmdNameRe.FindStringSubmatch(content); m != nil {
			cmd = m[1]
		}
		if m := cmdArgsRe.FindStringSubmatch(content); m != nil {
			args = strings.TrimSpace(m[1])
		}
		// Claude Code records command-name inconsistently — built-ins carry a
		// leading slash ("/clear"), custom commands do not ("sonar") — so strip any
		// leading slashes and add exactly one rather than doubling to "//clear".
		return strings.TrimRight("/"+strings.TrimLeft(cmd, "/")+" "+args, " "), true
	}
	if text := strings.TrimSpace(content); text != "" {
		return text, true
	}
	return "", false
}

// ── Event building ─────────────────────────────────────────────────────────

// buildEvents flattens an assistant stream into ordered events. seen holds the
// subagent ids already expanded on the current path, breaking reference cycles
// (a skill subagent can match itself by name).
func buildEvents(entries []entry, subs map[string]*subagent, seen map[string]bool) []model.Event {
	results := toolResultMap(entries)
	agents := agentIDMap(entries)
	skills := skillSidecarMap(entries)
	var out []model.Event
	for _, e := range entries {
		if e.typ != "assistant" {
			continue
		}
		for _, b := range e.blocks {
			switch b.typ {
			case "text":
				if strings.TrimSpace(b.text) != "" {
					out = append(out, model.Event{Kind: model.EventText, Text: b.text})
				}
			case "thinking":
				if strings.TrimSpace(b.thinking) != "" {
					out = append(out, model.Event{Kind: model.EventThinking, Text: b.thinking})
				}
			case "tool_use":
				res := results[b.id]
				tool := &model.Tool{
					Name: b.name,
					Args: formatToolArgs(b.name, b.input),
					// Same function the listing groups by, so the two paths cannot
					// drift into naming one call two things.
					Identity: toolIdentity(b.name, b.input),
					Model:    toolModel(b.input),
					Denial:   res.denial,
					Result:   res.text,
					IsError:  res.isError,
					Start:    e.t,
					End:      res.end,
				}
				attachSubagent(tool, b, agents, skills, subs, seen)
				out = append(out, model.Event{Kind: model.EventTool, Tool: tool})
			}
		}
	}
	return out
}

// attachSubagent fills tool.Subagent for Agent and forked-Skill calls that
// spawned a sidecar. Agent and forked-Skill links resolve by id (the structured
// agentId, see sidecarIDs); for a Skill with no id link it falls back to matching
// a sidecar by skill name (pre-structured forked logs). An inline skill — which
// runs in the main chain and writes no sidecar — matches nothing and renders as a
// leaf call, its work staying inline in the transcript.
func attachSubagent(tool *model.Tool, b block, agents, skills map[string]string, subs map[string]*subagent, seen map[string]bool) {
	key := ""
	switch b.name {
	case "Agent":
		key = agents[b.id]
	case "Skill":
		key = skills[b.id]
		if key == "" {
			if skill, _ := b.input["skill"].(string); skill != "" {
				for id, s := range subs {
					if s.skillName == skill {
						key = id
						break
					}
				}
			}
		}
	}
	if key == "" || seen[key] || subs[key] == nil {
		return
	}
	seen[key] = true
	tool.Subagent = buildEvents(subs[key].entries, subs, seen)
}

func turnMetrics(entries []entry, subs map[string]*subagent) (u model.Usage, tools, errs int) {
	results := toolResultMap(entries)
	agents := agentIDMap(entries)
	// One tally for the turn, because a response's blocks all sit inside the turn
	// that prompted it — a response never spans two turns, so deduplicating within
	// the turn loses nothing and double-counting here would reorder the summary.
	var t usageTally
	for _, e := range entries {
		if e.typ != "assistant" {
			continue
		}
		t.add(usageKey(e.requestID, e.uuid), e.usage)
		for _, b := range e.blocks {
			if b.typ != "tool_use" {
				continue
			}
			tools++
			if results[b.id].isError {
				errs++
			}
			if b.name == "Agent" {
				if id, ok := agents[b.id]; ok {
					// Added to the running total rather than through the tally: a
					// subagent's tokens belong to no response of this turn, and its own
					// entries were already deduplicated inside their sidecar.
					u.Add(totalAgentUsage(id, subs, map[string]bool{}))
				}
			}
		}
	}
	u.Add(t.total)
	return u, tools, errs
}

// formatToolArgs is a short one-line summary of a tool call's input.
func formatToolArgs(name string, input map[string]any) string {
	get := func(k string) string {
		s, _ := input[k].(string)
		return s
	}
	switch name {
	case "Bash":
		return get("command")
	case "Read", "Write", "Edit":
		return get("file_path")
	case "Grep", "Glob":
		return get("pattern")
	case "Skill":
		return strings.TrimSpace(get("skill") + " " + get("args"))
	case "Agent":
		return get("description")
	case "WebFetch":
		return get("url")
	case "WebSearch", "ToolSearch":
		return get("query")
	default:
		if len(input) == 0 {
			return ""
		}
		b, _ := json.Marshal(input)
		return string(b)
	}
}
