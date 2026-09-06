package render

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/eitanpo/agentry/internal/model"
)

// TestSessionJSON pins the --format json shape: the full model, event kinds as
// strings not ordinals, nested subagent streams, and isError elided when false.
func TestSessionJSON(t *testing.T) {
	sess := &model.Session{
		Meta: model.Meta{ID: "s1", Model: "claude-opus-4-8", Usage: model.Usage{Input: 10, Output: 20}},
		Turns: []model.Turn{{
			Prompt:    "do it",
			ToolCount: 2,
			Events: []model.Event{
				{Kind: model.EventText, Text: "sure"},
				{Kind: model.EventThinking, Text: "hmm"},
				{Kind: model.EventTool, Tool: &model.Tool{
					Name: "Bash", Args: "ls", Result: "boom", IsError: true,
					Subagent: []model.Event{{Kind: model.EventText, Text: "child"}},
				}},
				{Kind: model.EventTool, Tool: &model.Tool{Name: "Read"}},
			},
		}},
	}
	var b strings.Builder
	if err := SessionJSON(&b, sess); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(b.String()), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, b.String())
	}

	meta := got["meta"].(map[string]any)
	if meta["id"] != "s1" || meta["model"] != "claude-opus-4-8" {
		t.Errorf("meta wrong: %s", b.String())
	}
	if usage := meta["usage"].(map[string]any); usage["input"].(float64) != 10 || usage["output"].(float64) != 20 {
		t.Errorf("usage wrong: %s", b.String())
	}

	turns := got["turns"].([]any)
	if len(turns) != 1 {
		t.Fatalf("want 1 turn, got %d: %s", len(turns), b.String())
	}
	events := turns[0].(map[string]any)["events"].([]any)
	if len(events) != 4 {
		t.Fatalf("want 4 events, got %d: %s", len(events), b.String())
	}
	// Event kinds serialize as stable strings, not iota ordinals.
	kinds := []string{"text", "thinking", "tool", "tool"}
	for i, want := range kinds {
		if k := events[i].(map[string]any)["kind"]; k != want {
			t.Errorf("event %d kind = %v, want %q", i, k, want)
		}
	}
	// The erroring tool carries its result, isError=true, and a nested stream.
	tool := events[2].(map[string]any)["tool"].(map[string]any)
	if tool["name"] != "Bash" || tool["isError"] != true || tool["result"] != "boom" {
		t.Errorf("tool wrong: %s", b.String())
	}
	sub := tool["subagent"].([]any)
	if len(sub) != 1 || sub[0].(map[string]any)["kind"] != "text" {
		t.Errorf("subagent stream wrong: %s", b.String())
	}
	// A non-erroring tool omits isError entirely (false is elided).
	okTool := events[3].(map[string]any)["tool"].(map[string]any)
	if _, present := okTool["isError"]; present {
		t.Errorf("isError should be omitted when false: %s", b.String())
	}
}

func minimalSession() *model.Session {
	return &model.Session{
		Meta: model.Meta{Model: "claude-opus-4-7"},
		Turns: []model.Turn{{
			Prompt: "hi there",
			Events: []model.Event{{Kind: model.EventText, Text: "hello back"}},
		}},
	}
}

func TestPlural(t *testing.T) {
	tests := []struct {
		n    int
		noun string
		want string
	}{
		{1, "tool", "1 tool"}, {2, "tool", "2 tools"}, {0, "error", "0 errors"},
	}
	for _, tt := range tests {
		if got := plural(tt.n, tt.noun); got != tt.want {
			t.Errorf("plural(%d, %q) = %q, want %q", tt.n, tt.noun, got, tt.want)
		}
	}
}

