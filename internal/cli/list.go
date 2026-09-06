package cli

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/eitanpo/agentry/internal/entrypoint"
	"github.com/eitanpo/agentry/internal/list"
	"github.com/eitanpo/agentry/internal/locate"
	"github.com/eitanpo/agentry/internal/model"
	"github.com/eitanpo/agentry/internal/parse"
)

// newListCmd is the `list` verb: resolve the project's sessions, summarize,
// filter, and print one row per session. Its flags exist only here — render
// flags are structurally absent, not silently ignored.
func newListCmd(noColor *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "list this project's sessions instead of rendering one",
		Args:  cobra.NoArgs,
		Example: "  agentry list\n" +
			"  agentry list --limit 25\n" +
			"  agentry list --since today\n" +
			"  agentry list --include prompts",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(cmd, noColor)
		},
	}
	addListFlags(cmd)
	addFormatFlag(cmd)
	return cmd
}

// addListFlags installs the listing selectors and detail toggles. They live on
// both the `list` verb and the root, because bare `agentry` defaults to listing;
// a flag is read from whichever command was invoked. --format is added
// separately (addFormatFlag) since it is shared with the render path.
func addListFlags(cmd *cobra.Command) {
	cmd.Flags().Int("limit", 10, "cap to N most-recent sessions (0 = no cap)")
	cmd.Flags().String("since", "", "only sessions active at or after WHEN (today|yesterday, Nh|Nd|Nw, YYYY-MM-DD)")
	cmd.Flags().String("until", "", "only sessions active at or before WHEN")
	// The channel list is spelled from includeNames rather than repeated, so help
	// and the parser cannot name different sets — they already had, the help text
	// having been left behind when a channel was added.
	cmd.Flags().String("include", "", "add detail channels (comma-separated): "+strings.Join(includeNames, ", "))
	for _, u := range usageFilters {
		cmd.Flags().String(u.flag, "", "only sessions that "+u.did)
		cmd.Flags().String("not-"+u.flag, "", "only sessions that never "+u.did)
	}
	cmd.Flags().String("model", "", "only sessions that ran on a matching model (substring: opus, claude-opus-5)")
	cmd.Flags().String("effort", "", "only sessions run at this reasoning effort (exact: low, medium, high, xhigh, max)")
	_ = cmd.RegisterFlagCompletionFunc("effort", fixedComp(effortLevels))
	cmd.Flags().Int("min-lines", 0, "only sessions that changed at least N lines (added plus removed, as Claude Code recorded them)")
	cmd.Flags().Int("max-lines", 0, "only sessions that changed at most N lines; 0 selects the sessions that changed nothing")
	cmd.Flags().Bool("all-projects", false, "list every project's sessions, not just this directory's")
	cmd.Flags().String("project", "", "list PATH's sessions instead of this directory's, including anything nested under it")
	addFromFlag(cmd)
}

// addFromFlag installs --from. It is registered separately from the rest of the
// list flags because `view` takes it too — it picks which session the no-id
// lookup resolves to — and `view` carries none of the other selectors. The root
// gets it through addListFlags and must not register it twice.
func addFromFlag(cmd *cobra.Command) {
	cmd.Flags().String("from", "", "where the session ran: cli, app, sdk, all (default: everything but sdk)")
	// Complete the enum flag to its allowed values instead of filenames.
	_ = cmd.RegisterFlagCompletionFunc("from", fixedComp(entrypoint.Names))
}

