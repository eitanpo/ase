package locate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProjectDirName(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/Users/me/Projects/dotfiles", "-Users-me-Projects-dotfiles"},
		{"/a", "-a"},
		{"/", "-"},
		{"/x/y/z", "-x-y-z"},
		// Every non-alphanumeric character is replaced, not only "/". A dot
		// component doubles the separator, which is what a slash-only encoder got
		// wrong — and it got it wrong for every worktree Claude Code creates,
		// since those live under <repo>/.claude/worktrees/.
		{"/Users/me/.central/worktrees/pr-1", "-Users-me--central-worktrees-pr-1"},
		{"/repo/.claude/worktrees/w", "-repo--claude-worktrees-w"},
		{"/tmp/a_b/c", "-tmp-a-b-c"},
		{"/x/a.b_c-d/y", "-x-a-b-c-d-y"},
	}
	for _, tt := range tests {
		if got := ProjectDirName(tt.path); got != tt.want {
			t.Errorf("ProjectDirName(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

// TestDotComponentDirResolves pins the bug the encoding fix closed: a working
// directory with a dot component has a project folder like any other, and
// agentry used to report "no Claude project for this directory" for all of them.
func TestDotComponentDirResolves(t *testing.T) {
	root := t.TempDir()
	old := ProjectsRoot
	ProjectsRoot = root
	t.Cleanup(func() { ProjectsRoot = old })

	const cwd = "/repo/.claude/worktrees/feature"
	if err := os.MkdirAll(filepath.Join(root, ProjectDirName(cwd)), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ProjectDir(cwd); err != nil {
		t.Fatalf("ProjectDir(%q) = %v, want the folder to resolve", cwd, err)
	}
}

// writeSession creates a project folder for cwd holding one session whose
// entries carry that cwd — the shape ProjectCwd and SessionsUnder read.
func writeSession(t *testing.T, root, cwd, id string) string {
	t.Helper()
	dir := filepath.Join(root, ProjectDirName(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	body := `{"type":"ai-title","aiTitle":"meta line with no cwd"}` + "\n" +
		`{"type":"user","cwd":"` + cwd + `"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProjectCwd(t *testing.T) {
	root := t.TempDir()
	old := ProjectsRoot
	ProjectsRoot = root
	t.Cleanup(func() { ProjectsRoot = old })

	t.Run("reads the path the log recorded", func(t *testing.T) {
		// The folder name alone cannot answer this: "-a-b-c" could encode /a/b/c
		// or /a/b-c. The recorded cwd is the only non-ambiguous source.
		const cwd = "/x/a-b/c"
		writeSession(t, root, cwd, "s1")
		got, err := ProjectCwd(filepath.Join(root, ProjectDirName(cwd)))
		if err != nil {
			t.Fatal(err)
		}
		if got != cwd {
			t.Errorf("ProjectCwd = %q, want %q", got, cwd)
		}
	})

	t.Run("skips leading entries that carry no cwd", func(t *testing.T) {
		// Meta entries (ai-title, agent-name, …) omit the field, so reading only
		// the first line would report no path for a session that has one.
		const cwd = "/x/meta-first"
		writeSession(t, root, cwd, "s2")
		got, _ := ProjectCwd(filepath.Join(root, ProjectDirName(cwd)))
		if got != cwd {
			t.Errorf("ProjectCwd = %q, want %q", got, cwd)
		}
	})

	t.Run("no cwd anywhere yields empty, not an error", func(t *testing.T) {
		dir := filepath.Join(root, "-no-cwd")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := ProjectCwd(dir)
		if err != nil || got != "" {
			t.Errorf("ProjectCwd = %q, %v; want \"\", nil", got, err)
		}
	})

	t.Run("a malformed line does not stop the scan", func(t *testing.T) {
		dir := filepath.Join(root, "-malformed")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "{not json\n" + `{"type":"user","cwd":"/x/after-bad-line"}` + "\n"
		if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		got, _ := ProjectCwd(dir)
		if got != "/x/after-bad-line" {
			t.Errorf("ProjectCwd = %q, want the cwd after the malformed line", got)
		}
	})
}

func TestSessionsUnderAndAll(t *testing.T) {
	root := t.TempDir()
	old := ProjectsRoot
	ProjectsRoot = root
	t.Cleanup(func() { ProjectsRoot = old })

	writeSession(t, root, "/w/repo", "main")
	writeSession(t, root, "/w/repo/.claude/worktrees/feature", "wt")
	// /w/repo-tools is a sibling whose path begins with the same characters as
	// /w/repo. A string-prefix test would sweep it in; a component test must not.
	writeSession(t, root, "/w/repo-tools", "sibling")
	writeSession(t, root, "/elsewhere/other", "other")

	t.Run("a repo sweeps its nested worktrees", func(t *testing.T) {
		got, err := SessionsUnder("/w/repo")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d sessions, want 2 (the repo and its worktree): %v", len(got), got)
		}
	})

	t.Run("a same-prefix sibling is not under the repo", func(t *testing.T) {
		got, _ := SessionsUnder("/w/repo")
		for _, p := range got {
			if filepath.Base(p) == "sibling.jsonl" {
				t.Errorf("/w/repo-tools swept into /w/repo: %v", got)
			}
		}
	})

	t.Run("a parent directory sweeps every repo beneath it", func(t *testing.T) {
		got, err := SessionsUnder("/w")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 {
			t.Errorf("got %d sessions, want 3 under /w: %v", len(got), got)
		}
	})

	t.Run("no project under the path is ErrNoProject", func(t *testing.T) {
		if _, err := SessionsUnder("/nothing/here"); !errors.Is(err, ErrNoProject) {
			t.Errorf("got %v, want ErrNoProject", err)
		}
	})

	t.Run("a folder matching both ways is listed once", func(t *testing.T) {
		got, err := SessionsUnder("/w/repo")
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, p := range got {
			if seen[p] {
				t.Errorf("%s listed twice: %v", p, got)
			}
			seen[p] = true
		}
	})

	t.Run("SessionsAll spans every project", func(t *testing.T) {
		got, err := SessionsAll()
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 4 {
			t.Errorf("got %d sessions, want 4: %v", len(got), got)
		}
	})

	t.Run("SessionsAll on an empty root is ErrNoSession", func(t *testing.T) {
		empty := t.TempDir()
		ProjectsRoot = empty
		t.Cleanup(func() { ProjectsRoot = root })
		if _, err := SessionsAll(); !errors.Is(err, ErrNoSession) {
			t.Errorf("got %v, want ErrNoSession", err)
		}
	})
}

// TestSessionsUnderTakesRootByName pins the half of SessionsUnder that does not
// read recorded cwds: a folder whose sessions record some other path — one
// relocated into it, or one written before the cwd field existed — is
// unreachable by the subtree rule, yet it is the folder the root path resolves
// to by name. The listing's default scope runs through here, so dropping it
// would make a project invisible from inside its own directory.
func TestSessionsUnderTakesRootByName(t *testing.T) {
	root := t.TempDir()
	old := ProjectsRoot
	ProjectsRoot = root
	t.Cleanup(func() { ProjectsRoot = old })

	dir := filepath.Join(root, ProjectDirName("/w/stale"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"user","cwd":"/somewhere/else"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "stale.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := SessionsUnder("/w/stale")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || filepath.Base(got[0]) != "stale.jsonl" {
		t.Errorf("got %v, want the folder's own session", got)
	}
}

// TestSessionResolvesNestedID pins the render path's scope: an id belonging to a
// project nested under the working directory resolves from the working
// directory, so an id read off a listing opens where it was read. The
// directory's own project still wins the lookup.
func TestSessionResolvesNestedID(t *testing.T) {
	root := t.TempDir()
	old := ProjectsRoot
	ProjectsRoot = root
	t.Cleanup(func() { ProjectsRoot = old })

	own := writeSession(t, root, "/w/repo", "mine")
	nested := writeSession(t, root, "/w/repo/.claude/worktrees/feature", "theirs")
	writeSession(t, root, "/elsewhere/other", "outside")

	t.Run("an id in a nested project resolves", func(t *testing.T) {
		got, err := Session("/w/repo", "theirs")
		if err != nil {
			t.Fatal(err)
		}
		if got != nested {
			t.Errorf("got %q, want %q", got, nested)
		}
	})

	t.Run("the directory's own project still resolves", func(t *testing.T) {
		got, err := Session("/w/repo", "mine")
		if err != nil {
			t.Fatal(err)
		}
		if got != own {
			t.Errorf("got %q, want %q", got, own)
		}
	})

	t.Run("an id outside the subtree is ErrNoSession", func(t *testing.T) {
		// The scope has to stop somewhere, and it stops where the listing's does:
		// a session from an unrelated project is not reachable by id from here.
		if _, err := Session("/w/repo", "outside"); !errors.Is(err, ErrNoSession) {
			t.Errorf("got %v, want ErrNoSession", err)
		}
	})

	t.Run("no id picks the newest in the whole subtree", func(t *testing.T) {
		// `view` reaches every project a listing covers, so standing in a repo
		// whose latest work happened in a worktree renders that work.
		base := time.Now().Add(-time.Hour)
		mustChtime(t, own, base)
		mustChtime(t, nested, base.Add(time.Minute))
		got, err := Session("/w/repo", "")
		if err != nil {
			t.Fatal(err)
		}
		if got != nested {
			t.Errorf("got %q, want the nested project's newer session %q", got, nested)
		}
	})
}

func TestSession(t *testing.T) {
	root := t.TempDir()
	old := ProjectsRoot
	ProjectsRoot = root
	t.Cleanup(func() { ProjectsRoot = old })

	const cwd = "/fake/proj"
	projDir := filepath.Join(root, "-fake-proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(projDir, "older.jsonl")
	newer := filepath.Join(projDir, "newer.jsonl")
	for _, p := range []string{older, newer} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().Add(-time.Hour)
	mustChtime(t, older, base)
	mustChtime(t, newer, base.Add(time.Minute))

	t.Run("no arg picks newest by mtime", func(t *testing.T) {
		got, err := Session(cwd, "")
		if err != nil {
			t.Fatal(err)
		}
		if got != newer {
			t.Errorf("got %q, want %q", got, newer)
		}
	})

	t.Run("id resolves that session", func(t *testing.T) {
		got, err := Session(cwd, "older")
		if err != nil {
			t.Fatal(err)
		}
		if got != older {
			t.Errorf("got %q, want %q", got, older)
		}
	})

	t.Run("missing id is ErrNoSession", func(t *testing.T) {
		if _, err := Session(cwd, "nope"); !errors.Is(err, ErrNoSession) {
			t.Errorf("got %v, want ErrNoSession", err)
		}
	})

	t.Run("unknown project is ErrNoProject", func(t *testing.T) {
		if _, err := Session("/no/such/project", ""); !errors.Is(err, ErrNoProject) {
			t.Errorf("got %v, want ErrNoProject", err)
		}
	})
}

// TestSessionsUnderRootOnly pins the degenerate subtree — a project with nothing
// nested under it — where the scope is just the one folder the path encodes to.
func TestSessionsUnderRootOnly(t *testing.T) {
	root := t.TempDir()
	old := ProjectsRoot
	ProjectsRoot = root
	t.Cleanup(func() { ProjectsRoot = old })

	const cwd = "/fake/proj"
	projDir := filepath.Join(root, "-fake-proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("empty project is ErrNoSession", func(t *testing.T) {
		if _, err := SessionsUnder(cwd); !errors.Is(err, ErrNoSession) {
			t.Errorf("got %v, want ErrNoSession", err)
		}
	})

	for _, name := range []string{"a.jsonl", "b.jsonl"} {
		if err := os.WriteFile(filepath.Join(projDir, name), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("lists every session jsonl", func(t *testing.T) {
		got, err := SessionsUnder(cwd)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Errorf("got %d sessions, want 2: %v", len(got), got)
		}
	})

	t.Run("unknown project is ErrNoProject", func(t *testing.T) {
		if _, err := SessionsUnder("/no/such/project"); !errors.Is(err, ErrNoProject) {
			t.Errorf("got %v, want ErrNoProject", err)
		}
	})
}

func mustChtime(t *testing.T, path string, mod time.Time) {
	t.Helper()
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

// TestSessionByPrefix pins prefix resolution: the rule that lets a caller pass
// back the abbreviated id a listing printed. An exact match is checked first, so
// a full id can never be reported ambiguous, and a prefix naming several
// sessions is an error rather than a silent pick of one of them.
func TestSessionByPrefix(t *testing.T) {
	root := t.TempDir()
	old := ProjectsRoot
	ProjectsRoot = root
	t.Cleanup(func() { ProjectsRoot = old })

	const cwd = "/fake/proj"
	projDir := filepath.Join(root, "-fake-proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ids := []string{
		"abcdef12-1111-4c3a-96fe-99698a557d14",
		"abcdef12-2222-4c3a-96fe-99698a557d14",
		"ffffffff-3333-4c3a-96fe-99698a557d14",
	}
	for _, id := range ids {
		if err := os.WriteFile(filepath.Join(projDir, id+".jsonl"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("a unique prefix resolves", func(t *testing.T) {
		got, err := Session(cwd, "ffffffff")
		if err != nil {
			t.Fatalf("Session by unique prefix: %v", err)
		}
		if filepath.Base(got) != ids[2]+".jsonl" {
			t.Errorf("resolved %q, want %q", filepath.Base(got), ids[2]+".jsonl")
		}
	})

	t.Run("a full id resolves", func(t *testing.T) {
		got, err := Session(cwd, ids[0])
		if err != nil {
			t.Fatalf("Session by full id: %v", err)
		}
		if filepath.Base(got) != ids[0]+".jsonl" {
			t.Errorf("resolved %q, want %q", filepath.Base(got), ids[0]+".jsonl")
		}
	})

	t.Run("an ambiguous prefix errors and names the matches", func(t *testing.T) {
		_, err := Session(cwd, "abcdef12")
		var amb *AmbiguousIDError
		if !errors.As(err, &amb) {
			t.Fatalf("got %v, want an AmbiguousIDError", err)
		}
		if len(amb.IDs) != 2 {
			t.Errorf("matched %v, want the two abcdef12 sessions", amb.IDs)
		}
		// The message has to say how many, or the caller cannot tell an ambiguous
		// prefix from a missing session.
		if !strings.Contains(amb.Error(), "2 sessions") {
			t.Errorf("message %q should say how many matched", amb.Error())
		}
	})

	t.Run("a prefix matching nothing is not found", func(t *testing.T) {
		if _, err := Session(cwd, "0123abcd"); !errors.Is(err, ErrNoSession) {
			t.Errorf("got %v, want ErrNoSession", err)
		}
	})
}
