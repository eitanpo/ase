package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/eitanpo/agentry/internal/entrypoint"
	"github.com/eitanpo/agentry/internal/locate"
	"github.com/eitanpo/agentry/internal/parse"
	"github.com/eitanpo/agentry/internal/render"
)

// newRootCmd assembles the command tree. The bare root lists (no argument) or
// renders (a full session id); it also parents the verbs. Because it does both,
// it carries both flag sets — the list selectors and the render toggles. noColor
// is shared by reference into the verbs so the persistent flag has one backing
// value.
func newRootCmd(version string) *cobra.Command {
	var noColor bool

	root := &cobra.Command{
		Use:   "agentry [session-id]",
		Short: "render Claude Code session logs (bare command lists them)",
		Long: "agentry " + version + " — render a Claude Code session log to the terminal\n\n" +
			"With no argument it lists the current project's sessions; pass a full\n" +
			"session id to render one, or `agentry view` to render the most recent.",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeSessionIDs,
		Version:           version,
		SilenceErrors:     true, // Execute prints in agentry's voice
		SilenceUsage:      true, // a usage error must not dump full help
		Example: "  agentry                      list this project's sessions\n" +
			"  agentry --since today        list sessions active today\n" +
			"  agentry <uuid>               render a specific session\n" +
			"  agentry view                 render the most recent session\n" +
			"  agentry view --level full    render the most recent in full detail\n" +
			"  agentry list --since 7d      list sessions from the last 7 days",
		RunE: func(cmd *cobra.Command, args []string) error {
			// No id lists; a full id renders. renderSession handles the
			// verb-vs-id did-you-mean for a non-id first token.
			if len(args) == 0 {
				return runList(cmd, &noColor)
			}
			return renderSession(cmd, args, &noColor, true)
		},
	}
	root.PersistentFlags().BoolVar(&noColor, "no-color", false, "disable color (also honors NO_COLOR)")
	// Predefine --version (no -v shorthand) so Cobra's auto flag — which would
	// bind -v — is skipped; -v conventionally means verbose, not version.
	root.Flags().Bool("version", false, "print version and exit")
	root.SetVersionTemplate("agentry {{.Version}}\n")
	root.SetFlagErrorFunc(flagErrorFunc)
	addRenderFlags(root)
	addListFlags(root)
	addFormatFlag(root)

	cobra.AddTemplateFunc("renderFlagUsages", renderFlagUsages)
	cobra.AddTemplateFunc("otherLocalFlagUsages", otherLocalFlagUsages)
	root.SetUsageTemplate(usageTemplate)

	root.AddCommand(newViewCmd(&noColor))
	root.AddCommand(newListCmd(&noColor))
	return root
}

