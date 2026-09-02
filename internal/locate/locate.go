// Package locate maps the current directory to its Claude project folder and
// selects which session JSONL to render.
package locate

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrNoProject means $PWD has no matching folder under ~/.claude/projects.
var ErrNoProject = errors.New("no Claude project for this directory")

// ErrNoSession means the project folder holds no selectable session.
var ErrNoSession = errors.New("session not found")

// AmbiguousIDError means a prefix named more than one session. It carries the
// ids it matched so the caller can say how many and show them: a prefix that
// resolves to several sessions must never be resolved to one of them silently.
type AmbiguousIDError struct {
	Prefix string
	IDs    []string
}

func (e *AmbiguousIDError) Error() string {
	return fmt.Sprintf("session id %q is ambiguous: it matches %d sessions", e.Prefix, len(e.IDs))
}

// ProjectsRoot is the directory Claude Code stores logs under. Overridable
// for tests.
var ProjectsRoot = defaultProjectsRoot()

func defaultProjectsRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude/projects"
	}
	return filepath.Join(home, ".claude", "projects")
}

// ProjectDirName encodes an absolute path the way Claude Code names its project
// folders: every character outside [A-Za-z0-9] becomes "-", the leading "/"
// included — which is why the name starts with one.
// e.g. /Users/x/Projects/dotfiles -> -Users-x-Projects-dotfiles.
//
// It is not only "/" that is replaced. "." and "_" go too, so
// /Users/x/.claude/worktrees/w encodes as -Users-x--claude-worktrees-w with a
// doubled "-". Replacing only "/" reproduced 32 of the 63 project folders on
// the development machine; this rule reproduces all 63. Getting it wrong is not
// a near miss — the wrong name simply does not exist, so agentry reported "no
// Claude project for this directory" for every dot-component path, which is
// every worktree Claude Code creates under <repo>/.claude/worktrees/.
//
// Exported so tests build a fixture project folder through the same encoder the
// lookup uses, rather than open-coding the rule a second time and drifting.
func ProjectDirName(absPath string) string {
	var b strings.Builder
	b.Grow(len(absPath))
	for _, r := range absPath {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// ProjectDir returns the project folder for the given working directory, or
// ErrNoProject if it does not exist.
func ProjectDir(cwd string) (string, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(ProjectsRoot, ProjectDirName(abs))
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", ErrNoProject
	}
	return dir, nil
}

// Session returns the JSONL path to render, resolved in the scope a listing
// covers: cwd's own project and every project nested under it (SessionsUnder).
// With a non-empty id it resolves <id>.jsonl there, so an id read off a listing
// opens from where it was read rather than only from the directory the session
// ran in. With an empty id it picks the most recent session in that scope by
// modification time (which may be one still in progress).
//
// cwd's own project is tried first and alone, because it answers nearly every
// call and the subtree scan opens a file per project folder to read its recorded
// cwd. It also settles a duplicate id in favor of the directory the caller is
// standing in; ids are UUIDs, so that is a tie that does not arise in practice.
func Session(cwd, id string) (string, error) {
	if id == "" {
		paths, err := SessionsByRecency(cwd)
		if err != nil {
			return "", err
		}
		return paths[0], nil // non-empty: SessionsByRecency errors instead
	}
	if dir, err := ProjectDir(cwd); err == nil {
		path := filepath.Join(dir, id+".jsonl")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	paths, err := SessionsUnder(cwd)
	if err != nil {
		return "", err // ErrNoProject when nothing sits at or under cwd
	}
	want := id + ".jsonl"
	for _, p := range paths {
		if filepath.Base(p) == want {
			return p, nil
		}
	}
	// No exact match: treat the id as a prefix, the rule git applies to object
	// names. An exact match is checked first and wins, so a full id can never be
	// reported ambiguous. Several matches are an error rather than a pick —
	// resolving one of them silently would render a session the caller did not
	// name and give no sign of it.
	var hits []string
	for _, p := range paths {
		base := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		if strings.HasPrefix(base, id) {
			hits = append(hits, p)
		}
	}
	switch len(hits) {
	case 0:
		return "", ErrNoSession
	case 1:
		return hits[0], nil
	}
	ids := make([]string, 0, len(hits))
	for _, p := range hits {
		ids = append(ids, strings.TrimSuffix(filepath.Base(p), ".jsonl"))
	}
	sort.Strings(ids)
	return "", &AmbiguousIDError{Prefix: id, IDs: ids}
}

// SessionsByRecency returns the sessions in cwd's scope newest-first by
// modification time — the order Session's no-id case walks. Exported so the
// caller can apply its own "which session counts" rule (skipping non-interactive
// runs) without this package having to know what an entrypoint is.
func SessionsByRecency(cwd string) ([]string, error) {
	paths, err := SessionsUnder(cwd)
	if err != nil {
		return nil, err
	}
	mod := make(map[string]int64, len(paths))
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil {
			mod[p] = info.ModTime().UnixNano()
		}
	}
	sort.SliceStable(paths, func(i, j int) bool { return mod[paths[i]] > mod[paths[j]] })
	return paths, nil
}

// ProjectDirs returns every project folder under ProjectsRoot, sorted. An
// unreadable root is an error; a readable but empty one returns no dirs.
func ProjectDirs() ([]string, error) {
	ents, err := os.ReadDir(ProjectsRoot)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, e := range ents {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(ProjectsRoot, e.Name()))
		}
	}
	sort.Strings(dirs)
	return dirs, nil
}

