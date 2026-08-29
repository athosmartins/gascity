#!/bin/sh
# gc dolt health — Lightweight Dolt data-plane health report.
#
# Checks server status and latency, per-database commit counts and open
# beads, backup freshness, orphan databases, and zombie Dolt processes.
#
# Environment: GC_CITY_PATH, GC_DOLT_PORT, GC_DOLT_HOST, GC_DOLT_USER,
#              GC_DOLT_PASSWORD
set -e

: "${GC_DOLT_USER:=root}"
PACK_DIR="${GC_PACK_DIR:-$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)}"
. "$PACK_DIR/assets/scripts/runtime.sh"

# rig_discovery_degraded is set to true by metadata_files() whenever it
# could not positively confirm the full external rig set (gc missing, the
# bounded `gc rig list` call failed/timed out, or -- lacking jq to verify
# the response shape -- an empty parse that can't be told apart from a
# failure). ga-eu2x: an incomplete scan here used to be silently
# indistinguishable from "confirmed no other rigs" -- every database not
# in the resulting (HQ-only) referenced set then got reported by the
# orphan check below as a CONFIRMED orphan, including live production
# databases whose only "crime" is living in a rig outside
# $GC_CITY_PATH/rigs (true for every externally-cloned rig in this city:
# `gc rig list` timing out under Dolt load -- 8-17s measured against a 5s
# bound -- silently dropped whatsapp_automation/gastown/dc/lexbh/
# marketing/property_scrapers to "orphan" in the same report a human or
# agent might act on with `gc dolt cleanup --force`). Consumers of the
# orphan report MUST treat a degraded scan as "unknown", never as
# "confirmed absent" -- see the "erro e vazio não podem produzir o mesmo
# valor" rule this bug is the canonical example of.
rig_discovery_degraded=false

metadata_files() {
  printf '%s\n' "$GC_CITY_PATH/.beads/metadata.json"

  if command -v gc >/dev/null 2>&1; then
    # Bound the gc rig list call: if gc is itself in a bad state (the
    # failure mode this patrol is meant to detect) we must not block
    # here. Degrade to the fallback rig scan below.
    if rig_list_json=$(run_bounded 5 gc rig list --json 2>/dev/null); then
      if command -v jq >/dev/null 2>&1; then
        rig_paths=$(printf '%s' "$rig_list_json" | jq -r '.rigs[].path' 2>/dev/null) || true
      else
        rig_paths=$(printf '%s' "$rig_list_json" | grep '"path"' | sed 's/.*"path": *"//;s/".*//') || true
      fi
      if [ -n "$rig_paths" ]; then
        printf '%s\n' "$rig_paths" | while IFS= read -r p; do
          [ -n "$p" ] && printf '%s\n' "$p/.beads/metadata.json"
        done
        return
      fi
      # `gc rig list` ran and exited 0 but produced no paths. With jq
      # available to have confirmed the response actually parsed as
      # `.rigs`, an empty result here is a genuine positive "zero external
      # rigs" and is trusted as such (falls through to the harmless local
      # fallback below with no degraded flag). Without jq we cannot tell
      # that apart from "the output didn't look like what grep expected" --
      # degrade only in that case.
      if ! command -v jq >/dev/null 2>&1; then
        rig_discovery_degraded=true
      fi
    else
      # run_bounded killed it (timeout) or `gc rig list` itself exited
      # non-zero. An empty rig_paths derived from this must NEVER be read
      # as "confirmed zero rigs".
      rig_discovery_degraded=true
    fi
  else
    rig_discovery_degraded=true
  fi

  # Fallback: scan local rigs/ directory only. Cannot discover external rigs
  # when gc is unavailable/degraded — acceptable degradation, but (per the
  # flag set above) never presented as a complete rig set.
  find "$GC_CITY_PATH/rigs" -path '*/.beads/metadata.json' 2>/dev/null || true
}

