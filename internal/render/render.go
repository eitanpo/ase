// Package render turns a model.Session into a styled terminal view: glamour
// renders the markdown bodies (prose, code blocks), lipgloss draws the chrome
// (boxes, per-actor glyphs, color). When color is off the same layout is
// emitted as plain text.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/eitanpo/agentry/internal/entrypoint"
	"github.com/eitanpo/agentry/internal/model"
	"github.com/eitanpo/agentry/internal/spend"
	"github.com/eitanpo/agentry/internal/trail"
	"github.com/muesli/termenv"
)

const (
	fallbackWidth    = 100 // used when stdout is not a TTY
	toolBodyMaxLines = 10
	assistantIndent  = "  " // left pad before the assistant turn's rail (│ … ╰─)
	glyphUser        = "❯"
	glyphClaude      = "◆"
	glyphTool        = "●"
	glyphSubagent    = "▶"
	glyphThinking    = "✻"
	glyphOK          = "✓"
	glyphErr         = "✗"
)

// Channels selects which optional sections render. Tools gates the per-call
// activation line (that a tool fired); ToolResults gates its result body.
type Channels struct {
	Thinking, Tools, ToolResults, Subagents, Metrics bool
}

// Options configures a render pass.
type Options struct {
	Width    int
	Color    bool
	Channels Channels
}

type renderer struct {
	opts    Options
	gcache  map[int]*glamour.TermRenderer
	user    lipgloss.Style
	userRow lipgloss.Style
	claude  lipgloss.Style
	tool    lipgloss.Style
	subnt   lipgloss.Style
	think   lipgloss.Style
	ok      lipgloss.Style
	bad     lipgloss.Style
	body    lipgloss.Style
	args    lipgloss.Style
	link    lipgloss.Style
	dim     lipgloss.Style
	border  lipgloss.Style
	userBox lipgloss.Style
}

