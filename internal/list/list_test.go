package list

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eitanpo/agentry/internal/entrypoint"
	"github.com/eitanpo/agentry/internal/model"
)

func TestParseWhen(t *testing.T) {
	now := time.Date(2026, 6, 3, 14, 30, 0, 0, time.Local)
	midnightToday := time.Date(2026, 6, 3, 0, 0, 0, 0, time.Local)

	tests := []struct {
		in   string
		want time.Time
	}{
		{"today", midnightToday},
		{"yesterday", midnightToday.AddDate(0, 0, -1)},
		{"24h", now.Add(-24 * time.Hour)},
		{"7d", now.Add(-7 * 24 * time.Hour)},
		{"2w", now.Add(-2 * 7 * 24 * time.Hour)},
		{"2026-06-01", time.Date(2026, 6, 1, 0, 0, 0, 0, time.Local)},
		{"TODAY", midnightToday}, // case-insensitive
	}
	for _, tt := range tests {
		got, err := ParseWhen(tt.in, now)
		if err != nil {
			t.Errorf("ParseWhen(%q) error: %v", tt.in, err)
			continue
		}
		if !got.Equal(tt.want) {
			t.Errorf("ParseWhen(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}

	for _, bad := range []string{"", "soon", "5", "5y", "2026/06/01", "-3d"} {
		if _, err := ParseWhen(bad, now); err == nil {
			t.Errorf("ParseWhen(%q) = nil error, want error", bad)
		}
	}
}

func TestSelect(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 6, 3, h, 0, 0, 0, time.UTC) }
	sums := []model.Summary{
		{ID: "noon", End: at(12)},
		{ID: "morning", End: at(9)},
		{ID: "evening", End: at(18)},
		{ID: "onlystart", Start: at(15)}, // no End: activity falls back to Start
	}

	t.Run("orders most-recent first", func(t *testing.T) {
		got := Select(sums, time.Time{}, time.Time{}, 0)
		want := []string{"evening", "onlystart", "noon", "morning"}
		assertIDs(t, got, want)
	})

	t.Run("limit caps count", func(t *testing.T) {
		got := Select(sums, time.Time{}, time.Time{}, 2)
		assertIDs(t, got, []string{"evening", "onlystart"})
	})

	t.Run("since drops earlier", func(t *testing.T) {
		got := Select(sums, at(12), time.Time{}, 0)
		assertIDs(t, got, []string{"evening", "onlystart", "noon"})
	})

	t.Run("until drops later", func(t *testing.T) {
		got := Select(sums, time.Time{}, at(12), 0)
		assertIDs(t, got, []string{"noon", "morning"})
	})

	t.Run("window matching none is empty", func(t *testing.T) {
		got := Select(sums, at(20), time.Time{}, 0)
		if len(got) != 0 {
			t.Errorf("got %d, want 0", len(got))
		}
	})
}

func TestFmtDur(t *testing.T) {
	base := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		secs int
		want string
	}{
		{8, "8s"},
		{45 * 60, "45m"},
		{2*3600 + 5*60, "2h05m"},
		{27*3600 + 14*60, "27h14m"},
	}
	for _, tt := range tests {
		got := fmtDur(base, base.Add(time.Duration(tt.secs)*time.Second))
		if got != tt.want {
			t.Errorf("fmtDur(%ds) = %q, want %q", tt.secs, got, tt.want)
		}
	}
	if got := fmtDur(time.Time{}, base); got != "" {
		t.Errorf("fmtDur(zero start) = %q, want empty", got)
	}
	if got := fmtDur(base, base.Add(-time.Hour)); got != "" {
		t.Errorf("fmtDur(negative) = %q, want empty", got)
	}
}

