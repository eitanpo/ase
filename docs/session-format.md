# Claude Code session log format

Reverse-engineered from real logs (observed 2026-05-27; last re-verified 2026-09-06 against
1,834 logs spanning Claude Code 2.1.195 through 2.1.263). **Not an official spec** —
Claude Code may change it without notice; re-verify against live files before relying
on a detail here. `internal/parse` and `internal/locate` encode this format.

**The shipped binary is a second source, and it settles what the logs only hint at.** Claude
Code installs as one large binary with its JavaScript bundle embedded as plain text, so
`grep -ao 'PATTERN.\{0,200\}' "$(readlink -f "$(which claude)")"` prints the code that writes
the log — the field names it emits, the values it validates, and the table it keys entry
types by. Use it for the question a corpus sweep cannot answer: whether a type missing from
local logs was removed or was simply never written on this machine. Two mechanics: give the
pattern a window on one side only, because a two-sided window exceeds the complexity limit
of the `grep` on this machine and errors instead of matching; and label every fact drawn
this way with the build it came from, since the next release rewrites the bundle. Facts
below marked 2.1.263 come from that build's binary rather than from a log.

Re-verify with `scripts/schema-scan.sh` (see [DEVELOPMENT.md](../DEVELOPMENT.md)). It reports
every field, entry type, `system` subtype, content-block type and `entrypoint` value in the
local logs, how often each occurs, the build range that wrote it, and whether this file
already names it; `--new` narrows to the ones it does not. Run it before trusting anything
below that a change would depend on — the counts and version ranges quoted here are from the
2026-09-06 sweep and age from the day they were written. The scan closes with the entry-type
roster it reads out of the installed binary, so a type Claude Code knows and no local session
has written is named there rather than left to a hand sweep; the same roster is reproduced
under [Entry types](#entry-types).

## Location and naming

- Root: `~/.claude/projects/`.
- One folder per project (working directory). The folder name is the project's
  absolute path with **every non-alphanumeric character replaced by `-`** — the leading `/`
  included, which is why the name starts with one. E.g. `/Users/me/Projects/dotfiles` →
  `-Users-me-Projects-dotfiles`. It is not only `/` that is replaced: `.` and `_` go too, so
  `/Users/me/.central/worktrees/pr-1` → `-Users-me--central-worktrees-pr-1` (note the doubled
  `-`) and `…/tvkkp3y92z1f212zv4k_9nvh0000gn/…` → `…-tvkkp3y92z1f212zv4k-9nvh0000gn-…`. Measured
  over the 63 local project folders on 2026-08-07: replacing only `/` reproduces 32 of them,
  adding `.` reproduces 60, replacing every non-alphanumeric reproduces all 63. Earlier
  revisions of this file said only `/` was replaced; that was wrong, and it made agentry unable
  to find the project for any path with a dot-component — every worktree Claude Code creates for
  itself, since those live under `<repo>/.claude/worktrees/`. No local path carries a non-ASCII
  character, so whether those are replaced is **unverified**.
- **The folder name cannot be reversed, but the path is recorded anyway.** `-a-b-c` could have
  come from `/a/b/c` or `/a/b-c`, so the encoding is lossy. Do not reverse it — read the `cwd`
  field off any entry in any session in the folder. That yielded a path for 63 of 63 local
  folders, including all 37 whose directory no longer exists on disk, so it is both complete and
  the only method that survives a project directory being deleted or renamed.
- One file per session: `<project>/<session-uuid>.jsonl`. The session id is a full UUID.
- Subagent sidecars: `<project>/<session-uuid>/subagents/agent-<id>.jsonl`, each with an
  `agent-<id>.meta.json` sibling. A sidecar is itself session-shaped JSONL. Sidecars
  dominate the tree — 1,346 of the 1,834 files in the 2026-09-06 sweep, against 488 main
  session logs — so any count over `*.jsonl` is counting mostly subagents unless it excludes
  `*/subagents/*`.
- **A git worktree is a separate project.** Its absolute path slugs like any other, so
  `~/.claude/projects/` holds one folder per worktree
  (`-Users-me--central-worktrees-pr-1`) and a repo's sessions are split across them. Nothing
  in the slug marks it as a worktree of anything.
- **Where the worktree was created shows in its directory name.** A worktree created from the
  desktop app carries a 6-character hex suffix (`ecr-gar-single-registry-f399e1`); one created
  from the terminal does not (`jfrog-usage`). The correlation held on all 9 worktrees of the
  2026-08-17 sweep — 6 hashed, every one `claude-desktop`; 3 unhashed, every one `cli`. The
  consequence for display: a worktree name's head is what someone chose and its tail can be
  generated, so shortening one must keep the head.
- **Claude Desktop sessions live here too**, in this same format, distinguished only by
  `entrypoint: claude-desktop`. The desktop app additionally keeps
  `~/Library/Application Support/Claude/claude-code-sessions/<workspace>/<profile>/local_<session-uuid>.json`
  — one JSON object per session holding UI state (`title`, `titleSource`, `cwd`,
  `permissionMode`, `completedTurns`, `isArchived`, `effort`, and a `forkedFromSessionId`
  that the JSONL has no equivalent of). That file is **not** a transcript and is not a
  substitute for the log: the conversation for the same id is the `.jsonl` under
  `~/.claude/projects/`.

## Line format

Each line is one JSON event. Lines can be large — tool results are stored inline with
full content. Skip a malformed line rather than failing on it: a reader that aborts at the
first one silently drops every later line in that session, which reads as a short session
rather than as an error. The 2026-09-06 sweep found none in 391,612 lines across 1,834
files, so treat this as a guard against a condition earlier revisions recorded, not as one
you should expect to hit.

Common top-level fields: `type`, `timestamp` (RFC3339), `message`, plus context such as
`cwd`, `gitBranch`, `sessionId`, `uuid`, `parentUuid`, `version`, `isSidechain`.

The set grows over time and is additive — Claude Code adds fields without removing
existing ones, so unknown fields are expected and the parser ignores them. Fields seen
since the initial observation, with their meaning:

- `entrypoint` — where the session was started: `cli` (276,842 entries), `sdk-cli`
  (19,931), or `claude-desktop` (16,073). The desktop app writes ordinary logs to the same
  `~/.claude/projects/` tree, so nothing but this field separates a desktop session from a
  terminal one. It ships its own bundled Claude Code build, which lags the CLI's, so a
  desktop session's `version` can be older than anything the terminal wrote that week.
  **`sdk-cli` is a misnomer — it means non-interactive, not "used the Agent SDK".** Verified
  2026-08-07 by running `claude -p 'reply with the single word: pong'` in an empty directory:
  every entry of the resulting session carried `entrypoint: "sdk-cli"`, with no SDK involved.
  Anything headless lands here — a shell script, a hook, a CI step, another agent shelling
  out. Do not read it as evidence that SDK code exists.
  Per session rather than per entry, over the 488 local main sessions: `cli` 222, `sdk-cli`
  205, `claude-desktop` 59, and 2 carrying two values.
- **A session can carry two entrypoints**, when it is started in one client and resumed from
  another. Both local cases are one contiguous block of each value, never interleaved
  (`claude-desktop`×710 then `cli`×1267; `sdk-cli`×7 then `cli`×7). The second is an exact
  tie, so a majority rule has no answer — order is the only usable signal.
- **`promptSource` does not identify the entrypoint.** Both `claude-desktop` (393 prompts)
  and `sdk-cli` (266) carry `promptSource: "sdk"`, so `sdk` there means "not typed at a
  terminal" and covers both non-terminal clients. Only `cli` sessions ever show `typed`
  (2,757), `system` (1,004), `suggestion_accepted` (259), or `queued` (18) — the split still
  held on the 2026-09-06 sweep, with no value crossing to a second entrypoint.
- `userType` — `external` for human-driven sessions.
- `promptSource` (`user` entries) — how the prompt arrived: `typed`, `sdk`, `system`,
  `queued`, or `suggestion_accepted`. Earlier revisions of this file said the field was
  absent for typed prompts and present only for SDK ones; that is wrong — `typed` is an
  explicit value, seen 2,757 times.
- `origin` (`user` entries) — `{"kind": …}`, one of `human` (3,451), `task-notification`
  (1,061), `coordinator` (73), `auto-continuation` (8), or `peer` (4). The last two are new
  since the previous sweep and rare enough that no purpose is inferable from the entries
  carrying them. Present only on prompt-bearing `user` entries, never on the ones carrying a
  `tool_result`. **A positive signal only**: in one recent session, 13 of 16 string-content
  `user` entries carried it, so its absence does not mean a prompt was injected and the
  marker heuristic below is still required.
- `attributionSkill` (assistant entries) — names the **inline** skill whose execution
  produced this main-chain turn (see Subagent stitching). Absent on turns not run under
  a skill.
- `attributionAgent` / `attributionMcpServer` / `attributionMcpTool` — the same idea for
  the other three things a turn can be run under: a subagent type, an MCP server, an MCP
  tool. They co-occur with `attributionSkill` rather than replacing it.
- `toolDenialKind` — on the `user` entry carrying a **denied** call's `tool_result`. One of
  `permission-rule` (a settings deny rule fired), `automode-blocked` (the auto-mode
  classifier refused), `automode-unavailable`, or `user-rejected` (the human said no). This
  is the only per-call permission signal in the log; `permission-mode` entries record the
  mode in effect and nothing about individual calls. Logs before 2.1.198 lack the field —
  there, a denial is a `tool_result` with `is_error` whose body starts `Permission to use`
  or reads `The user doesn't want to proceed with this tool use`. Never apply that free-text
  fallback to an `mcp__*` tool: a tool_result body is authored by the tool, so a hostile MCP
  server can emit the same sentence and manufacture a denial that never happened.
- `toolUseResult` — a top-level **structured** mirror of a tool's result, on the same
  `user` entry that carries the `tool_result` block. Shape varies by tool (e.g. `Edit`
  → `structuredPatch`/`userModified`; `Bash` → `stdout`/`stderr`; sometimes just a
  string). For `Agent` and forked-`Skill` calls it includes `agentId` — the stitch key
  the parser uses (see Subagent stitching). The result **body** still comes from the
  in-`message` `tool_result` block.
- `sourceToolAssistantUUID` / `sourceToolUseID` — link a synthetic `user` entry back to
  the assistant turn / tool call that generated it.
- `apiBlockIndex` (assistant entries, since 2.1.258) — which content block of one API
  response this entry carries. Claude Code writes one assistant entry per block, so a reply
  holding a thought and two tool calls becomes three entries that share a `requestId` and
  number 0, 1, 2; the counter restarts at 0 on the next request. It orders the blocks of a
  single reply and nothing wider — it is never a running count over the session. The binary
  (2.1.263) sets it from the streaming block's own index.

  **Every entry of one response repeats that response's whole `usage` object**, identically —
  input, output and both cache counters. Summing `usage` across assistant entries therefore
  multiplies a reply's tokens by how many blocks it held: measured over 22 local sessions the
  inflation ran 1.75x to 3.11x, averaging 2.34x, and it varies per turn, so it distorts a
  comparison between turns as well as a total. Group by `requestId` and take one entry per
  group. Verified across 277 multi-entry groups in two sessions: all four counters were
  identical within every group, so which entry you take does not matter. Group by `requestId`
  rather than by `apiBlockIndex`: the duplication is older than the index field, measurable
  back to 2.1.206, while `requestId` is on every assistant entry of the oldest local log
  (2.1.205). What `requestId` does not cover is the `<synthetic>` entries Claude Code composes
  itself, which carry none — those are one entry each and can be keyed by `uuid`.
- `turnCompanion` (`user` entries, always `true` when present) — the entry carries material
  the harness attached to the turn rather than anything a person typed: a loaded skill's
  body, a re-invocation notice for a skill loaded earlier, a note about an image's scaling, a
  nudge after a malformed tool call. **Claude Code's own test for "is this a user prompt"
  rejects it** — the binary (2.1.263) counts an entry only when `turnCompanion !== true` — so
  this is the authoritative form of the question the marker heuristic under
  [User entries](#user-entries-typed-vs-injected) answers by matching text. Seen on 358
  entries across 148 files since 2.1.236, every one also carrying `isMeta: true`.
- Queue fields on `user` entries: `queuePriority` (`later` in all 18 local entries),
  `queueSkipAttachments`, and `queueOrigin` — an object naming what enqueued the prompt,
  e.g. `{"kind":"task-notification","source":"artifact-watch-lifecycle","slug":…}`.
- `quotaLimits` (assistant entries, 17 occurrences) — the rate-limit state at a refusal:
  `status`, `rateLimitType` (`five_hour`), `resetsAt` (epoch seconds), `overageStatus`,
  `overageDisabledReason`, `isUsingOverage`, `unifiedRateLimitFallbackAvailable`. Every local
  occurrence rides a `<synthetic>` assistant entry — the "you've hit your limit" text Claude
  Code composes itself — so it annotates one of the `<synthetic>` entries described under
  [message](#message) rather than a real reply.
- `truncatedAfterOutput` (assistant entries, 1 occurrence) — too rare to characterize.
- `scheduledTaskId` / `scheduledFireId` (`user` entries) — the scheduled task and the
  individual firing that produced this prompt. They pair with the `scheduled_task_fire`
  `system` entry below.
- `system` entries carry `subtype` — `stop_hook_summary` (4,271), `turn_duration` (4,017),
  `away_summary` (984), `local_command` (156), `compact_boundary` (137), `informational`
  (23), `api_error` (10), `scheduled_task_fire` (10), `model_refusal_fallback` (1) — and
  sometimes `level` (`info`, `suggestion`). `stop_hook_summary` entries also carry
  `hasOutput`, `stopReason`, `preventedContinuation` and `toolUseID`; `turn_duration` carries
  `durationMs` and `messageCount`.

  `scheduled_task_fire` is a wake-up from a scheduled task or a `/loop`: `taskId`, `taskKind`
  and `cronKind` (both `loop` in every local entry), the `cron` expression, and the `prompt`
  being re-fired. Where the loop had nothing to report it also carries `noOpStreak`,
  `streakStartedAt`, and `foldedUuids` — the earlier quiet wake-ups this one folds together,
  so a reader counting entries sees one line where the session ran several.

  **`away_summary` stopped being written after 2.1.238** — 984 entries, none later than
  2026-08-22, in a corpus that runs to 2.1.263. Rarity explains a recent gap for the error
  subtypes (`api_error`, `model_refusal_fallback`); it does not explain this one.
- Hook metadata: `hookInfos`, `hookErrors`, `hookCount`, `hookAdditionalContext`.
- `slug` — a human-readable session handle (`bubbly-honking-blanket`), constant across a
  session's assistant entries. A second name for a session besides its UUID.
- `session_id` — a snake_case duplicate of `sessionId` on assistant entries, carrying the
  same value. Both appear on the same line; read `sessionId`.
- `requestId` / `promptId` / `messageId` — per-API-request, per-prompt and per-message ids.
  `promptId` groups every entry produced by one prompt, including the tool calls.
- `effort` — the reasoning-effort setting in force (seen from 2.1.212).
- `isMeta` — marks an entry injected by the harness rather than produced by the exchange.
- `compactMetadata` / `isCompactSummary` / `logicalParentUuid` /
  `isVisibleInTranscriptOnly` — compaction bookkeeping; `logicalParentUuid` re-links a chain
  across the boundary that `parentUuid` no longer spans.
- API-failure fields, all rare: `isApiErrorMessage`, `apiErrorStatus`, `error`,
  `maxRetries`, `retryAttempt`, `retryInMs`, `interruptedMessageId`, and a refusal family
  (`apiRefusalCategory`, `apiRefusalExplanation`, `refusedUserMessageUuid`,
  `originalModel`, `fallbackModel`, `retractedMessageUuids`, `direction`, `trigger`).
- Payload fields belonging to one entry type, listed so a survey does not report them as
  unexplained: `snapshot` / `isSnapshotUpdate` (`file-history-snapshot`), `backup` /
  `trackingPath` / `snapshotMessageId` (`file-history-delta`), `agentName`
  (`agent-name`), `agentSetting` (`agent-setting`), `lastPrompt` / `leafUuid`
  (`last-prompt`), `operation` / `content` / `reason` (`queue-operation`), `relocatedCwd`
  (`relocated`), `worktreeSession` (`worktree-state`), `prNumber` / `prUrl` /
  `prRepository` (`pr-link`), `frameUrl` / `path` / `title` / `artifactCount`
  (`frame-link`), `parentSessionId` / `parentLastUuid` / `contextLength`
  (`fork-context-ref`), `aiTitle` (`ai-title`), `customTitle` (`custom-title`),
  `permissionMode` (`permission-mode`), `mode` (`mode`), `atis` (`atis-latch`),
  `totalCostUSD` / `totalDuration` / `totalAPIDuration` /
  `totalAPIDurationWithoutRetries` / `totalToolDuration` / `totalLinesAdded` /
  `totalLinesRemoved` / `startTime` / `modelUsage` / `hasUnknownModelCost`
  (`cost-state`), and `v` / `artifacts` / `accountUuid` (the two artifact bookkeeping
  types).
- Fields observed but not yet explained, each rare enough that no purpose was inferable:
  `pendingBackgroundAgentCount`, `classifierMetaLines`, `sessionKind`, `source`, `mcpMeta`.
  They are listed so the next sweep reports them as known-unexplained rather than as new.

### Entry types

Twenty-three types across the 1,834-log sweep, and the binary names fifteen more that no
local log holds. **Only `assistant` and `user` carry renderable content**; ignore the rest —
but ignore by skipping what you do not recognize, never by matching a closed list, because
this list has grown at every re-verification.

Content: `assistant` (137,056 entries), `user` (81,701).

**The full roster is in the binary, grouped by what Claude Code does with each type.** At
2.1.263 it keys every type it can write to one of four retention classes, and a type the
table omits defaults to `accumulate`. `scripts/schema-scan.sh` re-reads this table on every
run and reports what it finds against the local logs, so the copy below is a snapshot to read
rather than the thing to re-derive by hand:

| Class | Meaning | Types |
|:--|:--|:--|
| `transcript` | the conversation itself, chained by `uuid` | `user`, `assistant`, `system`, `attachment` |
| `boundary-cleared` | dropped for everything before the last compaction boundary | `progress`, `file-history-snapshot`, `file-history-delta`, `last-prompt`, `continued-in`, `marble-origami-commit`, `marble-origami-snapshot`, `marble-origami-reset` |
| `accumulate` | every entry kept | `content-replacement`, `fork-context-ref`, `frame-link`, `artifact-comment-monitor` |
| `last-wins` | only the newest entry of the type matters | `summary`, `custom-title`, `ended-by-model`, `ai-title`, `tag`, `relocated`, `agent-name`, `agent-color`, `agent-setting`, `pr-link`, `artifact-autoreact-ledger`, `bridge-session`, `history-suppression`, `attribution-snapshot`, `mode`, `permission-mode`, `isolation-latch`, `atis-latch`, `worktree-state`, `cost-state`, `queue-operation`, `observer-ref` |

Fifteen of those 38 names never appear in a local log: `progress`, `continued-in`, the three
`marble-origami-*` types, `content-replacement`, `summary`, `ended-by-model`, `tag`,
`agent-color`, `bridge-session`, `history-suppression`, `attribution-snapshot`,
`isolation-latch`, and `observer-ref`. Take the roster as the set of names a reader can meet
and as Claude Code's own grouping of them — not as two other things it looks like:

- **The classes describe a rewrite Claude Code plans, not the file on disk.** The routine
  holding the table walks a log and builds a keep-or-drop plan per line, dropping transcript
  lines before the last compaction boundary and hoisting the surviving `last-wins` metadata
  onto the boundary line. No local log shows that happening: all 56 sessions carrying a
  compaction boundary still hold their pre-boundary lines, 239 to 2,671 of them. So a
  compacted session's history is still readable, as [Session continuation and
  forking](#session-continuation-and-forking) says.
- **`last-wins` is not a reading rule.** `pr-link` sits in that class, and one session can
  name several distinct pull requests — a reader keeping only the newest entry reports one of
  them and loses the rest. Deduplicate by the thing each entry names instead, as the
  `pr-link` and `frame-link` notes below do.

Session metadata. Each is a `sessionId` plus one or two fields of its own, and **none
carries a `timestamp` or a `version`** — nothing in the entry dates it, so attribute it to
the build and time of the surrounding lines.

- `ai-title`, `custom-title`, `agent-name` — three separate title sources; see below.
- `agent-setting` — the named agent definition the session runs as. Values match
  `attributionAgent` (`central-pr-builder`, …).
- `mode` — the session's mode. Only `normal` observed, in 5,038 entries.
- `permission-mode` — the permission mode in effect (`auto`, …). Records the mode and never
  a per-call outcome; for that read `toolDenialKind`.
- `last-prompt` — `lastPrompt` plus `leafUuid`, the anchor a resume attaches to.
- `atis-latch` — a `sessionId` plus `atis`, a printable-ASCII string (the binary at 2.1.263
  validates it against `/^[\x21-\x7e]*$/`) latched at the entry a branch is taken from. Its
  meaning is **unknown**: all 6,234 local entries across 187 files carry the empty string, so
  nothing here says what a non-empty one would mean. The binary writes it beside an
  `isolation-latch` entry, which carries a `side` and appears in no local log. Present since
  2.1.234 and in every recent session, so any survey meets it immediately — and it holds
  nothing a transcript reader can use.

Session events, each recording something the session did. Most carry a `timestamp`;
`relocated`, `worktree-state` and `fork-context-ref` do not, so they cannot be ordered
against the turns around them by their own content.

- `pr-link` — a pull request it opened: `prNumber`, `prUrl`, `prRepository`.
- `frame-link` — an artifact it published: local `path`, the `frameUrl` on claude.ai, and a
  `title`. `title` is optional — 21 of the 135 entries that name a URL omit it.

  **Two shapes share this type, and the newer one names no artifact at all.** The count-only
  shape is `{type, artifactCount, sessionId, timestamp}` — no path, no URL, no title — and it
  is now the common one: 478 of the 613 local entries, in 6 sessions, since 2.1.237. An
  earlier revision of this file said the other three fields were present on every entry
  observed; that is no longer true. A reader keyed on the URL drops these by construction,
  which is what agentry's "a record naming nothing is dropped" rule in `dedupeOutputs` does;
  a reader that treats the type's presence as an artifact reports hundreds of phantoms.

  **Both are re-recorded, not written once.** Claude Code re-emits the entry on later
  turns of the same session, so the entry count is not the thing count: on 2026-09-06,
  3,331 `pr-link` entries across 70 sessions named 146 session-and-pull-request pairs (135
  distinct pull requests), and 613 `frame-link` entries across 16 sessions named 25 pairs
  (13 distinct artifacts). The ratio has widened since the 2026-08-09 measurement — then
  963 `pr-link` entries named 67 pairs — so read the entries as a stream of restatements
  rather than a list, whatever the counts are on the day. Reading them without
  deduplication reports one pull request dozens of times.

  **Dedupe on the URL, not the local path.** Repeated `frame-link` entries for one
  artifact keep a constant `frameUrl` while `path` changes, observed where a page was
  republished from a file that had moved (scratchpad → repository). Keying on `path`
  splits one artifact in two; keying on `frameUrl` does not. Repeated entries can also
  disagree on `title` — one session carries entries with a title and, from an earlier
  session publishing the same `frameUrl`, entries without.

  **Neither type appears in a subagent sidecar.** Zero occurrences across 645 sidecars,
  and every entry's `sessionId` equals its own file's stem. These are recorded against the
  session as a whole, so reading only the main log loses nothing — unlike a token tally,
  which must open the sidecars.
- `relocated` — its working directory moved, to `relocatedCwd`. Observed only for a move
  into a worktree.
- `worktree-state` — it entered a git worktree. Nested `worktreeSession` carries
  `originalCwd`, `worktreePath`, `worktreeName`, `worktreeBranch`, `originalBranch`, and
  `originalHeadCommit`.
- `queue-operation` — a queued prompt moving: `operation` is `enqueue` (2,493), `dequeue`
  (1,681), `remove` (799), or `popAll` (1). A `remove` sometimes says why in `reason` —
  `absorbed_mid_turn` (174) where the turn already running swallowed the prompt,
  `delivered_to_agent` (35) where another agent took it.
- `attachment`, `file-history-snapshot`, `file-history-delta` — attachments, whole-file
  snapshots, and per-file deltas (`trackingPath`, `snapshotMessageId`, nested `backup`).
  `file-history-delta` is new since 2.1.211.

  Together these are Claude Code's own record of **which files a session
  modified**, independent of any tool's arguments — so they catch a file rewritten
  by a shell command, which reading `Edit`/`Write` inputs does not. A snapshot
  keys `snapshot.trackedFileBackups` by path and is cumulative, re-listing the
  whole tracked set (up to 59 paths observed) on each entry; a delta names one
  path. **Paths mix forms**: one inside the session's working directory is
  recorded relative to it, one outside is absolute — 13965 relative against 12258
  absolute across 118 sessions on 2026-08-08 — so anything grouping by path must
  resolve the relative ones against `cwd` first or it will hold two spellings of
  one file. The backup map has no inherent order, so a stable reading sorts within
  each snapshot. Present in only about 118 of 260 local sessions: absence is not
  evidence that nothing changed.

  **A tracked path is not always the path the tool wrote.** Resolving the relative
  form against the session's `cwd` puts a worktree file under the main checkout —
  a session editing `<repo>/.claude/worktrees/w/AGENTS.md` had it tracked as
  `AGENTS.md`, which resolves to `<repo>/AGENTS.md`. So the two records can name
  one edit twice, at two paths. Measured 2026-08-08: across every local session,
  exactly one tracked path had no matching `Edit`/`Write` target, and it was this
  case — meaning the tracked record adds almost nothing over reading tool
  arguments, and what it does add may be a path that never existed.
- `system` — carries `subtype`; the nine observed values are listed under Common
  top-level fields above.
- `cost-state` — the session's running totals, rewritten as it goes: `totalCostUSD`,
  `totalDuration`, `totalAPIDuration`, `totalAPIDurationWithoutRetries`,
  `totalToolDuration` (milliseconds), `totalLinesAdded`, `totalLinesRemoved`, `startTime`
  (epoch milliseconds), `hasUnknownModelCost`, and `modelUsage` keyed by model id — each
  model holding `inputTokens`, `outputTokens`, `cacheReadInputTokens`,
  `cacheCreationInputTokens`, `webSearchRequests` and `costUSD`. Since 2.1.241, in 42 local
  sessions. **It is cumulative, so read the last entry and never a sum of them** — the
  largest local value, 124.85 USD, is a session total and not one turn's cost. Whether it
  counts a subagent's tokens is **unverified**; agentry sums `usage` across the sidecars
  instead of reading this.

  `totalLinesAdded` / `totalLinesRemoved` are present on every local record, so their
  presence is the record's. Two things are settled about what they count. They **do** reach
  delegated work: a session whose main thread made no `Edit`/`Write` call and whose subagents
  made 29 recorded 22 lines added. They do **not** count a file written through the shell: a
  session with 115 shell writes, `cat > f <<EOF` heredocs included, and no edit call recorded
  zero both ways. Two thirds of local records read `0`/`0`, which is what a read-only session
  looks like rather than a gap.
- `artifact-comment-monitor` and `artifact-autoreact-ledger` — bookkeeping for artifacts the
  session watches, not for artifacts it published. The monitor names which are `armed` for
  comment replies, with a `writtenAtMs` and a `title`; the ledger records `savedAt`,
  `stampHighWater`, `everBaselined`, `everHadThreads`, `turnTimestamps` and `threads` per
  artifact. Both carry a schema version `v` and an `artifacts` object keyed by artifact id,
  and the ledger also carries `accountUuid`. For what a session actually published, read
  `frame-link`.
- `fork-context-ref` — appears only inside subagent sidecars; see Subagent stitching.

`progress` is real and simply not written here. It is still in the binary's own type table
at 2.1.263, in the `boundary-cleared` class, and still absent from all 1,834 local logs.
Earlier revisions of this file concluded it had been removed or was never real — the two
guesses a corpus sweep alone can produce, and reading the binary rules out both.

The last entry's type is not an end-of-session marker — it is whatever happened last.
There is no reliable in-file "session complete" signal.

`ai-title` entries carry a top-level `aiTitle` string — Claude Code's own one-line summary
of the session, with `sessionId` naming the session. **It is written once and then
re-recorded, never revised**: across the 163 local sessions holding 9,133 of these entries,
not one carries two different values, so the many copies restate a single string and the
last one says nothing the first did not. An earlier revision of this file called it
"rewritten as the session evolves", which the generator's own conditions rule out — it runs
at most once per session (below). It is not renderable content, but the session listing
(`agentry list`) uses the latest `aiTitle` as the session's title.

**It is alive on 2.1.263, and a named session never gets one.** No local session whose
newest build is 2.1.260 or above carries an `ai-title`, which reads like a removal until you
notice that every recent session here was launched with a name. Two probe sessions run on
2026-09-06 settle it: an unnamed session wrote `ai-title: "Pong"` after its single turn, and
the same prompt under `--name probe-named` wrote `custom-title` and `agent-name` and no
`ai-title` at all.

The generator's conditions are in the binary (2.1.263), and it runs only when every one of
them holds:

- **No title is set in any of the three senses** — no custom title, no already-generated
  title, and no agent name. So `--name` at launch suppresses generation from the start,
  rather than stopping later appends the way a mid-session rename does.
- **The feature is not switched off** by a setting.
- **The session was not resumed.** A resume marks the attempt as already made, so a resumed
  session never generates a title even when it has none.
- **The first prompt is ordinary text**, not a slash command.

It fires **once, right after the first turn** — a session does not have to grow long to earn
one. And it is a terminal-session feature: `ai-title` appears in no `sdk-cli` session (0 of
205 local) and no `claude-desktop` session (0 of 59), the desktop app keeping its title in
its own JSON file instead.

**To re-run the probe**, drive a real terminal session rather than `claude -p`, which
generates nothing because the generator lives in the terminal UI. Run `script -q /dev/null
claude` with the prompt piped in, and pace the input: the TUI reads the pty in raw mode, so
Enter is a carriage return and a bare newline only inserts a line break — send `\n` and the
prompt sits unsent in the input box while the probe times out.

`custom-title` entries carry a top-level `customTitle` string — the name you give a
session by renaming it in Claude Code. It overrides `aiTitle` in the listing (see the
title ladder in PRODUCT.md §`agentry list`), and once one is written Claude Code stops
appending fresh `ai-title` entries, so the latest `aiTitle` is frozen at its pre-rename
value. The latest non-empty `customTitle` wins. A second, non-obvious origin: running
`/clear NAME` appends a `custom-title` (value `NAME`) to the **previous** session's log —
the label is for the conversation being left, not the new one (see the `/clear` note
under [User entries](#user-entries-typed-vs-injected)).

`agent-name` entries carry a top-level `agentName` string — the name you set with `--name`
at launch or `/rename` in session, and the one Claude Code's statusline shows. It is a
**third** title source, and it is not the same mechanism as `custom-title`, though the two
now travel together: all 45 local sessions carrying an `agent-name` also carry a matching
`custom-title`, including a session named at launch with `--name` on 2.1.263, which wrote
both. An earlier revision of this file reported one session of twelve with an `agent-name`
and no `custom-title`, and warned that a title ladder reading only `customTitle` would fall
through to a generated `aiTitle` and show a name the user did not choose. That
counterexample is no longer in the corpus, so treat agentry's `agentName` rung as defensive
rather than as a case the logs currently exercise — and read the pairing as re-measured on
2026-09-06, not as a guarantee.

### message

`{ role, model, content, usage, ... }`.

`message.model` names the model an assistant entry came from, and two things about it are
easy to get wrong (measured 2026-08-08 over 250 local sessions):

- **`<synthetic>` is not a model.** Claude Code writes it on assistant entries it composed
  itself — `API Error: 529 Overloaded.`, `You've hit your session limit`, `Login expired`,
  `No response requested.` — 31 such entries across 17 sessions, every one of them carrying
  zero tokens in `usage`. It is never the last *entry* in a session, but it is often the
  last *distinct value first seen*, so a "last distinct wins" reading resolves 17 sessions
  to `<synthetic>` unless it is filtered out first.
- **A session's model changes more often than the entrypoint does.** 13 of 250 sessions
  carry more than one real model, against 2 of 251 for the entrypoint; one carries three.
  The values arrive in contiguous blocks and never interleave — checked on all 13 — so the
  last distinct value equals the model on the final assistant entry in every case, and the
  same last-wins-with-a-trail reading the entrypoint uses is correct here too. Reading the
  *first* value instead — which agentry did originally — misreports those sessions as still
  on a model they left.

`content` is **either** a JSON string **or** an array of typed blocks:

- `text` — `.text`
- `thinking` — `.thinking` (+ `.signature`)
- `tool_use` — `.id`, `.name`, `.input` (object). `.input` shape varies by tool;
  the fields used for a call's identity (`list --include tools`) are `.command`
  (Bash), `.skill` (Skill), and `.subagent_type` (Agent).

  An `Agent` call's `.input` carries `.description` and `.prompt` always, and
  `.subagent_type`, `.model`, `.run_in_background` optionally — measured over 543
  local calls: `.subagent_type` on 526, `.model` on 332, `.run_in_background` on
  110. **Both optional identity fields are genuinely optional, and their absence
  means something.** No `.model` means the subagent ran on the session's own
  model, so substituting the session model would report a choice the caller never
  made; no `.subagent_type` means the harness default (`general-purpose`), which
  agentry reports as absent rather than filling in, since the log does not say it.
  `.model` values are the short aliases the tool accepts (`sonnet` 285, `opus` 22,
  `haiku` 15, `fable` 10), never a full model id like the session-level
  `message.model`. **`.model` appears on `Agent` and no other tool** — checked
  across every `tool_use` block in `~/.claude/projects` on 2026-08-08.
- `tool_result` (inside `user` entries) — `.tool_use_id`, `.is_error`, `.content`
  (a string, or an array of `{type:"text", text}`)

  **A refused call is reported as an ordinary error.** `.is_error` is true whether
  the tool ran and failed or was never allowed to run; the distinguishing field is
  `toolDenialKind`, at the **top level of the `user` entry**, not inside the
  result block. Values measured 2026-08-08: `automode-blocked` 42,
  `permission-rule` 34, `automode-unavailable` 31, `user-rejected` 9. Of those,
  20/29/26/2 respectively are in main session files and the rest in subagent
  sidecars, so anything counting top-level calls only will legitimately see about
  two thirds of the total. `permission-mode` entries are **not** this signal —
  they record the mode in effect, never a per-call decision.
- `image` (94 occurrences), `document` (3) — pasted or attached media. Inline images are a
  stated non-goal, so a renderer skips these rather than failing on them.
- `fallback` (1) — not content at all but a model swap mid-turn, shaped
  `{"type":"fallback","from":{"model":…},"to":{"model":…}}`. It occupies a content slot, so
  a renderer that assumes every block has text must skip it by type.

### usage (assistant entries)

`input_tokens`, `output_tokens`, `cache_read_input_tokens`,
`cache_creation_input_tokens` (plus nested `cache_creation`, `iterations`,
`server_tool_use`, and metadata `service_tier`, `speed`, `inference_geo` — none needed
for token totals).

## User entries: typed vs injected

One case has an authoritative flag and needs no heuristic: the summary Claude Code writes at
a compaction boundary is a `user` entry with plain string content, no `isMeta`, and none of
the markers below — it would read as a typed prompt — but it carries
`isCompactSummary: true`. Read the flag rather than its opening sentence ("This session is
being continued from a previous conversation…"), which is Claude Code's wording and can
change; keep the sentence only as a fallback for logs written before the flag existed.

Three more structured fields answer part of the general question and none answers all of it,
so the marker heuristic below remains the working rule. `origin.kind == "human"` positively
marks a typed prompt, `promptSource` distinguishes `typed` / `sdk` / `queued` /
`suggestion_accepted` from `system`, and `turnCompanion: true` positively marks an injected
one — Claude Code's own prompt test (2.1.263) refuses to count an entry carrying it. But
`origin` is absent from a minority of string-content `user` entries, and `turnCompanion` is
written only for material attached to a turn, so neither absence proves anything. Use them
to confirm, not to decide.

A `user` entry's string content is a human-typed prompt **unless** it is
system-injected. Injected markers include `<local-command-caveat>`, `<bash-input>`,
`<bash-stdout>`, `<bash-stderr>`, `<local-command-stdout>`,
`Base directory for this skill:`, and `<task-notification>` (the harness's
background-task event/completion reports — a `user` entry wrapping `<task-id>`,
`<status>`, `<summary>`, `<output-file>`, not anything the human typed). Slash
commands appear as `<command-name>…</command-name>` / `<command-args>…</command-args>`.
The leading slash in `<command-name>` is **inconsistent**: built-ins carry it (`/clear`,
`/compact`, `/refine`), custom commands do not (`sonar`, `exa`, `agent-guidelines`). So
code that reconstructs the prompt must normalize — strip any leading slashes and add
exactly one — rather than blindly prefixing `/` (which doubles built-ins to `//clear`).
Trailing text typed on the same line as a command lands in `<command-args>`, so
`/clear improve the parser` records as name `/clear`, args `improve the parser` — not a
separate prompt. For `/clear NAME` specifically, that arg is the `/resume` label for the
prior conversation: Claude Code starts a new session, records the `/clear` command as one
of its first turns, and writes `NAME` back as a `custom-title` on the previous session
(see [Entry types](#entry-types)). So the args describe the session being left — which is
why the title ladder skips a `/clear` turn rather than titling the new session with it. Note: a command can also appear as **plain string content** (no
`<command-name>` wrapper, e.g. a literal `/commit push`), which renders verbatim with its
single slash. Array-of-`text` user content is also injected (e.g. skill bodies), not a
typed prompt; since 2.1.236 those entries also carry `turnCompanion: true`, which is the
signal to prefer wherever it is present.

## Subagent stitching

A `tool_use` that spawns a child session writes a sidecar; stitching maps the call to it.

- **`Agent` calls** always fork a sidecar. The id is `toolUseResult.agentId` on the
  result `user` entry (see Common top-level fields) → `agent-<id>.jsonl`. Pre-structured
  logs lack that field but carry an `agentId: <id>` line in the `tool_result` text; the
  parser prefers the structured field and falls back to the text line.
- **`Skill` calls run in one of two modes**, distinguished by the tool's result:
  - **Inline** — result text `Launching skill: <name>`. The skill runs in the **main
    chain**: its body is injected as a `user` entry carrying `Base directory for this
    skill: <path>`, and the assistant turns it produces are tagged
    `attributionSkill: <name>`. **No sidecar is written** — there is nothing to stitch.
  - **Forked** — result text `Skill "<name>" completed (forked execution).`. This writes
    a sidecar, and `toolUseResult.agentId` gives its id directly — the key the parser
    uses. The sidecar also names its skill in a `Base directory for this skill: <path>`
    line (base name = skill name); the parser falls back to matching by that name for
    pre-structured logs that lack `agentId`. Name-matching is ambiguous when the same
    skill forks more than once in a session, so the `agentId` is preferred.
- Because inline skills inject the `Base directory for this skill:` marker into the main
  chain, that marker now appears in **both** main-chain and sidecar files — sidecar
  skill-name detection must read only `agent-*.jsonl`, not the main log.
- Subagents nest recursively; a sidecar may itself contain `Agent`/`Skill` calls. Guard
  against reference cycles — see [implementation-gotchas.md](implementation-gotchas.md).
- A sidecar for a **context-inheriting** subagent (the `fork` agent type) opens with a
  `fork-context-ref` entry: `agentId`, `parentSessionId`, `parentLastUuid`, `contextLength`.
  It names the main session the child inherited from and the entry it branched at. Do not
  read it as a session-fork record — it is about a subagent, not about `--fork-session`
  (see Session continuation and forking).

## Session continuation and forking

A session can be a continuation of an earlier one. The split that matters is whether the
continuation **appends to the same file** or **opens a new file**:

- **Same file (same `sessionId`, lines appended):** `--continue`/`-c`,
  `--resume`/`-r`/`/resume`, `--from-pr`, and compaction (`/compact` and the automatic
  compaction when context fills, both marked by a `system` entry with
  `subtype: compact_boundary`). No new file appears; the prior content is the head of the
  same log.
- **New file (new `sessionId`):** `/clear` and `--fork-session`/`/branch`. These differ
  in whether prior history is carried into the new file:
  - **`/clear`** starts the new session with **empty** context — no prior lines are
    copied, so the new file shares no `uuid`s with the old one. The only cross-file link
    is the `custom-title` that `/clear NAME` writes back onto the **previous** file (see
    [Entry types](#entry-types)).
  - **`--fork-session` / `/branch`** copies the parent's linear message chain verbatim
    into `<new-uuid>.jsonl` (observed 2026-06-27 via `--fork-session`). Each copied line
    keeps its original `uuid` and `parentUuid`; new turns append after, chaining onto the
    last copied line. Every copied line's `sessionId` is **rewritten** to the new id, and
    meta lines (`ai-title`, `mode`) are regenerated fresh rather than copied.

A fork carries **no explicit back-reference**: no field on any entry in the forked file
names the session it came from, and by any single content field a fork looks like an
independent session (its own `sessionId`, its own freshly generated `aiTitle`). A
`parentSessionId` field does exist in the format, but not here — it appears on
`fork-context-ref` entries inside **subagent sidecars**, where it names the main session a
context-inheriting subagent branched from (see Subagent stitching). Grepping for the field
name finds those and not what you want. Two signals together recover the relationship
(observed 2026-06-27, still the case on 2026-08-07):

- **Family** — the fork copies the parent's chain verbatim, so both share the **root
  `uuid`** (the first entry's `uuid`, the conversation root). Sessions with the same root
  `uuid` are one fork family. This is the cheap key — one `uuid` per file. `/clear` does
  **not** copy history, so it gets a fresh root `uuid` and never joins a family.
- **Direction** — content cannot tell parent from fork: the copied prefix is identical,
  **including the first entry's `timestamp`**, so `Start` matches across the family. The
  distinguishing signal is **outside** the content — the **file's creation time**
  (`st_birthtime` on macOS). A fork is a new file written at fork time, so the
  earliest-born file in a family is the original. Birthtime survives the original being
  continued (append bumps mtime, not birthtime), which the uuid-subset test (parent's
  `uuid` set ⊆ fork's) does not. Off macOS, creation time isn't portably readable, so
  consumers fall back to mtime.

Treat fork files as distinct sessions with duplicated history, not as one continued log.
`internal/list` uses both signals to group a family and indent its forks.