// SessionJSON writes the full session model to w as indented JSON — the render
// path's machine-readable form (`--format json`), for agent consumption and
// piping into jq. It emits the complete model regardless of verbosity or color,
// which shape only the styled text view.
func SessionJSON(w io.Writer, s *model.Session) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// Session writes the styled session to w.
func Session(w io.Writer, s *model.Session, opts Options) error {
	if opts.Width <= 0 {
		opts.Width = fallbackWidth
	}
	if !opts.Color {
		lipgloss.SetColorProfile(termenv.Ascii) // strips all ANSI from styles
	}
	r := &renderer{opts: opts, gcache: map[int]*glamour.TermRenderer{}}
	r.initStyles()

	var b strings.Builder
	b.WriteString(r.header(s))
	for _, t := range s.Turns {
		b.WriteString("\n")
		b.WriteString(r.turn(t))
	}
	// What the session produced comes after the last turn and before the metrics,
	// because an output is a result of the session rather than a property of it —
	// the header says what the session was. It is not gated on a channel: thinking
	// and tool bodies are hidden at low verbosity because they are the machinery,
	// and a link to the pull request the session opened is the opposite of
	// machinery. A session that produced nothing renders nothing here.
	if out := r.outputs(s); out != "" {
		b.WriteString("\n")
		b.WriteString(out)
	}
	if opts.Channels.Metrics {
		b.WriteString("\n")
		b.WriteString(r.summary(s))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func (r *renderer) initStyles() {
	c := func(code string) lipgloss.Color { return lipgloss.Color(code) }
	userBg := c("237")                                                            // prompt-row highlight
	r.user = lipgloss.NewStyle().Foreground(c("6")).Bold(true).Background(userBg) // cyan ❯ on highlight
	r.userRow = lipgloss.NewStyle().Background(userBg)
	r.claude = lipgloss.NewStyle().Foreground(c("5")).Bold(true)    // magenta
	r.tool = lipgloss.NewStyle().Foreground(c("3")).Bold(true)      // yellow
	r.subnt = lipgloss.NewStyle().Foreground(c("4")).Bold(true)     // blue
	r.think = lipgloss.NewStyle().Foreground(c("243")).Italic(true) // medium gray, readable but secondary
	r.ok = lipgloss.NewStyle().Foreground(c("2")).Bold(true)        // green
	r.bad = lipgloss.NewStyle().Foreground(c("1")).Bold(true)       // red
	r.body = lipgloss.NewStyle().Foreground(c("15"))                // tool result body: bright white
	r.args = lipgloss.NewStyle().Foreground(c("248"))               // tool args parenthetical: light gray
	r.link = lipgloss.NewStyle().Foreground(c("80"))                // hyperlink text: sky cyan, distinct from glamour's heading blue (39) (underline omitted — lipgloss renders it per-rune)
	r.dim = lipgloss.NewStyle().Foreground(c("8"))
	r.border = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(c("7")).
		Padding(0, 1)
	r.userBox = r.border // prompt box: border + padding sit on the highlight
	if r.opts.Color {    // guard: BorderBackground emits empty ANSI under the Ascii profile
		r.userBox = r.border.Background(userBg).BorderBackground(userBg)
	}
}

// ── Session header ─────────────────────────────────────────────────────────

func (r *renderer) header(s *model.Session) string {
	m := s.Meta
	title := fmt.Sprintf("Session · %s → %s", fmtTime(m.Start), fmtTime(m.End))
	if d := fmtDuration(m.Start, m.End); d != "" {
		title += " · " + d
	}
	// What it ran on, with the transition spelled out when the session switched
	// models mid-way. A session whose log names no model says nothing: "unknown"
	// asserted a fact the log does not carry, which is the rule the effort and
	// entrypoint beside it already follow.
	if mo := trail.Of(m.Model, m.Models); mo != "" {
		title += " · " + mo
	}
	// How hard the model was run, as a phrase — "high" alone beside a model name
	// would not say high what. A session that changed effort shows the transition
	// with the same arrow the entrypoint trail uses.
	if e := trail.Of(m.Effort, m.Efforts); e != "" {
		title += " · " + e + " effort"
	}
	// Where the session ran, spelled out rather than abbreviated to the "+" the
	// listing column uses — the header has a line to itself.
	if t := entrypoint.Trail(m.Entrypoint, m.Entrypoints); t != "" {
		title += " · " + t
	}

	tools, errs := 0, 0
	for _, t := range s.Turns {
		tools += t.ToolCount
		errs += t.ErrorCount
	}
	counts := []string{
		plural(len(s.Turns), "turn"), plural(tools, "tool"),
	}
	if errs > 0 {
		counts = append(counts, r.bad.Render(plural(errs, "error")))
	}
	if m.NumSubagents > 0 {
		counts = append(counts, plural(m.NumSubagents, "subagent"))
	}

	// What the session spent, in the wording the listing's cost channel also
	// prints — one phrasing, so a spend read off a rendered session and one read
	// off a listing cannot differ.
	spent := spend.Line(m.Usage, m.CostUSD, m.LinesAdded, m.LinesRemoved)

	body := r.claude.Render(title) + "\n" +
		strings.Join(counts, " · ") + "\n" +
		r.dim.Render(spent)
	return r.box(body) + "\n"
}

func (r *renderer) box(content string) string {
	w := r.opts.Width - 2
	if w < 20 {
		w = 20
	}
	return r.border.Width(w).Render(content) + "\n"
}

// ── Turns ────────────────────────────────────────────────────────────────

func (r *renderer) turn(t model.Turn) string {
	var b strings.Builder
	b.WriteString(r.userPrompt(t.Prompt))

	bar := assistantIndent + r.dim.Render("│") + " "
	b.WriteString(assistantIndent + r.claude.Render(glyphClaude) + "\n")
	for _, line := range r.events(t.Events, bar, 0) {
		b.WriteString(line + "\n")
	}
	b.WriteString(r.turnClose(t) + "\n")
	return b.String()
}

// userPrompt renders the prompt as a highlighted block enclosed in a rounded
// border, the prompt text prefixed with the ❯ glyph. Wrapped and continuation
// lines hang-indent two columns (the width of "❯ ") so they align under the
// first character of the prompt. The highlight fills the box's inner width so it
// spans edge to edge inside the border.
func (r *renderer) userPrompt(prompt string) string {
	w := r.opts.Width - 2
	if w < 20 {
		w = 20
	}
	inner := w - 2 // text area inside the border's horizontal padding
	lines := wrapPlain(prompt, inner-2)
	for i, line := range lines {
		if i == 0 {
			lines[i] = r.user.Render(glyphUser) + r.userRow.Render(" "+line)
		} else {
			lines[i] = r.userRow.Render("  " + line)
		}
	}
	block := r.userRow.Width(inner).Render(strings.Join(lines, "\n"))
	return r.userBox.Width(w).Render(block) + "\n"
}

func (r *renderer) turnClose(t model.Turn) string {
	parts := []string{r.claude.Render(glyphClaude)}
	if d := fmtDuration(t.Start, t.End); d != "" {
		parts[0] += " " + d
	}
	if t.ToolCount > 0 {
		parts = append(parts, plural(t.ToolCount, "tool"))
	}
	if t.ErrorCount > 0 {
		parts = append(parts, r.bad.Render(plural(t.ErrorCount, "error")))
	}
	return assistantIndent + r.dim.Render("╰─ ") + strings.Join(parts, " · ")
}

// events renders an assistant event stream, each line carrying the left-bar
// prefix. depth controls glamour wrap width for nesting.
func (r *renderer) events(events []model.Event, prefix string, depth int) []string {
	var out []string
	avail := r.opts.Width - lipgloss.Width(prefix)
	for _, e := range events {
		switch e.Kind {
		case model.EventText:
			for _, line := range r.markdown(e.Text, avail) {
				out = append(out, prefix+line)
			}
		case model.EventThinking:
			if !r.opts.Channels.Thinking {
				continue
			}
			for i, line := range wrapPlain(e.Text, avail-2) {
				lead := "  "
				if i == 0 {
					lead = glyphThinking + " "
				}
				out = append(out, prefix+r.think.Render(lead+line))
			}
		case model.EventTool:
			if !r.opts.Channels.Tools {
				continue
			}
			out = append(out, r.toolLines(e.Tool, prefix, depth)...)
		}
		out = append(out, strings.TrimRight(prefix, " ")) // spacer
	}
	for len(out) > 0 && out[len(out)-1] == strings.TrimRight(prefix, " ") {
		out = out[:len(out)-1]
	}
	return out
}

// delegation is the bracketed suffix naming what an Agent call handed work to —
// "[Explore@haiku]", the subagent type and the model, either half absent when
// the call did not name it. Only Agent gets one: it is the sole tool whose args
// hide its own identity, since that string is the human description, where a
// Bash line already opens with its program and a Skill line with its skill.
func delegation(t *model.Tool) string {
	if t.Name != "Agent" {
		return ""
	}
	label := t.Identity
	if t.Model != "" {
		label += "@" + t.Model
	}
	if label == "" {
		return "" // an Agent call that named neither says nothing rather than "[]"
	}
	return "[" + label + "]"
}

func (r *renderer) toolLines(t *model.Tool, prefix string, depth int) []string {
	glyph, style := glyphTool, r.tool
	if t.Subagent != nil {
		glyph, style = glyphSubagent, r.subnt
	}
	status := r.ok.Render(glyphOK)
	if t.IsError {
		status = r.bad.Render(glyphErr)
	}
	dur := fmtToolDuration(t.Start, t.End)
	// A refused call says why in place of its duration. The error glyph alone
	// reads as "ran and failed", which sends a reader to fix the wrong thing —
	// and how long a call took before being denied is not worth the space.
	if t.Denial != "" {
		dur = r.bad.Render("denied: " + t.Denial)
	}

	head := fmt.Sprintf("%s%s %s%s %s %s",
		prefix, r.dim.Render("╭─"), style.Render(glyph+" "+t.Name+delegation(t)),
		r.args.Render("("+truncate(oneLine(t.Args), 60)+")"), status, dur)
	out := []string{strings.TrimRight(head, " ")}

	if t.Subagent != nil && r.opts.Channels.Subagents {
		nested := prefix + r.dim.Render("│") + " "
		return append(out, r.events(t.Subagent, nested, depth+1)...)
	}
	// Otherwise show the (possibly truncated) result body, if enabled. With
	// ToolResults off the activation line stands alone — the notion that the
	// tool fired, without its output.
	if r.opts.Channels.ToolResults {
		bodyPrefix := prefix + r.dim.Render("│") + " "
		return append(out, r.toolBody(t.Result, bodyPrefix)...)
	}
	return out
}

func (r *renderer) toolBody(text, prefix string) []string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}
	width := r.opts.Width - lipgloss.Width(prefix)
	lines := strings.Split(text, "\n")
	var out []string
	for _, raw := range lines {
		if len(out) >= toolBodyMaxLines {
			extra := len(lines) - len(out)
			out = append(out, prefix+r.dim.Render(fmt.Sprintf("… %s", plural(extra, "more line"))))
			break
		}
		for _, w := range wrapPlain(raw, width) {
			out = append(out, prefix+r.body.Render(w))
			if len(out) >= toolBodyMaxLines {
				break
			}
		}
	}
	return out
}