// sessionPaths resolves which sessions the listing covers: this directory's
// subtree by default, another named subtree under --project, or every project
// under --all-projects. The two scope flags are mutually exclusive — silently
// preferring one would make the other look broken rather than rejected.
//
// The default is a subtree rather than the one project this directory's path
// encodes to, because Claude Code gives every git worktree its own project
// folder: an exact-folder listing in a main checkout showed none of the repo's
// worktree sessions. --project moves that same rule's root; it does not turn it
// on.
func sessionPaths(cmd *cobra.Command) ([]string, error) {
	allProjects, _ := cmd.Flags().GetBool("all-projects")
	project, _ := cmd.Flags().GetString("project")
	if allProjects && project != "" {
		return nil, usageErr("--all-projects and --project are mutually exclusive: --all-projects covers every project, so naming one narrows nothing")
	}
	switch {
	case allProjects:
		return locate.SessionsAll()
	case project != "":
		return locate.SessionsUnder(project)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return locate.SessionsUnder(cwd)
}

// usageFilters is the single source for the what-a-session-did filter surface:
// each entry registers its flag and the --not- twin of the same name, and fills
// the matching field on both sides of list.Filters. One list rather than three
// parallel ones, so a filter cannot be added to the flag set and forgotten in the
// negation or in the limit-lifting below. Most entries name a tool the session
// used; the last two name something it produced, which is a different axis but
// the same flag shape and the same negation rule.
//
// did completes both "only sessions that <did>" and "only sessions that never
// <did>", which is why it is phrased as a past-tense verb phrase.
//
// set returns an error because one filter value can be malformed: --reply-matches
// takes a regular expression. The error is carried on the shared shape rather
// than special-casing that one flag outside the table, so a future filter with a
// validated value cannot be added to the flag set and forgotten in the negation.
// The caller names the flag, so the set functions stay flag-agnostic.
var usageFilters = []struct {
	flag string
	did  string
	set  func(*list.Criteria, string) error
}{
	{"used-tool", "used this tool, by name (Bash, Skill, Agent, WebFetch, …)", func(c *list.Criteria, v string) error { c.Tool = v; return nil }},
	{"used-skill", "invoked this skill", func(c *list.Criteria, v string) error { c.Skill = v; return nil }},
	{"used-agent", "spawned this subagent type", func(c *list.Criteria, v string) error { c.Agent = v; return nil }},
	{"used-command", "ran a Bash command matching this text", func(c *list.Criteria, v string) error { c.Command = v; return nil }},
	{"used-file", "modified a file matching this path", func(c *list.Criteria, v string) error { c.File = v; return nil }},
	{"used", "used this as a skill, agent, or command", func(c *list.Criteria, v string) error { c.Any = v; return nil }},
	{"opened-pr", "opened a matching pull request, by repository, number, or url", func(c *list.Criteria, v string) error { c.PR = v; return nil }},
	{"published-artifact", "published a matching artifact, by title, url, or local path", func(c *list.Criteria, v string) error { c.Artifact = v; return nil }},
	{"reply-matches", "wrote a reply matching this pattern (case-insensitive regexp)", func(c *list.Criteria, v string) error {
		if v == "" {
			return nil // unset flag: no constraint, and "" would match every reply
		}
		re, err := compileReply(v)
		if err != nil {
			return err
		}
		c.Reply = re
		return nil
	}},
}

// compileReply turns a --reply-matches value into the matcher the filter uses:
// the caller's own pattern, made case-insensitive to match the rest of the
// family. The (?i) prefix covers the whole pattern including every branch of a
// top-level alternation, and a caller who wants case to matter overrides it with
// (?-i). The error names the pattern, since cobra reports only the flag.
func compileReply(pattern string) (*regexp.Regexp, error) {
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return nil, fmt.Errorf("%q is not a valid regular expression: %w", pattern, err)
	}
	return re, nil
}

// parseChanged reads the two line bounds off the command line. A bound is set
// only when the flag was given, so --max-lines 0 asks for the sessions that
// changed nothing rather than reading as an unset flag. A negative bound is a
// usage error: no session changed fewer than zero lines, so the value can only be
// a mistake, and silently accepting it would return the whole listing for
// --min-lines and nothing for --max-lines.
func parseChanged(cmd *cobra.Command) (list.Changed, error) {
	var c list.Changed
	for _, b := range []struct {
		flag string
		set  func(*int)
	}{
		{"min-lines", func(v *int) { c.Min = v }},
		{"max-lines", func(v *int) { c.Max = v }},
	} {
		if !cmd.Flags().Changed(b.flag) {
			continue
		}
		n, _ := cmd.Flags().GetInt(b.flag)
		if n < 0 {
			return list.Changed{}, usageErr("--%s: %d is negative; a line count cannot be", b.flag, n)
		}
		set := n
		b.set(&set)
	}
	if c.Min != nil && c.Max != nil && *c.Min > *c.Max {
		return list.Changed{}, usageErr("--min-lines %d is above --max-lines %d, so no session can match", *c.Min, *c.Max)
	}
	return c, nil
}

// usedFlags are every usageFilters flag name, positive and negated; any of them,
// like a time filter, lifts the default --limit so a filtered listing is not
// silently capped.
var usedFlags = func() []string {
	names := make([]string, 0, 2*len(usageFilters))
	for _, u := range usageFilters {
		names = append(names, u.flag, "not-"+u.flag)
	}
	return names
}()

