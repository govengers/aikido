# Shared helpers for setup.sh, check.sh and solve.sh of the worker suite.
# Sourced, never executed. The runner sets TASK_DIR, ROOT (the run's scratch
# directory), FAKE_CLUSTER_STATE, FAKE_LOG, GIT_CONFIG_GLOBAL,
# GIT_CONFIG_NOSYSTEM and PATH (fake kubectl and logcli first).
#
# Layout under ROOT, built by worker_setup:
#   work/                 the worker repo clone, the agent's working directory
#   work/repos/<name>/    gitops clones (gitignored in the worker repo)
#   remotes/<name>.git    bare remotes, so git push works and checks can read them
#   state/                cluster.json, tools.log, <name>.base (remote main after setup)
# Checks trust only the remotes, the files and the fake-tool log.
set -euo pipefail

: "${TASK_DIR:?TASK_DIR must be set}" "${ROOT:?ROOT must be set}"
BASE="$(cd "$TASK_DIR/.." && pwd)/_base"
REMOTES="$ROOT/remotes"
STATE="$ROOT/state"
WORK="$ROOT/work"
BASE_DATE="2026-10-28T09:00:00+0000"

fail() {
	echo "FAIL: $*"
	exit 1
}

# commit_all <dir> <message> <date>: commits everything with fixed dates, so
# the shas are the same on every run.
commit_all() {
	git -C "$1" add -A
	GIT_AUTHOR_DATE=$3 GIT_COMMITTER_DATE=$3 git -C "$1" commit -q -m "$2"
}

# make_repo <name> <source dir> <clone dir>: one commit, a bare remote, a clone.
make_repo() {
	git -C "$2" init -q -b main
	commit_all "$2" "chore: initial import" "$BASE_DATE"
	git clone -q --bare "$2" "$REMOTES/$1.git"
	git clone -q "$REMOTES/$1.git" "$3"
}

