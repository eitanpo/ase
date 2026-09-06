# agentry

**AGENT ReplaY**  — render a Claude Code session log into a styled terminal view.

See [PRODUCT.md](PRODUCT.md) for scope and design rationale.

## Install

macOS (Homebrew cask):

```
brew install eitanpo/tap/agentry
```

Linux: `go install github.com/eitanpo/agentry@latest`, or download a binary from the [releases](https://github.com/eitanpo/agentry/releases).

### Shell completion

The Homebrew cask installs tab-completion automatically — nothing to do. For a `go install` or a downloaded binary, generate and load the script for your shell:

```
source <(agentry completion zsh)     # add to ~/.zshrc
source <(agentry completion bash)    # add to ~/.bashrc
agentry completion fish | source     # or: > ~/.config/fish/completions/agentry.fish
```

Completion covers the verbs and flags, the enum values of `--format`, `--level`, `--from` and `--effort`, and — the useful one — the current project's **session ids**, each shown with its title, so you tab a UUID instead of pasting it.

## Usage

Run `agentry` from the directory you ran Claude Code in:

```
agentry                     # list this project's sessions (see below)
agentry <uuid>              # render a session by id, or any unambiguous prefix (agentry 489ce01)
agentry view                # render the most recent session (no id needed)
agentry view --from sdk     # render the most recent headless run (a hook, `claude -p`)
agentry <uuid> --format json | jq  # the full session model as JSON, for piping
```

With no id, `agentry` lists this directory's sessions and those of any project nested under it (below); with an id it renders that one, looked up in the same set of projects, under `~/.claude/projects/`; the id may be a full UUID or any prefix that names one session, and a prefix matching several is an error saying how many. `agentry view` with no id picks the most recent session you actually worked in, skipping headless runs — an id you name is always rendered as asked. `--from` changes which kind it picks: `--from sdk` for the last headless run, `--from all` for the last session of any kind. Asking for a kind this project has none of is an error, not a quiet fall back to another kind. The first token is a verb (`view`, `list`) when it names one, otherwise a session id — they can't collide, since ids are hex and verbs are words. Flags may go before or after operands, and a mistyped verb, flag, or value is met with a "did you mean" suggestion rather than full help.

To find a session, list them — bare `agentry` does this, and `agentry list` is its explicit form:

```
agentry                                   # the 10 most recent sessions (bare command == list)
agentry --since today                     # list flags work on the bare command too
agentry list                              # the explicit form of the bare listing
agentry list --limit 25                   # the 25 most recent
agentry list --since today                # everything from today
agentry list --since 7d                   # the last 7 days
agentry list --since 2026-06-01 --until 2026-06-03
agentry list --include prompts            # list each session's prompts beneath its row
agentry list --include tools              # break down each session's tool calls by command/skill/agent
agentry list --include files              # every file each session modified
agentry list --include model              # what each session ran on: model and reasoning effort
agentry list --include cost               # what each session amounted to: tokens, dollars, lines changed
agentry list --include outputs            # the PRs each session opened and the artifacts it published
agentry list --model opus                 # only sessions that ran on an opus model
agentry list --model claude-opus-5        # only that one model
agentry list --effort xhigh               # only sessions run at xhigh reasoning effort
agentry list --used-command exa           # only sessions that ran a Bash command matching "exa"
agentry list --used-skill expert          # only sessions that invoked the expert skill
agentry list --used-file PRODUCT.md       # only sessions that modified that file
agentry list --used researcher            # skill, agent, or command matching "researcher"
agentry list --used-command 'git commit' --not-used-skill review   # committed without loading a skill
agentry list --opened-pr 187              # the session that opened PR 187
agentry list --all-projects --opened-pr wix-private/artifactory-migration --include outputs
agentry list --published-artifact cost    # sessions that published an artifact matching "cost"
agentry list --reply-matches '(?m)^\*{0,2}Learnings\b'       # sessions whose replies carried a Learnings block
agentry list --not-reply-matches 'file://' # sessions that never printed a file:// link
agentry list --used-skill expert --format json | jq   # machine-readable, for piping
agentry list --all-projects --from all --limit 0 --format json \
  | jq -r 'group_by(.model)[] | "\(.[0].model) \(map(.usage.output) | add)"'   # output tokens per model, everywhere
agentry list --all-projects --limit 0 --format json \
  | jq -r '.[] | select(.prs) | "\(.id) \(.prs | map(.url) | join(" "))"'   # every session that opened any PR
agentry list                               # this directory and every project nested under it
agentry list --all-projects                # every project, not just this directory
agentry list --project ~/Projects/me/app   # that repo and every worktree nested in it
agentry list --project ~/Projects/me       # every repo under that directory
agentry list --from app                    # only sessions started in the desktop app
agentry list --from all                    # include headless runs, hidden by default
```

**A rendered session ends with what it produced.** If the session opened a pull request or published
an artifact, an `Outputs` section after the last turn lists each one, clickable on a terminal — a
pull request as its URL, an artifact as its title. It is not gated on `--level`, so it shows even at
`minimal`: an output is an outcome, not working detail. Sessions that produced neither show no
section. These are the same facts `list --include outputs` and `--opened-pr` read.

**Headless sessions are hidden unless you ask for them.** Anything non-interactive — a `claude -p`
from a script, a hook, a CI step — writes a session log like any other, and on a machine that uses
hooks these outnumber the ones you typed. They are excluded by default so a listing shows work you
did; `--from sdk` shows only those, `--from all` shows everything. When a listing spans more than
one kind, each row gains a 3-letter tag: `cli` (terminal), `app` (desktop), `sdk` (headless), and
`cli+` for a session that started in one and was resumed in another.

**A listing covers this directory and everything nested under it.** That matters because Claude
Code gives every git worktree its own project folder, so a repo's sessions are split across them —
standing in the main checkout lists the worktrees' sessions too. `--project PATH` applies the same
subtree rule from a root you name instead, and `--all-projects` covers every project there is. All
three reach projects whose directory you have since deleted or renamed, which walking directories
yourself cannot. When a listing spans more than one project, each row gains a project column before
the title, labelled by the repository — a worktree's sessions carry the repo's name, not the
worktree's, so one repo reads as one project. Inside a single repository that slot carries a
worktree column instead, naming which worktree each session ran in (`—` for the repo's own
checkout); it appears only when the sessions span more than one. `--format json` carries the full
path as `cwd` on every session. Rendering follows the
same scope, so an id copied off a listing opens where you read it — no `cd` into the worktree
first — and `agentry view` with no id reaches the whole subtree too, picking the most recently
written session file rather than the current directory's.

Sessions print oldest-to-newest, so the most recent is at the bottom, next to your prompt. Each row shows the last-activity time (when the session's most recent turn ended — the same recency the list is ordered by), duration, turn count, a title (a name you chose if set — from renaming the session, or from `--name` / `/rename`, whichever the log records last — else Claude Code's own `ai-title` summary, falling back to the first prompt, skipping a leading `/clear`), and its id, shortened to the shortest prefix unique among the rows and never under 8 characters — copy it and pass it to `agentry <id>` to render that session. `--format json` keeps every id in full. A forked session (Claude Code's `--fork-session` / `/branch`) is grouped under the original it was forked from and its title indented with `└─`; while it still carries the original's inherited title it is shown by its first new prompt instead, so the two are distinguishable. A title that just repeats the row's worktree — what you get when one argument names both the worktree and the conversation, as `devx -n plan -w` does — is replaced by the session's first prompt for the same reason: the worktree column already shows it.

### Options

| Flag | Mode | Default | Description |
|---|---|---|---|
| `--level minimal\|standard\|detailed\|full` | render | `minimal` | Preset of channel defaults. `minimal` prompts+response; `standard` +thinking+metrics; `detailed` +tools+subagents (no output); `full` +tool-results. |
| `--[no-]thinking\|tools\|tool-results\|subagents\|metrics` | render | — | Override a single channel on top of `--level` (adds or subtracts). `tools` = a tool fired; `tool-results` = its output. An `Agent` line also names what it delegated to: `Agent[Explore@haiku]` is the subagent type and the model, and the `@model` half is absent when the call left the subagent on the session's model. |
| `--limit N` | `list` | `10` | Cap to N most-recent (`0` = no cap; lifted when any filter flag is set). |
| `--since WHEN`, `--until WHEN` | `list` | — | Filter by last-activity time. WHEN: `today`/`yesterday`, `Nh`/`Nd`/`Nw`, or `YYYY-MM-DD`. |
| `--include CHANNELS` | `list` | — | Add per-session detail. Comma-separated; channels: `prompts`, `tools`, `files`, `model`, `cost`, `outputs` (or `all`). `tools` breaks down a session's top-level tool calls grouped by identity — Bash by program, Skill by name, Agent by subagent type, Edit/Write by target file, everything else by tool name — and adds a `Denied` line naming the calls that were refused and by what (`permission-rule`, `automode-blocked`, `automode-unavailable`, `user-rejected`), which an error glyph alone cannot tell you. `files` lists every file the session modified by any means, from Claude Code's own file-history record rather than from tool arguments. `model` names what the session ran on — its model and reasoning effort, in the rendered header's phrasing — which is otherwise invisible in the text table. `cost` names what it amounted to — the token tally, then the dollar total and the lines added and removed that Claude Code recorded — in the rendered header's exact wording, and is the only place the text table states any of them; the recorded halves are absent on any session whose log carries no cost record, which is every session before Claude Code 2.1.241 and many since, and the line counters are dropped again on a session that changed nothing. `outputs` lists what the session produced beyond its transcript: one line per pull request it opened (its URL) and per artifact it published (title, then `claude.ai` URL), deduplicated, since Claude Code re-records both on later turns. `outputs` lists what the session produced beyond its transcript: one line per pull request it opened (its URL) and per artifact it published (title, then `claude.ai` URL), deduplicated, since Claude Code re-records both on later turns. |
| `--used-tool NAME` | `list` | — | Only sessions where that tool fired, by tool-use name (case-insensitive, exact). The "which mechanism" axis. |
| `--used-skill`, `--used-agent`, `--used-command` | `list` | — | Identity axis: a Skill's skill, an Agent's subagent type, a Bash command's text (case-insensitive substring). |
| `--used-file PATH` | `list` | — | Only sessions that modified a matching file (case-insensitive substring, so `list.go` catches every directory's and `internal/cli/list.go` names one). Reads `Edit`/`Write` targets and the tracked-file record together; the tool targets do nearly all the work, since about half of sessions have no tracked-file record at all. Not covered by `--used`. |
| `--used TOKEN` | `list` | — | Catch-all over the identity axis: skill name, agent type, or command. Not tool names — use `--used-tool` for those. |
| `--opened-pr TEXT` | `list` | — | Only sessions that opened a matching pull request, over its repository, number, and URL (case-insensitive substring, so `artifactory-migration` selects a repository's worth and `187` picks one). Read from Claude Code's own `pr-link` record, which is written for the session as a whole — so this finds a PR opened inside a subagent, which `--used-command 'gh pr'` cannot. |
| `--published-artifact TEXT` | `list` | — | Only sessions that published a matching artifact, over its title, its `claude.ai` URL, and the local file it was rendered from (case-insensitive substring). Same session-level record as `--opened-pr`. |
| `--reply-matches PATTERN` | `list` | — | Only sessions whose assistant reply text matched. PATTERN is a **regular expression** (RE2), case-insensitive by default (`(?-i)` to override) — the one filter here that is not a substring match, because prose questions are positional and alternation-shaped. Tested against each assistant text block separately, so `^`/`$` anchor to one reply: `'(?m)^\*{0,2}Learnings\b'` finds the block, where the substring `Learnings` would also hit every session that merely mentioned it. Thinking blocks and subagent sidecars are not read. An unparseable pattern is a usage error. Reply text is matched but never printed or serialized — see the `--format json` note in [PRODUCT.md](PRODUCT.md) for the size reason. |
| `--not-used-*`, `--not-opened-pr`, `--not-published-artifact`, `--not-reply-matches` | `list` | — | Every filter in this family has a `--not-` twin (`--not-used-tool`, `--not-used-skill`, `--not-used-agent`, `--not-used-command`, `--not-used-file`, `--not-used`, `--not-opened-pr`, `--not-published-artifact`, `--not-reply-matches`) keeping the sessions the positive one drops. Combine the two for a compliance audit: `--used-command 'git commit' --not-used-skill review`, or count the misses of a reply rule with `--not-reply-matches`. For the `--used*` flags, absence is judged over top-level calls only, so a subagent may have used what the main thread did not; the two output filters read session-level records and carry no such gap. |
| `--model NAME` | `list` | — | Only sessions that ran on a matching model (case-insensitive substring, so `opus` covers `claude-opus-5` and `claude-opus-4-8` while `claude-opus-5` picks one). Matches any model the session carried, so one that switched mid-way matches both. |
| `--effort LEVEL` | `list` | — | Only sessions run at that reasoning effort (`low`, `medium`, `high`, `xhigh`, `max`). Case-insensitive and **exact**, unlike the substring filters — the levels nest, so `high` must not quietly include `xhigh`. Unknown levels return nothing rather than erroring, since the set grows. |
| `--all-projects` | `list` | — | Every project under `~/.claude/projects/`, not just this directory's. Mutually exclusive with `--project`. |
| `--project PATH` | `list` | — | PATH's sessions instead of this directory's, including every project nested under PATH. It moves the root, not the depth — a listing already covers what is nested under the current directory, which is how standing in a repo picks up its git worktrees. |
| `--from cli\|app\|sdk\|all` | `list`, `view` | `cli`+`app` | Where the session was run. `sdk` is anything non-interactive (`claude -p`, a hook, CI) and is **hidden by default**; `all` restores it. On `view` (no id) it picks which kind the most-recent lookup walks back to; it cannot be combined with a session id. |
| `--format json\|text` | render, `list` | `text` | `json` emits machine-readable output for piping. On the render path it's the full session model (`meta` + `turns`, ignoring `--level`/channels and color), with `meta.effort` beside `meta.model`, `meta.prs` and `meta.artifacts` carrying what the session produced, and each tool call carrying the `identity` that `list --include tools` groups by plus the `model` an `Agent` call delegated to; on `list` it's a JSON array of per-session summaries, each carrying its `cwd`, the `files` it modified as absolute paths, its `denials`, its `model` and `effort`, its `usage`, `costUSD`, `linesAdded` and `linesRemoved` — the token tally over the main thread and every subagent, plus Claude Code's own dollar and line totals where the log recorded them, the same number the render path reports as `meta.usage`, which is what makes a cross-project cost tally one call instead of one render per session — and its outputs, `prs` (`{repository, number, url}`) and `artifacts` (`{title, url, path}`) (ignoring `--include` and color), and stdout is always a valid array — a directory with no project, or a project with no sessions, prints `[]` while still reporting the error on stderr and exiting non-zero, so you can pipe into `jq` without a guard. |
| `--no-color` | global | — | Disable color (also honors the `NO_COLOR` env var). |
| `--help`, `--version` | global | — | Per-verb `--help` lists only that mode's flags. |

Bare `agentry` is the listing, so the "`list`" flags apply to it as well as to `agentry list`; the "render" flags apply to `agentry <uuid>` and `view`; "global" flags work anywhere.

Markdown-file export, content search, and an interactive browser are planned — see the roadmap in [PRODUCT.md](PRODUCT.md).

## Development

Go + [Charm](https://charm.sh) (Glamour, Lip Gloss). Released via GoReleaser to a Homebrew tap. Build, test, and install workflow: [DEVELOPMENT.md](DEVELOPMENT.md).

## License

MIT — see [LICENSE](LICENSE).