// markdown renders a body through glamour at the given wrap width, returning
// trimmed lines. With color on, markdown links in the prose become OSC 8
// terminal hyperlinks (see linkifyMarkdown).
func (r *renderer) markdown(text string, width int) []string {
	// With color on, strip [text](url) syntax before glamour so it renders the
	// link text as plain prose (no inline URL noise, no wrap-mangled href), then
	// wrap each rendered text in an OSC 8 hyperlink. With color off, leave the
	// source untouched — glamour emits its default "text url" form.
	src, links := text, []mdLinkSpec(nil)
	if r.opts.Color {
		src, links = extractLinks(text)
	}

	var lines []string
	if g := r.glamourFor(width); g == nil {
		lines = wrapPlain(src, width)
	} else if out, err := g.Render(src); err != nil {
		lines = wrapPlain(src, width)
	} else {
		lines = strings.Split(strings.Trim(out, "\n"), "\n")
		for i := range lines {
			lines[i] = strings.TrimRight(lines[i], " ") // drop glamour's wrap padding
		}
	}
	if len(links) > 0 {
		r.linkifyMarkdown(lines, links)
	}
	return lines
}

// linkPattern matches either a markdown inline link [text](url) (no nested
// brackets, parens, or newline) or a bare scheme://… URL of any scheme (the
// RFC 3986 scheme grammar: a leading letter, then letters/digits/+/-/.). A
// scheme without "//" (mailto:, tel:) is deliberately excluded — too easy to
// false-match ordinary "word:" prose. Alternation is leftmost-first, so at a
// '[' the markdown form wins and a URL inside its (url) is consumed there, never
// re-matched as bare. The bare branch runs to the next whitespace; trimBareURL
// then peels trailing sentence punctuation and unbalanced ')' back off, so a URL
// wrapped in "(see https://x)" and a URL that itself contains balanced parens
// (a Wikipedia article, say) both resolve correctly.
var linkPattern = regexp.MustCompile(`\[([^\]\n]+)\]\(([^)\n]+)\)|[a-zA-Z][a-zA-Z0-9+.-]*://[^\s]+`)