metadata_db() {
  meta="$1"
  if command -v jq >/dev/null 2>&1; then
    jq -r '.dolt_database // empty' "$meta" 2>/dev/null || true
    return
  fi
  grep -o '"dolt_database"[[:space:]]*:[[:space:]]*"[^"]*"' "$meta" 2>/dev/null | sed 's/.*: *"//;s/"$//' || true
}

json_output=false
data_dir="$DOLT_DATA_DIR"

while [ $# -gt 0 ]; do
  case "$1" in
    --json) json_output=true; shift ;;
    -h|--help)
      echo "Usage: gc dolt health [--json]"
      echo ""
      echo "Lightweight Dolt data-plane health report for patrol cycles."
      echo ""
      echo "Flags:"
      echo "  --json    Output as JSON (consumed by deacon patrol)"
      exit 0
      ;;
    *) echo "gc dolt health: unknown flag: $1" >&2; exit 1 ;;
  esac
done

# Note: run_bounded / TIMEOUT_BIN are provided by assets/scripts/runtime.sh.

# Determine host for probing.
host="${GC_DOLT_HOST:-127.0.0.1}"

# Check if server is running.
server_running=false
server_pid=0
server_latency=0
server_reachable=false

# Portable millisecond timestamp. BSD date(1) on macOS treats %N as a
# literal 'N' (exits 0, output like "1776740122N"), so the GNU-only
# || fallback never triggers. Feature-test the output instead.
now_ms() {
  _raw=$(date +%s%N 2>/dev/null)
  case "$_raw" in
    ''|*[!0-9]*) printf '%s000' "$(date +%s 2>/dev/null)" ;;
    *)        printf '%s' "$_raw" | cut -c1-13 ;;
  esac
}

# Always export DOLT_CLI_PASSWORD (even empty) so no dolt invocation in this
# script ever prompts for a password on stdin. Without this, any `dolt
# --user ...` call silently fails with "Failed to parse credentials:
# operation not supported by device" on sessions without a controlling TTY.
# ga-soxi9 gate-fix-1 (gate_run=ga-g3hay): this used to live INSIDE the
# server_running branch below, which the SQL probe (--user via $conn_args)
# never reached when the server was down anyway — but the per-database
# backup-freshness loop further down deliberately runs regardless of
# server_reachable (backups are on-disk and worth checking even when the
# server is down), and ALSO passes --user. Scoping the export to
# server_running meant every "dolt --user ... backup -v" call would hit the
# same interactive-prompt failure precisely when the server is down —
# reproduced live: 100% of databases reported query-failed in that state,
# even ones with a perfectly good, freshly-synced backup, defeating the
# whole point of checking backups independent of server reachability.
# Exporting unconditionally costs nothing for the server_running path
# (identical value, just set slightly earlier) and is safe file-wide: every
# `dolt` invocation in this script already passes --user (verified by
# inspection — the two conn_args-based SQL calls and this backup call are
# the only three).
export DOLT_CLI_PASSWORD="${GC_DOLT_PASSWORD:-}"

# Find dolt PID by port.
pid=$(managed_runtime_listener_pid "$GC_DOLT_PORT" || true)
if [ -n "$pid" ] || managed_runtime_tcp_reachable "$GC_DOLT_PORT"; then
  server_running=true
  [ -n "$pid" ] && server_pid="$pid"
  # Measure query latency.
  start_ms=$(now_ms)
  conn_args="--host $host --port $GC_DOLT_PORT --user $GC_DOLT_USER --no-tls"
  # Bound the ping. A TCP-reachable but unresponsive server (stuck
  # goroutine, saturated pool, migration lock) would otherwise hang.
  if run_bounded 5 dolt $conn_args sql -q "SELECT 1" >/dev/null 2>&1; then
    server_reachable=true
    end_ms=$(now_ms)
    server_latency=$((end_ms - start_ms))
    [ "$server_latency" -lt 0 ] && server_latency=0
  fi
fi

