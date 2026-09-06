#!/usr/bin/env bash
# Survey the Claude Code session-log schema on this machine and diff it against
# what docs/session-format.md documents.
#
# For every schema element — top-level field, entry type, system subtype, content
# block type, entrypoint value — report how often it occurs, in how many files,
# the Claude Code version range that wrote it, when it was last seen, and whether
# the doc mentions it. That is the input to keeping docs/session-format.md
# current: the format is observed, not specified, so the only way to know it
# drifted is to re-measure.
#
# The logs answer what Claude Code wrote on this machine; they cannot answer what it
# can write. The installed binary carries its JavaScript bundle as plain text,
# including the table mapping every entry type it knows to a retention class, so the
# scan reads that table too and names the types no local log has produced. A type
# added upstream is visible there a release before any session here writes one.
#
# Usage: scripts/schema-scan.sh [--root DIR] [--doc FILE] [--kind KIND] [--new] [--binary FILE]
#   --root DIR     log root to scan          (default ~/.claude/projects)
#   --doc FILE     doc to diff against       (default docs/session-format.md)
#   --kind KIND    restrict to one of: key type subtype block entrypoint
#   --new          list only elements the doc never mentions
#   --binary FILE  read the entry-type roster from FILE instead of the `claude`
#                  on PATH; the roster block is skipped when neither is readable
#
# Exit codes: 0 scan completed · 2 the scan could not run (missing jq, no logs).
set -uo pipefail

root="$HOME/.claude/projects"
doc="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/docs/session-format.md"
kind_filter=""
new_only=0
binary=""
binary_given=0

while [ $# -gt 0 ]; do
	case "$1" in
	--root)
		root="$2"
		shift 2
		;;
	--doc)
		doc="$2"
		shift 2
		;;
	--kind)
		kind_filter="$2"
		shift 2
		;;
	--new)
		new_only=1
		shift
		;;
	--binary)
		binary="$2"
		binary_given=1
		shift 2
		;;
	*)
		echo "schema-scan.sh: unknown argument '$1'" >&2
		exit 2
		;;
	esac
done

[ "$binary_given" -eq 1 ] || binary=$(command -v claude 2>/dev/null || true)

command -v jq >/dev/null 2>&1 || {
	echo "schema-scan.sh: jq not on PATH" >&2
	exit 2
}
[ -d "$root" ] || {
	echo "schema-scan.sh: no log root at $root" >&2
	exit 2
}

tmp=$(mktemp -d) || exit 2
trap 'rm -rf "$tmp"' EXIT