// asciiPunct is the CommonMark set of backslash-escapable ASCII punctuation.
const asciiPunct = "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"

// mdLinkSpec is one link's visible text and its href, in source order.
type mdLinkSpec struct{ text, url string }

// extractLinks rewrites text for glamour and records its links in source order.
// A markdown link [text](url) is reduced to its text — glamour renders inline
// links as "text url" with the raw URL shown and no OSC 8, so we keep the URL
// out of glamour and re-attach it afterward (see linkifyMarkdown). A bare URL is
// left in place — glamour renders it as text (coloring http/https via its own
// autolinker, leaving obsidian:// plain) but never emits OSC 8 — with its text
// and href both the URL, trailing punctuation trimmed off the href. A bare
// obsidian://open?…&file=X URI is the exception: its source is replaced by a
// [[X]] wikilink label (see obsidianNote) that links to the full URI, so a note
// reference shows [[Note]] instead of the raw URI string.
func extractLinks(text string) (string, []mdLinkSpec) {
	var links []mdLinkSpec
	src := linkPattern.ReplaceAllStringFunc(text, func(m string) string {
		if strings.HasPrefix(m, "[") {
			sm := linkPattern.FindStringSubmatch(m)
			links = append(links, mdLinkSpec{sm[1], sm[2]})
			return sm[1]
		}
		href := trimBareURL(m)
		trailer := m[len(href):] // punctuation trimmed off the href stays in prose
		if note, ok := obsidianNote(href); ok {
			links = append(links, mdLinkSpec{"[[" + note + "]]", href})
			// glamour parses whatever we feed it, so a note name containing *, `,
			// [] etc. would be mangled and no longer match the recorded label —
			// escape the name so glamour renders it verbatim (brackets survive
			// on their own; see linkifyMarkdown for the post-render match).
			return "[[" + escapeMarkdown(note) + "]]" + trailer
		}
		links = append(links, mdLinkSpec{href, href})
		return m // leave the bare URL in source; glamour renders it verbatim
	})
	return src, links
}