# Cache metadata file paths once (avoids repeated gc calls and word-splitting).
_meta_cache=$(mktemp)
# Scratch file for the zombie scan's matched-server filter. The foreign-managed
# decision runs in a `... | while read` subshell (so $zombie_count can't be
# mutated through the pipe); the survivors are spooled here and read back in
# the parent shell.
_zombie_scan_out=$(mktemp)
# Scratch file for one database's `dolt backup -v` output at a time (ga-soxi9
# backup-freshness loop below). Same reason as _zombie_scan_out: a `... | while
# read` would run the loop body in a subshell, losing every variable it sets
# once the pipe closes. Redirecting from a file instead of piping into the
# loop keeps it in the parent shell, where the mutations need to land.
_backup_remotes_tmp=$(mktemp)
metadata_files > "$_meta_cache"
trap 'rm -f "$_meta_cache" "$_zombie_scan_out" "$_backup_remotes_tmp"' EXIT

# Collect database info.
#
# NOTE: we must NOT invoke `dolt log` against the on-disk database
# directory while the sql-server holds it open. Historically this was
# done with `cd "$d" && dolt log --oneline | wc -l`; on an active DB
# the client contends with the server for Dolt's file locks and the
# client process blocks indefinitely, orphaning zombie `dolt log`
# processes and wedging the health CLI. Query the running server via
# SQL instead — it's the authoritative source, never deadlocks with
# itself, and is cheap (dolt_log is indexed by commit hash).
db_info=""
if [ -d "$data_dir" ] && [ "$server_reachable" = true ]; then
  for d in "$data_dir"/*/; do
    [ ! -d "$d/.dolt" ] && continue
    name="$(basename "$d")"
    case "$(printf '%s' "$name" | tr '[:upper:]' '[:lower:]')" in information_schema|mysql|dolt_cluster|performance_schema|sys|__gc_probe) continue ;; esac
    # Reject names with anything outside [A-Za-z0-9_-] before interpolating
    # into the SQL identifier. The first byte must still be alnum/underscore
    # to avoid option-shaped names. Dolt permits directory names that shell
    # basename happily returns (e.g. backticks, semicolons) but which
    # would break out of the identifier and execute attacker-chosen SQL
    # as the patrol user. Not an external-attack surface today — data
    # directories are server-controlled — but fragile enough under
    # config drift that it's worth skipping rather than probing.
    case "$name" in
      [A-Za-z0-9_]*)
        case "$name" in *[!A-Za-z0-9_-]*) continue ;; esac
        ;;
      *) continue ;;
    esac
    # Count commits via SQL (bounded). 0 on timeout or error — keep
    # going rather than hang the whole report. Extract the first
    # fully-numeric line rather than `sed -n '2p'`: future dolt builds
    # may emit a status row for `USE` or a warning banner, in which
    # case positional parsing silently collapses the count to 0 and the
    # "empty repo" fallback masks the parse miss. Numeric-line grep
    # gives a deterministic result or clearly-failed parse.
    commits_csv=$(run_bounded 5 dolt $conn_args sql --result-format csv \
      -q "USE \`$name\`; SELECT COUNT(*) FROM dolt_log;" 2>/dev/null || true)
    commits=$(printf '%s\n' "$commits_csv" | grep -E '^[0-9]+$' | head -1)
    # JSON consumers (deacon patrol) require a number; use 0 on failure.
    case "$commits" in
      ''|*[!0-9]*) commits=0 ;;
    esac
    # Count open beads (best-effort).
    open_beads=0
    while IFS= read -r meta; do
      [ -f "$meta" ] || continue
      db=$(metadata_db "$meta")
      if [ "$db" = "$name" ]; then
        beads_dir="$(dirname "$meta")"
        if [ -f "$beads_dir/beads.jsonl" ]; then
          open_beads=$(grep -c '"status":"open"' "$beads_dir/beads.jsonl" 2>/dev/null || echo 0)
        fi
        break
      fi
    done < "$_meta_cache"
    db_info="$db_info$name|$commits|$open_beads
"
  done
fi

# Format a whole-seconds age as a short human string (h/m/s), same shape the
# old single-value check used to produce.
_backup_human_age() {
  _a="$1"
  if [ "$_a" -ge 3600 ]; then
    printf '%sh%sm' "$((_a / 3600))" "$((_a % 3600 / 60))"
  elif [ "$_a" -ge 60 ]; then
    printf '%sm%ss' "$((_a / 60))" "$((_a % 60))"
  else
    printf '%ss' "$_a"
  fi
}

# Check backup freshness, per database. ga-soxi9: this used to glob
# "$GC_CITY_PATH"/migration-backup-* and report on THAT — but those
# directories belong to a different subsystem entirely (gc dolt rollback's
# own pre-migration snapshots; see commands/rollback/run.sh, the only other
# reader of that naming convention). The check never asked Dolt about its
# actual configured backup remotes, so "Backups: none found" and "Backups:
# Xh ago" were both reporting on data that has nothing to do with whether
# `dolt backup sync` (mol-dog-backup.sh, the real offsite-backup driver) is
# working. Ask Dolt itself instead — it is the source of truth for what
# backups it thinks it has (same doctrine mol-dog-backup.toml already states
# for the sync side: never assume a naming convention, check every remote
# name `dolt backup` actually returns) — and report per database, since a
# single city-wide line cannot distinguish "db A has no backup" from "db B
# does".
#
# backup_info accumulator, one line per database: name|state|age_sec|path|
# size_bytes|remote_count|verified_remote_count|stale
#   state=none        no backup remote registered at all (real risk)
#   state=unverified   remote(s) registered, but none resolved to a verifiable
#                      local manifest — either a file:// target with the
#                      backup data actually missing (real risk), or every
#                      remote uses a scheme this check cannot stat locally
#                      (e.g. s3://) — "don't know" must never render the same
#                      as "confirmed good" or the same as "confirmed absent"
#   state=ok           at least one file:// remote has a real manifest;
#                      age/path/size describe the freshest such remote
#   state=query-failed `dolt backup -v` itself failed or timed out (e.g.
#                      transient contention on a busy host) — distinct from
#                      "none": we did not learn anything about this
#                      database's backups, so we must not report it as
#                      either registered or absent
backup_info=""
if [ -d "$data_dir" ]; then
  for d in "$data_dir"/*/; do
    [ ! -d "$d/.dolt" ] && continue
    name="$(basename "$d")"
    case "$(printf '%s' "$name" | tr '[:upper:]' '[:lower:]')" in information_schema|mysql|dolt_cluster|performance_schema|sys|__gc_probe) continue ;; esac

    # --user must precede the subcommand (dolt's global-flag position) and
    # is required here even though `backup -v` makes no server connection:
    # DOLT_CLI_PASSWORD is exported unconditionally above (even as "") and
    # applies to every dolt invocation for the rest of this script's
    # lifetime — without a matching --user, dolt's credential parser treats
    # a present password with no user as a hard error ("Failed to parse
    # credentials"), not a timeout, and every database failed identically
    # until this was added (reproduced live on this host). The export was
    # gate-fixed (ga-g3hay) to be unconditional, not scoped to
    # server_running, specifically so this call — which runs regardless of
    # server reachability — never falls back to dolt's interactive password
    # prompt when the server is down.
    if (cd "$d" && run_bounded 5 dolt --user "$GC_DOLT_USER" backup -v 2>/dev/null) > "$_backup_remotes_tmp"; then
      # grep -c always writes a count to stdout, including "0" for zero
      # matches — but a "0" result also has a non-zero EXIT status (no match
      # found), which under this file's `set -e` would abort the whole
      # script right here (a bare `x=$(cmd)` propagates cmd's exit status).
      # `|| true` neutralizes only that exit-status trip; it does NOT affect
      # what already landed in $remote_count, so it does not reintroduce the
      # double-"0" bug a `|| echo 0` fallback would (that form appends a
      # SECOND "0" behind the one grep already printed on the zero-match
      # path, corrupting remote_count with an embedded newline — caught
      # live while building this fix). Only fall back to 0 if grep produced
      # no output at all (e.g. the file genuinely couldn't be read).
      remote_count=$(grep -c . "$_backup_remotes_tmp" 2>/dev/null) || true
      [ -z "$remote_count" ] && remote_count=0
    else
      # The query itself failed/timed out — do not conflate with "ran
      # successfully and found zero remotes" (state=none below). Reproduced
      # live on this exact host: a single database's `dolt backup -v` failed
      # once under concurrent load while 10 others in the same sweep
      # succeeded, and while every other run in isolation (13/13) succeeded
      # too — a transient hiccup, not a real "no backup" condition, and
      # exactly the silent-collapse shape this bug is about.
      backup_info="$backup_info$name|query-failed|0||0|0|0|false