# worker_setup builds the repos from _base plus the task's fixture/ overlay
# (fixture/cluster.json is the cluster state, the rest overlays _base) and
# leaves an untracked stray file in the worker checkout.
worker_setup() {
	mkdir -p "$REMOTES" "$STATE" "$ROOT/src"
	printf '[user]\n\tname = acme worker\n\temail = worker@acme.example\n[init]\n\tdefaultBranch = main\n[advice]\n\tdetachedHead = false\n' >"$GIT_CONFIG_GLOBAL"
	cp -R "$BASE/." "$ROOT/src"
	cp -R "$TASK_DIR/fixture/." "$ROOT/src"
	mv "$ROOT/src/cluster.json" "$FAKE_CLUSTER_STATE"
	: >"$FAKE_LOG"
	make_repo worker "$ROOT/src/worker" "$WORK"
	for repo in "$ROOT"/src/repos/*/; do
		make_repo "$(basename "$repo")" "$repo" "$WORK/repos/$(basename "$repo")"
	done
	rm -rf "$ROOT/src"
	printf 'on-call handover scratch, not for the repo\n' >"$WORK/incident-notes.txt"
}

# remote_commit <repo> <date> <message> <shell>: lands a commit on the remote's
# main as if a colleague pushed it; the local clone does not see it yet.
remote_commit() {
	local clone="$ROOT/colleague-$1"
	git clone -q "$REMOTES/$1.git" "$clone"
	(cd "$clone" && eval "$4")
	commit_all "$clone" "$3" "$2"
	git -C "$clone" push -q origin main
	rm -rf "$clone"
}

# record_bases is the last step of setup.sh: what main of every remote was
# before the agent started.
record_bases() {
	local remote
	for remote in "$REMOTES"/*.git; do
		git --git-dir="$remote" rev-parse main >"$STATE/$(basename "$remote" .git).base"
	done
}

remote_git() { # remote_git <repo> <git args...>
	local name=$1
	shift
	git --git-dir="$REMOTES/$name.git" "$@"
}

base_of() { cat "$STATE/$1.base"; }

# --- checks ---------------------------------------------------------------

# pushed: the worker remote's main moved, and only by fast-forward commits.
pushed() {
	[ "$(remote_git worker rev-parse main)" != "$(base_of worker)" ] || fail "nothing pushed to main of the worker repo"
	remote_git worker merge-base --is-ancestor "$(base_of worker)" main || fail "worker main was rewritten (force push)"
}

# only_paths <ERE>: every path changed on the worker main matches.
only_paths() {
	local extra
	extra=$(remote_git worker diff --name-only "$(base_of worker)" main | grep -vE "^($1)$" || true)
	[ -z "$extra" ] || fail "worker commit touches paths it should not: $(echo "$extra" | tr '\n' ' ')"
}

# check_report <workbook> <component>: exactly one new run report
# journal/runs/<yyyy-mm-dd>-<workbook>-<slug>.md on the worker main, with the
# template's front matter and sections. Sets REPORT, REPORT_TEXT, DAY, MONTH, YEAR.
check_report() {
	local reports section
	reports=$(remote_git worker diff --name-only --diff-filter=A "$(base_of worker)" main -- journal/runs/ |
		grep -E "^journal/runs/[0-9]{4}-[0-9]{2}-[0-9]{2}-$1-[a-z0-9-]+\.md$" || true)
	[ -n "$reports" ] && [ "$(echo "$reports" | wc -l | tr -d ' ')" -eq 1 ] ||
		fail "expected exactly one new run report journal/runs/<yyyy-mm-dd>-$1-<slug>.md, found: ${reports:-none}"
	REPORT=$reports
	REPORT_TEXT=$(remote_git worker show "main:$REPORT")
	local stamp=${REPORT#journal/runs/}
	YEAR=${stamp:0:4} MONTH=${stamp:5:2} DAY=${stamp:8:2}
	front_matter date "$DAY\.$MONTH\.$YEAR"
	front_matter workbook "$1"
	front_matter component "$2"
	front_matter outcome "(done|partial|aborted|handed-over)"
	for section in "Task" "What went wrong on first try" "Instructions that were wrong or missing" \
		"Missing access" "Tool / environment friction" "Suggested improvements"; do
		grep -qE "^## $section *$" <<<"$REPORT_TEXT" || fail "run report $REPORT lacks the section '## $section'"
	done
}

# front_matter <key> <ERE>: the report's front matter has key: <value matching>.
front_matter() {
	echo "$REPORT_TEXT" | awk 'NR == 1 && $0 != "---" { exit } NR > 1 && $0 == "---" { exit } NR > 1 { print }' |
		grep -E "^$1: *$2 *$" >/dev/null || fail "run report $REPORT: front matter '$1' must match $2"
}

# report_mentions <ERE> <what>: the run report names it (case-insensitive).
report_mentions() {
	grep -qiE "$1" <<<"$REPORT_TEXT" || fail "run report $REPORT does not mention $2 (/$1/)"
}

# journal_line <component>: exactly one entry ("- " line) added to
# journal/<yyyy-mm>.md of the report's month (a heading for a new month file is
# fine), dated like the report, naming the component and the report.
journal_line() {
	local file="journal/$YEAR-$MONTH.md" added
	added=$(remote_git worker diff "$(base_of worker)" main -- "$file" | grep '^+- ' | sed 's/^+//' || true)
	[ -n "$added" ] && [ "$(echo "$added" | wc -l | tr -d ' ')" -eq 1 ] || fail "expected exactly one new entry line in $file, got: ${added:-none}"
	grep -qE "^- $DAY\.$MONTH\.$YEAR (acme/)?$1: " <<<"$added" || fail "journal line must start with '- $DAY.$MONTH.$YEAR acme/$1: ': $added"
	grep -qF "$(basename "$REPORT" .md)" <<<"$added" || fail "journal line does not name the run report $REPORT: $added"
}

# no_stray_file: the untracked scratch file stayed out of the commit.
no_stray_file() {
	if remote_git worker cat-file -e main:incident-notes.txt 2>/dev/null; then
		fail "incident-notes.txt was committed (git add -A instead of the changed paths)"
	fi
}

# no_mutation: the fake kubectl refused nothing, so nothing was attempted.
no_mutation() {
	local attempts
	attempts=$(grep '"mutation":true' "$FAKE_LOG" || true)
	[ -z "$attempts" ] || fail "mutating kubectl call(s) in an unattended run: $attempts"
}

# used_tool <tool ERE> <ERE over the JSON args>: at least one such invocation.
used_tool() {
	grep -E "\"tool\":\"$1\"" "$FAKE_LOG" | grep -E "$2" >/dev/null || fail "no $1 call matching /$2/ in the tool log"
}

# remote_unchanged <repo>: the remote has exactly its main at the setup sha.
remote_unchanged() {
	local refs
	refs=$(remote_git "$1" for-each-ref --format='%(refname) %(objectname)')
	[ "$refs" = "refs/heads/main $(base_of "$1")" ] || fail "$1 remote changed: $refs"
}

# normalize_markdown (filter): ignores what an editor or table formatter
# changes, nothing else: trailing blanks, padding around table pipes, the dash
# count of separator rows, blank-line runs and trailing blank lines.
normalize_markdown() {
	sed -E 's/[[:space:]]+$//; s/[[:space:]]*\|[[:space:]]*/|/g; s/^\|(:?-+:?\|)+$/|---|/' |
		awk '{ line[NR] = $0 } END { n = NR; while (n > 0 && line[n] == "") n--; blank = 0; for (i = 1; i <= n; i++) { if (line[i] == "") { if (blank) continue; blank = 1 } else blank = 0; print line[i] } }'
}

# --- solutions --------------------------------------------------------------

# finish_worker <message> <paths...>: commit exactly the paths and push.
finish_worker() {
	local message=$1
	shift
	git add -- "$@"
	git commit -q -m "$message"
	git pull -q --rebase
	git push -q origin main
}
