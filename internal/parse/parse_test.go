package parse

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eitanpo/agentry/internal/model"
)

func TestSummarize(t *testing.T) {
	s, err := Summarize(filepath.Join("testdata", "sample.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "sample" {
		t.Errorf("ID = %q, want sample", s.ID)
	}
	if s.NumTurns != 2 {
		t.Errorf("NumTurns = %d, want 2", s.NumTurns)
	}
	if s.Title != "first prompt" {
		t.Errorf("Title = %q, want %q", s.Title, "first prompt")
	}
	wantPrompts := []string{"first prompt", "second prompt"}
	if len(s.Prompts) != len(wantPrompts) {
		t.Fatalf("Prompts = %v, want %v", s.Prompts, wantPrompts)
	}
	for i, w := range wantPrompts {
		if s.Prompts[i] != w {
			t.Errorf("Prompts[%d] = %q, want %q", i, s.Prompts[i], w)
		}
	}
	wantStart := time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 5, 27, 10, 1, 3, 0, time.UTC)
	if !s.Start.Equal(wantStart) {
		t.Errorf("Start = %v, want %v", s.Start, wantStart)
	}
	if !s.End.Equal(wantEnd) {
		t.Errorf("End = %v, want %v", s.End, wantEnd)
	}
	// sample.jsonl has one Bash (ls -la) and one Read.
	assertToolStats(t, s.Tools, []model.ToolStat{
		{Tool: "Bash", Identity: "ls", Count: 1},
		{Tool: "Read", Identity: "", Count: 1},
	})
}