"
      continue
    fi
    if [ "$remote_count" -eq 0 ]; then
      backup_info="$backup_info$name|none|0||0|0|0|false
"
      continue
    fi

    best_mtime=0; best_path=""; best_size=0; verified_count=0
    while read -r rname rurl _rrest; do
      [ -z "$rname" ] && continue
      case "$rurl" in
        file://*)
          rpath="${rurl#file://}"
          [ -f "$rpath/manifest" ] || continue
          verified_count=$((verified_count + 1))
          m_mtime=$(stat -c %Y "$rpath/manifest" 2>/dev/null || stat -f %m "$rpath/manifest" 2>/dev/null || echo 0)
          if [ "$m_mtime" -gt "$best_mtime" ]; then
            best_mtime="$m_mtime"
            best_path="$rpath"
            size_kb=$(du -sk "$rpath" 2>/dev/null | cut -f1)
            best_size=$(( ${size_kb:-0} * 1024 ))
          fi
          ;;
      esac
    done < "$_backup_remotes_tmp"

    if [ "$best_mtime" -gt 0 ]; then
      now=$(date +%s)
      age=$((now - best_mtime))
      stale=false
      [ "$age" -gt 1800 ] && stale=true
      backup_info="$backup_info$name|ok|$age|$best_path|$best_size|$remote_count|$verified_count|$stale
