#!/bin/sh
# compact-gc-cancel-classify.sh — discriminate WHY a `CALL DOLT_GC('--full')`
# (or similar long-running admin call) failed (gastownhall/gascity, ga-3euoj).
#
# Before this, run_full_gc()'s failure path logged the same shape
# ("DOLT_GC failed rc=%s duration=%ss" + raw stderr) whether the CLIENT-SIDE
# run_bounded wrapper killed the `dolt` CLI after call_timeout elapsed (never
# got a server response at all), or the `dolt` CLI exited cleanly with a real
# SQL error the SERVER sent back. Those are different failures with different
# fixes, and collapsing them into one line is exactly the trap this codebase's
# own history warns about (comments/logs that don't distinguish "didn't
# happen" from "happened and failed" from "failed for a different reason").
#
# Concretely (ga-3euoj): hq's nightly compact retried `CALL DOLT_GC('--full')`,
# got back rc=1 with "Error in SaveHashes call: SaveHashes, error calling
# getManyCompressed: context canceled" after 112s — well under both
# call_timeout (300s+) and the outer routine timeout (3600s), so neither
# wrapper-level timeout fired. "context canceled" is Go's ctx.Err() wording
# surfacing through the SQL error text — the cancellation happened INSIDE the
# Dolt SQL server's own query execution, most likely its listener's
# read_timeout_millis/net_read_timeout (confirmed present in this deployment's
# dolt-config.yaml and `SHOW VARIABLES`) firing on a call long enough to
# exceed it — not a client/wrapper timeout, and not (by this evidence) a
# disk-space error, which would carry a different message (no I/O or
# no-space wording here). See the bead for why raising that timeout globally
# is explicitly NOT this fix: it existed at 30s specifically to reap dead
# per-call sockets (200-460 idle Sleep connections observed before), and a
# blind global bump would reopen that. A per-call `SET SESSION
# net_read_timeout=<n>` ahead of just the long admin call is the scoped
# alternative to evaluate next — this lib only classifies, it does not change
# the timeout.
#
# Depends on nothing from run.sh — takes rc and a stderr-capture file path as
# plain arguments so it is testable without a live Dolt server.

# classify_gc_cancel_cause <rc> <stderr_file> — echoes one of:
#   OK               — rc=0. Defensive: callers should only invoke this in
#                      the failure branch, but a 0 must never read as a
#                      failure of any kind if it is passed in anyway.
#   CLIENT_TIMEOUT   — rc is coreutils `timeout`'s own convention for "the
#                      command was killed because it ran past the bound":
#                      124 (SIGTERM) or 137 (128+9, run_bounded's
#                      --kill-after=2 escalation to SIGKILL). The `dolt` CLI
#                      never got to read a server response — the CLIENT/
#                      wrapper is what ended the call.
#   SERVER_CANCELLED — a real (non-timeout) rc AND stderr carries a
#                      cancellation signature ("context canceled", case-
#                      insensitive — Go's ctx.Err() wording). The `dolt` CLI
#                      exited because the SERVER sent back an error, and that
#                      error says the server's own query context was
#                      cancelled — a listener-level read/net timeout on the
#                      connection is the leading candidate, not the
#                      call_timeout wrapper (which would show as
#                      CLIENT_TIMEOUT above, not a clean SQL error).
#   SERVER_ERROR     — a real (non-timeout) rc with no cancellation
#                      signature in stderr. Fail closed: an unreadable/
#                      missing/empty stderr file is evidence of NOTHING, so
#                      it must not be guessed as SERVER_CANCELLED — the
#                      signature has to actually be present.
classify_gc_cancel_cause() {
  _gcc_rc="$1"
  _gcc_stderr_file="$2"
  case "$_gcc_rc" in
    0) printf 'OK'; return 0 ;;
    124|137) printf 'CLIENT_TIMEOUT'; return 0 ;;
  esac
  if [ -s "$_gcc_stderr_file" ] 2>/dev/null && \
     grep -qi 'context canceled' "$_gcc_stderr_file" 2>/dev/null; then
    printf 'SERVER_CANCELLED'
    return 0
  fi
  printf 'SERVER_ERROR'
  return 0
}