func TestSummarizeToolStats(t *testing.T) {
	s, err := Summarize(filepath.Join("testdata", "tools.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// git ×2 (status + push), exa ×1, jq ×1 (after leading VAR= assignments),
	// Skill expert ×1, Agent researcher ×2, Edit ×1. Order is first-seen.
	// Edit carries its target path: a summary saying a session edited something
	// and never what cannot tell one kind of work from another.
	assertToolStats(t, s.Tools, []model.ToolStat{
		{Tool: "Bash", Identity: "git", Count: 2},
		{Tool: "Bash", Identity: "exa", Count: 1},
		{Tool: "Skill", Identity: "expert", Count: 1},
		{Tool: "Agent", Identity: "researcher", Count: 2},
		{Tool: "Edit", Identity: "/a/b.go", Count: 1},
		{Tool: "Bash", Identity: "jq", Count: 1},
	})
	// Commands are the distinct full Bash commands, first-seen order, for
	// --used-command / --used substring matching.
	wantCmds := []string{
		"git status",
		"/Users/x/.claude/skills/exa/scripts/exa --contents -n 5 query",
		"git push origin main",
		"FOO=1 BAR=2 jq . file.json",
	}
	if len(s.Commands) != len(wantCmds) {
		t.Fatalf("Commands = %q, want %q", s.Commands, wantCmds)
	}
	for i, w := range wantCmds {
		if s.Commands[i] != w {
			t.Errorf("Commands[%d] = %q, want %q", i, s.Commands[i], w)
		}
	}
}

// TestSummarizeDenials pins the outcome a summary could not report: which calls
// were refused and by what. A denied call errors like any other, so without the
// kind it is indistinguishable from one that ran and failed — and the fix for
// each is different.
func TestSummarizeDenials(t *testing.T) {
	s, err := Summarize(filepath.Join("testdata", "outcomes.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// Two Bash/rm denials collapse into one entry; the user-rejected git call is
	// a separate kind and stays separate. First-seen order.
	want := []model.DenialStat{
		{Kind: "permission-rule", Tool: "Bash", Identity: "rm", Count: 2},
		{Kind: "user-rejected", Tool: "Bash", Identity: "git", Count: 1},
	}
	if len(s.Denials) != len(want) {
		t.Fatalf("Denials = %+v, want %+v", s.Denials, want)
	}
	for i := range want {
		if s.Denials[i] != want[i] {
			t.Errorf("Denials[%d] = %+v, want %+v", i, s.Denials[i], want[i])
		}
	}
	// A call that ran is in Tools and in no denial entry — the two are different
	// questions about the same session, not one tally.
	assertToolStats(t, s.Tools, []model.ToolStat{
		{Tool: "Bash", Identity: "rm", Count: 2},
		{Tool: "Edit", Identity: "/repo/internal/list/list.go", Count: 1},
		{Tool: "Write", Identity: "/repo/docs/notes.md", Count: 1},
		{Tool: "Bash", Identity: "git", Count: 1},
	})
}

// TestSummarizeFiles pins the session-level record of what changed. The log
// mixes path forms — relative to the working directory inside it, absolute
// outside — so a reader grouping by path gets two spellings of one file unless
// they are resolved first.
func TestSummarizeFiles(t *testing.T) {
	s, err := Summarize(filepath.Join("testdata", "outcomes.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// list.go appears twice in the log (a delta, then the snapshot) and once
	// here; the snapshot's relative paths are resolved against cwd, and the
	// absolute one is left alone. Order is first-seen across entries — the delta
	// precedes the snapshot — and within the one snapshot, whose backups are a
	// map with no order of its own, sorted so a session reads the same every run.
	want := []string{
		"/repo/internal/list/list.go",
		"/elsewhere/shared.md",
		"/repo/docs/notes.md",
	}
	if len(s.Files) != len(want) {
		t.Fatalf("Files = %q, want %q", s.Files, want)
	}
	for i := range want {
		if s.Files[i] != want[i] {
			t.Errorf("Files[%d] = %q, want %q", i, s.Files[i], want[i])
		}
	}
}

// TestSummarizeFilesWithoutHistory pins the absence case: a session whose log
// carries no file-history entries reports no files. Claiming it changed nothing
// would be a different, unsupported statement — roughly half of local sessions
// have no such entries at all.
func TestSummarizeFilesWithoutHistory(t *testing.T) {
	s, err := Summarize(filepath.Join("testdata", "tools.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Files) != 0 {
		t.Errorf("Files = %q, want none", s.Files)
	}
	if len(s.Denials) != 0 {
		t.Errorf("Denials = %+v, want none", s.Denials)
	}
}

func TestBashProgram(t *testing.T) {
	tests := []struct{ cmd, want string }{
		{"ls -la", "ls"},
		{"git push origin main", "git"},
		{"/Users/x/.claude/skills/exa/scripts/exa --contents q", "exa"},
		{"FOO=1 BAR=2 jq . f.json", "jq"},
		{"   ", ""},
		{"", ""},
		{"=notassign cmd", "=notassign"}, // leading '=' is not a VAR= assignment
	}
	for _, tt := range tests {
		if got := bashProgram(tt.cmd); got != tt.want {
			t.Errorf("bashProgram(%q) = %q, want %q", tt.cmd, got, tt.want)
		}
	}
}

func assertToolStats(t *testing.T, got, want []model.ToolStat) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("ToolStats = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ToolStats[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSummarizePrefersAITitle(t *testing.T) {
	s, err := Summarize(filepath.Join("testdata", "ai-title.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// The latest ai-title wins over the first prompt and over an earlier ai-title.
	if s.Title != "Refactor the widget pipeline and add tests" {
		t.Errorf("Title = %q, want the latest ai-title", s.Title)
	}
}

func TestSummarizePrefersCustomTitle(t *testing.T) {
	s, err := Summarize(filepath.Join("testdata", "custom-title.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// A manual rename (custom-title) wins over the ai-title, which Claude Code
	// freezes at its stale pre-rename value once a custom title is set.
	if s.Title != "widgets" {
		t.Errorf("Title = %q, want the custom-title", s.Title)
	}
}

// A session named with --name or /rename carries an agent-name and, when that is
// the only naming that happened, no custom-title. Reading only custom-title falls
// through to the ai-title and shows a name the user never chose.
func TestSummarizePrefersAgentNameOverAITitle(t *testing.T) {
	s, err := Summarize(filepath.Join("testdata", "agent-name.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "cloudsmith" {
		t.Errorf("Title = %q, want the agent-name", s.Title)
	}
}

// custom-title and agent-name are both names the user chose, so neither wins by
// kind — the later entry wins. Both orderings are asserted because a ladder that
// simply ranks one type above the other passes exactly one of them.
func TestSummarizeManualTitleLastWins(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		want    string
	}{
		{"agent-name then custom-title", "manual-title-order.jsonl", "renamed later"},
		{"custom-title then agent-name", "manual-title-order-reversed.jsonl", "named later"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := Summarize(filepath.Join("testdata", tt.fixture))
			if err != nil {
				t.Fatal(err)
			}
			if s.Title != tt.want {
				t.Errorf("Title = %q, want %q — the last manual title in the file", s.Title, tt.want)
			}
		})
	}
}

func TestSummarizeSkipsLeadingClear(t *testing.T) {
	s, err := Summarize(filepath.Join("testdata", "clear-start.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// /clear is the first turn but is skipped: the title is the next prompt.
	if s.Title != "actually fix the parser" {
		t.Errorf("Title = %q, want %q", s.Title, "actually fix the parser")
	}
	// The /clear turn still counts toward the turn total.
	if s.NumTurns != 2 {
		t.Errorf("NumTurns = %d, want 2", s.NumTurns)
	}
	// /clear is omitted from the prompt list, leaving only the real prompt.
	if len(s.Prompts) != 1 || s.Prompts[0] != "actually fix the parser" {
		t.Errorf("Prompts = %v, want [actually fix the parser]", s.Prompts)
	}
}

func TestSummarizeRootUUID(t *testing.T) {
	s, err := Summarize(filepath.Join("testdata", "rooted.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// RootUUID is the first entry's uuid — the conversation root that a fork
	// copies verbatim, so it keys the fork family.
	if s.RootUUID != "root-aaa" {
		t.Errorf("RootUUID = %q, want %q", s.RootUUID, "root-aaa")
	}
	// Born is read from the file (birthtime on macOS, else mtime); it must be set
	// so a fork family can be ordered. The testdata file exists, so it is non-zero.
	if s.Born.IsZero() {
		t.Error("Born is zero, want the file's creation/modification time")
	}
}

// TestSummarizeCwd pins the field a cross-project listing is read by: without
// it every row names a session and not where it ran. The fixture opens with a
// meta entry that carries no cwd, so taking the first entry's value
// unconditionally would report none.
func TestSummarizeCwd(t *testing.T) {
	s, err := Summarize(filepath.Join("testdata", "cwd.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Cwd != "/Users/me/Projects/me/agentry" {
		t.Errorf("Cwd = %q, want %q", s.Cwd, "/Users/me/Projects/me/agentry")
	}
}

// TestSummarizeEntrypoint pins how a session resumed in another client is
// resolved: last value wins (matching the last-activity time the listing orders
// by), and every distinct value is kept in first-seen order so the JSON does not
// lose what the text table compresses to a "+".
func TestSummarizeEntrypoint(t *testing.T) {
	t.Run("resumed session keeps both, resolves to the last", func(t *testing.T) {
		s, err := Summarize(filepath.Join("testdata", "entrypoint-resumed.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if s.Entrypoint != "cli" {
			t.Errorf("Entrypoint = %q, want %q (the last value)", s.Entrypoint, "cli")
		}
		want := []string{"claude-desktop", "cli"}
		if len(s.Entrypoints) != 2 || s.Entrypoints[0] != want[0] || s.Entrypoints[1] != want[1] {
			t.Errorf("Entrypoints = %v, want %v (first-seen order)", s.Entrypoints, want)
		}
	})

	t.Run("single-entrypoint session lists none", func(t *testing.T) {
		// Entrypoints exists to record divergence; repeating a single value would
		// put a redundant array on every session in the JSON. Asserting Entrypoint
		// too is what keeps this honest — a fixture carrying no entrypoint at all
		// would satisfy the nil check for the wrong reason.
		s, err := Summarize(filepath.Join("testdata", "entrypoint-single.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if s.Entrypoint != "cli" {
			t.Fatalf("Entrypoint = %q, want %q — the fixture must actually carry one", s.Entrypoint, "cli")
		}
		if s.Entrypoints != nil {
			t.Errorf("Entrypoints = %v, want nil for a session with one value", s.Entrypoints)
		}
	})

	t.Run("a session with no entrypoint at all is not an error", func(t *testing.T) {
		// Logs written before Claude Code added the field carry none, and the
		// format doc requires older sessions keep rendering.
		s, err := Summarize(filepath.Join("testdata", "cwd.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if s.Entrypoint != "" || s.Entrypoints != nil {
			t.Errorf("Entrypoint = %q, Entrypoints = %v; want both empty", s.Entrypoint, s.Entrypoints)
		}
	})
}

func TestIsClearCmd(t *testing.T) {
	clear := []string{"//clear", "/clear", "  //clear  ", "clear",
		"/clear improve the parser", "//clear do the thing"}
	notClear := []string{"//clear-cache", "/research-lookup x", "clear the table", ""}
	for _, p := range clear {
		if !isClearCmd(p) {
			t.Errorf("isClearCmd(%q) = false, want true", p)
		}
	}
	for _, p := range notClear {
		if isClearCmd(p) {
			t.Errorf("isClearCmd(%q) = true, want false", p)
		}
	}
}

func TestLoad(t *testing.T) {
	sess, err := Load(filepath.Join("testdata", "sample.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	if sess.Meta.Model != "claude-opus-4-7" {
		t.Errorf("model = %q, want claude-opus-4-7", sess.Meta.Model)
	}
	// Usage sums across both assistant entries.
	wantUsage := model.Usage{Input: 14, Output: 28, CacheRead: 5, CacheCreate: 3}
	if sess.Meta.Usage != wantUsage {
		t.Errorf("usage = %+v, want %+v", sess.Meta.Usage, wantUsage)
	}
	if sess.Meta.NumSubagents != 0 {
		t.Errorf("subagents = %d, want 0", sess.Meta.NumSubagents)
	}

	// The injected <bash-input> and <task-notification> entries must not start
	// their own turns.
	if len(sess.Turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(sess.Turns))
	}

	turn0 := sess.Turns[0]
	if turn0.Prompt != "first prompt" {
		t.Errorf("turn0 prompt = %q, want %q", turn0.Prompt, "first prompt")
	}
	if turn0.ToolCount != 1 || turn0.ErrorCount != 0 {
		t.Errorf("turn0 tools=%d errors=%d, want 1/0", turn0.ToolCount, turn0.ErrorCount)
	}
	kinds := eventKinds(turn0.Events)
	wantKinds := []model.EventKind{model.EventThinking, model.EventText, model.EventTool}
	if !equalKinds(kinds, wantKinds) {
		t.Errorf("turn0 event kinds = %v, want %v", kinds, wantKinds)
	}
	tool := lastTool(turn0.Events)
	if tool == nil {
		t.Fatal("turn0 has no tool event")
	}
	if tool.Name != "Bash" || tool.Args != "ls -la" {
		t.Errorf("tool = %q(%q), want Bash(ls -la)", tool.Name, tool.Args)
	}
	if tool.Result != "file listing output" || tool.IsError {
		t.Errorf("tool result=%q err=%v, want non-error file listing", tool.Result, tool.IsError)
	}

	turn1 := sess.Turns[1]
	if turn1.Prompt != "second prompt" {
		t.Errorf("turn1 prompt = %q, want %q", turn1.Prompt, "second prompt")
	}
	if turn1.ErrorCount != 1 {
		t.Errorf("turn1 errors = %d, want 1", turn1.ErrorCount)
	}
	errTool := lastTool(turn1.Events)
	if errTool == nil || !errTool.IsError || errTool.Result != "file not found" {
		t.Errorf("turn1 error tool = %+v, want Read error 'file not found'", errTool)
	}
}

// TestLoadStitching pins subagent stitching across both the structured
// toolUseResult.agentId key and the legacy fallbacks, so a regression in either
// path is caught. The session wires four spawning calls to sidecars plus one
// inline skill that must stay a leaf:
//   - Agent  via toolUseResult.agentId (result text has no agentId line)
//   - Agent  via the legacy "agentId:" result line (no toolUseResult)
//   - Skill  forked via toolUseResult.agentId
//   - Skill  forked via legacy skill-name match (toolUseResult is a bare string)
//   - Skill  inline ("Launching skill") with no sidecar → no expansion
func TestLoadStitching(t *testing.T) {
	sess, err := Load(filepath.Join("testdata", "stitch.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if sess.Meta.NumSubagents != 4 {
		t.Errorf("subagents = %d, want 4", sess.Meta.NumSubagents)
	}
	if len(sess.Turns) != 1 {
		t.Fatalf("turns = %d, want 1", len(sess.Turns))
	}

	var tools []*model.Tool
	for _, e := range sess.Turns[0].Events {
		if e.Kind == model.EventTool {
			tools = append(tools, e.Tool)
		}
	}
	if len(tools) != 5 {
		t.Fatalf("tool events = %d, want 5", len(tools))
	}

	// firstText returns the first text event of a tool's expansion, or "" if the
	// tool has no subagent attached.
	firstText := func(tool *model.Tool) string {
		for _, e := range tool.Subagent {
			if e.Kind == model.EventText {
				return e.Text
			}
		}
		return ""
	}

	cases := []struct {
		idx      int
		wantText string // "" means: expect no expansion
	}{
		{0, "explorer aaa1 work"}, // Agent, structured agentId
		{1, "explorer aaa2 work"}, // Agent, legacy agentId line
		{2, "alpha work"},         // Skill, structured agentId
		{3, "beta work"},          // Skill, legacy name match
		{4, ""},                   // Skill, inline — leaf, no sidecar
	}
	for _, c := range cases {
		got := firstText(tools[c.idx])
		if got != c.wantText {
			t.Errorf("tool[%d] %s(%s): expansion first text = %q, want %q",
				c.idx, tools[c.idx].Name, tools[c.idx].Args, got, c.wantText)
		}
	}
	if tools[4].Subagent != nil {
		t.Errorf("inline skill tool[4] has %d subagent events, want none", len(tools[4].Subagent))
	}
}

// TestLoadEffort pins the setting that separates two sessions on the same model:
// how hard it was run. Effort moves both cost and quality, and no output named
// it before, so those two sessions were indistinguishable in every view.
func TestLoadEffort(t *testing.T) {
	t.Run("a session that changed effort keeps both values", func(t *testing.T) {
		// Rare but real: 1 of 136 local sessions carrying the field does this.
		sess, err := Load(filepath.Join("testdata", "effort-changed.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		// The resolved value is the last, matching how the entrypoint resolves and
		// what the session's most recent activity actually ran at.
		if sess.Meta.Effort != "high" {
			t.Errorf("Effort = %q, want the last value", sess.Meta.Effort)
		}
		want := []string{"xhigh", "high"}
		if len(sess.Meta.Efforts) != len(want) {
			t.Fatalf("Efforts = %q, want %q", sess.Meta.Efforts, want)
		}
		for i := range want {
			if sess.Meta.Efforts[i] != want[i] {
				t.Errorf("Efforts[%d] = %q, want %q", i, sess.Meta.Efforts[i], want[i])
			}
		}
	})

	t.Run("a session predating the field reports none", func(t *testing.T) {
		// About half of local sessions have no effort at all. Reporting a default
		// would state a setting the log does not record.
		sess, err := Load(filepath.Join("testdata", "sample.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if sess.Meta.Effort != "" || sess.Meta.Efforts != nil {
			t.Errorf("Effort = %q / %q, want neither", sess.Meta.Effort, sess.Meta.Efforts)
		}
	})
}

// TestLoadCarriesDelegation pins the structured facts a rendered Agent call used
// to lose. Args flattens an Agent's input to its human description, so before
// this the subagent type and the delegated model were unrecoverable from a
// rendered session — and a cost audit asking what a subagent ran on had nowhere
// to read it.
func TestLoadCarriesDelegation(t *testing.T) {
	sess, err := Load(filepath.Join("testdata", "agent-delegation.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Turns) != 1 {
		t.Fatalf("turns = %d, want 1", len(sess.Turns))
	}
	var tools []*model.Tool
	for _, e := range sess.Turns[0].Events {
		if e.Kind == model.EventTool {
			tools = append(tools, e.Tool)
		}
	}
	if len(tools) != 4 {
		t.Fatalf("tool events = %d, want 4", len(tools))
	}

	cases := []struct {
		what             string
		tool             *model.Tool
		identity, model_ string
	}{
		// The audit case: both facts named, neither derivable from args.
		{"an Agent naming type and model", tools[0], "Explore", "haiku"},
		// No model named means the subagent inherited the session's. Defaulting to
		// Meta.Model here would report a choice the caller never made.
		{"an Agent naming no model", tools[1], "researcher", ""},
		// subagent_type is optional in the log (17 of 543 local calls omit it);
		// agentry reports it absent rather than guessing the harness default.
		{"an Agent naming no type", tools[2], "", "sonnet"},
		// Identity is not Agent-only: it is the same label the listing groups by,
		// which is what stops the two paths naming one call two things.
		{"a Bash call", tools[3], "git", ""},
	}
	for _, c := range cases {
		if c.tool.Identity != c.identity {
			t.Errorf("%s: identity = %q, want %q", c.what, c.tool.Identity, c.identity)
		}
		if c.tool.Model != c.model_ {
			t.Errorf("%s: model = %q, want %q", c.what, c.tool.Model, c.model_)
		}
	}
	// Args keeps being the human summary — the new fields are additions to it,
	// not a redefinition, so anything reading args still sees what it did.
	if tools[0].Args != "sweep for callers" {
		t.Errorf("args = %q, want the description", tools[0].Args)
	}
}

func TestUserPrompt(t *testing.T) {
	tests := []struct {
		name   string
		entry  entry
		want   string
		wantOK bool
	}{
		{"typed", entry{hasStr: true, contentStr: "hello"}, "hello", true},
		{"bash injected", entry{hasStr: true, contentStr: "<bash-input>x</bash-input>"}, "", false},
		{"skill injected", entry{hasStr: true, contentStr: "Base directory for this skill: /x"}, "", false},
		{"command", entry{hasStr: true, contentStr: "<command-name>foo</command-name><command-args>bar</command-args>"}, "/foo bar", true},
		{"command name with slash not doubled", entry{hasStr: true, contentStr: "<command-name>/clear</command-name><command-args>fix it</command-args>"}, "/clear fix it", true},
		{"compaction by text, pre-flag logs", entry{hasStr: true, contentStr: "...This session is being continued from a previous conversation..."}, compactSummaryPlaceholder, true},
		// The flag alone must be enough: this body is what an upstream rewording of
		// the summary looks like, and the text match cannot see it.
		{"compaction by flag, reworded body", entry{hasStr: true, isCompactSummary: true, contentStr: "Picking up where the last context left off. Summary follows."}, compactSummaryPlaceholder, true},
		// A summary quoting an injected marker must still read as the boundary
		// rather than being dropped as injected content.
		{"compaction by flag, body quotes an injected marker", entry{hasStr: true, isCompactSummary: true, contentStr: "Earlier the user ran <bash-input>ls</bash-input> and then asked for a fix."}, compactSummaryPlaceholder, true},
		{"empty", entry{hasStr: true, contentStr: "   "}, "", false},
		{"array content not a prompt", entry{hasStr: false}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := userPrompt(tt.entry)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("got (%q, %v), want (%q, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestFormatToolArgs(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"Bash", map[string]any{"command": "ls"}, "ls"},
		{"Read", map[string]any{"file_path": "/a"}, "/a"},
		{"Grep", map[string]any{"pattern": "x"}, "x"},
		{"Skill", map[string]any{"skill": "s", "args": "a"}, "s a"},
		{"Unknown", map[string]any{"foo": "bar"}, `{"foo":"bar"}`},
		{"Unknown empty", map[string]any{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatToolArgs(tt.name, tt.input); got != tt.want {
				t.Errorf("formatToolArgs(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func eventKinds(events []model.Event) []model.EventKind {
	out := make([]model.EventKind, len(events))
	for i, e := range events {
		out[i] = e.Kind
	}
	return out
}

func equalKinds(a, b []model.EventKind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func lastTool(events []model.Event) *model.Tool {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == model.EventTool {
			return events[i].Tool
		}
	}
	return nil
}

// TestModelResolution pins how a session's model is read. It used to be the
// first assistant entry's, which misreports a session that switched: 13 of 250
// local sessions did, and the header then names a model the session left.
func TestModelResolution(t *testing.T) {
	t.Run("a session that switched keeps both, resolving to the last", func(t *testing.T) {
		sess, err := Load(filepath.Join("testdata", "model-changed.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if sess.Meta.Model != "claude-opus-5" {
			t.Errorf("Model = %q, want the last one the session ran on", sess.Meta.Model)
		}
		want := []string{"claude-sonnet-5", "claude-opus-5"}
		if !equalStrings(sess.Meta.Models, want) {
			t.Errorf("Models = %q, want %q", sess.Meta.Models, want)
		}
	})

	t.Run("<synthetic> is not a model", func(t *testing.T) {
		// Claude Code writes it on messages it composed itself — an API-error
		// notice, a session-limit warning. The fixture ends on one, which is the
		// shape that matters: 17 of 250 local sessions carry such an entry, and
		// counting it would end each of them on a model that never ran.
		sess, err := Load(filepath.Join("testdata", "model-changed.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if sess.Meta.Model == syntheticModel {
			t.Errorf("Model = %q, which names no model the session ran on", sess.Meta.Model)
		}
		for _, m := range sess.Meta.Models {
			if m == syntheticModel {
				t.Errorf("Models = %q, want %q excluded", sess.Meta.Models, syntheticModel)
			}
		}
	})

	t.Run("a session naming no model reports none", func(t *testing.T) {
		// It used to report the word "unknown", which asserted a fact the log does
		// not carry. Effort and entrypoint already say nothing in this case.
		sess, err := Load(filepath.Join("testdata", "rooted.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if sess.Meta.Model != "" || sess.Meta.Models != nil {
			t.Errorf("Model = %q / %q, want neither", sess.Meta.Model, sess.Meta.Models)
		}
	})
}

// TestSummarizeCarriesRun pins that a listing knows what a session ran on. The
// render path named the model and effort while a Summary carried neither, so
// "which sessions ran at xhigh" had no answer short of rendering each one.
func TestSummarizeCarriesRun(t *testing.T) {
	t.Run("model and its trail", func(t *testing.T) {
		s, err := Summarize(filepath.Join("testdata", "model-changed.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if s.Model != "claude-opus-5" {
			t.Errorf("Model = %q, want the last one", s.Model)
		}
		want := []string{"claude-sonnet-5", "claude-opus-5"}
		if !equalStrings(s.Models, want) {
			t.Errorf("Models = %q, want %q", s.Models, want)
		}
	})

	t.Run("effort and its trail", func(t *testing.T) {
		s, err := Summarize(filepath.Join("testdata", "effort-changed.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if s.Effort != "high" {
			t.Errorf("Effort = %q, want the last one", s.Effort)
		}
		want := []string{"xhigh", "high"}
		if !equalStrings(s.Efforts, want) {
			t.Errorf("Efforts = %q, want %q", s.Efforts, want)
		}
	})

	t.Run("a summary agrees with the rendered session", func(t *testing.T) {
		// The two paths read one session, so they must not name two different
		// models: that disagreement is the whole reason this field exists.
		path := filepath.Join("testdata", "model-changed.jsonl")
		s, err := Summarize(path)
		if err != nil {
			t.Fatal(err)
		}
		sess, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if s.Model != sess.Meta.Model || !equalStrings(s.Models, sess.Meta.Models) {
			t.Errorf("summary %q/%q vs meta %q/%q", s.Model, s.Models, sess.Meta.Model, sess.Meta.Models)
		}
	})
}

// TestSummarizeUsageIncludesSubagents pins that a listing's token tally covers
// delegated work. Summarize otherwise never opens a sidecar, so the natural
// implementation counts the main thread alone — and undercounts exactly the
// sessions that cost the most, while Meta.Usage reports the full figure.
func TestSummarizeUsageIncludesSubagents(t *testing.T) {
	path := filepath.Join("testdata", "subagent-usage.jsonl")
	s, err := Summarize(path)
	if err != nil {
		t.Fatal(err)
	}
	want := model.Usage{Input: 110, Output: 220, CacheRead: 55, CacheCreate: 33}
	if s.Usage != want {
		t.Errorf("Usage = %+v, want %+v (main thread plus the agent-u1 sidecar)", s.Usage, want)
	}

	sess, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Usage != sess.Meta.Usage {
		t.Errorf("summary usage %+v != meta usage %+v; a cost read off a listing must match one read off a render", s.Usage, sess.Meta.Usage)
	}
}

// TestSummarizeOutputs pins how a session's outputs are read. Claude Code
// re-records a pr-link or frame-link entry on later turns, so the natural
// implementation — collect every entry — reports one pull request as many, and
// the fixture's five pr-link entries name three pull requests.
func TestSummarizeOutputs(t *testing.T) {
	s, err := Summarize(filepath.Join("testdata", "outputs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("pull requests are deduplicated in first-seen order", func(t *testing.T) {
		want := []model.PR{
			{Repository: "eitanpo/central", Number: 14, URL: "https://github.com/eitanpo/central/pull/14"},
			{Repository: "eitanpo/central", Number: 27, URL: "https://github.com/eitanpo/central/pull/27"},
			{Repository: "wix-private/devex-costs", Number: 3, URL: "https://github.com/wix-private/devex-costs/pull/3"},
		}
		if len(s.PRs) != len(want) {
			t.Fatalf("PRs = %+v, want %d entries", s.PRs, len(want))
		}
		for i := range want {
			if s.PRs[i] != want[i] {
				t.Errorf("PRs[%d] = %+v, want %+v", i, s.PRs[i], want[i])
			}
		}
	})

	t.Run("an artifact republished from a moved file stays one artifact", func(t *testing.T) {
		// The fixture publishes artifact aaa from a scratchpad file, then from a
		// path inside the repository. Keying on the path would report two.
		if len(s.Artifacts) != 2 {
			t.Fatalf("Artifacts = %+v, want 2", s.Artifacts)
		}
		got := s.Artifacts[0]
		if got.URL != "https://claude.ai/code/artifact/aaa" {
			t.Errorf("first artifact URL = %q", got.URL)
		}
		// The later record wins on the path it names...
		if got.Path != "/repo/reports/cost.html" {
			t.Errorf("Path = %q, want the later publish's path", got.Path)
		}
		// ...but says nothing about the title, and an omission is not a deletion.
		if got.Title != "Cost report" {
			t.Errorf("Title = %q, want the earlier record's title to survive", got.Title)
		}
	})

	t.Run("a summary agrees with the rendered session", func(t *testing.T) {
		// The two paths read one session, so they must not name different outputs:
		// a pull request visible in the listing and absent from the render is the
		// disagreement carrying these onto Meta was meant to end.
		sess, err := Load(filepath.Join("testdata", "outputs.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(s.PRs, sess.Meta.PRs) {
			t.Errorf("summary PRs %+v vs meta %+v", s.PRs, sess.Meta.PRs)
		}
		if !slices.Equal(s.Artifacts, sess.Meta.Artifacts) {
			t.Errorf("summary artifacts %+v vs meta %+v", s.Artifacts, sess.Meta.Artifacts)
		}
	})
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSummarizeReplies pins the corpus --reply-matches tests: the main thread's
// assistant text, and nothing else. Each exclusion here is a way the filter
// would otherwise answer a question about the reply with something that was not
// one.
func TestSummarizeReplies(t *testing.T) {
	s, err := Summarize(filepath.Join("testdata", "sample.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("text blocks are carried in order, one entry per block", func(t *testing.T) {
		// One entry per block rather than one joined string, so a pattern's ^ and $
		// anchor to a single reply.
		want := []string{"here is an answer", "trying to read"}
		if !equalStrings(s.Replies, want) {
			t.Errorf("Replies = %q, want %q", s.Replies, want)
		}
	})

	t.Run("thinking is not a reply", func(t *testing.T) {
		// sample.jsonl's first assistant entry thinks "let me think" before
		// answering. A rule about what a reply said must not be satisfied by a
		// thought the user never saw.
		for _, r := range s.Replies {
			if strings.Contains(r, "let me think") {
				t.Errorf("a thinking block was carried as a reply: %q", r)
			}
		}
	})

	t.Run("a subagent's reply is not the session's", func(t *testing.T) {
		// Sidecars are opened only for the token tally. --reply-matches is a
		// top-level filter like the --used* family, and this is the observable
		// that keeps it one.
		sub, err := Summarize(filepath.Join("testdata", "subagent-usage.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range sub.Replies {
			if strings.Contains(r, "found it") {
				t.Errorf("a sidecar reply was carried: %q", r)
			}
		}
	})
}

// TestUsageCountsEachResponseOnce pins the rule that separates a token tally
// from a line count. Claude Code splits one API response across an assistant
// entry per content block and writes that response's whole usage object on every
// one of them, so the obvious implementation — add up every assistant entry —
// reports a reply three times over. The fixture's first response spans three
// entries carrying identical usage; a regression reads 300 output tokens where
// the response produced 100.
func TestUsageCountsEachResponseOnce(t *testing.T) {
	path := filepath.Join("testdata", "blocked-usage.jsonl")
	s, err := Summarize(path)
	if err != nil {
		t.Fatal(err)
	}
	// Main thread: req_A once (10/100/40/4), req_B once (1/7/2/0), and the
	// synthetic entry that names no response, keyed by its own uuid (3/0/0/0).
	// The sidecar's two entries are one response (1000/2000/500/300).
	want := model.Usage{Input: 1014, Output: 2107, CacheRead: 542, CacheCreate: 304}
	if s.Usage != want {
		t.Errorf("Usage = %+v, want %+v", s.Usage, want)
	}

	sess, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Meta.Usage != want {
		t.Errorf("Meta.Usage = %+v, want %+v; the render path must count responses the way a listing does", sess.Meta.Usage, want)
	}
	// The per-turn tally is the surface that orders the summary, so it dedupes
	// too — a turn inflated by its block count would sort above turns that cost
	// more. Subagents are excluded here: this fixture spawns none.
	if len(sess.Turns) != 1 {
		t.Fatalf("Turns = %d, want 1", len(sess.Turns))
	}
	wantTurn := model.Usage{Input: 14, Output: 107, CacheRead: 42, CacheCreate: 4}
	if sess.Turns[0].Usage != wantTurn {
		t.Errorf("turn Usage = %+v, want %+v", sess.Turns[0].Usage, wantTurn)
	}
}

// TestUsageKeylessEntriesEachCount covers the case the fixtures cannot reach: an
// assistant entry naming neither a response nor itself. Deduplicating on an empty
// key would collapse every such entry into the first, silently undercounting a
// log old enough to carry neither field.
func TestUsageKeylessEntriesEachCount(t *testing.T) {
	entries := []entry{
		{typ: "assistant", usage: model.Usage{Output: 5}},
		{typ: "assistant", usage: model.Usage{Output: 7}},
	}
	if got := sumUsage(entries); got.Output != 12 {
		t.Errorf("Output = %d, want 12; an entry with no identity has nothing to deduplicate against", got.Output)
	}
}