"
    else
      backup_info="$backup_info$name|unverified|0||0|$remote_count|0|false
"
    fi
  done
fi

# Find orphan databases.
orphan_list=""
orphan_count=0
if [ -d "$data_dir" ]; then
  referenced=""
  while IFS= read -r meta; do
    [ -f "$meta" ] || continue
    db=$(metadata_db "$meta")
    [ -n "$db" ] && referenced="$referenced $db "
  done < "$_meta_cache"
  for d in "$data_dir"/*/; do
    [ ! -d "$d/.dolt" ] && continue
    name="$(basename "$d")"
    case "$(printf '%s' "$name" | tr '[:upper:]' '[:lower:]')" in information_schema|mysql|dolt_cluster|performance_schema|sys|__gc_probe) continue ;; esac
    case "$referenced" in *" $name "*) continue ;; esac
    size_kb=$(du -sk "$d" 2>/dev/null | cut -f1)
    size_bytes=$(( ${size_kb:-0} * 1024 ))
    if [ "$size_bytes" -ge 1048576 ]; then
      size=$(awk "BEGIN {printf \"%.1f MB\", $size_bytes/1048576}")
    elif [ "$size_bytes" -ge 1024 ]; then
      size=$(awk "BEGIN {printf \"%.1f KB\", $size_bytes/1024}")
    else
      size="${size_bytes} B"
    fi
    orphan_list="$orphan_list$name|$size
"
    orphan_count=$((orphan_count + 1))
  done
fi

# Check for zombie dolt processes.
# Use pgrep -x to match only processes named "dolt", then verify
# each is actually running sql-server via ps. This avoids false
# positives from processes that merely mention "dolt" in their args
# (e.g., Claude sessions whose prompt text contains "dolt sql-server").
#
# Rig-local Dolt servers (configured via dolt.port in config.yaml)
# are legitimate — exclude any PID listening on a known rig port.
#
# Foreign Dolt servers (managed by OTHER cities on the same host) are
# also legitimate. We recognize them by parsing `--config <path>` from
# the process command line and checking the sibling dolt.pid for a
# self-reference. Without this, every patrol in every city flags the
# others as zombies on shared dev hosts. The `--config` parse happens
# inside the single bounded `ps -eo` + awk pass below (it already has
# the full args line in hand); only the sibling dolt.pid read is left
# to the shell loop, which iterates O(matched sql-servers) — never
# O(all pids/zombies) — so the bounded-fork invariant still holds.
#
# GC_HEALTH_SKIP_ZOMBIE_SCAN is a test-only escape hatch. Zombie
# enumeration spawns one `ps` per matching process, which on shared
# dev machines with many accumulated dolt processes dominates the
# runtime of the hang-mode test below. Setting it to "1" skips the
# scan so tests exercise just the bounded-probe behavior they care
# about without being hostage to ambient process state.
zombie_count=0
zombie_pids=""
if [ "${GC_HEALTH_SKIP_ZOMBIE_SCAN:-0}" != "1" ]; then
  # Collect PIDs of legitimate rig-local Dolt servers.
  rig_dolt_pids=""
  while IFS= read -r meta; do
    [ -f "$meta" ] || continue
    config_file="$(dirname "$meta")/config.yaml"
    [ -f "$config_file" ] || continue
    rig_port=$(grep '^dolt\.port:' "$config_file" 2>/dev/null | sed "s/^dolt\\.port:[[:space:]]*//; s/[[:space:]]*#.*$//; s/['\\\"]//g; s/[[:space:]]*$//" | head -1)
    case "$rig_port" in ''|*[!0-9]*) continue ;; esac
    [ "$rig_port" = "$GC_DOLT_PORT" ] && continue
    rig_pid=$(managed_runtime_listener_pid "$rig_port" || true)
    [ -n "$rig_pid" ] && rig_dolt_pids="$rig_dolt_pids $rig_pid "
  done < "$_meta_cache"

  # Enumerate the process table ONCE, not one `ps -p <pid> -o args=` fork per
  # `pgrep -x dolt` match. pgrep matches every dolt-named process including
  # Z-state zombies, so under a non-reaping PID 1 the old per-PID fork became
  # an O(zombies) `ps` storm re-paid on every 30s health tick (#2482). Collect
  # the candidate PIDs from pgrep, then classify them in a single `ps`+`awk`
  # pass: keep candidates that are dolt sql-server processes, skip Z-state
  # zombies (a defunct dolt never carries sql-server args anyway), and exclude
  # the managed city server and rig-local dolts. For each survivor the awk
  # pass also extracts the dolt `--config <path>` (or `--config=<path>`) from
  # the args line it already holds, and emits `pid<TAB>config_path` so the
  # shell loop below can do the foreign-managed check without re-forking ps.
  candidate_pids=" $(pgrep -x dolt 2>/dev/null | tr '\n' ' ' || true)"
  ps -eo pid=,stat=,args= 2>/dev/null | awk \
    -v server="$server_pid" -v rigs="$rig_dolt_pids" -v cands="$candidate_pids" '
    BEGIN {
      # Build an O(1) lookup set from the pgrep candidates once. The
      # per-row membership test below was an index() substring scan
      # re-paid for every process-table row, i.e. O(rows x candidate
      # string length); the reported incident had ~41k candidate PIDs
      # (#2618). Splitting into an associative set makes each lookup O(1).
      n = split(cands, a, " ")
      for (i = 1; i <= n; i++) if (a[i] != "") cand[a[i]] = 1
    }
    {
      pid = $1
      if (!(pid in cand)) next                   # not a pgrep -x dolt match
      if (pid == server) next                     # the managed city server
      if (index(rigs, " " pid " ") != 0) next     # a configured rig-local dolt
      if ($2 ~ /Z/) next                          # Z-state zombie: never a server
      if (index($0, "sql-server") == 0) next      # not a dolt sql-server
      # Extract the dolt --config path from the args fields (args start at
      # $3 after pid/stat). Accept both the space-separated `--config PATH`
      # and the `--config=PATH` spellings. Emitted alongside the pid so the
      # shell can read the sibling dolt.pid; empty when no --config is given.
      config = ""
      for (i = 3; i <= NF; i++) {
        if ($i == "--config" && (i + 1) <= NF) { config = $(i+1); break }
        if (index($i, "--config=") == 1) { config = substr($i, 10); break }
      }
      print pid "\t" config
    }' > "$_zombie_scan_out" 2>/dev/null || true

  # Iterate ONLY the matched sql-servers (O(matched servers)) the awk pass
  # emitted — not the full candidate/zombie set. This loop is where the
  # foreign-managed decision lives; keeping it bounded by the awk output is
  # what preserves the bounded-fork invariant. Reading from the scratch file
  # (not a pipe) keeps the loop in the parent shell so the zombie_count /
  # zombie_pids accumulation survives.
  _tab="$(printf '\t')"
  while IFS="$_tab" read -r p config_path; do
    [ -n "$p" ] || continue
    # Foreign-managed check: if --config points at a yaml whose sibling
    # dolt.pid claims this PID, the process is owned by another managed
    # Dolt instance (another city on this host) — not a zombie.
    if [ -n "$config_path" ] && [ -f "$config_path" ]; then
      foreign_pid_file="$(dirname "$config_path")/dolt.pid"
      if [ -f "$foreign_pid_file" ]; then
        recorded_pid=$(head -1 "$foreign_pid_file" 2>/dev/null | tr -d ' \t\r\n')
        [ "$recorded_pid" = "$p" ] && continue
      fi
    fi
    zombie_count=$((zombie_count + 1))
    zombie_pids="$zombie_pids $p"
  done < "$_zombie_scan_out"