// trimBareURL peels off the run of the greedy bare-URL match that isn't really
// part of the link: trailing sentence punctuation, and a trailing ')' that has
// no matching '(' inside the URL (so "(see https://x)" drops the wrapping paren
// while "…/Foo_(bar)" keeps its balanced one). It loops because the two kinds
// interleave, e.g. "…/foo)." → "…/foo".
func trimBareURL(u string) string {
	for {
		if t := strings.TrimRight(u, ".,;:!?"); t != u {
			u = t
			continue
		}
		if strings.HasSuffix(u, ")") && strings.Count(u, ")") > strings.Count(u, "(") {
			u = u[:len(u)-1]
			continue
		}
		return u
	}
}

// escapeMarkdown backslash-escapes ASCII punctuation so glamour renders the
// string as literal text rather than interpreting *, _, `, [] etc. as markup.
func escapeMarkdown(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x80 && strings.IndexByte(asciiPunct, byte(r)) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// obsidianNote extracts the wikilink note name from an obsidian://open?…&file=<path>
// URI — the file's base name with folders and any trailing .md dropped, a
// #heading or #^block anchor kept. It reports false for any other obsidian URI
// (no file, or an action other than open), leaving it to render as a plain bare
// URL.
func obsidianNote(uri string) (string, bool) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "obsidian" || u.Host != "open" {
		return "", false
	}
	file := u.Query().Get("file")
	if file == "" {
		return "", false
	}
	return strings.TrimSuffix(path.Base(file), ".md"), true
}

// linkifyMarkdown wraps each link's visible text in glamour's rendered lines in
// an OSC 8 hyperlink to its href, editing lines in place. glamour interleaves
// SGR color codes through the text, so we match on the ANSI-stripped text and
// splice the wrapper back onto the styled string by offset. The matched text is
// re-rendered in the dedicated link style (replacing glamour's prose styling).
// Links are matched in source order; a link whose text was split across wrapped
// lines simply stays plain (no hyperlink) rather than blocking later links.
func (r *renderer) linkifyMarkdown(lines []string, links []mdLinkSpec) {
	placed := make([]bool, len(links))
	for n, line := range lines {
		plain, idx := stripANSI(line)
		type span struct{ ss, se, li int }
		var spans []span
		cursor := 0 // plain-byte offset; matches advance it to stay ordered
		for li := range links {
			if placed[li] {
				continue
			}
			at := strings.Index(plain[cursor:], links[li].text)
			if at < 0 {
				continue
			}
			s := cursor + at
			e := s + len(links[li].text)
			spans = append(spans, span{idx[s], idx[e], li})
			placed[li] = true
			cursor = e
		}
		if len(spans) == 0 {
			continue
		}
		var b strings.Builder
		last := 0
		for _, sp := range spans {
			b.WriteString(line[last:sp.ss])
			// Through the shared helper, so prose links and the Outputs section
			// cannot emit two different escape sequences for one idea. Unguarded
			// because reaching here already means links were collected, which
			// markdown does only when color is on.
			b.WriteString(r.hyperlink(links[sp.li].text, links[sp.li].url))
			last = sp.se
		}
		b.WriteString(line[last:])
		lines[n] = b.String()
	}
}