func runList(cmd *cobra.Command, noColor *bool) error {
	limit, _ := cmd.Flags().GetInt("limit")
	since, _ := cmd.Flags().GetString("since")
	until, _ := cmd.Flags().GetString("until")
	include, _ := cmd.Flags().GetString("include")

	var showPrompts, showTools, showFiles, showModel, showCost, showOutputs bool
	for _, tok := range strings.Split(include, ",") {
		switch tok = strings.TrimSpace(tok); tok {
		case "": // empty entries (e.g. unset flag) contribute nothing
		case "prompts":
			showPrompts = true
		case "tools":
			showTools = true
		case "files":
			showFiles = true
		case "model":
			showModel = true
		case "cost":
			showCost = true
		case "outputs":
			showOutputs = true
		case "all":
			showPrompts, showTools, showFiles = true, true, true
			showModel, showCost, showOutputs = true, true, true
		default:
			if g := nearest(tok, includeNames); g != "" {
				return usageErr("--include: unknown channel %q — did you mean %q?", tok, g)
			}
			return usageErr("--include: unknown channel %q (want: %s)", tok, strings.Join(includeNames, ", "))
		}
	}

	// Validate --format before touching the filesystem, so a bad value errors
	// (with a suggestion) the same way a bad --include channel does.
	format, err := parseFormat(cmd)
	if err != nil {
		return err
	}
	from, err := parseFrom(cmd)
	if err != nil {
		return err
	}

	now := time.Now()
	var sinceT, untilT time.Time
	if since != "" {
		t, err := list.ParseWhen(since, now)
		if err != nil {
			return usageErr("--since: %v", err)
		}
		sinceT = t
	}
	if until != "" {
		t, err := list.ParseWhen(until, now)
		if err != nil {
			return usageErr("--until: %v", err)
		}
		untilT = t
	}
	// A time, --used* or run filter without an explicit --limit lifts the default
	// cap, so a filtered listing shows every match, not just ten.
	filtering := cmd.Flags().Changed("since") || cmd.Flags().Changed("until") ||
		cmd.Flags().Changed("model") || cmd.Flags().Changed("effort") ||
		cmd.Flags().Changed("min-lines") || cmd.Flags().Changed("max-lines")
	for _, f := range usedFlags {
		filtering = filtering || cmd.Flags().Changed(f)
	}
	if filtering && !cmd.Flags().Changed("limit") {
		limit = 0
	}

	get := func(name string) string { v, _ := cmd.Flags().GetString(name); return v }
	var filters list.Filters
	for _, u := range usageFilters {
		// Both sides are validated before any session is read, so a malformed
		// value errors as usage rather than after a directory scan's delay.
		if err := u.set(&filters.Used, get(u.flag)); err != nil {
			return usageErr("--%s: %v", u.flag, err)
		}
		if err := u.set(&filters.NotUsed, get("not-"+u.flag)); err != nil {
			return usageErr("--not-%s: %v", u.flag, err)
		}
	}
	run := list.Run{Model: get("model"), Effort: get("effort")}
	changed, err := parseChanged(cmd)
	if err != nil {
		return err
	}

	paths, err := sessionPaths(cmd)
	if err != nil {
		// A usage error is the caller's mistake, not an empty result: it must not
		// be dressed up as a well-formed empty listing.
		var ue *exitError
		if errors.As(err, &ue) && ue.code == exUsage {
			return err
		}
		// Under --format json the output contract is an array, and these
		// failures — no project for the directory or --project path, or no
		// project holding a session — are the only ones that would leave stdout
		// empty instead. Emitting [] keeps one shape for every outcome so a
		// caller sweeping directories can pipe into jq without guarding. The
		// error still goes to stderr with its exit code: an empty array is not a
		// claim of success.
		if format == "json" {
			_ = list.RenderJSON(os.Stdout, nil)
		}
		return noInputErr(err)
	}

	var sums []model.Summary
	for _, p := range paths {
		s, err := parse.Summarize(p)
		if err != nil {
			continue // skip a session that won't parse, like a malformed line
		}
		sums = append(sums, s)
	}

	// The entrypoint filter runs before the rest so --limit counts sessions the
	// caller will actually see: capping first and filtering after would return
	// fewer than N rows and give no hint why.
	visible := list.FilterByFrom(sums, from)
	selected := list.Select(list.Filter(visible, filters, run, changed), sinceT, untilT, limit)
	// A default that empties the listing must say so. Hidden non-interactive
	// sessions are the one exclusion the caller did not ask for, so without this
	// an empty result is indistinguishable from a project holding nothing.
	// Written to the command's error stream, not os.Stderr directly, so it goes
	// wherever the caller routed diagnostics — the same stream errors use.
	if from == "" && len(visible) == 0 && len(sums) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "agentry: %d headless session(s) hidden — pass --from all to include them\n", len(sums))
	}
	if format == "json" {
		if err := list.RenderJSON(os.Stdout, selected); err != nil {
			return &exitError{code: 1, err: err}
		}
		return nil
	}
	color, width := terminal(*noColor)
	if err := list.Render(os.Stdout, selected, list.Options{
		Width: width, Color: color, Prompts: showPrompts, Tools: showTools, Files: showFiles,
		Model: showModel, Cost: showCost, Outputs: showOutputs,
	}); err != nil {
		return &exitError{code: 1, err: err}
	}
	return nil
}