func TestFmtToolDuration(t *testing.T) {
	base := time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		start, end time.Time
		want       string
	}{
		{"sub-10s", base, base.Add(1500 * time.Millisecond), "1.5s"},
		{"seconds", base, base.Add(12 * time.Second), "12s"},
		{"minutes", base, base.Add(90 * time.Second), "1m30s"},
		{"whole minute", base, base.Add(2 * time.Minute), "2m"},
		{"zero end", base, time.Time{}, ""},
		{"negative", base.Add(time.Second), base, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fmtToolDuration(tt.start, tt.end); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFmtDuration(t *testing.T) {
	base := time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		start, end time.Time
		want       string
	}{
		{"minutes", base, base.Add(5 * time.Minute), "5m"},
		{"hours", base, base.Add(time.Hour + time.Minute), "1h01m"},
		{"zero", time.Time{}, base, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fmtDuration(tt.start, tt.end); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWrapPlain(t *testing.T) {
	t.Run("short line unchanged", func(t *testing.T) {
		got := wrapPlain("hello world", 40)
		if len(got) != 1 || got[0] != "hello world" {
			t.Errorf("got %q, want one line", got)
		}
	})
	t.Run("wraps at width", func(t *testing.T) {
		got := wrapPlain("aaaa bbbb cccc dddd", 10)
		if len(got) < 2 {
			t.Fatalf("expected multiple lines, got %q", got)
		}
		for _, line := range got {
			if len([]rune(line)) > 10 {
				t.Errorf("line %q exceeds width 10", line)
			}
		}
	})
	t.Run("preserves explicit newlines", func(t *testing.T) {
		got := wrapPlain("a\nb", 40)
		if len(got) != 2 {
			t.Errorf("got %q, want 2 lines", got)
		}
	})
}

func TestExtractLinks(t *testing.T) {
	src, links := extractLinks(
		"Top: [Researcher task](obsidian://open?vault=research&file=06-Tasks%2FR.md) — see [Diffy](obsidian://open?vault=research&file=02-Wiki%2FDiffy.md).")
	wantSrc := "Top: Researcher task — see Diffy."
	if src != wantSrc {
		t.Errorf("src = %q, want %q", src, wantSrc)
	}
	if len(links) != 2 || links[0].text != "Researcher task" || links[1].text != "Diffy" {
		t.Fatalf("links = %+v", links)
	}
	if links[0].url != "obsidian://open?vault=research&file=06-Tasks%2FR.md" {
		t.Errorf("links[0].url = %q", links[0].url)
	}
	if got := mustExtract(t, "no links here"); got != "no links here" {
		t.Errorf("plain text altered: %q", got)
	}
}

func TestExtractLinksBareURL(t *testing.T) {
	t.Run("bare url stays in source, becomes its own link", func(t *testing.T) {
		src, links := extractLinks("Visit https://example.com for more.")
		if src != "Visit https://example.com for more." {
			t.Errorf("bare URL altered in source: %q", src)
		}
		if len(links) != 1 || links[0].text != "https://example.com" || links[0].url != "https://example.com" {
			t.Fatalf("links = %+v", links)
		}
	})
	t.Run("trailing sentence punctuation excluded from href", func(t *testing.T) {
		_, links := extractLinks("See https://example.com.")
		if len(links) != 1 || links[0].url != "https://example.com" {
			t.Fatalf("links = %+v", links)
		}
	})
	t.Run("url inside a markdown link is not double-matched", func(t *testing.T) {
		_, links := extractLinks("A [labeled](https://example.com/x) link.")
		if len(links) != 1 || links[0].text != "labeled" || links[0].url != "https://example.com/x" {
			t.Fatalf("links = %+v", links)
		}
	})
	t.Run("markdown link and bare url kept in source order", func(t *testing.T) {
		_, links := extractLinks("bare https://a.com then [txt](https://b.com)")
		if len(links) != 2 || links[0].url != "https://a.com" || links[1].url != "https://b.com" {
			t.Fatalf("links = %+v", links)
		}
	})
	t.Run("bare obsidian open URI collapses to a [[wikilink]] label", func(t *testing.T) {
		uri := "obsidian://open?vault=research&file=02-Wiki%2FDiffy.md"
		src, links := extractLinks("see " + uri + " here")
		if src != "see [[Diffy]] here" {
			t.Errorf("src = %q, want %q", src, "see [[Diffy]] here")
		}
		if len(links) != 1 || links[0].text != "[[Diffy]]" || links[0].url != uri {
			t.Fatalf("links = %+v", links)
		}
	})
	t.Run("obsidian label keeps a heading anchor", func(t *testing.T) {
		_, links := extractLinks("obsidian://open?vault=v&file=Note%23Heading")
		if len(links) != 1 || links[0].text != "[[Note#Heading]]" {
			t.Fatalf("links = %+v", links)
		}
	})
	t.Run("trailing punctuation stays in prose after the label", func(t *testing.T) {
		src, links := extractLinks("see obsidian://open?vault=v&file=Note.md.")
		if src != "see [[Note]]." {
			t.Errorf("src = %q, want %q", src, "see [[Note]].")
		}
		if links[0].url != "obsidian://open?vault=v&file=Note.md" {
			t.Errorf("href = %q", links[0].url)
		}
	})
	t.Run("obsidian URI without a file stays a raw bare link", func(t *testing.T) {
		uri := "obsidian://search?query=diffy"
		src, links := extractLinks("run " + uri + " now")
		if src != "run "+uri+" now" {
			t.Errorf("src altered: %q", src)
		}
		if len(links) != 1 || links[0].text != uri || links[0].url != uri {
			t.Fatalf("links = %+v", links)
		}
	})
	t.Run("any scheme:// is autolinked as a raw bare link", func(t *testing.T) {
		for _, uri := range []string{"ftp://host/file", "vscode://file/tmp/x.go:12", "myapp+beta://do/it"} {
			_, links := extractLinks("go " + uri + " now")
			if len(links) != 1 || links[0].text != uri || links[0].url != uri {
				t.Errorf("%q: links = %+v", uri, links)
			}
		}
	})
	t.Run("scheme without // is not autolinked", func(t *testing.T) {
		if got := mustExtract(t, "write mailto:a@b.com or tel:123 now"); got != "write mailto:a@b.com or tel:123 now" {
			t.Errorf("altered: %q", got)
		}
	})
	t.Run("balanced parens kept, wrapping paren dropped", func(t *testing.T) {
		wiki := "https://en.wikipedia.org/wiki/Ruby_(programming_language)"
		_, links := extractLinks("see (" + wiki + ") ok")
		if len(links) != 1 || links[0].url != wiki {
			t.Fatalf("balanced-paren URL truncated: links = %+v", links)
		}
	})
	t.Run("trailing paren and period peeled off the href", func(t *testing.T) {
		_, links := extractLinks("see https://x.com/foo).")
		if len(links) != 1 || links[0].url != "https://x.com/foo" {
			t.Fatalf("links = %+v", links)
		}
	})
	t.Run("obsidian note name with markdown chars is escaped in source", func(t *testing.T) {
		uri := "obsidian://open?vault=v&file=My%20%2ANote%2A.md" // file = "My *Note*.md"
		src, links := extractLinks("see " + uri + " x")
		if len(links) != 1 || links[0].text != "[[My *Note*]]" || links[0].url != uri {
			t.Fatalf("links = %+v", links)
		}
		if !strings.Contains(src, `[[My \*Note\*]]`) {
			t.Errorf("note name not escaped in source: %q", src)
		}
	})
}

func mustExtract(t *testing.T, s string) string {
	t.Helper()
	out, links := extractLinks(s)
	if len(links) != 0 {
		t.Fatalf("expected no links, got %+v", links)
	}
	return out
}

// stripOSC removes OSC 8 hyperlink sequences (ESC ] … ST) so the remaining
// CSI-styled text can be checked for what's actually visible.
func stripOSC(s string) string {
	for {
		i := strings.Index(s, "\x1b]")
		if i < 0 {
			return s
		}
		end := strings.Index(s[i:], "\x1b\\")
		if end < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+end+2:]
	}
}

func TestLinkifyMarkdown(t *testing.T) {
	const osc = "\x1b]8;;"
	r := &renderer{}
	r.initStyles()
	// visible reduces a styled line to the text the user actually sees.
	visible := func(s string) string { p, _ := stripANSI(stripOSC(s)); return p }

	t.Run("wraps styled text, drops url from view", func(t *testing.T) {
		// glamour fragments the text with SGR codes; linkify must still match it.
		out := []string{"see \x1b[1mResearcher task\x1b[0m here"}
		url := "obsidian://open?vault=research&file=R.md"
		r.linkifyMarkdown(out, []mdLinkSpec{{text: "Researcher task", url: url}})
		if !strings.Contains(out[0], osc+url+"\x1b\\") || !strings.Contains(out[0], "\x1b]8;;\x1b\\") {
			t.Errorf("missing OSC 8 hyperlink, got %q", out[0])
		}
		if got := visible(out[0]); got != "see Researcher task here" {
			t.Errorf("visible text = %q, want %q", got, "see Researcher task here")
		}
	})

	t.Run("unmatched link leaves line unchanged", func(t *testing.T) {
		out := []string{"nothing to see"}
		r.linkifyMarkdown(out, []mdLinkSpec{{text: "absent", url: "u"}})
		if out[0] != "nothing to see" {
			t.Errorf("line changed: %q", out[0])
		}
	})

	t.Run("multiple links in source order", func(t *testing.T) {
		out := []string{"A and B"}
		r.linkifyMarkdown(out, []mdLinkSpec{{text: "A", url: "ua"}, {text: "B", url: "ub"}})
		if !strings.Contains(out[0], osc+"ua\x1b\\") || !strings.Contains(out[0], osc+"ub\x1b\\") {
			t.Errorf("missing hyperlinks, got %q", out[0])
		}
		if got := visible(out[0]); got != "A and B" {
			t.Errorf("visible text = %q, want %q", got, "A and B")
		}
	})

	t.Run("bare url: visible text is the url, href matches", func(t *testing.T) {
		// glamour colors a bare URL but emits no OSC 8; a bare-URL spec has
		// text == url, so linkify wraps the URL text as its own hyperlink.
		url := "https://example.com"
		out := []string{"Visit \x1b[38;5;30;4m" + url + "\x1b[0m for more."}
		r.linkifyMarkdown(out, []mdLinkSpec{{text: url, url: url}})
		if !strings.Contains(out[0], osc+url+"\x1b\\") || !strings.Contains(out[0], "\x1b]8;;\x1b\\") {
			t.Errorf("missing OSC 8 hyperlink, got %q", out[0])
		}
		if got := visible(out[0]); got != "Visit "+url+" for more." {
			t.Errorf("visible text = %q", got)
		}
	})
}

// TestMarkdownBareURLEndToEnd drives the full color path — extractLinks, glamour,
// then linkifyMarkdown — proving a bare URL in prose emerges as an OSC 8 link
// with a clean href (trailing period excluded), while a URL glamour word-wrapped
// across lines degrades to plain (no OSC 8).
func TestMarkdownBareURLEndToEnd(t *testing.T) {
	r := &renderer{opts: Options{Color: true}, gcache: map[int]*glamour.TermRenderer{}}
	r.initStyles()

	joined := strings.Join(r.markdown("See https://example.com. now", 80), "\n")
	href := "\x1b]8;;https://example.com\x1b\\"
	if !strings.Contains(joined, href) {
		t.Errorf("bare URL not linkified with clean href; got %q", joined)
	}
	if strings.Contains(joined, "\x1b]8;;https://example.com.\x1b\\") {
		t.Errorf("trailing period leaked into href; got %q", joined)
	}

	// A bare obsidian open URI renders as a [[wikilink]] label (visible text)
	// hyperlinked to the full URI (href), not the raw URI string.
	obs := "obsidian://open?vault=v&file=02-Wiki%2FDiffy.md"
	obsOut := strings.Join(r.markdown("Open "+obs+" now", 80), "\n")
	if !strings.Contains(obsOut, "\x1b]8;;"+obs+"\x1b\\") {
		t.Errorf("obsidian href missing; got %q", obsOut)
	}
	if vis, _ := stripANSI(stripOSC(obsOut)); !strings.Contains(vis, "[[Diffy]]") || strings.Contains(vis, "obsidian://") {
		t.Errorf("visible text should be [[Diffy]], not the raw URI; got %q", vis)
	}

	// A URL containing balanced parens keeps them in the href; the wrapping
	// paren is dropped.
	wiki := "https://en.wikipedia.org/wiki/Ruby_(programming_language)"
	wikiOut := strings.Join(r.markdown("see ("+wiki+") ok", 200), "\n")
	if !strings.Contains(wikiOut, "\x1b]8;;"+wiki+"\x1b\\") {
		t.Errorf("balanced-paren URL href truncated; got %q", wikiOut)
	}

	// A note name with markdown-active chars still renders its [[label]]
	// verbatim and stays clickable (the name is escaped before glamour).
	star := "obsidian://open?vault=v&file=My%20%2ANote%2A.md"
	starOut := strings.Join(r.markdown("Ref "+star+" x", 80), "\n")
	if !strings.Contains(starOut, "\x1b]8;;"+star+"\x1b\\") {
		t.Errorf("obsidian href missing for starred note; got %q", starOut)
	}
	if vis, _ := stripANSI(stripOSC(starOut)); !strings.Contains(vis, "[[My *Note*]]") {
		t.Errorf("visible label should be [[My *Note*]]; got %q", vis)
	}

	long := "https://example.com/very/long/path/that/keeps/going/way/past/the/edge/of/the/wrap/width/for/sure"
	wrapped := strings.Join(r.markdown("Long: "+long, 40), "\n")
	if strings.Contains(wrapped, "\x1b]8;;") {
		t.Errorf("wrapped URL should stay plain, but got an OSC 8 link: %q", wrapped)
	}
}

func TestTruncateAndOneLine(t *testing.T) {
	if got := truncate("abcdef", 3); got != "abc…" {
		t.Errorf("truncate = %q, want abc…", got)
	}
	if got := truncate("ab", 3); got != "ab" {
		t.Errorf("truncate short = %q, want ab", got)
	}
	if got := oneLine("  first\nsecond  "); got != "first" {
		t.Errorf("oneLine = %q, want first", got)
	}
}

// gatingSession has one turn with a plain tool (a result body) and a subagent
// call (a nested event stream plus its own result body), so a render can be
// probed for which channels surfaced what.
func gatingSession() *model.Session {
	return &model.Session{
		Meta: model.Meta{Model: "claude-opus-4-7"},
		Turns: []model.Turn{{
			Prompt: "go",
			Events: []model.Event{
				{Kind: model.EventText, Text: "RESPONSEMARKER"},
				{Kind: model.EventTool, Tool: &model.Tool{Name: "Read", Result: "TOOLBODYMARKER"}},
				{Kind: model.EventTool, Tool: &model.Tool{
					Name:     "Agent",
					Result:   "AGENTRESULTMARKER",
					Subagent: []model.Event{{Kind: model.EventText, Text: "NESTEDMARKER"}},
				}},
			},
		}},
	}
}

func renderChannels(t *testing.T, ch Channels) string {
	t.Helper()
	var b strings.Builder
	if err := Session(&b, gatingSession(), Options{Width: 80, Color: false, Channels: ch}); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestChannelGating verifies the activation/body/expansion split: Tools gates
// whether a tool's head line appears, ToolResults gates its result body, and
// Subagents gates expansion of a nested stream (falling through to the
// ToolResults body when off). The response text is always shown.
func TestChannelGating(t *testing.T) {
	has := func(t *testing.T, s, marker string, want bool) {
		t.Helper()
		if got := strings.Contains(s, marker); got != want {
			t.Errorf("contains %q = %v, want %v", marker, got, want)
		}
	}

	t.Run("minimal shows response, no tools", func(t *testing.T) {
		out := renderChannels(t, Channels{})
		has(t, out, "RESPONSEMARKER", true)
		has(t, out, "Read", false)
		has(t, out, "TOOLBODYMARKER", false)
		has(t, out, "NESTEDMARKER", false)
	})

	t.Run("detailed: activation + expansion, no bodies", func(t *testing.T) {
		out := renderChannels(t, Channels{Thinking: true, Tools: true, Subagents: true, Metrics: true})
		has(t, out, "Read", true)            // tool fired
		has(t, out, "TOOLBODYMARKER", false) // but no result body
		has(t, out, "NESTEDMARKER", true)    // subagent expanded
		has(t, out, "AGENTRESULTMARKER", false)
	})

	t.Run("full: activation + expansion + bodies", func(t *testing.T) {
		out := renderChannels(t, Channels{Thinking: true, Tools: true, ToolResults: true, Subagents: true, Metrics: true})
		has(t, out, "Read", true)
		has(t, out, "TOOLBODYMARKER", true)
		has(t, out, "NESTEDMARKER", true)
	})

	t.Run("subagents off falls through to result body", func(t *testing.T) {
		out := renderChannels(t, Channels{Tools: true, ToolResults: true})
		has(t, out, "Agent", true)             // head line present
		has(t, out, "NESTEDMARKER", false)     // not expanded
		has(t, out, "AGENTRESULTMARKER", true) // its result body shown instead
	})

	t.Run("tools on, results off: head without body", func(t *testing.T) {
		out := renderChannels(t, Channels{Tools: true})
		has(t, out, "Read", true)
		has(t, out, "TOOLBODYMARKER", false)
	})
}

// TestHeaderEffort pins how the header reports reasoning effort. It reads as a
// phrase because "high" alone beside a model name does not say high what.
func TestHeaderEffort(t *testing.T) {
	head := func(t *testing.T, m model.Meta) string {
		t.Helper()
		sess := &model.Session{Meta: m, Turns: []model.Turn{{Prompt: "go"}}}
		var b strings.Builder
		if err := Session(&b, sess, Options{Width: 120, Color: false}); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}

	t.Run("named beside the model", func(t *testing.T) {
		out := head(t, model.Meta{Model: "claude-opus-5", Effort: "high"})
		if !strings.Contains(out, "claude-opus-5 · high effort") {
			t.Errorf("want the effort after the model, got %q", out)
		}
	})

	t.Run("a mid-session change shows the transition", func(t *testing.T) {
		out := head(t, model.Meta{Model: "claude-opus-5", Effort: "high", Efforts: []string{"xhigh", "high"}})
		if !strings.Contains(out, "xhigh→high effort") {
			t.Errorf("want the whole sequence, got %q", out)
		}
	})

	t.Run("a session without the field says nothing", func(t *testing.T) {
		// Half of sessions predate it; an invented default would be a claim about
		// how the model was run that the log never made.
		out := head(t, model.Meta{Model: "claude-opus-5"})
		if strings.Contains(out, "effort") {
			t.Errorf("no effort recorded, so none should be shown: %q", out)
		}
	})
}

// TestDenialOnToolLine pins that a refused call says so. Both a denied call and
// a failed one carry isError, so the error glyph alone sends a reader to fix the
// tool when the thing to fix is a permission rule.
func TestDenialOnToolLine(t *testing.T) {
	line := func(t *testing.T, tool *model.Tool) string {
		t.Helper()
		sess := &model.Session{Turns: []model.Turn{{
			Prompt: "go",
			Events: []model.Event{{Kind: model.EventTool, Tool: tool}},
		}}}
		var b strings.Builder
		if err := Session(&b, sess, Options{Width: 120, Color: false, Channels: Channels{Tools: true}}); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}

	t.Run("a denied call names what refused it", func(t *testing.T) {
		out := line(t, &model.Tool{Name: "Bash", Args: "rm -rf /tmp/x", IsError: true, Denial: "permission-rule"})
		if !strings.Contains(out, "denied: permission-rule") {
			t.Errorf("want the denial kind on the line, got %q", out)
		}
	})

	t.Run("a call that ran and failed says nothing about denial", func(t *testing.T) {
		out := line(t, &model.Tool{Name: "Bash", Args: "false", IsError: true})
		if strings.Contains(out, "denied") {
			t.Errorf("an ordinary error is not a denial: %q", out)
		}
	})
}

// TestDelegationMarker pins what an Agent line says about the work it handed
// off. Its args are the human description, so without this the line names the
// task and never which agent ran it or on what model.
func TestDelegationMarker(t *testing.T) {
	line := func(t *testing.T, tool *model.Tool) string {
		t.Helper()
		sess := &model.Session{Turns: []model.Turn{{
			Prompt: "go",
			Events: []model.Event{{Kind: model.EventTool, Tool: tool}},
		}}}
		var b strings.Builder
		if err := Session(&b, sess, Options{Width: 120, Color: false, Channels: Channels{Tools: true}}); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}

	t.Run("type and model", func(t *testing.T) {
		out := line(t, &model.Tool{Name: "Agent", Identity: "Explore", Model: "haiku", Args: "sweep"})
		if !strings.Contains(out, "Agent[Explore@haiku]") {
			t.Errorf("want Agent[Explore@haiku] on the line, got %q", out)
		}
	})

	t.Run("no model named shows the type alone", func(t *testing.T) {
		// The subagent inherited the session's model. Printing that model here
		// would report a choice the caller never made.
		out := line(t, &model.Tool{Name: "Agent", Identity: "researcher", Args: "research"})
		if !strings.Contains(out, "Agent[researcher]") {
			t.Errorf("want Agent[researcher], got %q", out)
		}
		if strings.Contains(out, "@") {
			t.Errorf("no model was named, so nothing should follow @: %q", out)
		}
	})

	t.Run("neither named leaves no empty brackets", func(t *testing.T) {
		out := line(t, &model.Tool{Name: "Agent", Args: "unnamed"})
		if strings.Contains(out, "[") {
			t.Errorf("an Agent naming neither should print no brackets: %q", out)
		}
	})

	t.Run("other tools are not bracketed", func(t *testing.T) {
		// Bash carries an identity too, but its args already open with the
		// program — bracketing it would repeat what the line already shows.
		out := line(t, &model.Tool{Name: "Bash", Identity: "git", Args: "git status"})
		if strings.Contains(out, "Bash[") {
			t.Errorf("only Agent is bracketed, got %q", out)
		}
	})
}

func TestSessionPlainNoANSI(t *testing.T) {
	// A minimal render must contain no ESC bytes when color is off.
	var b strings.Builder
	err := Session(&b, minimalSession(), Options{Width: 80, Color: false})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(b.String(), '\x1b') {
		t.Error("plain render contains ANSI escape bytes")
	}
	if !strings.Contains(b.String(), "hi there") {
		t.Error("render missing prompt text")
	}
}

// TestHeaderModel pins how the header names what a session ran on. The model
// used to be printed unconditionally from the first assistant entry, so a
// session that switched was reported as still on the model it left, and one
// whose log names none was reported as "unknown".
func TestHeaderModel(t *testing.T) {
	head := func(t *testing.T, m model.Meta) string {
		t.Helper()
		sess := &model.Session{Meta: m, Turns: []model.Turn{{Prompt: "go"}}}
		var b strings.Builder
		if err := Session(&b, sess, Options{Width: 120, Color: false}); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}

	t.Run("a mid-session switch shows the transition", func(t *testing.T) {
		out := head(t, model.Meta{Model: "claude-opus-5", Models: []string{"claude-sonnet-5", "claude-opus-5"}})
		if !strings.Contains(out, "claude-sonnet-5→claude-opus-5") {
			t.Errorf("want the whole sequence, got %q", out)
		}
	})

	t.Run("a session naming no model says nothing", func(t *testing.T) {
		// The word "unknown" claimed a fact the log does not carry; the effort and
		// entrypoint beside it already stay silent in this case.
		out := head(t, model.Meta{Effort: "high"})
		if strings.Contains(out, "unknown") {
			t.Errorf("no model recorded, so none should be named: %q", out)
		}
		if !strings.Contains(out, "high effort") {
			t.Errorf("the rest of the header should survive a missing model: %q", out)
		}
	})
}

// TestOutputsSection pins what a rendered session says it produced. The header
// describes what a session was; without this, a pull request the session opened
// is visible in `list` and nowhere in the render path, which is the two paths
// knowing different things about one session.
func TestOutputsSection(t *testing.T) {
	sess := &model.Session{
		Meta: model.Meta{
			ID: "s1",
			PRs: []model.PR{
				{Repository: "eitanpo/central", Number: 14, URL: "https://github.com/eitanpo/central/pull/14"},
			},
			Artifacts: []model.Artifact{
				{Title: "Cost report", URL: "https://claude.ai/code/artifact/aaa"},
				{URL: "https://claude.ai/code/artifact/bbb"},
			},
		},
		Turns: []model.Turn{{Prompt: "ship it"}},
	}

	t.Run("plain output shows every URL, since there is no href to hide one in", func(t *testing.T) {
		var b strings.Builder
		if err := Session(&b, sess, Options{Width: 100, Color: false}); err != nil {
			t.Fatal(err)
		}
		out := b.String()
		if !strings.Contains(out, "── Outputs ──") {
			t.Fatalf("no Outputs section: %q", out)
		}
		for _, want := range []string{
			"https://github.com/eitanpo/central/pull/14",
			"Cost report  https://claude.ai/code/artifact/aaa",
			"https://claude.ai/code/artifact/bbb",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output missing %q: %q", want, out)
			}
		}
		if strings.Contains(out, "\x1b]8;;") {
			t.Errorf("plain output must carry no OSC 8 escape: %q", out)
		}
	})

	t.Run("the section renders at minimal verbosity", func(t *testing.T) {
		// It is deliberately ungated: thinking and tool bodies are hidden at low
		// verbosity because they are the machinery, and a link to the pull request
		// the session opened is the opposite of machinery.
		var b strings.Builder
		if err := Session(&b, sess, Options{Width: 100, Color: false, Channels: Channels{}}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(b.String(), "pull/14") {
			t.Errorf("Outputs must not be gated on a channel: %q", b.String())
		}
	})

	t.Run("a session that produced nothing draws no section", func(t *testing.T) {
		var b strings.Builder
		quiet := &model.Session{Meta: model.Meta{ID: "s1"}, Turns: []model.Turn{{Prompt: "think"}}}
		if err := Session(&b, quiet, Options{Width: 100, Color: false}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(b.String(), "Outputs") {
			t.Errorf("empty section drawn: %q", b.String())
		}
	})

	t.Run("with color on, an artifact links by title and a PR by its URL", func(t *testing.T) {
		// The split assistant prose already makes: a markdown link hides its URL
		// behind a name, a bare URL is its own name. An artifact id is an opaque
		// uuid, so the title is the only useful name it has.
		var b strings.Builder
		if err := Session(&b, sess, Options{Width: 100, Color: true}); err != nil {
			t.Fatal(err)
		}
		out := b.String()
		if !strings.Contains(out, "\x1b]8;;https://claude.ai/code/artifact/aaa\x1b\\") {
			t.Errorf("artifact is not an OSC 8 hyperlink: %q", out)
		}
		// The artifact's URL is hidden behind its title, so it appears only in the
		// href — never as visible text beside it.
		plain, _ := stripANSI(out)
		if strings.Contains(plain, "Cost report  https://claude.ai/code/artifact/aaa") {
			t.Errorf("a linked artifact must hide its URL from the visible text: %q", plain)
		}
		if !strings.Contains(plain, "https://github.com/eitanpo/central/pull/14") {
			t.Errorf("a pull request's visible text is its URL: %q", plain)
		}
	})
}

// TestHeaderCost pins that the rendered header states what the session cost, and
// that it says nothing where the log recorded no cost — the rule the model and
// effort beside it already follow.
func TestHeaderCost(t *testing.T) {
	sess := minimalSession()
	cost, added, removed := 17.254517250000003, 342, 8
	sess.Meta.Usage = model.Usage{Input: 236, Output: 111499, CacheRead: 17633669}
	sess.Meta.CostUSD = &cost
	sess.Meta.LinesAdded, sess.Meta.LinesRemoved = &added, &removed

	var b strings.Builder
	if err := Session(&b, sess, Options{Width: 100, Color: false}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "Tokens: 236 in / 111k out") {
		t.Errorf("header missing the token tally: %q", out)
	}
	if !strings.Contains(out, "$17.25") {
		t.Errorf("header missing the cost: %q", out)
	}
	if !strings.Contains(out, "+342/-8") {
		t.Errorf("header missing the lines changed: %q", out)
	}

	t.Run("no cost recorded, no dollar figure", func(t *testing.T) {
		sess := minimalSession()
		sess.Meta.Usage = model.Usage{Input: 236, Output: 4}
		var b strings.Builder
		if err := Session(&b, sess, Options{Width: 100, Color: false}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(b.String(), "$") {
			t.Errorf("a session with no cost record must show no dollar figure: %q", b.String())
		}
	})
}