// stripANSI returns line with CSI escape sequences removed, plus idx mapping
// each plain byte offset to its offset in the styled line (idx[len(plain)] =
// len(line)), so a match on the plain text can be spliced back onto the styled
// string.
func stripANSI(line string) (string, []int) {
	var plain strings.Builder
	idx := make([]int, 0, len(line))
	for i := 0; i < len(line); {
		if line[i] == 0x1b { // skip a CSI escape: ESC '[' … final byte (0x40–0x7e)
			j := i + 1
			if j < len(line) && line[j] == '[' {
				j++ // past '['; its byte 0x5b is itself in the final-byte range
				for j < len(line) && !(line[j] >= 0x40 && line[j] <= 0x7e) {
					j++
				}
				if j < len(line) {
					j++ // include the final byte
				}
			} else {
				j++
			}
			i = j
			continue
		}
		idx = append(idx, i)
		plain.WriteByte(line[i])
		i++
	}
	idx = append(idx, len(line))
	return plain.String(), idx
}

func (r *renderer) glamourFor(width int) *glamour.TermRenderer {
	if width < 20 {
		width = 20
	}
	if g, ok := r.gcache[width]; ok {
		return g
	}
	style := styles.DarkStyleConfig
	if !r.opts.Color {
		style = styles.NoTTYStyleConfig
	}
	// Drop glamour's 2-space document margin so prose hugs the left rail; the
	// rail prefix (assistantIndent + "│ ") supplies all the indentation.
	zero := uint(0)
	style.Document.Margin = &zero
	g, err := glamour.NewTermRenderer(
		glamour.WithStyles(style),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		g = nil
	}
	r.gcache[width] = g
	return g
}

// ── Outputs ────────────────────────────────────────────────────────────────

// outputs lists what the session produced beyond its own transcript: the pull
// requests it opened, then the artifacts it published. Empty when it produced
// neither, which is the signal not to draw the section at all.
//
// The two kinds link differently, following the split assistant prose already
// makes. A pull request is its own URL as the link text, the way a bare URL in
// prose is — the URL already spells the repository and the number, so there is no
// better name to show. An artifact is its title with the URL hidden in the href,
// the way a markdown link is, because a claude.ai artifact id is an opaque uuid
// and showing it in place of a name would be strictly worse than the name.
func (r *renderer) outputs(s *model.Session) string {
	m := s.Meta
	if len(m.PRs) == 0 && len(m.Artifacts) == 0 {
		return ""
	}
	// Two columns of indent, matching the summary table's rows below.
	const indent = "  "
	width := max(r.opts.Width-len(indent), 20)

	var b strings.Builder
	b.WriteString(r.dim.Render("── Outputs ──") + "\n")
	for _, p := range m.PRs {
		u := p.Key()
		b.WriteString(indent + r.maybeLink(truncate(u, width), u) + "\n")
	}
	for _, a := range m.Artifacts {
		href, text := a.Key(), a.Title
		switch {
		case text == "":
			text = href // no title recorded: the URL is the only name there is
		case !r.opts.Color:
			// Plain output has no href to hide the URL in, so it is shown beside the
			// title — the same degradation a markdown link in prose makes.
			text += "  " + href
		}
		b.WriteString(indent + r.maybeLink(truncate(text, width), href) + "\n")
	}
	return b.String()
}

