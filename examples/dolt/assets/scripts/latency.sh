#!/bin/sh
# latency.sh — millisecond-resolution latency measurement for dolt-pack
# health probes. Sourced by mol-dog-doctor.sh; unit-tested by
# test/dolt/latency_test.sh.
#
# Replaces whole-second 'date +%s' timing, which quantizes a sub-second probe
# to 0s or 1s depending on whether it straddles a wall-clock second tick —
# producing false latency WARNs (and MEDIUM advisory mail) at a 1s threshold.

# now_ms — echo the current time in epoch milliseconds.
# GNU/coreutils date supports %3N (3-digit nanoseconds = milliseconds). BSD
# date (stock macOS) does not: it prints "%3N" back literally (e.g.
# "17884027323N"), which the digits-only guard below correctly rejects.
#
# ga-ouqtg (regressed 2026-08-03 and again — this is the 3rd occurrence,
# ga-3bqak): a bare `date +%s%3N`-or-whole-second fallback degrades SILENTLY
# to second resolution on stock macOS. A sub-second probe that straddles a
# wall-clock tick then reads 0 or 1000, never its real value — exactly the
# quantization this file exists to avoid. Homebrew coreutils installs GNU
# date as `gdate` (it does not overwrite the system `date`), and python3 is
# present on every target platform even without coreutils — so try both
# before accepting second-resolution as a last resort. Whichever branch is
# taken, verify DIGITS-ONLY before trusting the value: a shell/PATH oddity
# printing something unexpected must fall through, not be trusted.
now_ms() {
  for _now_ms_cmd in "date +%s%3N" "gdate +%s%3N"; do
    _now_ms_v=$($_now_ms_cmd 2>/dev/null)
    case "$_now_ms_v" in
      ''|*[!0-9]*) _now_ms_v="" ;;  # %3N printed literally, or command missing
    esac
    if [ -n "$_now_ms_v" ] && [ "${#_now_ms_v}" -ge 13 ]; then
      printf '%s\n' "$_now_ms_v"    # epoch-ms is 13+ digits from 2001..2286
      return 0
    fi
  done
  _now_ms_v=$(python3 -c 'import time; print(int(time.time()*1000))' 2>/dev/null)
  case "$_now_ms_v" in
    ''|*[!0-9]*) _now_ms_v="" ;;
  esac
  if [ -n "$_now_ms_v" ] && [ "${#_now_ms_v}" -ge 13 ]; then
    printf '%s\n' "$_now_ms_v"
    return 0
  fi
  # Last resort: whole-second resolution — no worse than the pre-fix behavior.
  printf '%s\n' "$(( $(date +%s) * 1000 ))"
}

# latency_should_warn ELAPSED_MS THRESHOLD_MS — exit 0 (warn) when measured
# latency meets or exceeds the threshold, 1 otherwise. Preserves the original
# '>=' semantics, now in milliseconds.
latency_should_warn() {
  [ "${1:-0}" -ge "${2:-0}" ]
}