func TestRenderPlain(t *testing.T) {
	sums := []model.Summary{
		{
			ID:       "abc123",
			Start:    time.Date(2026, 6, 3, 14, 5, 0, 0, time.UTC),
			End:      time.Date(2026, 6, 3, 14, 50, 0, 0, time.UTC),
			NumTurns: 12,
			Title:    "first\nline only",
		},
	}
	var b strings.Builder
	if err := Render(&b, sums, Options{Width: 100, Color: false}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.Count(out, "\n") != 1 {
		t.Errorf("want one row, got %q", out)
	}
	for _, want := range []string{"abc123", "45m", "12t", "first"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %q", want, out)
		}
	}
	// The when column is the last-activity time (End), not the start. Format via
	// the same .Local() the renderer uses so the assertion is timezone-independent.
	wantWhen := sums[0].End.Local().Format("2006-01-02 15:04")
	startWhen := sums[0].Start.Local().Format("2006-01-02 15:04")
	if !strings.Contains(out, wantWhen) {
		t.Errorf("when column should show End time %q: %q", wantWhen, out)
	}
	if wantWhen != startWhen && strings.Contains(out, startWhen) {
		t.Errorf("when column should not show Start time %q: %q", startWhen, out)
	}
	if strings.Contains(out, "line only") {
		t.Errorf("title should be truncated at newline: %q", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("color off should emit no ANSI: %q", out)
	}
}

func TestTag(t *testing.T) {
	tests := []struct {
		name string
		s    model.Summary
		want string
	}{
		{"terminal", model.Summary{Entrypoint: "cli"}, "cli"},
		{"desktop app", model.Summary{Entrypoint: "claude-desktop"}, "app"},
		{"headless", model.Summary{Entrypoint: "sdk-cli"}, "sdk"},
		// Claude Code adds entrypoints without notice. A blank would read as "no
		// data" and hide the new value; "?" says a value exists and is unmapped.
		{"unrecognized value", model.Summary{Entrypoint: "future-client"}, "?"},
		// Logs predating the field carry none at all — that really is no data.
		{"absent", model.Summary{}, ""},
		{"resumed elsewhere", model.Summary{
			Entrypoint:  "cli",
			Entrypoints: []string{"claude-desktop", "cli"},
		}, "cli+"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Tag(tt.s); got != tt.want {
				t.Errorf("Tag = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFilterByFrom(t *testing.T) {
	sums := []model.Summary{
		{ID: "a", Entrypoint: "cli"},
		{ID: "b", Entrypoint: "claude-desktop"},
		{ID: "c", Entrypoint: "sdk-cli"},
		{ID: "d", Entrypoint: "future-client"},
		{ID: "e"},
	}
	ids := func(got []model.Summary) string {
		var b strings.Builder
		for _, s := range got {
			b.WriteString(s.ID)
		}
		return b.String()
	}
	tests := []struct {
		name string
		from string
		want string
	}{
		// The default is the behavior change: headless runs are the bulk of what
		// a machine using hooks accumulates and almost none of what is read back.
		// An unrecognized entrypoint is kept — a new value is more likely a new
		// way of working than a new kind of noise — and so is a session with none.
		{"default hides only headless", "", "abde"},
		{"all keeps everything", entrypoint.All, "abcde"},
		{"cli alone", entrypoint.CLI, "a"},
		{"app alone", entrypoint.App, "b"},
		{"sdk alone", entrypoint.SDK, "c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ids(FilterByFrom(sums, tt.from)); got != tt.want {
				t.Errorf("FilterByFrom(%q) kept %q, want %q", tt.from, got, tt.want)
			}
		})
	}
}

// TestRenderFromColumn pins when the tag column is drawn. It follows the project
// column's rule — only when it varies — so a listing of a single kind keeps the
// layout it had before entrypoints were shown.
func TestRenderFromColumn(t *testing.T) {
	mk := func(id, ep, title string) model.Summary {
		return model.Summary{
			ID: id, Entrypoint: ep, Title: title,
			Start: time.Date(2026, 6, 3, 14, 0, 0, 0, time.UTC),
			End:   time.Date(2026, 6, 3, 14, 5, 0, 0, time.UTC),
		}
	}
	render := func(t *testing.T, sums []model.Summary) string {
		t.Helper()
		var b strings.Builder
		if err := Render(&b, sums, Options{Width: 120, Color: false}); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}

	t.Run("one kind draws no tag column", func(t *testing.T) {
		out := render(t, []model.Summary{mk("a", "cli", "alpha"), mk("b", "cli", "beta")})
		if strings.Contains(out, "cli") {
			t.Errorf("single-kind listing must not draw the tag column: %q", out)
		}
	})

	t.Run("two kinds tag every row", func(t *testing.T) {
		out := render(t, []model.Summary{mk("a", "cli", "alpha"), mk("b", "claude-desktop", "beta")})
		for _, want := range []string{"cli", "app", "alpha", "beta"} {
			if !strings.Contains(out, want) {
				t.Errorf("output missing %q: %q", want, out)
			}
		}
	})

	t.Run("a resumed session alone does not draw the column", func(t *testing.T) {
		// The column exists to separate kinds. One row reading "cli+" among plain
		// "cli" rows already says the session moved, so the suffix alone is not
		// variation worth a column for.
		s := mk("a", "cli", "alpha")
		s.Entrypoints = []string{"claude-desktop", "cli"}
		out := render(t, []model.Summary{s, mk("b", "cli", "beta")})
		if strings.Contains(out, "cli+") {
			t.Errorf("suffix alone should not trigger the column: %q", out)
		}
	})
}

func TestProjectLabels(t *testing.T) {
	tests := []struct {
		name string
		cwds []string
		want map[string]string
	}{
		{
			// One project is the whole history of the listing before this change:
			// nil means the column is not drawn at all, so no existing output moves.
			name: "a single project draws no column",
			cwds: []string{"/Users/me/Projects/me/agentry", "/Users/me/Projects/me/agentry"},
			want: nil,
		},
		{
			name: "distinct basenames need only the last component",
			cwds: []string{"/Users/me/Projects/me/agentry", "/Users/me/Projects/me/dotfiles"},
			want: map[string]string{
				"/Users/me/Projects/me/agentry":  "agentry",
				"/Users/me/Projects/me/dotfiles": "dotfiles",
			},
		},
		{
			// The case a bare basename gets wrong: a repo tree groups colliding
			// names under owners, so both rows would read "agentry".
			name: "a colliding basename grows until it is unique",
			cwds: []string{"/p/me/agentry", "/p/wix/agentry", "/p/me/dotfiles"},
			want: map[string]string{
				"/p/me/agentry":  "me/agentry",
				"/p/wix/agentry": "wix/agentry",
				"/p/me/dotfiles": "dotfiles",
			},
		},
		{
			// A worktree is a place inside a repo, not a project: labelling it by
			// its own name reported one project as two, and the listing's scope
			// rule already treats a repo's worktrees as the repo. With one project
			// left, the column is not drawn at all.
			name: "a repo and its worktree are one project",
			cwds: []string{"/p/repo", "/p/repo/.claude/worktrees/feature"},
			want: nil,
		},
		{
			// The case that made this wrong visible: three worktrees of one repo
			// read as `plan`, `ngnix` and `jfrog-usage` — three labels naming no
			// project. Every row must carry the repo, beside a real second project.
			name: "worktrees of one repo share the repo's label",
			cwds: []string{
				"/p/wix/artifactory-migration",
				"/p/wix/artifactory-migration/.claude/worktrees/plan",
				"/p/wix/artifactory-migration/.claude/worktrees/ngnix",
				"/p/me/dotfiles",
			},
			want: map[string]string{
				"/p/wix/artifactory-migration":                         "artifactory-migration",
				"/p/wix/artifactory-migration/.claude/worktrees/plan":  "artifactory-migration",
				"/p/wix/artifactory-migration/.claude/worktrees/ngnix": "artifactory-migration",
				"/p/me/dotfiles": "dotfiles",
			},
		},
		{
			// One path is a full suffix of the other, so no suffix of the shorter
			// is ever unique; it must fall back to the whole path rather than
			// silently duplicating its sibling's label.
			name: "a path that is a suffix of another keeps its full path",
			cwds: []string{"/a/b", "/x/a/b"},
			want: map[string]string{
				"/a/b":   "/a/b",
				"/x/a/b": "x/a/b",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sums []model.Summary
			for i, c := range tt.cwds {
				sums = append(sums, model.Summary{ID: string(rune('a' + i)), Cwd: c})
			}
			got := projectLabels(sums)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("label for %q = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

// TestRenderProjectColumn pins when the column appears and that it does not
// eat the title: a listing confined to one project must render exactly as it did
// before cross-project listing existed.
func TestRenderProjectColumn(t *testing.T) {
	mk := func(id, cwd, title string) model.Summary {
		return model.Summary{
			ID: id, Cwd: cwd, Title: title,
			Start: time.Date(2026, 6, 3, 14, 0, 0, 0, time.UTC),
			End:   time.Date(2026, 6, 3, 14, 5, 0, 0, time.UTC),
		}
	}
	t.Run("one project shows no project column", func(t *testing.T) {
		var b strings.Builder
		sums := []model.Summary{mk("a", "/p/me/agentry", "alpha"), mk("b", "/p/me/agentry", "beta")}
		if err := Render(&b, sums, Options{Width: 120, Color: false}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(b.String(), "agentry") {
			t.Errorf("single-project listing must not draw the project column: %q", b.String())
		}
	})

	t.Run("two projects label every row", func(t *testing.T) {
		var b strings.Builder
		sums := []model.Summary{mk("a", "/p/me/agentry", "alpha"), mk("b", "/p/me/dotfiles", "beta")}
		if err := Render(&b, sums, Options{Width: 120, Color: false}); err != nil {
			t.Fatal(err)
		}
		out := b.String()
		for _, want := range []string{"agentry", "dotfiles", "alpha", "beta"} {
			if !strings.Contains(out, want) {
				t.Errorf("output missing %q: %q", want, out)
			}
		}
	})

	t.Run("a long project name leaves the title readable", func(t *testing.T) {
		// The failure this guards: at 100 columns a 21-character project name
		// drove the title to its 10-column floor, truncating away the one field
		// the row is actually scanned by.
		var b strings.Builder
		sums := []model.Summary{
			mk("a", "/p/wix/artifactory-migration", "a distinctly long session title"),
			mk("b", "/p/wix/other", "beta"),
		}
		if err := Render(&b, sums, Options{Width: 100, Color: false}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(b.String(), "a distinctly") {
			t.Errorf("title squeezed out by the project column: %q", b.String())
		}
	})
}

// TestWorktreeLabels pins both gates on the column that fills the project
// column's slot inside one repository, and the two shapes that must not draw it.
func TestWorktreeLabels(t *testing.T) {
	const repo = "/p/wix/artifactory-migration"
	wt := func(name string) string { return repo + "/.claude/worktrees/" + name }
	tests := []struct {
		name string
		cwds []string
		want map[string]string
	}{
		{
			// Without this column the four rows differ by nothing but their titles,
			// since collapsing worktrees into the project label removed the only
			// field that separated them.
			name: "one repo across worktrees names each place",
			cwds: []string{repo, wt("plan"), wt("ngnix")},
			want: map[string]string{
				repo:        "—",
				wt("plan"):  "plan",
				wt("ngnix"): "ngnix",
			},
		},
		{
			// Appear-only-when-it-varies: every session sat in the same place, so
			// the column would repeat one value down the listing.
			name: "one worktree alone draws no column",
			cwds: []string{wt("plan"), wt("plan")},
			want: nil,
		},
		{
			name: "a repo with no worktree draws no column",
			cwds: []string{repo, repo},
			want: nil,
		},
		{
			// The mutual exclusion the layout depends on: with two projects the
			// project column is drawn, so this one must not be, or a row would
			// carry two path columns and the title would pay for both.
			name: "more than one project draws no worktree column",
			cwds: []string{repo, wt("plan"), "/p/me/dotfiles"},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sums []model.Summary
			for i, c := range tt.cwds {
				sums = append(sums, model.Summary{ID: string(rune('a' + i)), Cwd: c})
			}
			got := worktreeLabels(sums)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("label for %q = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

// TestRenderWorktreeColumn pins what the rendered row shows: the worktree keeps
// its head where the project label keeps its tail, since a worktree Claude Code
// creates from the desktop app carries a generated hash suffix.
func TestRenderWorktreeColumn(t *testing.T) {
	const repo = "/p/wix/artifactory-migration"
	mk := func(id, cwd, title string) model.Summary {
		return model.Summary{
			ID: id, Cwd: cwd, Title: title,
			Start: time.Date(2026, 6, 3, 14, 0, 0, 0, time.UTC),
			End:   time.Date(2026, 6, 3, 14, 5, 0, 0, time.UTC),
		}
	}
	var b strings.Builder
	sums := []model.Summary{
		mk("a", repo, "alpha"),
		mk("b", repo+"/.claude/worktrees/ecr-gar-single-registry-f399e1", "beta"),
	}
	if err := Render(&b, sums, Options{Width: 160, Color: false}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	// The head is the name someone chose; left-truncation would keep the hash and
	// drop it, which is why the direction differs from the project column's.
	if !strings.Contains(out, "ecr-gar-single-registry") {
		t.Errorf("worktree name lost its head: %q", out)
	}
	if !strings.Contains(out, "—") {
		t.Errorf("a session in the repo's own checkout must show —: %q", out)
	}
	if strings.Contains(out, "artifactory-migration") {
		t.Errorf("one project must not draw the project column: %q", out)
	}
}

func TestTruncateLeft(t *testing.T) {
	// The label is a path suffix, so the tail distinguishes it — dropping the
	// head keeps the informative end, which right-truncation would discard.
	if got := truncateLeft("artifactory-migration", 9); got != "…migration" && got != "…igration" {
		t.Errorf("truncateLeft = %q, want the tail kept", got)
	}
	if got := truncateLeft("short", 9); got != "short" {
		t.Errorf("truncateLeft should not alter a value that fits, got %q", got)
	}
}

func TestRenderNewestLast(t *testing.T) {
	// Input arrives most-recent first (as Select returns it); output must print
	// it oldest-to-newest, so the newest row is last.
	sums := []model.Summary{
		{ID: "newer", End: time.Date(2026, 6, 3, 18, 0, 0, 0, time.UTC)},
		{ID: "older", End: time.Date(2026, 6, 3, 9, 0, 0, 0, time.UTC)},
	}
	var b strings.Builder
	if err := Render(&b, sums, Options{Width: 100, Color: false}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.Index(out, "older") > strings.Index(out, "newer") {
		t.Errorf("want older before newer (newest last), got:\n%s", out)
	}
}

func TestArrangeGroupsForks(t *testing.T) {
	born := func(day int) time.Time { return time.Date(2026, 6, 20+day, 0, 0, 0, 0, time.UTC) }
	act := func(hour int) time.Time { return time.Date(2026, 6, 27, hour, 0, 0, 0, time.UTC) }
	// Input arrives most-recent first (as Select returns). Family "R" is an
	// original (born day 0) plus a fork (born day 1, most recent); "solo" stands
	// alone (older, no root id).
	sums := []model.Summary{
		{ID: "fork", RootUUID: "R", Born: born(1), End: act(18)},
		{ID: "orig", RootUUID: "R", Born: born(0), End: act(12)},
		{ID: "solo", Born: born(0), End: act(9)},
	}
	rows := arrange(sums)
	// Top-to-bottom: solo (family anchor 09:00, oldest) then family R (anchor
	// 18:00): original first, fork indented beneath it.
	want := []struct {
		id   string
		fork bool
	}{{"solo", false}, {"orig", false}, {"fork", true}}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i, w := range want {
		if rows[i].s.ID != w.id || rows[i].fork != w.fork {
			t.Errorf("row %d = {%q fork=%v}, want {%q fork=%v}", i, rows[i].s.ID, rows[i].fork, w.id, w.fork)
		}
	}
}

func TestForkInheritedTitleShownByDivergentPrompt(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 6, 20+d, 0, 0, 0, 0, time.UTC) }
	// Fork shares the parent's title (ai-title not regenerated) and its prompt
	// prefix, plus one new prompt.
	parent := model.Summary{ID: "orig", RootUUID: "R", Born: day(0), Title: "shared title",
		Prompts: []string{"set up the parser", "fix the bug"}}
	fork := model.Summary{ID: "fork", RootUUID: "R", Born: day(1), Title: "shared title",
		Prompts: []string{"set up the parser", "fix the bug", "try a different approach"}}
	rows := arrange([]model.Summary{fork, parent}) // most-recent first

	titleOf := func(id string) string {
		for _, r := range rows {
			if r.s.ID == id {
				return r.s.Title
			}
		}
		t.Fatalf("no row for %q", id)
		return ""
	}
	if got := titleOf("fork"); got != "try a different approach" {
		t.Errorf("fork title = %q, want its first divergent prompt", got)
	}
	if got := titleOf("orig"); got != "shared title" {
		t.Errorf("original title = %q, want it untouched", got)
	}

	// A regenerated (differing) title is left alone; a fork with no new prompt
	// keeps the shared title.
	fork2 := fork
	fork2.Title = "its own summary"
	rows = arrange([]model.Summary{fork2, parent})
	if got := titleOf2(rows, "fork"); got != "its own summary" {
		t.Errorf("regenerated fork title = %q, want it kept", got)
	}
	noNew := model.Summary{ID: "fork", RootUUID: "R", Born: day(1), Title: "shared title",
		Prompts: []string{"set up the parser", "fix the bug"}}
	rows = arrange([]model.Summary{noNew, parent})
	if got := titleOf2(rows, "fork"); got != "shared title" {
		t.Errorf("fork with no new prompt = %q, want the shared title", got)
	}
}

func titleOf2(rows []frow, id string) string {
	for _, r := range rows {
		if r.s.ID == id {
			return r.s.Title
		}
	}
	return ""
}

func TestRenderForkIndent(t *testing.T) {
	sums := []model.Summary{
		{ID: "forkid", Title: "the fork", RootUUID: "R", Born: time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 6, 27, 18, 0, 0, 0, time.UTC)},
		{ID: "origid", Title: "the original", RootUUID: "R", Born: time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)},
	}
	var b strings.Builder
	if err := Render(&b, sums, Options{Width: 100, Color: false}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	// The fork's title is indented with the marker; the original's is not.
	if !strings.Contains(out, forkGlyph+"the fork") {
		t.Errorf("fork title not indented with %q: %q", forkGlyph, out)
	}
	if strings.Contains(out, forkGlyph+"the original") {
		t.Errorf("original should not be indented: %q", out)
	}
	// Original prints above its fork.
	if strings.Index(out, "the original") > strings.Index(out, "the fork") {
		t.Errorf("want original above fork:\n%s", out)
	}
}

func TestRenderIncludePrompts(t *testing.T) {
	sums := []model.Summary{
		{ID: "s1", Title: "do a thing", Prompts: []string{"first ask", "second ask"}},
	}
	// Off: prompts absent.
	var off strings.Builder
	if err := Render(&off, sums, Options{Width: 100, Color: false, Prompts: false}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(off.String(), "first ask") {
		t.Errorf("prompts should be hidden without Prompts: %q", off.String())
	}
	// On: prompts listed, each with the glyph, one per line.
	var on strings.Builder
	if err := Render(&on, sums, Options{Width: 100, Color: false, Prompts: true}); err != nil {
		t.Fatal(err)
	}
	out := on.String()
	for _, p := range []string{"first ask", "second ask"} {
		if !strings.Contains(out, "❯ "+p) {
			t.Errorf("output missing %q with glyph: %q", p, out)
		}
	}
	// Prompts are grouped on a rail and the block is closed by a rule.
	if !strings.Contains(out, "│ ❯ first ask") {
		t.Errorf("prompt not on the rail: %q", out)
	}
	if !strings.Contains(out, "╰─") {
		t.Errorf("session block not closed by a rule: %q", out)
	}
}

func TestRenderJSON(t *testing.T) {
	sums := []model.Summary{
		{ID: "s1", Title: "do work", NumTurns: 3,
			Tools:    []model.ToolStat{{Tool: "Bash", Identity: "git", Count: 2}},
			Commands: []string{"git status"}},
	}
	var b strings.Builder
	if err := RenderJSON(&b, sums); err != nil {
		t.Fatal(err)
	}
	// Parses back as an array carrying the tagged model fields.
	var got []map[string]any
	if err := json.Unmarshal([]byte(b.String()), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, b.String())
	}
	if len(got) != 1 || got[0]["id"] != "s1" || got[0]["title"] != "do work" {
		t.Fatalf("unexpected JSON: %s", b.String())
	}
	tools, ok := got[0]["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools missing/wrong: %s", b.String())
	}
	tool := tools[0].(map[string]any)
	if tool["tool"] != "Bash" || tool["identity"] != "git" || tool["count"].(float64) != 2 {
		t.Errorf("tool entry wrong: %s", b.String())
	}
	if cmds := got[0]["commands"].([]any); len(cmds) != 1 || cmds[0] != "git status" {
		t.Errorf("commands wrong: %s", b.String())
	}

	// Empty input serializes as an array, not null.
	var empty strings.Builder
	if err := RenderJSON(&empty, nil); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(empty.String()) != "[]" {
		t.Errorf("empty input = %q, want []", empty.String())
	}
}

func TestFilterByTools(t *testing.T) {
	sums := []model.Summary{
		{ID: "expert-run", Tools: []model.ToolStat{
			{Tool: "Skill", Identity: "expert", Count: 2},
			{Tool: "Agent", Identity: "general-purpose", Count: 9},
		}, Commands: []string{"git status", "python3 collect.py"}},
		{ID: "exa-run", Tools: []model.ToolStat{
			{Tool: "Bash", Identity: "exa", Count: 1},
			{Tool: "Skill", Identity: "sonar-search", Count: 1},
		}, Commands: []string{"/skills/exa/scripts/exa --contents q"}},
		{ID: "research", Tools: []model.ToolStat{
			{Tool: "Agent", Identity: "researcher", Count: 3},
		}, Commands: nil},
	}
	match := func(f Filters) []string { return ids(FilterByTools(sums, f)) }
	eq := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range want {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	cases := []struct {
		name string
		f    Filters
		want []string
	}{
		{"empty is no-op", Filters{}, []string{"expert-run", "exa-run", "research"}},
		{"used-tool exact, case-insensitive", Filters{Used: Criteria{Tool: "bash"}}, []string{"exa-run"}},
		{"used-skill substring", Filters{Used: Criteria{Skill: "sonar"}}, []string{"exa-run"}}, // sonar-search
		{"used-agent", Filters{Used: Criteria{Agent: "researcher"}}, []string{"research"}},
		{"used-command substring", Filters{Used: Criteria{Command: "git"}}, []string{"expert-run"}},
		{"used matches command", Filters{Used: Criteria{Any: "exa"}}, []string{"exa-run"}}, // via command text
		{"used matches skill", Filters{Used: Criteria{Any: "expert"}}, []string{"expert-run"}},
		{"used does not match tool name", Filters{Used: Criteria{Any: "Bash"}}, nil}, // identity axis only
		{"AND of two fields", Filters{Used: Criteria{Skill: "expert", Agent: "general"}}, []string{"expert-run"}},
		{"AND with no overlap", Filters{Used: Criteria{Skill: "expert", Agent: "researcher"}}, nil},

		// Negation keeps exactly what its positive twin drops.
		{"not-used-tool", Filters{NotUsed: Criteria{Tool: "bash"}}, []string{"expert-run", "research"}},
		{"not-used-skill", Filters{NotUsed: Criteria{Skill: "sonar"}}, []string{"expert-run", "research"}},
		{"not-used-agent", Filters{NotUsed: Criteria{Agent: "researcher"}}, []string{"expert-run", "exa-run"}},
		{"not-used-command", Filters{NotUsed: Criteria{Command: "git"}}, []string{"exa-run", "research"}},
		{"not-used catch-all", Filters{NotUsed: Criteria{Any: "expert"}}, []string{"exa-run", "research"}},
		// The compliance shape the flag exists for: a presence AND an absence,
		// which used to take two listings and a set difference on ids.
		{"presence and absence together", Filters{
			Used:    Criteria{Agent: "general-purpose"},
			NotUsed: Criteria{Skill: "expert"},
		}, nil},
		{"presence and absence that do co-occur", Filters{
			Used:    Criteria{Agent: "general-purpose"},
			NotUsed: Criteria{Skill: "sonar"},
		}, []string{"expert-run"}},
		// Negations AND with each other: a session must fail every one.
		{"two negations", Filters{NotUsed: Criteria{Skill: "sonar", Agent: "researcher"}}, []string{"expert-run"}},
		// A flag and its negation on one value describe no session. That is an
		// empty result, not a usage error — they are not competing to decide the
		// same thing, they simply cannot both hold.
		{"a value both required and forbidden", Filters{
			Used:    Criteria{Skill: "expert"},
			NotUsed: Criteria{Skill: "expert"},
		}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := match(c.f); !eq(got, c.want) {
				t.Errorf("FilterByTools(%+v) = %v, want %v", c.f, got, c.want)
			}
		})
	}
}

func TestRenderIncludeTools(t *testing.T) {
	sums := []model.Summary{
		{ID: "s1", Title: "do work", Tools: []model.ToolStat{
			{Tool: "Bash", Identity: "gh", Count: 12},
			{Tool: "Bash", Identity: "git", Count: 40},
			{Tool: "Skill", Identity: "expert", Count: 2},
			{Tool: "Agent", Identity: "researcher", Count: 9},
			{Tool: "Read", Identity: "", Count: 100},
		}},
	}
	// Off: breakdown absent.
	var off strings.Builder
	if err := Render(&off, sums, Options{Width: 100, Color: false, Tools: false}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(off.String(), "git ×40") {
		t.Errorf("tools should be hidden without Tools: %q", off.String())
	}
	// On: one line per category, entries count-desc, Other by tool name.
	var on strings.Builder
	if err := Render(&on, sums, Options{Width: 100, Color: false, Tools: true}); err != nil {
		t.Fatal(err)
	}
	out := on.String()
	for _, want := range []string{"Skills", "expert ×2", "Agents", "researcher ×9", "Bash", "Other", "Read ×100"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %q", want, out)
		}
	}
	// Within Bash, the higher count sorts first.
	if i, j := strings.Index(out, "git ×40"), strings.Index(out, "gh ×12"); i < 0 || j < 0 || i > j {
		t.Errorf("Bash entries not ordered count-desc (git before gh): %q", out)
	}
	// The block is closed by a rule, like --include prompts.
	if !strings.Contains(out, "╰─") {
		t.Errorf("session block not closed by a rule: %q", out)
	}
}

// TestFilterByFile pins --used-file across the two records of what a session
// touched. Each source alone answers wrongly for a large share of real sessions,
// so both subtests below would pass against a half-implementation that dropped
// the other source — which is why they are asserted separately.
func TestFilterByFile(t *testing.T) {
	sums := []model.Summary{
		// A session with a tracked-file record and no matching Edit: the file was
		// rewritten by a shell command, which no tool argument records.
		{ID: "shell-rewrite",
			Files: []string{"/repo/PRODUCT.md", "/repo/Makefile"},
			Tools: []model.ToolStat{{Tool: "Bash", Identity: "sed", Count: 1}}},
		// A session with Edit targets and no tracked-file record at all — about
		// half of local sessions carry no file-history entries.
		{ID: "no-history",
			Tools: []model.ToolStat{{Tool: "Edit", Identity: "/repo/internal/list/list.go", Count: 3}}},
		// A Write target, to confirm the filter is not Edit-only.
		{ID: "wrote",
			Tools: []model.ToolStat{{Tool: "Write", Identity: "/repo/docs/notes.md", Count: 1}}},
		// Reads the file but changes nothing: --used-file is about what the work
		// landed on, so this must not match.
		{ID: "read-only",
			Tools: []model.ToolStat{{Tool: "Read", Identity: "", Count: 5}},
			Files: []string{"/repo/other.go"}},
	}
	cases := []struct {
		name, file string
		want       []string
	}{
		{"tracked file with no matching tool call", "PRODUCT.md", []string{"shell-rewrite"}},
		{"edit target in a session with no tracked record", "list/list.go", []string{"no-history"}},
		{"write target counts too", "notes.md", []string{"wrote"}},
		{"substring spans directories", "/repo/", []string{"shell-rewrite", "no-history", "wrote", "read-only"}},
		{"case-insensitive", "product.md", []string{"shell-rewrite"}},
		{"a file nothing touched", "absent.txt", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ids(FilterByTools(sums, Filters{Used: Criteria{File: c.file}}))
			if len(got) != len(c.want) {
				t.Fatalf("--used-file %q = %v, want %v", c.file, got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Errorf("--used-file %q = %v, want %v", c.file, got, c.want)
				}
			}
		})
	}

	// A file axis is not the identity axis: --used stays a skill/agent/command
	// catch-all, so widening it here would change what existing calls return.
	if got := ids(FilterByTools(sums, Filters{Used: Criteria{Any: "PRODUCT.md"}})); len(got) != 0 {
		t.Errorf("--used should not match files, got %v", got)
	}
}

// TestRenderIncludeEditsAndDenials pins the two dimensions the tools breakdown
// used to drop: which files a session edited, and which calls never ran.
func TestRenderIncludeEditsAndDenials(t *testing.T) {
	sums := []model.Summary{
		{ID: "s1", Title: "do work",
			Tools: []model.ToolStat{
				{Tool: "Edit", Identity: "/repo/internal/list/list.go", Count: 3},
				{Tool: "Write", Identity: "/repo/docs/notes.md", Count: 1},
				{Tool: "Bash", Identity: "git", Count: 4},
			},
			Denials: []model.DenialStat{
				{Kind: "permission-rule", Tool: "Bash", Identity: "rm", Count: 2},
				{Kind: "user-rejected", Tool: "Edit", Identity: "/repo/main.go", Count: 1},
			}},
	}
	var b strings.Builder
	if err := Render(&b, sums, Options{Width: 120, Color: false, Tools: true}); err != nil {
		t.Fatal(err)
	}
	out := b.String()

	// Edits are labelled by base name: a column of repeated directory prefixes
	// distinguishes nothing, and --format json keeps the full path.
	for _, want := range []string{"Edits", "list.go ×3", "notes.md ×1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "/repo/internal") {
		t.Errorf("the table should shorten paths, not print them whole: %q", out)
	}

	// Two files sharing a base name keep enough path to tell them apart. Real
	// sessions hit this immediately — a repo with internal/list/list.go and
	// internal/cli/list.go printed two entries reading "list.go" with different
	// counts, which looks like a bug in the tally rather than two files.
	collide := []model.Summary{{ID: "s3", Title: "collide", Tools: []model.ToolStat{
		{Tool: "Edit", Identity: "/repo/internal/list/list.go", Count: 2},
		{Tool: "Edit", Identity: "/repo/internal/cli/list.go", Count: 1},
	}}}
	var c strings.Builder
	if err := Render(&c, collide, Options{Width: 120, Color: false, Tools: true}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"list/list.go ×2", "cli/list.go ×1"} {
		if !strings.Contains(c.String(), want) {
			t.Errorf("colliding base names not disambiguated, missing %q: %q", want, c.String())
		}
	}
	// Denials name what refused the call, so an auto-allow decision has a source.
	for _, want := range []string{"Denied", "permission-rule: Bash/rm ×2", "user-rejected: Edit/main.go ×1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %q", want, out)
		}
	}
	// A session with no denials gets no Denied line at all — an empty one would
	// read as a report that something was checked and found clean.
	var clean strings.Builder
	if err := Render(&clean, []model.Summary{{ID: "s2", Title: "clean",
		Tools: []model.ToolStat{{Tool: "Bash", Identity: "git", Count: 1}}}},
		Options{Width: 120, Color: false, Tools: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(clean.String(), "Denied") {
		t.Errorf("no denials should print no Denied line: %q", clean.String())
	}
}

// TestRenderIncludeFiles pins the files channel — the session-level record of
// what changed, which covers a file a shell command rewrote and the per-call
// Edits line does not.
func TestRenderIncludeFiles(t *testing.T) {
	sums := []model.Summary{{ID: "s1", Title: "do work",
		Files: []string{"/repo/internal/list/list.go", "/repo/PRODUCT.md"}}}

	var off strings.Builder
	if err := Render(&off, sums, Options{Width: 120, Color: false}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(off.String(), "PRODUCT.md") {
		t.Errorf("files should be hidden without the channel: %q", off.String())
	}

	var on strings.Builder
	if err := Render(&on, sums, Options{Width: 120, Color: false, Files: true}); err != nil {
		t.Fatal(err)
	}
	out := on.String()
	for _, want := range []string{"/repo/internal/list/list.go", "/repo/PRODUCT.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %q", want, out)
		}
	}
	// The channel opens a detail block like the others, closed by a rule.
	if !strings.Contains(out, "╰─") {
		t.Errorf("session block not closed by a rule: %q", out)
	}
}

func assertIDs(t *testing.T, got []model.Summary, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d sessions %v, want %d %v", len(got), ids(got), len(want), want)
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Errorf("position %d = %q, want %q (got %v)", i, got[i].ID, want[i], ids(got))
		}
	}
}

func ids(sums []model.Summary) []string {
	out := make([]string, len(sums))
	for i, s := range sums {
		out[i] = s.ID
	}
	return out
}

// TestFilterByRun pins the two selectors over what a session ran on. Before
// them, "which sessions ran at xhigh" meant rendering each session in turn.
func TestFilterByRun(t *testing.T) {
	sums := []model.Summary{
		{ID: "opus5", Model: "claude-opus-5", Effort: "high"},
		{ID: "opus48", Model: "claude-opus-4-8", Effort: "xhigh"},
		{ID: "sonnet", Model: "claude-sonnet-5"},
		{ID: "switched", Model: "claude-opus-5", Models: []string{"claude-sonnet-5", "claude-opus-5"}},
		{ID: "none"},
	}

	t.Run("model is a substring, so a family and a release both work", func(t *testing.T) {
		assertIDs(t, FilterByRun(sums, Run{Model: "opus"}), []string{"opus5", "opus48", "switched"})
		assertIDs(t, FilterByRun(sums, Run{Model: "opus-5"}), []string{"opus5", "switched"})
	})

	t.Run("model matching is case-insensitive", func(t *testing.T) {
		assertIDs(t, FilterByRun(sums, Run{Model: "OPUS-4-8"}), []string{"opus48"})
	})

	t.Run("a session that switched matches either model it ran", func(t *testing.T) {
		// It really did run both; a test seeing only the resolved value would deny
		// the session ever ran the first one.
		assertIDs(t, FilterByRun(sums, Run{Model: "sonnet"}), []string{"sonnet", "switched"})
	})

	t.Run("effort is exact, so high does not swallow xhigh", func(t *testing.T) {
		// The levels nest as substrings. A substring rule here would make
		// --effort high a silent superset rather than the answer asked for.
		assertIDs(t, FilterByRun(sums, Run{Effort: "high"}), []string{"opus5"})
		assertIDs(t, FilterByRun(sums, Run{Effort: "xhigh"}), []string{"opus48"})
	})

	t.Run("effort matching is case-insensitive", func(t *testing.T) {
		assertIDs(t, FilterByRun(sums, Run{Effort: "XHigh"}), []string{"opus48"})
	})

	t.Run("both fields AND", func(t *testing.T) {
		assertIDs(t, FilterByRun(sums, Run{Model: "opus", Effort: "xhigh"}), []string{"opus48"})
	})

	t.Run("an unknown value is an empty listing, not an error", func(t *testing.T) {
		// The level set grows without notice, so agentry does not validate against
		// it: a level it has not heard of must return nothing, not reject the call.
		if got := FilterByRun(sums, Run{Effort: "ultra"}); len(got) != 0 {
			t.Errorf("got %v, want no sessions", ids(got))
		}
	})

	t.Run("an empty selector is a no-op", func(t *testing.T) {
		assertIDs(t, FilterByRun(sums, Run{}), ids(sums))
	})
}

// TestRenderIncludeModel pins the channel that shows what a session ran on.
// Without it, --model and --effort filter on a fact the text output never
// displays, which leaves a reader guessing at the values to pass.
func TestRenderIncludeModel(t *testing.T) {
	sums := []model.Summary{{ID: "s1", Title: "do work", Model: "claude-opus-5", Effort: "high"}}

	var off strings.Builder
	if err := Render(&off, sums, Options{Width: 120, Color: false}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(off.String(), "claude-opus-5") {
		t.Errorf("the model should be hidden without the channel: %q", off.String())
	}

	var on strings.Builder
	if err := Render(&on, sums, Options{Width: 120, Color: false, Model: true}); err != nil {
		t.Fatal(err)
	}
	out := on.String()
	// The rendered header's phrasing, so one session reads the same in both paths.
	if !strings.Contains(out, "claude-opus-5 · high effort") {
		t.Errorf("output missing the model and effort phrase: %q", out)
	}
	if !strings.Contains(out, "╰─") {
		t.Errorf("session block not closed by a rule: %q", out)
	}

	t.Run("a mid-session change shows the transition", func(t *testing.T) {
		var b strings.Builder
		sums := []model.Summary{{ID: "s1", Title: "do work",
			Model:  "claude-opus-5",
			Models: []string{"claude-sonnet-5", "claude-opus-5"},
			Effort: "high", Efforts: []string{"xhigh", "high"}}}
		if err := Render(&b, sums, Options{Width: 120, Color: false, Model: true}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(b.String(), "claude-sonnet-5→claude-opus-5 · xhigh→high effort") {
			t.Errorf("want both sequences spelled out, got %q", b.String())
		}
	})

	t.Run("a session naming neither shows no line", func(t *testing.T) {
		// About half of sessions predate the effort field, and a few name no model
		// either. An empty rail line would read as data rather than as absence.
		var b strings.Builder
		if err := Render(&b, []model.Summary{{ID: "s1", Title: "do work"}},
			Options{Width: 120, Color: false, Model: true}); err != nil {
			t.Fatal(err)
		}
		// The block is the row, then the closing rule — no line between them.
		if lines := strings.Count(strings.TrimSpace(b.String()), "\n"); lines != 1 {
			t.Errorf("want just the row and its closing rule, got %q", b.String())
		}
	})
}

// TestFilterByOutputs pins the "what came out of it" axis. It is the one filter
// family that reads no tool call, so a session whose pull request was opened by a
// subagent is findable here and by no --used* flag.
func TestFilterByOutputs(t *testing.T) {
	sums := []model.Summary{
		{ID: "central", PRs: []model.PR{
			{Repository: "eitanpo/central", Number: 14, URL: "https://github.com/eitanpo/central/pull/14"},
		}},
		{ID: "devex", PRs: []model.PR{
			{Repository: "wix-private/devex-costs", Number: 187, URL: "https://github.com/wix-private/devex-costs/pull/187"},
		}, Artifacts: []model.Artifact{
			{Title: "DevEx cost", URL: "https://claude.ai/code/artifact/aaa", Path: "/repo/reports/cost.html"},
		}},
		{ID: "untitled", Artifacts: []model.Artifact{
			{URL: "https://claude.ai/code/artifact/bbb", Path: "/tmp/scratch/notes.html"},
		}},
		{ID: "quiet"},
	}
	matching := func(t *testing.T, c Criteria) []model.Summary {
		t.Helper()
		return FilterByTools(sums, Filters{Used: c})
	}

	t.Run("a pull request matches by repository, by number, and by url", func(t *testing.T) {
		// All three because the question arrives in all three forms, and which field
		// a given phrasing lands in is not something a caller should have to know.
		assertIDs(t, matching(t, Criteria{PR: "devex-costs"}), []string{"devex"})
		assertIDs(t, matching(t, Criteria{PR: "187"}), []string{"devex"})
		assertIDs(t, matching(t, Criteria{PR: "eitanpo/central/pull/14"}), []string{"central"})
	})

	t.Run("pull-request matching is case-insensitive substring", func(t *testing.T) {
		assertIDs(t, matching(t, Criteria{PR: "CENTRAL"}), []string{"central"})
	})

	t.Run("an artifact matches by title, by url, and by local path", func(t *testing.T) {
		// The path is in because an artifact is often remembered by the file that
		// produced it rather than by a title it may not even carry.
		assertIDs(t, matching(t, Criteria{Artifact: "devex cost"}), []string{"devex"})
		assertIDs(t, matching(t, Criteria{Artifact: "artifact/bbb"}), []string{"untitled"})
		assertIDs(t, matching(t, Criteria{Artifact: "notes.html"}), []string{"untitled"})
	})

	t.Run("the two axes do not bleed into each other", func(t *testing.T) {
		// A pull-request filter must not match an artifact's URL, or a listing
		// answers a question nobody asked.
		if got := matching(t, Criteria{PR: "claude.ai"}); len(got) != 0 {
			t.Errorf("--opened-pr matched artifacts: %v", ids(got))
		}
		if got := matching(t, Criteria{Artifact: "github.com"}); len(got) != 0 {
			t.Errorf("--published-artifact matched pull requests: %v", ids(got))
		}
	})

	t.Run("the negated form keeps exactly what the positive drops", func(t *testing.T) {
		kept := FilterByTools(sums, Filters{NotUsed: Criteria{PR: "central"}})
		assertIDs(t, kept, []string{"devex", "untitled", "quiet"})
	})

	t.Run("a session that produced nothing matches no output filter", func(t *testing.T) {
		if got := matching(t, Criteria{PR: "quiet"}); len(got) != 0 {
			t.Errorf("got %v, want no sessions", ids(got))
		}
	})
}

// TestRenderIncludeOutputs pins the channel that shows what a session produced.
// Without it, --opened-pr and --published-artifact filter on facts the text
// output never displays, which leaves a reader guessing at the values to pass.
func TestRenderIncludeOutputs(t *testing.T) {
	sums := []model.Summary{{ID: "s1", Title: "ship it",
		PRs: []model.PR{
			{Repository: "eitanpo/central", Number: 14, URL: "https://github.com/eitanpo/central/pull/14"},
		},
		Artifacts: []model.Artifact{
			{Title: "Cost report", URL: "https://claude.ai/code/artifact/aaa"},
			{URL: "https://claude.ai/code/artifact/bbb"},
		}}}

	var off strings.Builder
	if err := Render(&off, sums, Options{Width: 200, Color: false}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(off.String(), "pull/14") {
		t.Errorf("outputs should be hidden without the channel: %q", off.String())
	}

	var on strings.Builder
	if err := Render(&on, sums, Options{Width: 200, Color: false, Outputs: true}); err != nil {
		t.Fatal(err)
	}
	out := on.String()
	// A pull request prints as the URL that identifies it — repository and number
	// are already in it, so a label beside it would only repeat itself.
	if !strings.Contains(out, "https://github.com/eitanpo/central/pull/14") {
		t.Errorf("output missing the pull request URL: %q", out)
	}
	// An artifact leads with its title, since a claude.ai artifact id names
	// nothing a person recognizes.
	if !strings.Contains(out, "Cost report  https://claude.ai/code/artifact/aaa") {
		t.Errorf("output missing the titled artifact line: %q", out)
	}
	// And an artifact whose record carried no title is its URL alone, not a line
	// that opens with a blank column.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "artifact/bbb") && !strings.HasSuffix(line, "https://claude.ai/code/artifact/bbb") {
			t.Errorf("untitled artifact should print as its URL alone, got %q", line)
		}
	}
	if !strings.Contains(out, "╰─") {
		t.Errorf("session block not closed by a rule: %q", out)
	}

	t.Run("a narrow terminal keeps the half that identifies each thing", func(t *testing.T) {
		// The two kinds truncate in opposite directions on purpose: a pull request is
		// named by its URL's tail, an artifact by its title at the head. Cutting
		// either from the wrong end leaves a line naming nothing a reader can act on.
		var b strings.Builder
		if err := Render(&b, sums, Options{Width: 60, Color: false, Outputs: true}); err != nil {
			t.Fatal(err)
		}
		out := b.String()
		if !strings.Contains(out, "central/pull/14") {
			t.Errorf("the pull request lost its number to truncation: %q", out)
		}
		if !strings.Contains(out, "Cost report") {
			t.Errorf("the artifact lost its title to truncation: %q", out)
		}
	})

	t.Run("a session that produced nothing shows no line", func(t *testing.T) {
		var b strings.Builder
		if err := Render(&b, []model.Summary{{ID: "s1", Title: "quiet"}},
			Options{Width: 120, Color: false, Outputs: true}); err != nil {
			t.Fatal(err)
		}
		if lines := strings.Count(strings.TrimSpace(b.String()), "\n"); lines != 1 {
			t.Errorf("want just the row and its closing rule, got %q", b.String())
		}
	})
}

// TestFilterByReply pins the "what the reply said" axis — the only filter that
// reads the model's own prose, and the only one whose corpus --format json does
// not carry. Without it, no listing can count whether a rule about how a reply
// is written ever fired.
func TestFilterByReply(t *testing.T) {
	sums := []model.Summary{
		{ID: "block", Replies: []string{
			"Here is the change.",
			"Done.\n\n**Learnings:** NONE",
		}},
		{ID: "mention", Replies: []string{
			"I rewrote the Learnings rule so /capture-learnings can sweep it.",
		}},
		{ID: "praise", Replies: []string{
			"Great question! Here is why that works.",
		}},
		{ID: "silent"},
	}
	matching := func(t *testing.T, pattern string) []model.Summary {
		t.Helper()
		re, err := regexp.Compile("(?i)" + pattern)
		if err != nil {
			t.Fatalf("compile %q: %v", pattern, err)
		}
		return FilterByTools(sums, Filters{Used: Criteria{Reply: re}})
	}

	t.Run("a line-anchored pattern separates the block from a mention of it", func(t *testing.T) {
		// This is the whole reason the filter takes a regexp: the substring
		// "learnings" cannot tell a session that emitted the block from one that
		// merely discussed the rule, and an audit counting compliance needs to.
		assertIDs(t, matching(t, `(?m)^\*{0,2}Learnings\b`), []string{"block"})
		assertIDs(t, matching(t, `learnings`), []string{"block", "mention"})
	})

	t.Run("^ anchors to one reply, not to the session's joined text", func(t *testing.T) {
		// "Did a reply open with praise" is a question about a reply. Joining the
		// blocks would answer it only about the first one, so a session whose
		// second reply opened with praise would read as compliant.
		assertIDs(t, matching(t, `^great\b`), []string{"praise"})
		assertIDs(t, matching(t, `^done\.`), []string{"block"})
	})

	t.Run("matching is case-insensitive across every branch of an alternation", func(t *testing.T) {
		// The (?i) prefix has to cover the whole pattern; if it stopped at the
		// first branch, "PRAISE|excellent" would silently match case-sensitively
		// on the right-hand side and under-count.
		assertIDs(t, matching(t, `GREAT QUESTION`), []string{"praise"})
		assertIDs(t, matching(t, `nomatch|GREAT`), []string{"praise"})
	})

	t.Run("the negated form keeps exactly what the positive drops", func(t *testing.T) {
		re := regexp.MustCompile(`(?i)(?m)^\*{0,2}Learnings\b`)
		kept := FilterByTools(sums, Filters{NotUsed: Criteria{Reply: re}})
		assertIDs(t, kept, []string{"mention", "praise", "silent"})
	})

	t.Run("a session with no replies matches no pattern", func(t *testing.T) {
		// A session carrying no reply text must not fall through as a match; an
		// empty corpus is an absence, and the negated form is where it belongs.
		if got := matching(t, `.`); slices.Contains(ids(got), "silent") {
			t.Errorf("a session with no replies matched: %v", ids(got))
		}
	})

	t.Run("an unset Reply imposes no constraint", func(t *testing.T) {
		// nil is the unset value, so Criteria{} must stay the identity filter.
		if !(Criteria{}).empty() {
			t.Error("Criteria{} with a nil Reply is not empty")
		}
		assertIDs(t, FilterByTools(sums, Filters{}), ids(sums))
	})
}

// TestReplyTextIsNeverSerialized pins the size decision behind --reply-matches:
// reply text is matched in memory and never shipped. It is the largest field a
// summary could carry (2.8 MB across one local project's 59 sessions, against
// 776 KB for that project's whole 50-session listing), and --include gates no
// JSON key, so serializing it would enlarge every listing for every caller.
func TestReplyTextIsNeverSerialized(t *testing.T) {
	var b strings.Builder
	if err := RenderJSON(&b, []model.Summary{
		{ID: "s1", Title: "ship it", Replies: []string{"a distinctive reply body"}},
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "distinctive reply body") {
		t.Errorf("reply text leaked into --format json: %s", b.String())
	}
	if strings.Contains(strings.ToLower(b.String()), "replies") {
		t.Errorf("a replies key appeared in --format json: %s", b.String())
	}
}

// TestWorktreeTitleFallThrough pins the rule that a title repeating the row's
// worktree falls to the first prompt: the worktree column already carries that
// string, so the title would spend the row's one scannable field on a duplicate.
func TestWorktreeTitleFallThrough(t *testing.T) {
	const repo = "/p/wix/artifactory-migration"
	wt := func(name string) string { return repo + "/.claude/worktrees/" + name }
	mk := func(id, cwd, title string, prompts ...string) model.Summary {
		return model.Summary{
			ID: id, Cwd: cwd, Title: title, Prompts: prompts,
			Start: time.Date(2026, 6, 3, 14, 0, 0, 0, time.UTC),
			End:   time.Date(2026, 6, 3, 14, 5, 0, 0, time.UTC),
		}
	}
	titleOf := func(sums []model.Summary, id string) string {
		for _, r := range arrange(sums) {
			if r.s.ID == id {
				return r.s.Title
			}
		}
		return ""
	}

	t.Run("a title equal to the worktree falls to the first prompt", func(t *testing.T) {
		sums := []model.Summary{mk("a", wt("plan"), "plan", "draft the migration gantt")}
		if got := titleOf(sums, "a"); got != "draft the migration gantt" {
			t.Errorf("title = %q, want the first prompt", got)
		}
	})

	t.Run("a title a person wrote stands", func(t *testing.T) {
		// The trigger is duplication, not similarity: this names the same work and
		// is not the worktree's name, so nothing replaces it.
		sums := []model.Summary{mk("a", wt("plan"), "Plan the migration", "draft the gantt")}
		if got := titleOf(sums, "a"); got != "Plan the migration" {
			t.Errorf("title = %q, want it left alone", got)
		}
	})

	t.Run("a session with no prompt keeps its title", func(t *testing.T) {
		sums := []model.Summary{mk("a", wt("plan"), "plan")}
		if got := titleOf(sums, "a"); got != "plan" {
			t.Errorf("title = %q, want the title kept", got)
		}
	})

	t.Run("the repo's own checkout is untouched", func(t *testing.T) {
		// worktreeName is empty there, so a session titled after the repo directory
		// is not a duplicate of anything the row shows.
		sums := []model.Summary{mk("a", repo, "artifactory-migration", "some prompt")}
		if got := titleOf(sums, "a"); got != "artifactory-migration" {
			t.Errorf("title = %q, want it left alone", got)
		}
	})
}

// TestIDWidth pins the abbreviation rule: the shortest prefix that tells the
// rows apart, floored so a short listing does not print an id too small to
// resolve later, and grown when the rows demand it.
func TestIDWidth(t *testing.T) {
	mk := func(ids ...string) []model.Summary {
		var out []model.Summary
		for _, id := range ids {
			out = append(out, model.Summary{ID: id})
		}
		return out
	}
	tests := []struct {
		name string
		sums []model.Summary
		want int
	}{
		{
			// Distinct at character 1, so uniqueness asks for 1 — the floor is what
			// decides, because the id is copied out and passed back later.
			name: "distinct ids take the floor",
			sums: mk("aaaaaaaaaaaa", "bbbbbbbbbbbb"),
			want: idFloor,
		},
		{
			// The case the floor cannot answer: equal through character 8.
			name: "a collision at the floor grows the prefix",
			sums: mk("abcdef12-0000", "abcdef12-1111"),
			want: 10,
		},
		{
			name: "ids shorter than the floor are printed whole",
			sums: mk("abc123", "def456"),
			want: 6,
		},
		{
			name: "one row still takes the floor",
			sums: mk("aaaaaaaaaaaa"),
			want: idFloor,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := idWidth(tt.sums); got != tt.want {
				t.Errorf("idWidth = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestRenderAbbreviatedID pins what the row shows and what it buys: the id is
// cut to the computed width, and the columns it frees go to the title.
func TestRenderAbbreviatedID(t *testing.T) {
	const id = "ba6b3ded-475b-4c3a-96fe-99698a557d14"
	sums := []model.Summary{{
		ID:    id,
		Title: "a title long enough to need the columns the full id was taking",
		Start: time.Date(2026, 6, 3, 14, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 6, 3, 14, 5, 0, 0, time.UTC),
	}}
	var b strings.Builder
	if err := Render(&b, sums, Options{Width: 100, Color: false}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.Contains(out, id) {
		t.Errorf("the full id must not be printed: %q", out)
	}
	if !strings.Contains(out, id[:idFloor]) {
		t.Errorf("output missing the id prefix %q: %q", id[:idFloor], out)
	}
	// 28 columns come back from the id; the title is what they are for. This
	// substring starts past character 29, where the title used to be cut.
	if !strings.Contains(out, "the columns the full id was") {
		t.Errorf("title should have grown into the freed columns: %q", out)
	}
}

// TestFitLabels pins the collision rule for both path columns: truncation first,
// then the colliders keep the end that separates them. The failure it prevents
// is silent — two places drawn as one label that claims to be either.
func TestFitLabels(t *testing.T) {
	tests := []struct {
		name     string
		labels   map[string]string
		width    int
		keepTail bool
		want     map[string]string
	}{
		{
			// The case from a real listing: three worktrees whose names differ in
			// their last character all rendered "update-cloudsmi…" at 16 columns.
			name:  "worktrees differing at the tail keep it",
			width: 16,
			labels: map[string]string{
				"/r/.claude/worktrees/update-cloudsmith":  "update-cloudsmith",
				"/r/.claude/worktrees/update-cloudsmith2": "update-cloudsmith2",
				"/r/.claude/worktrees/update-cloudsmith3": "update-cloudsmith3",
			},
			want: map[string]string{
				"/r/.claude/worktrees/update-cloudsmith":  "update-cloudsm…h",
				"/r/.claude/worktrees/update-cloudsmith2": "update-cloudsm…2",
				"/r/.claude/worktrees/update-cloudsmith3": "update-cloudsm…3",
			},
		},
		{
			// Many rows sharing one label is not a collision. The repo-root "—" and
			// several sessions in one worktree both take this path.
			name:   "one label on many rows is left alone",
			width:  8,
			labels: map[string]string{"/r/a": "—", "/r/b": "—", "/r/c": "ci"},
			want:   map[string]string{"/r/a": "—", "/r/b": "—", "/r/c": "ci"},
		},
		{
			// Mirrored for the project column, where the tail is what is kept: two
			// repos sharing a basename left-truncate to the same "…/agentry".
			name:     "projects differing at the head keep it",
			width:    9,
			keepTail: true,
			labels:   map[string]string{"/p/me/agentry": "me/agentry", "/p/wix/agentry": "wix-private/agentry"},
			want:     map[string]string{"/p/me/agentry": "m…agentry", "/p/wix/agentry": "w…agentry"},
		},
		{
			// Designed failure: the colliders differ 8 characters from the kept end,
			// so no head-plus-tail split fits 8 columns. The duplicate stands rather
			// than a meaningless marker being invented.
			name:   "no split that fits leaves the duplicate",
			width:  8,
			labels: map[string]string{"/r/x": "aaaaaaaaXbbbbbbbb", "/r/y": "aaaaaaaaYbbbbbbbb"},
			want:   map[string]string{"/r/x": "aaaaaaa…", "/r/y": "aaaaaaa…"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fitLabels(tt.labels, tt.width, tt.keepTail)
			for k, want := range tt.want {
				if got[k] != want {
					t.Errorf("label for %q = %q, want %q", k, got[k], want)
				}
			}
		})
	}
}

// TestRenderWorktreeCollision pins the rule end to end: three worktrees of one
// repo must be three distinguishable cells in the rendered row, not one string
// repeated.
func TestRenderWorktreeCollision(t *testing.T) {
	const repo = "/p/wix/artifactory-migration"
	mk := func(id, wt, title string) model.Summary {
		return model.Summary{
			ID: id, Cwd: repo + "/.claude/worktrees/" + wt, Title: title,
			Prompts: []string{title},
			Start:   time.Date(2026, 6, 3, 14, 0, 0, 0, time.UTC),
			End:     time.Date(2026, 6, 3, 14, 5, 0, 0, time.UTC),
		}
	}
	var b strings.Builder
	sums := []model.Summary{
		mk("aaaaaaaa-1111-4c3a-96fe-99698a557d14", "update-cloudsmith", "alpha"),
		mk("bbbbbbbb-2222-4c3a-96fe-99698a557d14", "update-cloudsmith2", "beta"),
		mk("cccccccc-3333-4c3a-96fe-99698a557d14", "update-cloudsmith3", "gamma"),
	}
	if err := Render(&b, sums, Options{Width: 90, Color: false}); err != nil {
		t.Fatal(err)
	}
	cells := map[string]bool{}
	for _, line := range strings.Split(strings.TrimRight(b.String(), "\n"), "\n") {
		// The worktree column sits after when, duration and turns; take the field
		// before the title rather than a fixed offset, so a width change here does
		// not silently make the assertion vacuous.
		fields := strings.Split(strings.TrimSpace(line[33:]), "  ")
		cells[fields[0]] = true
	}
	if len(cells) != 3 {
		t.Errorf("three worktrees must render three distinct cells, got %v\n%s", cells, b.String())
	}
}