// ProjectCwd returns the working directory a project folder belongs to, read
// from the cwd field of its sessions.
//
// The folder name cannot be reversed — "-a-b-c" could encode /a/b/c or /a/b-c
// (see ProjectDirName) — but every log entry records the path outright, so
// reversing is unnecessary. Reading it also works for a project whose directory
// has since been deleted or renamed, which walking the filesystem cannot: on the
// development machine 37 of 63 project folders had no surviving directory.
//
// One folder maps to exactly one working directory, since the folder name is
// derived from that path, so the first cwd found settles it. Returns "" with no
// error when no session carries one.
func ProjectCwd(dir string) (string, error) {
	sessions, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return "", err
	}
	sort.Strings(sessions)
	for _, path := range sessions {
		if cwd := firstCwd(path); cwd != "" {
			return cwd, nil
		}
	}
	return "", nil
}

// firstCwd scans a session log for the first entry carrying a non-empty cwd.
// Malformed lines are skipped rather than aborting the file, matching the
// parser: a reader that stops at the first bad line silently drops every later
// one, which reads as an absent field rather than as an error.
func firstCwd(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)
	for sc.Scan() {
		var e struct {
			Cwd string `json:"cwd"`
		}
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue
		}
		if e.Cwd != "" {
			return e.Cwd
		}
	}
	return ""
}

// maxLine bounds a single log line. Tool results are stored inline with full
// content, so lines run far past bufio's 64KB default; a short buffer would
// fail the scan on exactly the sessions that did the most work.
const maxLine = 16 * 1024 * 1024

// SessionsAll returns every session JSONL under every project folder, in no
// particular order, paired with nothing — the caller reads each session's own
// cwd. ErrNoSession when no project holds a session.
func SessionsAll() ([]string, error) {
	dirs, err := ProjectDirs()
	if err != nil {
		return nil, err
	}
	return sessionsIn(dirs)
}

// SessionsUnder returns every session JSONL belonging to a project at or under
// root — root itself plus anything nested inside it, which is how a repo picks
// up the worktrees Claude Code creates under <repo>/.claude/worktrees/ and how
// a parent directory picks up every repo beneath it. It backs both the listing's
// default scope (root = the working directory) and --project.
// ErrNoProject when no project matches.
//
// Nested projects are selected by each project's recorded cwd rather than by its
// folder name, because the name is lossy. Root's own folder is taken by name as
// well, and unconditionally: widening a scope must never drop what the narrow
// lookup found, and the two disagree whenever a folder's sessions record a path
// other than the one its name encodes — a session relocated into the folder, or
// one predating the cwd field. Taking the root by name is exactly what the narrow
// lookup did before the default widened, so standing in a project always lists it.
func SessionsUnder(root string) ([]string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	dirs, err := ProjectDirs()
	if err != nil {
		return nil, err
	}
	var matched []string
	// rootDir is "" when root has no folder of its own, and no folder name is
	// ever empty, so the skip below needs no second test.
	rootDir, rootErr := ProjectDir(abs)
	if rootErr == nil {
		matched = append(matched, rootDir)
	}
	for _, dir := range dirs {
		if dir == rootDir {
			continue // already taken by name
		}
		cwd, err := ProjectCwd(dir)
		if err != nil || cwd == "" {
			continue
		}
		if underPath(abs, cwd) {
			matched = append(matched, dir)
		}
	}
	if len(matched) == 0 {
		return nil, ErrNoProject
	}
	return sessionsIn(matched)
}

// underPath reports whether path is root or lives inside it, compared by whole
// path components — so /a/bc is not "under" /a/b, which a string-prefix test
// would wrongly accept.
func underPath(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

func sessionsIn(dirs []string) ([]string, error) {
	var out []string
	for _, dir := range dirs {
		matches, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
		if err != nil {
			return nil, err
		}
		out = append(out, matches...)
	}
	if len(out) == 0 {
		return nil, ErrNoSession
	}
	return out, nil
}