# One jq program per file, emitting per-file aggregates rather than one row per
# occurrence — the merge below then sees thousands of rows instead of millions.
#
# Reading with -R/-n and `fromjson? // empty` skips malformed lines instead of
# aborting the file at the first one. The logs do carry them, and a plain `jq .`
# stops dead at the first, silently dropping every later line from the survey;
# the `malformed-lines` counter below is what makes that visible instead of assumed.
#
# Version-less lines (ai-title, mode, agent-name, …) are attributed to the
# highest version the same file carries: one build wrote the meta line and the
# turns around it, so the file dates what the line does not.
read -r -d '' program <<'JQ'
def semver:
  if . == null or . == "" then -1
  else (split(".") | map(tonumber? // 0) | ((.[0] // 0) * 1000000 + (.[1] // 0) * 1000 + (.[2] // 0)))
  end;

[inputs] as $raw
| ($raw | map(fromjson? // empty)) as $lines
| ($lines | map(.version) | map(select(type == "string")) | sort_by(semver) | last) as $fv
| def elements:
    ( {kind: "scan", value: "malformed-lines", ver: ($fv // ""), ts: "",
       weight: (($raw | length) - ($lines | length))}
    , {kind: "build", value: ($fv // "unversioned"), ver: ($fv // ""), ts: "", weight: 1}
    , ( $lines[] as $e
        | ($e.version // $fv // "") as $v
        | ($e.timestamp // "") as $t
        | ( ($e | keys_unsorted[] | {kind: "key", value: .})
          , {kind: "type", value: ($e.type // "(absent)")}
          , (if $e.type == "system" then {kind: "subtype", value: ($e.subtype // "(absent)")} else empty end)
          , (if ($e.entrypoint | type) == "string" then {kind: "entrypoint", value: $e.entrypoint} else empty end)
          , (if ($e.message | type) == "object" and ($e.message.content | type) == "array"
             then ($e.message.content[] | {kind: "block", value: (.type // "(absent)")})
             else empty end)
          )
        | . + {ver: $v, ts: $t, weight: 1}
      )
    );
  reduce elements as $r ({};
    .[$r.kind][$r.value] = (
      (.[$r.kind][$r.value] // {n: 0, vmin: "", vmax: "", tmin: "", tmax: ""})
      | .n += $r.weight
      | (if $r.ver != "" and (.vmin == "" or ($r.ver | semver) < (.vmin | semver)) then .vmin = $r.ver else . end)
      | (if $r.ver != "" and (.vmax == "" or ($r.ver | semver) > (.vmax | semver)) then .vmax = $r.ver else . end)
      | (if $r.ts != "" and (.tmin == "" or $r.ts < .tmin) then .tmin = $r.ts else . end)
      | (if $r.ts != "" and (.tmax == "" or $r.ts > .tmax) then .tmax = $r.ts else . end)
    ))
| to_entries[] as $kind
| $kind.value | to_entries[] as $val
| [$kind.key, $val.key, $val.value.n, $val.value.vmin, $val.value.vmax, $val.value.tmin, $val.value.tmax]
| @tsv
JQ

files=0
while IFS= read -r -d '' f; do
	files=$((files + 1))
	jq -R -n -r "$program" <"$f"
done < <(find "$root" -name '*.jsonl' -print0) >"$tmp/rows.tsv" 2>"$tmp/jq.err"

[ -s "$tmp/rows.tsv" ] || {
	echo "schema-scan.sh: no session logs under $root" >&2
	exit 2
}
[ -s "$tmp/jq.err" ] && echo "schema-scan.sh: jq reported errors on $(wc -l <"$tmp/jq.err" | tr -d ' ') line(s); see below" >&2

# Documented vocabulary: every backticked token in the doc. Deriving it from the
# doc itself rather than from a hand-kept list is the point — a second list would
# have to be edited in lockstep with the prose, and silently would not be.
if [ -f "$doc" ]; then
	grep -o '`[^`]*`' "$doc" | tr -d '`' | sort -u >"$tmp/documented.txt"
else
	: >"$tmp/documented.txt"
	echo "schema-scan.sh: no doc at $doc — every element will report as NEW" >&2
fi

# Retention classes the binary assigns each entry type it knows, as name<TAB>class.
# The table is one object literal whose first member is `user:"transcript"` and which
# holds no nested braces, so matching from that anchor to the first `}` takes the whole
# of it and nothing more. A bounded repetition (`.{0,8000}`) reads more obviously and
# is not portable: POSIX caps a repetition count at 255 and BSD grep enforces it.
#
# Every path that cannot produce a roster says so on stderr. A block that is silently
# absent is indistinguishable from a build that knows no types beyond what these logs
# already hold, which is the reading that would let an upstream addition pass unseen.
roster_build=""
: >"$tmp/roster.tsv"
if [ -n "$binary" ] && [ -r "$binary" ]; then
	table_text=$(grep -aoE '\{user:"transcript"[^}]*\}' "$binary" | head -1)
	if [ -n "$table_text" ]; then
		printf '%s\n' "$table_text" | tr -d '{}"' | tr ',' '\n' | tr ':' '\t' | sort -u >"$tmp/roster.tsv"
		[ -x "$binary" ] && roster_build=$("$binary" --version 2>/dev/null | awk '{print $1}')
		[ -n "$roster_build" ] || roster_build="$binary"
	elif grep -aq 'user:"transcript"' "$binary"; then
		echo "schema-scan.sh: $binary carries the roster anchor in a shape this cannot read; no roster block" >&2
	else
		echo "schema-scan.sh: no entry-type roster in $binary; no roster block" >&2
	fi
elif [ -n "$binary" ]; then
	echo "schema-scan.sh: cannot read $binary; no roster block" >&2
else
	echo "schema-scan.sh: no claude on PATH and no --binary given; no roster block" >&2
fi

fmt='%-10s %-28s %8s %6s  %-17s  %-12s %-15s %s\n'

# awk splits on an explicit tab, which — unlike `read` with IFS=$'\t' — does not
# fold a run of separators, so an absent version or timestamp keeps its column.
awk -F'\t' \
	-v kind_filter="$kind_filter" \
	-v new_only="$new_only" \
	-v files="$files" \
	-v roster_build="$roster_build" \
	-v trailer="$tmp/trailer.txt" '
function semver(v,  a) {
  if (v == "") return -1
  split(v, a, ".")
  return a[1] * 1000000 + a[2] * 1000 + a[3]
}
FILENAME == ARGV[1] { rosterclass[$1] = $2; next }
FILENAME == ARGV[2] { documented[$0] = 1; next }
{
  k = $1 SUBSEP $2
  n[k] += $3
  nfiles[k] += 1
  if ($4 != "" && (!(k in vmin) || semver($4) < semver(vmin[k]))) vmin[k] = $4
  if ($5 != "" && (!(k in vmax) || semver($5) > semver(vmax[k]))) vmax[k] = $5
  if ($6 != "" && (!(k in tmin) || $6 < tmin[k])) tmin[k] = $6
  if ($7 != "" && (!(k in tmax) || $7 > tmax[k])) tmax[k] = $7
  if (semver($5) > semver(corpusmax)) corpusmax = $5
  kind[k] = $1
  value[k] = $2
  seen[$2] = 1
  if ($1 == "type") seentype[$2] = 1
}
END {
  for (k in n) {
    if (kind_filter != "" && kind[k] != kind_filter) continue
    if (n[k] == 0) continue
    # build and scan rows describe the survey, not the format, so a doc verdict on
    # them is meaningless — and would drag every build number into --new output.
    d = (kind[k] == "build" || kind[k] == "scan") ? "-" \
        : ((value[k] in documented) ? "documented" : "NEW")
    if (new_only && d != "NEW") continue
    vr = (vmin[k] == "") ? "-" : (vmin[k] == vmax[k] ? vmin[k] : vmin[k] "-" vmax[k])
    st = (vmax[k] == "") ? "undated" : (vmax[k] == corpusmax ? "current" : "last@" vmax[k])
    ls = (tmax[k] == "") ? "-" : substr(tmax[k], 1, 10)
    printf "%-10s %-28s %8d %6d  %-17s  %-12s %-15s %s\n", kind[k], value[k], n[k], nfiles[k], vr, ls, st, d
  }
  printf "\n%d files scanned - newest build in corpus %s - the build kind counts files per build, which is what calibrates a last@ status: an element absent from a build that wrote few files may be rare rather than removed\n", files, corpusmax > trailer
  gone = ""
  for (t in documented)
    if (t ~ /^[A-Za-z_][A-Za-z0-9_-]*$/ && !(t in seen)) gone = gone " " t
  if (gone != "")
    printf "doc names, scan never saw (identifier-shaped tokens only, expect unrelated prose):%s\n", gone > trailer
  if (roster_build != "") {
    unseen = ""; undoc = ""; nroster = 0; nunseen = 0
    for (t in rosterclass) {
      nroster++
      if (t in seentype) continue
      nunseen++
      unseen = unseen " " t "(" rosterclass[t] ")"
      if (!(t in documented)) undoc = undoc " " t
    }
    if (!new_only)
      printf "binary roster %s - %d entry types, %d written by no local log:%s\n", \
        roster_build, nroster, nunseen, (nunseen == 0 ? " none" : unseen) > trailer
    if (undoc != "")
      printf "binary roster - written by no local log and named by no doc:%s\n", undoc > trailer
    untabled = ""
    for (k in n)
      if (kind[k] == "type" && !(value[k] in rosterclass)) untabled = untabled " " value[k]
    if (untabled != "")
      printf "binary roster - in these logs but absent from the table, which defaults them to accumulate:%s\n", untabled > trailer
  }
}
' "$tmp/roster.tsv" "$tmp/documented.txt" "$tmp/rows.tsv" >"$tmp/table.txt"

# shellcheck disable=SC2059  # fmt is a fixed format string, not user input
printf "$fmt" KIND VALUE COUNT FILES VERSIONS "LAST SEEN" STATUS DOC
sort -k1,1 -k3,3rn "$tmp/table.txt"
cat "$tmp/trailer.txt"
[ -s "$tmp/jq.err" ] && head -5 "$tmp/jq.err" >&2
exit 0