fi

# Output.
timestamp=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

if [ "$json_output" = true ]; then
  # Build JSON output. `server.reachable` reports whether the SQL
  # handshake actually succeeded (port listening AND server answering
  # SELECT 1). Consumers (deacon patrol) should key health off
  # `server.reachable`, not `server.running`, because a process can
  # hold the port while its goroutines are wedged.
  cat <<JSONEOF
{
  "timestamp": "$timestamp",
  "server": {
    "running": $server_running,
    "reachable": $server_reachable,
    "pid": $server_pid,
    "port": $GC_DOLT_PORT,
    "latency_ms": $server_latency
  },
  "databases": [
JSONEOF
  first=true
  echo "$db_info" | while IFS='|' read -r name commits open_beads; do
    [ -z "$name" ] && continue
    if [ "$first" = true ]; then first=false; else echo ","; fi
    printf '    {"name": "%s", "commits": %s, "open_beads": %s}' "$name" "$commits" "$open_beads"
  done
  cat <<JSONEOF

  ],
  "backups": [
JSONEOF
  first=true
  echo "$backup_info" | while IFS='|' read -r name state age path size remote_count verified_count stale; do
    [ -z "$name" ] && continue
    if [ "$first" = true ]; then first=false; else echo ","; fi
    printf '    {"name": "%s", "state": "%s", "age_sec": %s, "path": "%s", "size_bytes": %s, "remote_count": %s, "verified_remote_count": %s, "stale": %s}' \
      "$name" "$state" "$age" "$path" "$size" "$remote_count" "$verified_count" "$stale"
  done
  cat <<JSONEOF

  ],
  "orphan_check_degraded": $rig_discovery_degraded,
  "orphans": [
JSONEOF
  first=true
  echo "$orphan_list" | while IFS='|' read -r name size; do
    [ -z "$name" ] && continue
    if [ "$first" = true ]; then first=false; else echo ","; fi
    printf '    {"name": "%s", "size": "%s"}' "$name" "$size"
  done
  cat <<JSONEOF

  ],
  "processes": {
    "zombie_count": $zombie_count,
    "zombie_pids": [$(echo "$zombie_pids" | tr -s ' ' ',' | sed 's/^,//;s/,$//')]
  }
}
JSONEOF
  # JSON mode always exits 0 when the payload is well-formed. Health
  # state is signalled in-band via `server.reachable` (and the rest of
  # the document). Automation that parses the JSON — notably the deacon
  # patrol formula — must not fail before stdout is parsed just because
  # the server is down; that's exactly the condition the patrol is
  # supposed to detect and react to. Callers that want exit-code
  # signalling should use the human-readable form.
  exit 0
