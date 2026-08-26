#!/bin/sh
# gc dolt status — Check if the Dolt server is running.
#
# Always prints a state line, then exits accordingly:
#   running      -> exit 0
#   not running  -> exit 1  (probe confirmed the server is down)
#   UNKNOWN      -> exit 2  (could not determine — treat as unhealthy, but
#                            do not conflate with a confirmed-down result)
# ga-dzado: the previous version discarded the probe's exit code into a bare
# `set -e` tail call, so it printed NOTHING and exited 0 in both the running
# AND not-running cases (the probe's own 0/2 contract never reached stdout,
# and its "2" leaked past the "1 otherwise" documented above unexamined).
# "healthy" and "the check did nothing" were indistinguishable. See the bead
# for the live measurement.
#
# Lightweight status probe for manual checks and scripts; the dolt-health order
# uses structured `gc dolt health --json | gc dolt health-check` diagnostics.
#
# Environment: GC_CITY_PATH
set -e

: "${GC_CITY_PATH:?GC_CITY_PATH must be set}"
PACK_DIR="${GC_PACK_DIR:-$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)}"
. "$PACK_DIR/assets/scripts/runtime.sh"

if [ ! -x "$GC_BEADS_BD_SCRIPT" ]; then
  echo "UNKNOWN (gc-beads-bd not found)"
  exit 2
fi

# gc-beads-bd's probe op exits 0 (running) or 2 (not running) on every
# expected path (see op_probe in gc-beads-bd.sh). Any OTHER exit code means
# something above that dispatch failed (bad GC_CITY_PATH, disk full, port
# resolution error, etc.) -- genuinely indeterminate, not a confirmed-down
# state, so it must not be reported the same way as a confirmed "not
# running". The probe call sits in an `if` (not a bare statement) because
# `set -e` exempts the command tested by `if` -- a bare call would kill this
# script on a nonzero exit before the case below could report it.
if GC_CITY_PATH="$GC_CITY_PATH" "$GC_BEADS_BD_SCRIPT" probe >/dev/null 2>&1; then
  probe_rc=0
else
  probe_rc=$?
fi

case "$probe_rc" in
  0) echo "running"; exit 0 ;;
  2) echo "not running"; exit 1 ;;
  *) echo "UNKNOWN (probe exited $probe_rc)"; exit 2 ;;
esac