// renderSession resolves the session and renders it. isRoot distinguishes the
// bare command (where a non-id first token was meant as a verb and gets a
// did-you-mean) from explicit `view` (where the token is always an id).
func renderSession(cmd *cobra.Command, args []string, noColor *bool, isRoot bool) error {
	channels, err := channelsFromFlags(cmd)
	if err != nil {
		return err
	}
	format, err := parseFormat(cmd)
	if err != nil {
		return err
	}
	from, err := parseFrom(cmd)
	if err != nil {
		return err
	}

	var id string
	if len(args) == 1 {
		id = args[0]
		if isRoot && !looksLikeID(id) {
			if g := nearest(id, verbNames); g != "" {
				return usageErr("unknown command %q — did you mean %q?", id, g)
			}
			return usageErr("unknown command %q (run \"agentry --help\")", id)
		}
		// --from chooses among sessions; an id has already chosen one. Accepting
		// both and ignoring the flag would leave a caller believing it applied.
		if cmd.Flags().Changed("from") {
			return usageErr("--from cannot be combined with a session id: %q already names the session to render", id)
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return noInputErr(err)
	}
	var path string
	if id != "" {
		// A named id is an explicit request and is never second-guessed, whatever
		// kind of session it turns out to be.
		path, err = locate.Session(cwd, id)
	} else {
		path, err = mostRecent(cmd, cwd, from)
	}
	if err != nil {
		// An ambiguous prefix is a bad argument, not a missing input: the sessions
		// are all there and the caller has to say which. Name the candidates so the
		// next attempt is a copy rather than another guess.
		var amb *locate.AmbiguousIDError
		if errors.As(err, &amb) {
			return usageErr("%v:\n  %s", amb, strings.Join(amb.IDs, "\n  "))
		}
		return noInputErr(err)
	}
	sess, err := parse.Load(path)
	if err != nil {
		return noInputErr(err)
	}

	if format == "json" {
		if err := render.SessionJSON(os.Stdout, sess); err != nil {
			return &exitError{code: 1, err: err}
		}
		return nil
	}

	color, width := terminal(*noColor)
	if err := render.Session(os.Stdout, sess, render.Options{
		Width: width, Color: color, Channels: channels,
	}); err != nil {
		return &exitError{code: 1, err: err}
	}
	return nil
}

// mostRecent resolves `view` with no id: the newest session matching the --from
// selector. The empty selector is the default — the newest session that was not
// a non-interactive run. On a machine using hooks the newest session is usually
// a few-second headless run, so "show me my last session" would otherwise render
// a hook, the same reason the listing hides them.
//
// Only the default falls back. When every session is headless it renders the
// newest one anyway and says so, because sessions plainly exist and, unlike a
// listing, there is no empty result to return. An explicit --from gets an error
// instead: falling back there would render a kind the caller did not ask for and
// present it as what they asked for.
func mostRecent(cmd *cobra.Command, cwd, from string) (string, error) {
	paths, err := locate.SessionsByRecency(cwd)
	if err != nil {
		return "", err
	}
	for _, p := range paths {
		s, err := parse.Summarize(p)
		if err != nil {
			continue // unparseable: same skip the listing makes
		}
		if entrypoint.Matches(from, s.Entrypoint) {
			return p, nil
		}
	}
	if from != "" {
		// --from all reaches here only when nothing parsed, so there is no wider
		// selector to suggest.
		hint := "; pass --from all for the most recent of any kind"
		if from == entrypoint.All {
			hint = ""
		}
		return "", fmt.Errorf("no session here matches --from %s (%d session(s) in this project)%s", from, len(paths), hint)
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"agentry: every session here is a headless run; showing the most recent one\n")
	return paths[0], nil
}

// completeSessionIDs is the shell-completion handler for the render path's
// positional session id. It lists the current project's sessions and offers
// each id annotated with its title (as `agentry list` would show it). Cobra's
// hidden __complete callback runs it on every Tab, so it reflects the sessions
// present at that moment; NoFileComp keeps an id from decaying into filename
// completion. Errors resolve to "no suggestions", never a crash mid-Tab.
func completeSessionIDs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 { // the render path takes at most one id
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	// Read --from off the line being completed: a flag that says "include
	// headless runs" must not be contradicted by completion still hiding them.
	// An unvalidated value matches nothing, which is the right answer mid-Tab —
	// completion has no channel for an error.
	from, _ := cmd.Flags().GetString("from")
	cwd, err := os.Getwd()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	paths, err := locate.SessionsUnder(cwd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, p := range paths {
		s, err := parse.Summarize(p)
		if err != nil || !strings.HasPrefix(s.ID, toComplete) {
			continue
		}
		// Offer the ids a listing under the same --from would show. Without this,
		// tabbing a UUID in a project that uses hooks surfaces headless runs ahead
		// of the work being looked for — and completion has no room to explain why
		// they are there.
		if !entrypoint.Matches(from, s.Entrypoint) {
			continue
		}
		out = append(out, s.ID+"\t"+compTitle(s.Title))
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// compTitle flattens a session title to a single short line fit for a
// completion description — a tab or newline would corrupt the shell's menu.
func compTitle(title string) string {
	title = strings.TrimSpace(strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(title))
	if r := []rune(title); len(r) > 50 {
		title = string(r[:47]) + "..."
	}
	if title == "" {
		return "(untitled)"
	}
	return title
}

// looksLikeID reports whether tok has the shape of a session id — hex digits
// and hyphens only. Verbs are English words and so always fail this, which is
// what makes first-token routing (verb vs. id) unambiguous.
func looksLikeID(tok string) bool {
	if tok == "" {
		return false
	}
	for _, r := range tok {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		case r == '-':
		default:
			return false
		}
	}
	return true
}