fi

# Human-readable output.
if [ "$server_running" = true ]; then
  echo "Server: running (PID $server_pid, port $GC_DOLT_PORT, latency ${server_latency}ms)"
else
  echo "Server: not running"
fi

if [ -n "$db_info" ]; then
  echo ""
  echo "Databases:"
  echo "$db_info" | while IFS='|' read -r name commits open_beads; do
    [ -z "$name" ] && continue
    echo "  $name: $commits commits, $open_beads open beads"
  done
fi

if [ -n "$backup_info" ]; then
  echo ""
  echo "Backups:"
  echo "$backup_info" | while IFS='|' read -r name state age path size remote_count verified_count stale; do
    [ -z "$name" ] && continue
    case "$state" in
      ok)
        human_age=$(_backup_human_age "$age")
        stale_tag=""
        [ "$stale" = true ] && stale_tag=" [STALE]"
        if [ "$size" -ge 1048576 ]; then
          human_size=$(awk "BEGIN {printf \"%.1f MB\", $size/1048576}")
        elif [ "$size" -ge 1024 ]; then
          human_size=$(awk "BEGIN {printf \"%.1f KB\", $size/1024}")
        else
          human_size="${size} B"
        fi
        echo "  $name: ${human_age} ago${stale_tag} ($human_size, $path)"
        ;;
      none)
        echo "  $name: NONE REGISTERED"
        ;;
      unverified)
        echo "  $name: registered ($remote_count remote(s)) but could not verify any locally — check manually"
        ;;
      query-failed)
        echo "  $name: could not query backup config (dolt backup -v failed/timed out) — check manually"
        ;;
    esac
  done
