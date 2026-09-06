#!/bin/sh
# Unit test for classify_gc_cancel_cause (ga-3euoj).
# Lib under test: examples/dolt/assets/scripts/compact-gc-cancel-classify.sh
#
# Covers the discriminator the bead asks for: a failed `CALL DOLT_GC('--full')`
# must not collapse "the wrapper's own run_bounded timeout fired" (client-side,
# rc=124/137, no server response was ever read), "the server cancelled the
# query's own context" (a real, non-timeout rc with a cancellation signature in
# stderr — e.g. read_timeout_millis/net_read_timeout firing mid-query on a
# long admin call), and "some other real server/query error" (a real,
# non-timeout rc with no cancellation signature) into one undifferentiated
# log line. No live Dolt/server needed — pure function over rc + a stderr file.
set -u

HERE=$(unset CDPATH; cd -- "$(dirname "$0")" && pwd)
LIB="$HERE/../../examples/dolt/assets/scripts/compact-gc-cancel-classify.sh"
[ -f "$LIB" ] || { echo "FAIL: lib not found at $LIB"; exit 1; }

# shellcheck disable=SC1090  # $LIB path is computed from the test's own location
. "$LIB"

# --- harness ----------------------------------------------------------------
pass=0
fail=0
ok() { pass=$((pass + 1)); printf 'ok   - %s\n' "$1"; }
no() { fail=$((fail + 1)); printf 'FAIL - %s\n' "$1"; }

_mk() { # _mk <content> -> echoes path to a tmp file holding it (no trailing newline surprises)
  _mk_f=$(mktemp)
  printf '%s' "$1" > "$_mk_f"
  printf '%s' "$_mk_f"
}

# 1. rc=0 -> OK (defensive; callers should not invoke on success, but must
#    never misclassify success as any failure bucket if they do)
empty_f=$(_mk "")
got=$(classify_gc_cancel_cause 0 "$empty_f")
if [ "$got" = "OK" ]; then ok "rc=0 -> OK"; else no "rc=0 -> OK (got: $got)"; fi

# 2. rc=124 (coreutils `timeout` default kill signal) -> CLIENT_TIMEOUT,
#    regardless of stderr content (the process was killed before it could
#    read/print any real server response).
got=$(classify_gc_cancel_cause 124 "$empty_f")
if [ "$got" = "CLIENT_TIMEOUT" ]; then ok "rc=124 -> CLIENT_TIMEOUT"; else no "rc=124 -> CLIENT_TIMEOUT (got: $got)"; fi

# 3. rc=137 (128+SIGKILL, run_bounded's --kill-after escalation) -> CLIENT_TIMEOUT
got=$(classify_gc_cancel_cause 137 "$empty_f")
if [ "$got" = "CLIENT_TIMEOUT" ]; then ok "rc=137 -> CLIENT_TIMEOUT"; else no "rc=137 -> CLIENT_TIMEOUT (got: $got)"; fi

# 4. real ga-3euoj signature: rc=1, stderr shows the server cancelled its own
#    query context (SaveHashes/getManyCompressed) -> SERVER_CANCELLED, not
#    conflated with either a client timeout or a generic server error.
sig_f=$(_mk "compact: db=hq error on line 1 for query CALL DOLT_GC('--full'): Error 1105 (HY000): Error in SaveHashes call: SaveHashes, error calling getManyCompressed: context canceled")
got=$(classify_gc_cancel_cause 1 "$sig_f")
if [ "$got" = "SERVER_CANCELLED" ]; then ok "rc=1 + 'context canceled' -> SERVER_CANCELLED"; else no "rc=1 + 'context canceled' -> SERVER_CANCELLED (got: $got)"; fi

# 5. case-insensitive match ("Context Canceled", different Go call site casing)
sig_f2=$(_mk "Error: Context Canceled while flushing")
got=$(classify_gc_cancel_cause 2 "$sig_f2")
if [ "$got" = "SERVER_CANCELLED" ]; then ok "case-insensitive 'Context Canceled' -> SERVER_CANCELLED"; else no "case-insensitive match (got: $got)"; fi

# 6. real, non-timeout rc with NO cancellation signature -> SERVER_ERROR (a
#    genuine different failure; must not be mislabeled as a cancellation).
other_f=$(_mk "compact: db=hq error on line 1 for query CALL DOLT_GC('--full'): Error 1105 (HY000): some unrelated storage error")
got=$(classify_gc_cancel_cause 1 "$other_f")
if [ "$got" = "SERVER_ERROR" ]; then ok "rc=1 + unrelated stderr -> SERVER_ERROR"; else no "rc=1 + unrelated stderr -> SERVER_ERROR (got: $got)"; fi

# 7. real, non-timeout rc with EMPTY stderr -> SERVER_ERROR (fail closed: no
#    signature to prove cancellation, must not guess SERVER_CANCELLED).
got=$(classify_gc_cancel_cause 1 "$empty_f")
if [ "$got" = "SERVER_ERROR" ]; then ok "rc=1 + empty stderr -> SERVER_ERROR (fail closed, no signature)"; else no "rc=1 + empty stderr -> SERVER_ERROR (got: $got)"; fi

# 8. missing/unreadable stderr file -> SERVER_ERROR (fail closed, never crash)
got=$(classify_gc_cancel_cause 1 "/nonexistent/path/$$")
if [ "$got" = "SERVER_ERROR" ]; then ok "rc=1 + missing stderr file -> SERVER_ERROR (fail closed)"; else no "rc=1 + missing stderr file -> SERVER_ERROR (got: $got)"; fi

rm -f "$empty_f" "$sig_f" "$sig_f2" "$other_f"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