// maybeLink hyperlinks text when color is on and leaves it plain when off. The
// escape is invisible in a terminal but is literal bytes in a pipe or a file, and
// plain output's contract is plain text.
func (r *renderer) maybeLink(text, url string) string {
	if !r.opts.Color {
		return text
	}
	return r.hyperlink(text, url)
}

// hyperlink wraps text in an OSC 8 terminal hyperlink to url, in the link style.
// It carries no color policy of its own — callers that render in both modes gate
// it through maybeLink — so the escape sequence has exactly one home and prose
// links and the Outputs section cannot drift into emitting different bytes.
func (r *renderer) hyperlink(text, url string) string {
	return "\x1b]8;;" + url + "\x1b\\" + r.link.Render(text) + "\x1b]8;;\x1b\\"
}

// ── Summary table (metrics channel) ────────────────────────────────────────

func (r *renderer) summary(s *model.Session) string {
	if len(s.Turns) == 0 {
		return ""
	}
	type row struct {
		n     int
		tok   int
		tools int
		label string
	}
	var rows []row
	total := 0
	for i, t := range s.Turns {
		tok := t.Usage.Input + t.Usage.Output
		total += tok
		rows = append(rows, row{i + 1, tok, t.ToolCount, oneLine(t.Prompt)})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].tok > rows[j].tok })

	var b strings.Builder
	b.WriteString(r.dim.Render("── Summary (by token cost) ──") + "\n")
	b.WriteString(r.dim.Render("    % tok    tokens  tools  step") + "\n")
	limit := min(len(rows), 8)
	for _, rw := range rows[:limit] {
		pct := 0.0
		if total > 0 {
			pct = float64(rw.tok) / float64(total) * 100
		}
		b.WriteString(fmt.Sprintf("  %5.1f%%  %8s  %5d  %d. %s\n",
			pct, spend.Tokens(rw.tok), rw.tools, rw.n, truncate(rw.label, max(r.opts.Width-30, 20))))
	}
	if rest := len(rows) - limit; rest > 0 {
		b.WriteString(r.dim.Render(fmt.Sprintf("  …  (%s)\n", plural(rest, "more step"))))
	}
	return b.String()
}

// ── Formatting helpers ──────────────────────────────────────────────────────

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "??:??"
	}
	return t.Local().Format("15:04")
}

func fmtDuration(start, end time.Time) string {
	if start.IsZero() || end.IsZero() {
		return ""
	}
	secs := int(end.Sub(start).Seconds())
	if secs < 0 {
		return ""
	}
	h, m := secs/3600, (secs%3600)/60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

func fmtToolDuration(start, end time.Time) string {
	if start.IsZero() || end.IsZero() {
		return ""
	}
	secs := end.Sub(start).Seconds()
	if secs < 0 {
		return ""
	}
	switch {
	case secs < 10:
		return fmt.Sprintf("%.1fs", secs)
	case secs < 60:
		return fmt.Sprintf("%.0fs", secs)
	}
	mins, rem := int(secs)/60, int(secs)%60
	if mins < 60 {
		if rem == 0 {
			return fmt.Sprintf("%dm", mins)
		}
		return fmt.Sprintf("%dm%02ds", mins, rem)
	}
	return fmt.Sprintf("%dh%02dm", mins/60, mins%60)
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func oneLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func truncate(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}

// wrapPlain soft-wraps plain text (no ANSI) to maxWidth runes per line.
func wrapPlain(text string, maxWidth int) []string {
	if maxWidth < 10 {
		maxWidth = 10
	}
	var out []string
	for _, raw := range strings.Split(text, "\n") {
		if len([]rune(raw)) <= maxWidth {
			out = append(out, raw)
			continue
		}
		var line strings.Builder
		for _, word := range strings.Fields(raw) {
			switch {
			case line.Len() == 0:
				line.WriteString(word)
			case len([]rune(line.String()))+1+len([]rune(word)) > maxWidth:
				out = append(out, line.String())
				line.Reset()
				line.WriteString(word)
			default:
				line.WriteByte(' ')
				line.WriteString(word)
			}
		}
		if line.Len() > 0 {
			out = append(out, line.String())
		}
	}
	return out
}