fi

if [ "$rig_discovery_degraded" = true ]; then
  echo ""
  echo "Orphans: UNKNOWN — rig discovery degraded (gc rig list failed, timed out, or gc/jq unavailable)."
  echo "  The external rig set could not be confirmed, so directories not matched"
  echo "  to a known rig CANNOT be trusted as orphans — some may be live"
  echo "  production databases. Do not act on this report (e.g. gc dolt cleanup)."
  if [ "$orphan_count" -gt 0 ]; then
    echo "  Unverified candidates ($orphan_count):"
    echo "$orphan_list" | while IFS='|' read -r name size; do
      [ -z "$name" ] && continue
      echo "    ? $name ($size)"
    done
  fi
elif [ "$orphan_count" -gt 0 ]; then
  echo ""
  echo "Orphans: $orphan_count"
  echo "$orphan_list" | while IFS='|' read -r name size; do
    [ -z "$name" ] && continue
    echo "  $name ($size)"
  done
fi

if [ "$zombie_count" -gt 0 ]; then
  echo ""
  echo "Zombie processes: $zombie_count (PIDs:$zombie_pids)"
fi

# Exit status (human mode only): 0 when the data plane is healthy
# (server running AND answering SQL). Non-zero signals a CLI caller
# that something is wrong — server not running, or port in use by a
# process that isn't speaking MySQL. Stale backups, orphans, and
# zombies are informational and do not fail the exit code.
#
# JSON mode is unconditionally exit 0 (see above) — programmatic
# consumers read `server.reachable` from the payload instead.
if [ "$server_reachable" = true ]; then
  exit 0
fi
exit 1
